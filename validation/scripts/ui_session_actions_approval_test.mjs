import test from 'node:test';
import assert from 'node:assert/strict';
import { t, tx } from '../../internal/webconsole/ui/src/lib/i18n.js';
import { APPROVAL_STORAGE_KEY, coverageOverrideConfirmation, startsNewApprovalOperation } from '../../internal/webconsole/ui/src/lib/approval-operations.js';
import { apiError, createHarness, memoryStorage } from './ui_session_actions_harness.mjs';

const target = { plan_mode_id: 'pm_1', plan_version: 2, expected_revision: 'rev_2' };
const planMode = (extra = {}) => ({ plan_mode_id: 'pm_1', plan_version: 2, approval_revision: 'rev_2', status: 'awaiting_approval', summary: 'Do it', ...extra });
const detail = (extra = {}) => ({ state: { status: 'awaiting_input', run_generation: 1, updated_at: 't0' }, plan_mode: planMode(), ...extra });

function admitted(payload, { replay = false, generation = 2 } = {}) {
  return {
    status: replay ? 'admitted' : 'accepted',
    session_id: 'sess_a',
    approval: { replay, recovery_required: false, lookup: { found: true, binding: { approval_request_id: payload.approval_request_id }, receipt: { stage: 'admitted', recovery: { run_generation: generation } } } },
    current_state: { status: 'running', run_generation: generation },
  };
}

function coverageRejection(payload) {
  return apiError('validation coverage blocks approval', {
    status: 409,
    payload: {
      error: 'validation coverage blocks approval',
      approval: { replay: false, recovery_required: false, lookup: { found: true, binding: { approval_request_id: payload.approval_request_id }, receipt: { stage: 'rejected', rejection: 'validation coverage blocks approval' } } },
    },
  });
}

const saved = (h) => JSON.parse(h.storage.getItem(APPROVAL_STORAGE_KEY)).sessions.sess_a;

test('approve: displayed target + saved request id; a new admission shows execution', async () => {
  const h = createHarness({ api: { approvePlanMode: (_id, payload) => admitted(payload) } });
  h.openSession('sess_a', detail());
  const result = await h.actions.approvePlan('sess_a');
  assert.equal(result.outcome, 'done');
  assert.deepEqual(h.calls[0], { name: 'approvePlanMode', args: ['sess_a', { ...target, override_coverage: false, approval_request_id: 'req_1' }] });
  assert.equal(saved(h).phase, 'admitted');
  assert.equal(h.state().session.approval.record.phase, 'admitted');
  assert.equal(h.state().session.generating, true);
  assert.equal(h.toasts.at(-1).message, t('approval.executionStarted'));
  assert.equal(h.state().session.pendingAction, '');
  assert.deepEqual(h.refreshes, ['sess_a']);
});

test('coverage block: confirm, then override with a NEW request id for the same target', async () => {
  const h = createHarness({ api: { approvePlanMode: (_id, payload) => { if (!payload.override_coverage) throw coverageRejection(payload); return admitted(payload); } } });
  h.openSession('sess_a', detail());
  const result = await h.actions.approvePlan('sess_a');
  assert.equal(result.outcome, 'done');
  const spec = coverageOverrideConfirmation();
  assert.deepEqual(h.dialogs[0], { title: tx(spec.title), body: tx(spec.message), confirmLabel: tx(spec.confirm), danger: true });
  assert.equal(h.calls.length, 2);
  const [first, second] = h.calls.map((call) => call.args[1]);
  assert.equal(first.override_coverage, false);
  assert.equal(second.override_coverage, true);
  assert.notEqual(second.approval_request_id, first.approval_request_id);
  assert.deepEqual({ plan_mode_id: second.plan_mode_id, plan_version: second.plan_version, expected_revision: second.expected_revision }, target);
  assert.equal(saved(h).phase, 'admitted');
});

test('declined coverage override keeps the rejection and says so', async () => {
  const h = createHarness({ confirm: async () => false, api: { approvePlanMode: (_id, payload) => { throw coverageRejection(payload); } } });
  h.openSession('sess_a', detail());
  const result = await h.actions.approvePlan('sess_a');
  assert.equal(result.outcome, 'override_declined');
  assert.equal(h.calls.length, 1);
  assert.equal(h.toasts.at(-1).message, t('approval.notOverridden'));
  assert.equal(h.state().session.approval.record.phase, 'rejected');
});

