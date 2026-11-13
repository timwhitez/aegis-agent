import test from 'node:test';
import assert from 'node:assert/strict';
import { initialState } from '../../internal/webconsole/ui/src/state/store.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';
import {
  APPROVAL_WAIT_KEYS, approvalCardState, canSubmit, composerMode, effectiveToggles, placeholderKey, primaryAction, primaryLabelKey,
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

// ---- inline approval card (ApprovalCard.js) ----------------------------------------------------

function cardState(base, { pendingAction = '', approvalBusy = false } = {}) {
  return { ...base, session: { ...base.session, pendingAction, approval: approvalBusy ? { busy: true } : null } };
}

test('approval card: idle plan awaiting approval offers approve, revise and cancel', () => {
  const card = approvalCardState(cardState(state({ status: 'awaiting_input', planStatus: 'awaiting_approval' })));
  assert.deepEqual(card, { wait: '', approving: false, approveDisabled: false, reviseDisabled: false, cancelDisabled: false });
});

test('approval card: the revise launch window disables approve/cancel exactly when the composer is "starting"', () => {
  // Accepted revise: the live handle started after the last state write while plan_mode stays awaiting_approval.
  const launch = handleState('awaiting_input', { planStatus: 'awaiting_approval' });
  assert.equal(composerMode(launch).kind, 'starting');
  const card = approvalCardState(cardState(launch));
  assert.equal(card.wait, 'starting');
  assert.deepEqual([card.approveDisabled, card.reviseDisabled, card.cancelDisabled], [true, true, true]);
  // Accepted revise before the refreshed detail shows a handle: the session.generating marker.
  const marker = state({ status: 'awaiting_input', planStatus: 'awaiting_approval', generating: true });
  assert.equal(composerMode(marker).kind, 'starting');
  assert.equal(approvalCardState(cardState(marker)).wait, 'starting');
  assert.equal(approvalCardState(cardState(marker)).approveDisabled, true);
});

test('approval card: a settling handle of the finished planning run still allows approve', () => {
  const settled = handleState('awaiting_input', { planStatus: 'awaiting_approval', settling: true });
  assert.equal(composerMode(settled).kind, 'revise');
  const card = approvalCardState(cardState(settled));
  assert.equal(card.wait, '');
  assert.equal(card.approveDisabled, false);
  assert.equal(card.cancelDisabled, false);
});

test('approval card: running/queued planning run is "settling" (revise stays available)', () => {
  for (const status of ['running', 'queued', 'pending']) {
    const card = approvalCardState(cardState(state({ status, planStatus: 'awaiting_approval' })));
    assert.equal(card.wait, 'settling', status);
    assert.deepEqual([card.approveDisabled, card.reviseDisabled, card.cancelDisabled], [true, false, true], status);
  }
});

test('approval card: in-flight revise/approve/cancel requests lock the conflicting actions', () => {
  const idle = state({ status: 'awaiting_input', planStatus: 'awaiting_approval' });
  const sending = approvalCardState(cardState(idle, { pendingAction: 'revise' }));
  assert.equal(sending.wait, 'sending');
  assert.deepEqual([sending.approveDisabled, sending.reviseDisabled, sending.cancelDisabled], [true, true, true]);
  const approving = approvalCardState(cardState(idle, { pendingAction: 'approve' }));
  assert.equal(approving.approving, true);
  assert.deepEqual([approving.approveDisabled, approving.reviseDisabled, approving.cancelDisabled], [false, true, true], 'approve keeps its own pending button');
  assert.equal(approvalCardState(cardState(idle, { approvalBusy: true })).approving, true);
  const cancelling = approvalCardState(cardState(idle, { pendingAction: 'cancel-plan' }));
  assert.deepEqual([cancelling.approveDisabled, cancelling.cancelDisabled], [true, true]);
  const checking = approvalCardState(cardState(idle, { pendingAction: 'approval-check' }));
  assert.equal(checking.approveDisabled, true);
});

test('approval card: approve is never enabled while the composer refuses sends as "starting"', () => {
  for (const status of ['', 'paused', 'awaiting_input', 'failed', 'completed', 'running', 'queued']) {
    for (const generating of [false, true]) {
      for (const handle of ['none', 'live', 'settling']) {
        const base = handle === 'none'
          ? state({ status, planStatus: 'awaiting_approval', generating })
          : handleState(status, { planStatus: 'awaiting_approval', generating, settling: handle === 'settling' });
        const kind = composerMode(base).kind;
        const card = approvalCardState(cardState(base));
        if (kind === 'starting' || kind === 'steer') assert.equal(card.approveDisabled, true, [status, generating, handle, kind].join('/'));
        if (kind === 'revise') assert.equal(card.approveDisabled, false, [status, generating, handle, kind].join('/'));
      }
    }
  }
});

test('approval wait statuses exist in both locales', () => {
  for (const key of Object.values(APPROVAL_WAIT_KEYS)) {
    for (const locale of ['zh-CN', 'en']) assert.ok(Object.hasOwn(dictionaries[locale], key), locale + ' missing ' + key);
  }
});
