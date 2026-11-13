// Unit tests for the cross-cutting Web console polish helpers: sidebar row
// status + group caps, inspector tab state while loading, the Goal tab's
// mission approval entry point, local live-activity feedback and the toast
// rack placement / cap.
import test from 'node:test';
import assert from 'node:assert/strict';
import { SIDEBAR_GROUP_LIMIT, capSessionGroup, groupSessions, sidebarRowStatus } from '../../internal/webconsole/ui/src/lib/session-list.js';
import { LOCAL_ACTIVITY_MAX_AGE_MS, isLocalActivitySettled, localSendActivity } from '../../internal/webconsole/ui/src/lib/send-recovery.js';
import { goalActions, goalPlanApprovalEntry, inspectorTabState } from '../../internal/webconsole/ui/src/features/inspector/inspector-model.js';
import { selectLiveActivity } from '../../internal/webconsole/ui/src/features/session/stream/model.js';
import { toastPlacement } from '../../internal/webconsole/ui/src/ui/Toasts.js';
import { MAX_VISIBLE_TOASTS, pushToast, dismissToast } from '../../internal/webconsole/ui/src/state/toasts.js';
import { getState, initialState, setState } from '../../internal/webconsole/ui/src/state/store.js';
import { createHarness } from './ui_session_actions_harness.mjs';

// ---- sidebar ------------------------------------------------------------------------------

test('sidebar rows use the plan-mode aware summary status; the open session uses the top-bar status', () => {
  const summary = { id: 's1', status: 'running', plan_mode_status: 'awaiting_approval' };
  assert.equal(sidebarRowStatus(summary), 'awaiting_plan_approval');
  assert.equal(sidebarRowStatus({ id: 's2', status: 'paused', plan_mode_status: 'awaiting_user_input' }), 'awaiting_plan_input');
  // Overview lags: the summary still says running, the detail already awaits approval.
  const stale = { id: 's3', status: 'running' };
  assert.equal(sidebarRowStatus(stale, { id: 's3', status: 'awaiting_plan_approval' }), 'awaiting_plan_approval');
  assert.equal(sidebarRowStatus(stale, { id: 'other', status: 'completed' }), 'running', 'only the open session is overridden');
  assert.equal(sidebarRowStatus(stale, { id: 's3', status: '' }), 'running', 'no detail yet: summary status');

  const now = Date.parse('2026-11-13T12:00:00Z');
  const items = [
    { id: 'a', status: 'completed', updated_at: '2026-11-13T11:00:00Z' },
    { id: 'b', status: 'running', updated_at: '2026-11-13T10:00:00Z' },
  ];
  const current = { id: 'a', status: 'awaiting_plan_approval' };
  const groups = groupSessions(items, now, { statusOf: (item) => sidebarRowStatus(item, current) });
  assert.deepEqual(groups.map((group) => [group.key, group.items.map((item) => item.id)]), [['attention', ['a', 'b']]]);
  assert.deepEqual(groupSessions(items, now).map((group) => group.key), ['attention', 'today'], 'default grouping is unchanged');
});

test('sidebar groups are capped with the active session always visible', () => {
  assert.equal(SIDEBAR_GROUP_LIMIT, 6);
  const items = Array.from({ length: 10 }, (_, index) => ({ id: 'r' + index }));
  const capped = capSessionGroup(items);
  assert.deepEqual(capped.visible.map((item) => item.id), ['r0', 'r1', 'r2', 'r3', 'r4', 'r5']);
  assert.equal(capped.hiddenCount, 4);
  const withActive = capSessionGroup(items, { activeId: 'r8' });
  assert.deepEqual(withActive.visible.map((item) => item.id), ['r0', 'r1', 'r2', 'r3', 'r4', 'r5', 'r8'], 'active row kept in order');
  assert.equal(withActive.hiddenCount, 3);
  assert.equal(capSessionGroup(items, { expanded: true }).hiddenCount, 0);
  assert.equal(capSessionGroup(items.slice(0, 7)).hiddenCount, 0, 'never hide a single row behind a toggle');
  assert.equal(capSessionGroup(items.slice(0, 8)).hiddenCount, 2);
  assert.deepEqual(capSessionGroup(null), { visible: [], hiddenCount: 0 });
});

// ---- inspector ------------------------------------------------------------------------------