test('stale target (409) asks for a re-review in both locales and refreshes', async () => {
  for (const [locale, text] of [['en', 'The plan has changed. Please review it again.'], ['zh-CN', '计划已更新，请重新审阅']]) {
    const h = createHarness({ locale, api: { approvePlanMode: () => { throw apiError('approval target conflict: review the current plan', { status: 409, code: 'APPROVAL_TARGET_CONFLICT' }); } } });
    h.openSession('sess_a', detail());
    const result = await h.actions.approvePlan('sess_a');
    assert.equal(result.outcome, 'error');
    assert.equal(h.toasts.at(-1).message, text);
    assert.equal(h.toasts.at(-1).tone, 'warning');
    assert.deepEqual(h.refreshes, ['sess_a']);
  }
});

test('plan changed during the coverage confirmation: no override is sent', async () => {
  let h;
  h = createHarness({
    confirm: async () => {
      h.store.update('session', (session) => ({ ...session, detail: { ...session.detail, plan_mode: planMode({ plan_version: 3, approval_revision: 'rev_3' }) } }));
      return true;
    },
    api: { approvePlanMode: (_id, payload) => { throw coverageRejection(payload); } },
  });
  h.openSession('sess_a', detail());
  await h.actions.approvePlan('sess_a');
  assert.equal(h.calls.length, 1);
  assert.equal(h.toasts.at(-1).message, t('approval.planChanged'));
});

test('a plan without a reviewable target is not approved', async () => {
  const h = createHarness();
  h.openSession('sess_a', detail({ plan_mode: planMode({ approval_revision: '' }) }));
  const result = await h.actions.approvePlan('sess_a');
  assert.equal(result.outcome, 'reload');
  assert.equal(h.calls.length, 0);
  assert.equal(h.toasts.at(-1).message, t('approval.error.reloadPlan'));
});

test('unknown delivery: query the receipt, keep the id, retry only the saved request', async () => {
  let posts = 0;
  const h = createHarness({ api: {
    approvePlanMode: (_id, payload) => { posts += 1; if (posts === 1) throw apiError('bad gateway', { status: 502 }); return admitted(payload); },
  } });
  h.openSession('sess_a', detail());
  await h.actions.approvePlan('sess_a');
  assert.deepEqual(h.calls.map((call) => call.name), ['approvePlanMode', 'getApprovalReceipt']);
  assert.deepEqual(h.calls[1].args, ['sess_a', 'req_1']);
  assert.equal(h.state().session.approval.record.phase, 'not_found');
  await h.actions.checkApproval('sess_a');
  assert.equal(h.calls.at(-1).name, 'getApprovalReceipt');
  await h.actions.retryApproval('sess_a');
  const retry = h.calls.at(-1);
  assert.equal(retry.name, 'approvePlanMode');
  assert.equal(retry.args[1].approval_request_id, 'req_1', 'retry reuses the original id');
  assert.equal(h.state().session.approval.record.phase, 'admitted');
});

