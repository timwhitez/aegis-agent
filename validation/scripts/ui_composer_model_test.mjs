import test from 'node:test';
import assert from 'node:assert/strict';
import { initialState } from '../../internal/webconsole/ui/src/state/store.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';
import {
  canSubmit, composerMode, effectiveToggles, placeholderKey, primaryAction, primaryLabelKey,
} from '../../internal/webconsole/ui/src/features/session/controls/composer-model.js';

function state({ route = 'session', status = '', planStatus = '', generating = false, detail = true, handle = false, activity = null, composer = {} } = {}) {
  const base = initialState();
  return {
    ...base,
    route: route === 'new' ? { name: 'new', path: '/', params: {} } : { name: 'session', path: '/s/a', params: { id: 'a' } },
    session: { ...base.session, id: route === 'new' ? '' : 'a', generating, activity, detail: detail ? { state: { status }, active_handle: handle, plan_mode: planStatus ? { status: planStatus, plan_version: 3 } : null } : null },
    composer: { ...base.composer, ...composer },
  };
}

test('composer mode follows the send route', () => {
  assert.equal(composerMode(state({ route: 'new' })).kind, 'start');
  assert.equal(composerMode(state({ detail: false })).kind, 'loading');
  for (const status of ['paused', 'awaiting_input', 'failed', 'completed']) assert.equal(composerMode(state({ status })).kind, 'continue');
  assert.equal(composerMode(state({ status: 'running', generating: true })).kind, 'steer');
  assert.equal(composerMode(state({ status: 'queued' })).kind, 'blocked');
  const revise = composerMode(state({ status: 'awaiting_input', planStatus: 'awaiting_approval' }));
  assert.equal(revise.kind, 'revise');
  assert.equal(revise.planVersion, 3);
  assert.equal(revise.planAwaitingApproval, true);
});

test('launch window: the composer shows "starting" (disabled) until the run is running', () => {
  const launching = composerMode(state({ detail: false, activity: { kind: 'start', at: 1 } }));
  assert.equal(launching.kind, 'starting', 'accepted start, detail not loaded yet');
  assert.equal(composerMode(state({ detail: false, activity: { kind: 'steer', at: 1 } })).kind, 'loading');
  assert.equal(composerMode(state({ status: 'queued', handle: true })).kind, 'starting');
  assert.equal(composerMode(state({ status: '', generating: true })).kind, 'starting');
  assert.equal(composerMode(state({ status: 'cancelled', generating: true })).kind, 'blocked', 'a local flag alone never offers steer');
  assert.equal(composerMode(state({ status: 'running', handle: true })).kind, 'steer');
  assert.equal(placeholderKey(launching, {}), 'composer.placeholder.starting');
  assert.equal(primaryAction(launching, 'x'), 'send');
  assert.equal(canSubmit(launching, { text: 'x', submitting: false }), false);
  assert.equal(launching.canInterrupt, false);
});

// Detail as GET /api/sessions/{id} reports it right after an accepted continue/approve/revise/follow-up
// (handle started after the last state write) or while the finished run's handle is settling.
function handleState(status, { planStatus = '', settling = false, generating = false } = {}) {
  const base = state({ status, planStatus, generating });
  const startedAt = settling ? '2026-11-13T12:00:00Z' : '2026-11-13T12:00:09Z';
  base.session.detail = {
    ...base.session.detail,
    state: { status, updated_at: '2026-11-13T12:00:05Z' },
    active_handle: true,
    active_handle_owner: { state: 'current_process', owned_by_current_process: true, started_at: startedAt },
  };
  return base;
}

test('launch window: a live handle on a non-running state is "starting" before the continuable check', () => {
  for (const status of ['paused', 'awaiting_input', 'completed', 'failed']) {
    const mode = composerMode(handleState(status));
    assert.equal(mode.kind, 'starting', status + ' + handle');
    assert.equal(canSubmit(mode, { text: 'x', submitting: false }), false, status + ' + handle never submits');
    assert.equal(placeholderKey(mode, {}), 'composer.placeholder.starting');
    assert.deepEqual([mode.canPlan, mode.canPickModel, mode.canInterrupt], [false, false, false]);
  }
  const approving = composerMode(handleState('awaiting_input', { planStatus: 'awaiting_approval' }));
  assert.equal(approving.kind, 'starting', 'approve/revise accepted: no second revise');
  // An accepted local launch newer than the loaded detail (no handle in it yet).
  assert.equal(composerMode(state({ status: 'completed', generating: true })).kind, 'starting');
  // The settling handle of a finished run: the server accepts the continue (it waits for the release).
  assert.equal(composerMode(handleState('awaiting_input', { settling: true })).kind, 'continue');
  assert.equal(composerMode(handleState('awaiting_input', { settling: true, planStatus: 'awaiting_approval' })).kind, 'revise');
  // Once the run claimed the session the same handle steers.
  assert.equal(composerMode(handleState('running')).kind, 'steer');
});