test('inspector keeps the persisted tab (with a loading panel) while a session detail loads', () => {
  const loading = inspectorTabState({ tab: 'goal', detail: null, sessionId: 's1', loading: true });
  assert.deepEqual(loading, { tabs: ['overview', 'goal'], active: 'goal', pending: true });
  assert.deepEqual(inspectorTabState({ tab: 'overview', detail: null, sessionId: 's1', loading: true }), { tabs: ['overview'], active: 'overview', pending: true });
  assert.equal(inspectorTabState({ tab: 'bogus', detail: null, sessionId: 's1', loading: true }).active, 'overview');
  const withGoal = { metadata: { id: 's1' }, state: { status: 'running' }, goal: { goal_id: 'g', status: 'active' } };
  assert.deepEqual(inspectorTabState({ tab: 'goal', detail: withGoal, sessionId: 's1' }), { tabs: ['overview', 'goal', 'context', 'timeline'], active: 'goal', pending: false });
  const noGoal = { metadata: { id: 's1' }, state: { status: 'running' } };
  assert.equal(inspectorTabState({ tab: 'goal', detail: noGoal, sessionId: 's1' }).active, 'overview', 'hidden tab falls back once loaded');
  assert.deepEqual(inspectorTabState({ tab: 'goal', detail: null, sessionId: 's1', loading: false }), { tabs: ['overview'], active: 'overview', pending: false }, 'failed load is not pending');
  assert.equal(inspectorTabState({ tab: 'goal', detail: null, sessionId: '', loading: true }).pending, false, 'no session, no skeleton');
});

const mission = (extra = {}) => ({ goal: { goal_id: 'goal_1', status: 'active', updated_at: 'g0', objective: 'x', mission: { plan_status: 'needs_approval' } }, ...extra });

test('goal tab mission approval: one approval path per state', () => {
  assert.equal(goalPlanApprovalEntry({}), 'none');
  assert.equal(goalPlanApprovalEntry({ goal: { status: 'active' } }), 'none', 'no mission plan');
  assert.equal(goalPlanApprovalEntry(mission()), 'mission', 'no plan mode');
  assert.equal(goalPlanApprovalEntry(mission({ plan_mode: { status: 'awaiting_approval' } })), 'mission', 'unlinked plan mode never blocks the mission');
  assert.equal(goalPlanApprovalEntry(mission({ plan_mode: { status: 'planning' } })), 'mission');
  assert.equal(goalPlanApprovalEntry(mission({ plan_mode: { status: 'awaiting_approval', linked_goal_id: 'goal_1' } })), 'inline', 'pending linked plan: inline card');
  assert.equal(goalPlanApprovalEntry(mission({ plan_mode: { status: 'planning', linked_goal_id: 'goal_1' } })), 'none');
  assert.equal(goalPlanApprovalEntry(mission({ plan_mode: { status: 'awaiting_user_input', linked_goal_id: 'goal_1' } })), 'none');
  assert.equal(goalPlanApprovalEntry({ goal: { status: 'active', mission: { plan_status: 'approved' } } }), 'none');
  assert.equal(goalPlanApprovalEntry({ goal: { status: 'complete', mission: { plan_status: 'needs_approval' } } }), 'none');
  assert.equal(goalActions(mission()).canApprovePlan, true);
  assert.equal(goalActions(mission({ plan_mode: { status: 'awaiting_approval', linked_goal_id: 'goal_1' } })).canApprovePlan, false, 'no duplicate button');
});

test('goal tab "mission" entry routes approvePlan to the unlinked mission approval', async () => {
  const h = createHarness({ api: { approveMissionPlan: () => ({ goal_id: 'goal_1' }) } });
  const detail = mission({ plan_mode: { plan_mode_id: 'pm_old', status: 'cancelled' } });
  assert.equal(goalPlanApprovalEntry(detail), 'mission');
  h.openSession('sess_a', detail);
  await h.actions.approvePlan('sess_a', { entrypoint: 'mission' });
  assert.deepEqual(h.calls.map((call) => call.name), ['approveMissionPlan']);
  assert.deepEqual(h.calls[0].args, ['sess_a', {}], 'facts-only approval without a plan-mode target');
});

// ---- live activity --------------------------------------------------------------------------

test('local send activity is stamped and settles after the run', () => {
  const activity = localSendActivity('steer', {}, 1000);
  assert.deepEqual(activity.title, { key: 'activity.steerQueued.title' });
  assert.equal(activity.at, 1000);
  assert.equal(localSendActivity('start', {}, 5).title.key, 'activity.launching.title');
  assert.equal(localSendActivity('steer', { interrupt: true }, 5).title.key, 'activity.interruptSteer.title');

  const at = Date.parse('2026-11-13T12:00:00Z');
  const base = { ...localSendActivity('continue', {}, at) };
  const detail = (status, updated) => ({ state: { status, updated_at: updated } });
  assert.equal(isLocalActivitySettled(null), false);
  assert.equal(isLocalActivitySettled({ title: {} }, { now: at }), true, 'no timestamp');
  assert.equal(isLocalActivitySettled(base, { detail: detail('completed', '2026-11-13T11:59:59Z'), now: at + 500 }), false, 'old settled state from before the send');
  assert.equal(isLocalActivitySettled(base, { detail: detail('running', '2026-11-13T12:00:03Z'), now: at + 3000 }), false);
  assert.equal(isLocalActivitySettled(base, { detail: detail('completed', '2026-11-13T12:00:05Z'), generating: true, now: at + 5000 }), false, 'still generating');
  assert.equal(isLocalActivitySettled(base, { detail: detail('completed', '2026-11-13T12:00:05Z'), now: at + 5000 }), true, 'settled after the send');
  assert.equal(isLocalActivitySettled(base, { detail: detail('running', '2026-11-13T12:00:05Z'), now: at + LOCAL_ACTIVITY_MAX_AGE_MS + 1 }), true, 'aged out');
});