test('saved unresolved approvals are re-checked once when the session loads', async () => {
  const storage = memoryStorage({ [APPROVAL_STORAGE_KEY]: JSON.stringify({ version: 1, sessions: { sess_a: {
    session_id: 'sess_a', approval_request_id: 'req_saved', entrypoint: 'planmode', phase: 'unknown',
    parameters: { ...target, override_coverage: false },
  } } }) });
  const h = createHarness({ storage, api: { getApprovalReceipt: (_id, requestID) => admitted({ approval_request_id: requestID }, { replay: true }) } });
  h.openSession('sess_a', detail());
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(h.calls.map((call) => [call.name, call.args[1]]), [['getApprovalReceipt', 'req_saved']]);
  assert.equal(h.state().session.approval.record.phase, 'admitted');
  assert.ok(!h.toasts.some((toast) => toast.message === t('approval.alreadyAdmitted')), 'restore stays quiet for historical receipts');
  h.store.update('session', (session) => ({ ...session, detail: { ...session.detail } }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(h.calls.length, 1, 'restore runs once per session');
});

test('refused recovery: the same target only re-checks; a newly reviewed target posts with a NEW id (spec 17 §7.13)', async () => {
  const recovery = () => apiError('legacy executing plan needs explicit recovery', { status: 409, code: 'APPROVAL_RECOVERY_REQUIRED', action: 'use ordinary continue' });
  const h = createHarness({ api: { approvePlanMode: (_id, payload) => { if (payload.plan_version === 2) throw recovery(); return admitted(payload); } } });
  h.openSession('sess_a', detail());
  await h.actions.approvePlan('sess_a');
  assert.equal(saved(h).phase, 'recovery_required');
  assert.equal(saved(h).approval_request_id, 'req_1');

  // Same target again: query the saved id, never resend it, never mint a new one.
  await h.actions.approvePlan('sess_a');
  assert.deepEqual(h.calls.map((call) => [call.name, call.args[1]?.approval_request_id || call.args[1]]),
    [['approvePlanMode', 'req_1'], ['getApprovalReceipt', 'req_1']]);
  assert.equal(saved(h).phase, 'recovery_required', 'a receipt miss keeps the explicit refusal');

  // A newly reviewed target: the old receipt stays visible until Approve is clicked.
  h.store.update('session', (session) => ({ ...session, detail: { ...session.detail, plan_mode: planMode({ plan_version: 3, approval_revision: 'rev_3' }) } }));
  assert.equal(h.state().session.approval.record.approval_request_id, 'req_1');
  assert.equal(h.state().session.approval.record.phase, 'recovery_required');
  const result = await h.actions.approvePlan('sess_a');
  assert.equal(result.outcome, 'done');
  const posts = h.calls.filter((call) => call.name === 'approvePlanMode').map((call) => call.args[1]);
  assert.deepEqual(posts.map((payload) => payload.approval_request_id), ['req_1', 'req_2'], 'the refused id is never resent');
  assert.deepEqual(posts[1], { plan_mode_id: 'pm_1', plan_version: 3, expected_revision: 'rev_3', override_coverage: false, approval_request_id: 'req_2' });
  assert.equal(saved(h).approval_request_id, 'req_2');
  assert.equal(saved(h).phase, 'admitted');
  assert.equal(h.state().session.generating, true);
});

test('startsNewApprovalOperation: which saved phases a reviewed target may replace', () => {
  const same = { ...target, override_coverage: false };
  const other = { ...target, plan_version: 3, expected_revision: 'rev_3', override_coverage: false };
  const record = (phase) => ({ phase, parameters: same });
  assert.equal(startsNewApprovalOperation(null, same), true);
  for (const phase of ['admitted', 'failed']) {
    assert.equal(startsNewApprovalOperation(record(phase), same), true, phase + ' same target');
    assert.equal(startsNewApprovalOperation(record(phase), other), true, phase + ' new target');
  }
  for (const phase of ['rejected', 'recovery_required']) {
    assert.equal(startsNewApprovalOperation(record(phase), same), false, phase + ' same target re-checks the saved id');
    assert.equal(startsNewApprovalOperation(record(phase), other), true, phase + ' new target gets a new id');
  }
  for (const phase of ['pending', 'unknown', 'not_found', 'prepared']) {
    assert.equal(startsNewApprovalOperation(record(phase), same), false, phase + ' same target');
    assert.equal(startsNewApprovalOperation(record(phase), other), false, phase + ' keeps its id while delivery is unproven');
  }
});

test('linked mission approval uses the mission entrypoint with the displayed target', async () => {
  const h = createHarness({ api: { approveMissionPlan: (_id, payload) => admitted(payload) } });
  h.openSession('sess_a', detail({ plan_mode: planMode({ linked_goal_id: 'goal_1' }), goal: { goal_id: 'goal_1', updated_at: 'g0', objective: 'x' } }));
  await h.actions.approvePlan('sess_a', { entrypoint: 'mission' });
  assert.equal(h.calls[0].name, 'approveMissionPlan');
  assert.equal(h.calls[0].args[1].plan_mode_id, 'pm_1');
});

test('stop: not-owned handles explain the backend detail/action; success toasts', async () => {
  const h = createHarness({ api: { stopSession: () => { throw apiError('active handle not owned', { status: 409, code: 'ACTIVE_HANDLE_NOT_OWNED', detail: 'owned by another process', action: 'use steer with interrupt' }); } } });
  h.openSession('sess_a', { state: { status: 'running' } }, { generating: true });
  await h.actions.stopRun('sess_a');
  assert.deepEqual(h.toasts.at(-1), { tone: 'warning', message: t('composer.stop.notOwned'), detail: 'owned by another process', action: 'use steer with interrupt', raw: { detail: true, action: true } });
  assert.equal(h.state().session.pendingAction, '');
  const ok = createHarness({ api: { stopSession: () => ({ reconciled: true }) } });
  ok.openSession('sess_a', { state: { status: 'running' } }, { generating: true });
  await ok.actions.stopRun();
  assert.equal(ok.calls[0].args[0], 'sess_a');
  assert.equal(ok.toasts.at(-1).message, t('composer.stop.recovered'));
});

test('interrupt reports backend errors with the fallback key', async () => {
  const h = createHarness({ api: { interruptSession: () => { throw apiError('no handle; use steer + interrupt', { status: 409 }); } } });
  h.openSession('sess_a', { state: { status: 'running' } }, { generating: true });
  await h.actions.interruptRun('sess_a');
  assert.equal(h.toasts.at(-1).fallbackKey, 'composer.interrupt.failed');
  assert.equal(h.toasts.at(-1).message, 'no handle; use steer + interrupt');
});

test('revisePlan switches into revising mode, closing an overlay inspector first', async () => {
  const h = createHarness();
  h.openSession('sess_a', detail());
  h.store.update('ui', { inspectorOpen: true, viewport: { narrow: true, wide: false, mobile: false } });
  const focus = h.state().composer.focusToken;
  h.setComposer({ planEnabled: true });
  await h.actions.revisePlan('sess_a');
  assert.equal(h.state().ui.inspectorOpen, false);
  assert.equal(h.state().composer.revising, true);
  assert.equal(h.state().composer.planEnabled, false);
  assert.equal(h.state().composer.focusToken, focus + 1);
  h.actions.cancelRevision();
  assert.equal(h.state().composer.revising, false);
  h.store.update('ui', { inspectorOpen: true, viewport: { narrow: false, wide: true, mobile: false } });
  await h.actions.revisePlan('sess_a');
  assert.equal(h.state().ui.inspectorOpen, true, 'a side panel inspector stays open');
});

test('cancelPlan asks first', async () => {
  let answer = false;
  const h = createHarness({ confirm: async () => answer });
  h.openSession('sess_a', detail());
  assert.equal((await h.actions.cancelPlan('sess_a')).reason, 'declined');
  assert.equal(h.calls.length, 0);
  answer = true;
  await h.actions.cancelPlan('sess_a');
  assert.equal(h.calls[0].name, 'cancelPlanMode');
  assert.equal(h.dialogs[0].danger, true);
  assert.equal(h.toasts.at(-1).message, t('composer.plan.cancelled'));
});

test('answerPlanInput validates every question and the pending request id', async () => {
  const request = { request_id: 'in_1', questions: [
    { id: 'scope', question: 'Which scope?', options: [{ label: 'Narrow', description: 'Focused' }, { label: 'Broad', description: 'Wide' }] },
    { id: 'tone', question: 'Tone?', options: [{ label: 'Calm', description: '' }] },
  ] };
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'awaiting_input' }, plan_mode: { status: 'awaiting_user_input', pending_request: request } });
  const partial = await h.actions.answerPlanInput('sess_a', { requestID: 'in_1', selections: { scope: { label: 'Narrow', value: 'Narrow' } } });
  assert.equal(partial.reason, 'incomplete');
  assert.equal(h.calls.length, 0);
  const stale = await h.actions.answerPlanInput('sess_a', { requestID: 'in_0', selections: {} });
  assert.equal(stale.reason, 'stale');
  const ok = await h.actions.answerPlanInput('sess_a', { requestID: 'in_1', selections: {
    scope: { label: 'Narrow', value: 'Narrow' },
    tone: { label: 'Other', value: 'Direct and brief', is_other: true },
  } });
  assert.equal(ok.ok, true);
  assert.deepEqual(h.calls[0], { name: 'answerPlanModeInput', args: ['sess_a', { requestID: 'in_1', answers: [
    { question_id: 'scope', label: 'Narrow', value: 'Narrow', is_other: false },
    { question_id: 'tone', label: 'Other', value: 'Direct and brief', is_other: true },
  ] }] });
});