test('an orphaned running state steers, as the server accepts steer for any running state', () => {
  const orphan = composerMode(state({ status: 'running' }));
  assert.equal(orphan.kind, 'steer');
  assert.equal(canSubmit(orphan, { text: 'x', submitting: false }), true);
  const notOwned = state({ status: 'running' });
  notOwned.session.detail.active_handle_owner = { state: 'running_not_owned', owned_by_current_process: false };
  assert.equal(composerMode(notOwned).kind, 'steer');
});

test('toolbar controls only where they apply', () => {
  const start = composerMode(state({ route: 'new' }));
  assert.deepEqual([start.canGoal, start.canPlan, start.canInterrupt, start.canPickModel], [true, true, false, true]);
  const cont = composerMode(state({ status: 'paused' }));
  assert.deepEqual([cont.canGoal, cont.canPlan, cont.canInterrupt, cont.canPickModel], [false, true, false, true]);
  const pending = composerMode(state({ status: 'awaiting_input', planStatus: 'awaiting_user_input' }));
  assert.equal(pending.canPlan, false);
  assert.equal(pending.planInputPending, true);
  const steer = composerMode(state({ status: 'running', generating: true }));
  assert.deepEqual([steer.canGoal, steer.canPlan, steer.canInterrupt, steer.canPickModel], [false, false, true, false]);
  assert.deepEqual(effectiveToggles(steer, { goalEnabled: true, planEnabled: true, interrupt: true }), { goal: false, plan: false, interrupt: true });
});

test('placeholder and primary action', () => {
  const start = composerMode(state({ route: 'new' }));
  assert.equal(placeholderKey(start, { goalEnabled: true }), 'composer.placeholder.goal');
  assert.equal(placeholderKey(start, { planEnabled: true }), 'composer.placeholder.plan');
  assert.equal(placeholderKey(start, {}), 'composer.placeholder.new');
  assert.equal(placeholderKey(composerMode(state({ status: 'completed' })), {}), 'composer.placeholder.followUp');
  const steer = composerMode(state({ status: 'running', generating: true }));
  assert.equal(placeholderKey(steer, { interrupt: true }), 'composer.placeholder.interrupt');
  assert.equal(primaryAction(steer, ''), 'stop');
  assert.equal(primaryAction(steer, '  '), 'stop');
  assert.equal(primaryAction(steer, 'more'), 'steer');
  assert.equal(primaryAction(start, ''), 'send');
  assert.equal(primaryLabelKey('send', start), 'composer.action.start');
  assert.equal(canSubmit(start, { text: 'x', submitting: false }), true);
  assert.equal(canSubmit(start, { text: 'x', submitting: true }), false);
  assert.equal(canSubmit(composerMode(state({ status: 'queued' })), { text: 'x' }), false);
});

test('every key the composer model can return exists in both locales', () => {
  const statuses = ['', 'paused', 'awaiting_input', 'failed', 'completed', 'running', 'queued'];
  const keys = new Set();
  for (const route of ['new', 'session']) {
    for (const status of statuses) {
      for (const planStatus of ['', 'awaiting_approval', 'awaiting_user_input']) {
        for (const generating of [false, true]) {
          for (const detail of [true, false]) {
            const mode = composerMode(state({ route, status, planStatus, generating, detail, activity: detail ? null : { kind: 'start', at: 1 } }));
            for (const composer of [{}, { goalEnabled: true }, { planEnabled: true }, { interrupt: true }]) keys.add(placeholderKey(mode, composer));
            for (const text of ['', 'x']) keys.add(primaryLabelKey(primaryAction(mode, text), mode));
          }
        }
      }
    }
  }
  for (const key of keys) {
    for (const locale of ['zh-CN', 'en']) assert.ok(Object.hasOwn(dictionaries[locale], key), locale + ' missing ' + key);
  }
});