test('local activity wins over older engine events until a newer one arrives', () => {
  const at = Date.parse('2026-11-13T12:00:00Z');
  const override = localSendActivity('steer', {}, at);
  const older = { id: 'e1', type: 'tool.before', time: '2026-11-13T11:59:58Z', data: { tool_name: 'shell' } };
  const newer = { id: 'e2', type: 'tool.before', time: '2026-11-13T12:00:01Z', data: { tool_name: 'read_file' } };
  const running = (events) => ({ state: { status: 'running', phase: 'tool' }, events });
  assert.deepEqual(selectLiveActivity({ detail: running([older]), override }).title, { key: 'activity.steerQueued.title' });
  assert.equal(selectLiveActivity({ detail: running([older, newer]), override }).eventType, 'tool.before');
  assert.deepEqual(selectLiveActivity({ detail: running([older, newer]), override }).subject, { raw: 'read_file' });
  const relayOld = { type: 'provider.call', receivedAt: at - 10 };
  const relayNew = { type: 'provider.call', receivedAt: at + 10 };
  assert.deepEqual(selectLiveActivity({ detail: running([]), liveEvents: [relayOld], override }).title, { key: 'activity.steerQueued.title' });
  assert.equal(selectLiveActivity({ detail: running([]), liveEvents: [relayOld, relayNew], override }).eventType, 'provider.call');
  // A refreshed running state from after the send supersedes it too (phase-derived activity).
  const refreshed = { state: { status: 'running', phase: 'model', updated_at: '2026-11-13T12:00:02Z' }, events: [older] };
  assert.deepEqual(selectLiveActivity({ detail: refreshed, override }).title, { key: 'phase.model', params: { fallback: 'Model' } });
  const before = { state: { status: 'running', phase: 'model', updated_at: '2026-11-13T11:59:59Z' }, events: [older] };
  assert.deepEqual(selectLiveActivity({ detail: before, override }).title, { key: 'activity.steerQueued.title' }, 'state from before the send');
  // Without a timestamp the override stays a fallback (previous behaviour).
  const untimed = { title: { key: 'activity.steerQueued.title' } };
  assert.equal(selectLiveActivity({ detail: running([older]), override: untimed }).eventType, 'tool.before');
});

test('accepted sends set session.activity immediately; failures do not', async () => {
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'running' } }, { generating: true });
  await h.actions.steerRun('sess_a', { message: 'also check docs' });
  const activity = h.state().session.activity;
  assert.deepEqual(activity?.title, { key: 'activity.steerQueued.title' });
  assert.equal(typeof activity.at, 'number');

  const launch = createHarness();
  launch.navigate('/');
  const result = await launch.actions.startRun({ prompt: 'build it' });
  assert.equal(result.ok, true);
  assert.equal(launch.state().session.id, 'sess_new');
  assert.deepEqual(launch.state().session.activity?.title, { key: 'activity.launching.title' }, 'set on the new session slice');

  const failing = createHarness({ api: { continueSession: () => { throw Object.assign(new Error('nope'), { status: 400 }); } } });
  failing.openSession('sess_b', { state: { status: 'completed' } });
  await failing.actions.continueRun('sess_b', { message: 'again' });
  assert.equal(failing.state().session.activity, null);
});

// ---- toasts -----------------------------------------------------------------------------------

test('toasts sit at the top (beside an inline inspector, in the top-bar band on pages) and at most three are kept', () => {
  const state = initialState();
  const at = (route, ui = {}) => toastPlacement({ ...state, route: { name: route, path: '/', params: {} }, ui: { ...state.ui, ...ui } });
  assert.equal(at('session'), 'session');
  assert.equal(at('new', { inspectorOpen: true, viewport: { narrow: false, wide: true, mobile: false } }), 'beside-inspector');
  assert.equal(at('session', { inspectorOpen: true, viewport: { narrow: true, wide: false, mobile: true } }), 'session', 'overlay inspector does not shift toasts');
  assert.equal(at('settings'), 'page');

  setState({ toasts: [] });
  const ids = [1, 2, 3, 4, 5].map((n) => pushToast({ message: 'toast ' + n, timeout: 0 }));
  assert.equal(MAX_VISIBLE_TOASTS, 3);
  assert.deepEqual(getState().toasts.map((toast) => toast.message), ['toast 3', 'toast 4', 'toast 5']);
  ids.forEach((id) => dismissToast(id));
  assert.equal(getState().toasts.length, 0);
});