test('goal actions: pause/resume direct; complete and clear confirm first', async () => {
  let answer = false;
  const h = createHarness({ confirm: async () => answer });
  h.openSession('sess_a', { state: { status: 'running' }, goal: { goal_id: 'g', status: 'active', objective: 'x' } }, { generating: true });
  await h.actions.pauseGoal('sess_a');
  await h.actions.resumeGoal('sess_a');
  assert.deepEqual(h.calls.map((call) => call.name), ['pauseGoal', 'resumeGoal']);
  assert.equal((await h.actions.clearGoal('sess_a')).reason, 'declined');
  assert.equal((await h.actions.completeGoal('sess_a')).reason, 'declined');
  assert.equal(h.calls.length, 2);
  answer = true;
  await h.actions.clearGoal('sess_a');
  await h.actions.completeGoal('sess_a');
  assert.deepEqual(h.calls.slice(2).map((call) => call.name), ['deleteGoal', 'completeGoal']);
  assert.equal(h.dialogs[0].danger, true, 'clear is a danger confirmation');
  assert.equal(h.toasts.at(-1).message, t('composer.goal.completed'));
  await h.actions.createGoal('sess_a', { objective: 'y' });
  assert.deepEqual(h.calls.at(-1), { name: 'createGoal', args: ['sess_a', { objective: 'y' }] });
});
