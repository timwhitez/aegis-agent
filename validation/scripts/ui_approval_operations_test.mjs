import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import {
  APPROVAL_STORAGE_KEY,
  ApprovalOperationController,
  ApprovalOperationError,
  approvalActionError,
  approvalNoticeState,
  approvalOutcomeNotice,
  approvalPresentation,
  approvalViewToken,
  approveUnlinkedMission,
  canPresentApprovalAdmission,
  coverageOverrideConfirmation,
  displayedApprovalTarget,
  executeReviewedApproval,
  generateApprovalRequestID,
  goalActionIdentity,
  isCoverageApprovalBlock,
  isCurrentApprovalViewToken,
  isDisplayedApprovalTarget,
  isNewApprovalAdmission,
  isStaleApprovalTarget,
  planInputActionIdentity,
  planModeActionIdentity
} from '../../internal/webconsole/ui/src/lib/approval-operations.js';

const target = { plan_mode_id: 'plan_reviewed', plan_version: 1, expected_revision: 'revision_reviewed' };

function response(replay = false) {
  return { session_id: 'session_a', status: replay ? 'admitted' : 'accepted', approval: {
    replay, recovery_required: false, lookup: { found: true,
      binding: { approval_request_id: 'request_fixture', operation_id: 'request_fixture', parameters: target },
      receipt: { stage: 'admitted', operation_id: 'request_fixture', target }
    }
  }, current_state: { status: 'completed', run_generation: 1 } };
}

function receipt(payload, stage = 'admitted', extra = {}) {
  return { ...response(true), ...extra, approval: { replay: true, recovery_required: false,
    lookup: { found: true, binding: { approval_request_id: payload.approval_request_id }, receipt: { stage, target } }, ...extra.approval } };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

// Handler harness: replays the v1 handlePlanModeAction / handleGoalAction
// approve flow using only this module's pure helpers.
function harness({ linked = false, reply = response(), storage } = {}) {
  const values = storage || new Map();
  const calls = [];
  const generating = [];
  const toasts = [];
  const queries = [];
  const ctx = {
    sessionId: 'session_a',
    epoch: 0,
    detail: {
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_a', updated_at: 'same' },
      plan_mode: { ...target, approval_revision: target.expected_revision, status: 'awaiting_approval', linked_goal_id: linked ? 'goal_a' : '' }
    },
    crypto: { randomUUID: () => 'request_' + (calls.length + 1) },
    confirm: async () => true,
    getApprovalReceipt: async (sessionID, requestID) => {
      queries.push({ sessionID, requestID });
      const err = new Error('Not found');
      err.status = 404;
      throw err;
    },
    unlinkedApprove: null
  };
  function replyFor(payload) {
    if (typeof reply === 'function') return reply(payload);
    const result = structuredClone(reply);
    if (result.approval) result.approval.lookup.binding.approval_request_id = payload.approval_request_id;
    return result;
  }
  let controller = null;
  ctx.controller = () => {
    if (!controller) {
      controller = new ApprovalOperationController({
        storage: { getItem: (key) => values.get(key) || null, setItem: (key, value) => values.set(key, value) },
        requestID: () => generateApprovalRequestID(ctx.crypto),
        submit: async (sessionID, entrypoint, payload) => {
          calls.push({ sessionID, entrypoint, payload: structuredClone(payload), stored: [...values.values()] });
          return replyFor(payload);
        },
        query: (sessionID, requestID) => ctx.getApprovalReceipt(sessionID, requestID)
      });
    }
    return controller;
  };
  async function approve(entrypoint) {
    const sessionID = ctx.sessionId;
    const goalIdentity = goalActionIdentity(ctx.detail.goal);
    const planIdentity = planModeActionIdentity(ctx.detail.plan_mode);
    const token = approvalViewToken({ sessionID, epoch: ctx.epoch, state: ctx.detail.state });
    const isViewCurrent = () => isCurrentApprovalViewToken(token, { sessionID: ctx.sessionId, epoch: ctx.epoch });
    try {
      const result = await executeReviewedApproval({
        controller: ctx.controller(),
        sessionID,
        entrypoint,
        target: displayedApprovalTarget(ctx.detail.plan_mode),
        isViewCurrent,
        isTargetDisplayed: (value) => isDisplayedApprovalTarget(ctx.detail.plan_mode, value),
        isGoalIdentityCurrent: entrypoint === 'mission' ? () => goalActionIdentity(ctx.detail.goal) === goalIdentity : undefined,
        confirmCoverageOverride: () => ctx.confirm()
      });
      const outcomeNotice = approvalOutcomeNotice(result.outcome);
      if (outcomeNotice) toasts.push(outcomeNotice.key);
      if (result.outcome === 'busy') return;
      const presentation = approvalPresentation(result.response, {
        viewCurrent: isViewCurrent(), sessionID: ctx.sessionId, goalID: ctx.detail.goal?.goal_id,
        savedRequestID: ctx.controller().get(ctx.sessionId)?.approval_request_id,
        loadedState: ctx.detail.state, viewToken: token
      });
      if (presentation.generating) generating.push(true);
      if (presentation.toast) toasts.push(presentation.toast.key);
    } catch (err) {
      const current = entrypoint === 'mission' ? goalActionIdentity(ctx.detail.goal) === goalIdentity : planModeActionIdentity(ctx.detail.plan_mode) === planIdentity;
      if (ctx.sessionId === sessionID && (current || isStaleApprovalTarget(err))) {
        const slot = approvalActionError(err, 'plan.actionFailed');
        toasts.push(slot.key || slot.raw);
      }
    }
  }
  ctx.handlePlanApprove = () => approve('planmode');
  ctx.handleGoalApprovePlan = async () => {
    if (ctx.detail.plan_mode?.linked_goal_id) return approve('mission');
    const outcome = await approveUnlinkedMission({
      approve: async (payload) => { calls.push({ payload: structuredClone(payload) }); return ctx.unlinkedApprove ? ctx.unlinkedApprove(payload) : replyFor(payload); },
      isCurrent: () => true,
      confirmCoverageOverride: () => ctx.confirm()
    });
    const notice = approvalOutcomeNotice(outcome.outcome);
    if (notice) toasts.push(notice.key);
  };
  return { ctx, calls, generating, toasts, queries, values };
}

function admittedResponse(payload, generation, status = 'running') {
  const result = receipt(payload);
  result.status = 'accepted';
  result.approval.replay = false;
  result.approval.lookup.receipt.recovery = { run_generation: generation };
  result.current_state = { status, run_generation: generation };
  return result;
}

function controllerHarness({ submit, query, values = new Map() } = {}) {
  let serial = 0;
  const posts = [];
  const queries = [];
  const controller = new ApprovalOperationController({
    storage: { getItem: (key) => values.get(key) || null, setItem: (key, value) => values.set(key, value) },
    requestID: () => 'operation_' + (++serial),
    submit: async (sessionID, entrypoint, payload) => {
      posts.push({ sessionID, entrypoint, payload: structuredClone(payload), stored: JSON.parse(values.get(APPROVAL_STORAGE_KEY)) });
      return submit ? submit(sessionID, payload) : receipt(payload);
    },
    query: async (sessionID, id) => {
      queries.push({ sessionID, id });
      if (query) return query(sessionID, id);
      const err = new Error('receipt missing');
      err.status = 404;
      throw err;
    }
  });
  return { controller, posts, queries, values };
}

// ---------------------------------------------------------------------------
// Request IDs (ported)
// ---------------------------------------------------------------------------

for (const linked of [false, true]) {
  test((linked ? 'mission' : 'planmode') + ' approval supports secure random IDs without randomUUID on trusted HTTP', async () => {
    const h = harness({ linked });
    let randomCalls = 0;
    h.ctx.crypto = { getRandomValues(bytes) { randomCalls += 1; bytes.fill(randomCalls); return bytes; } };
    await (linked ? h.ctx.handleGoalApprovePlan() : h.ctx.handlePlanApprove());
    assert.equal(h.calls.length, 1);
    assert.equal(randomCalls, 1);
    const id = h.calls[0].payload.approval_request_id;
    assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    assert.ok(h.calls[0].stored.some((value) => value.includes(id)), 'fallback ID must be persisted before POST');
    assert.notEqual(generateApprovalRequestID(h.ctx.crypto), id, 'each new identity consumes fresh secure randomness');
  });
}

test('approval ID retains native randomUUID and fails before POST without secure randomness', async () => {
  assert.equal(generateApprovalRequestID({ randomUUID: () => 'request_1', getRandomValues: () => { throw new Error('native UUID should be preferred'); } }), 'request_1');
  const absent = harness();
  absent.ctx.crypto = {};
  await absent.ctx.handlePlanApprove();
  assert.equal(absent.calls.length, 0);
  assert.equal(absent.values.size, 0);
  assert.deepEqual(absent.toasts, ['approval.error.secureRandomUnavailable']);
  for (const missing of [null, {}]) {
    assert.throws(() => generateApprovalRequestID(missing), (err) => err instanceof ApprovalOperationError && err.key === 'approval.error.secureRandomUnavailable');
  }
  assert.match(generateApprovalRequestID(), /^[0-9a-f-]{36}$/, 'default reads globalThis.crypto lazily');
});

// ---------------------------------------------------------------------------
// Admission presentation (ported)
// ---------------------------------------------------------------------------

test('late 202 never revives the same approval generation already loaded as completed', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  h.ctx.detail.state = { status: 'awaiting_input', run_generation: 'run_before_approval', updated_at: 'before' };
  const action = h.ctx.handlePlanApprove();
  await Promise.resolve();
  h.ctx.detail.state = { status: 'completed', run_generation: 'run_approved', updated_at: 'settled' };
  h.ctx.detail.plan_mode.status = 'executing';
  pending.resolve(admittedResponse(h.calls[0].payload, 'run_approved'));
  await action;
  assert.deepEqual(h.generating, [], 'loaded terminal facts must outrank the delayed accepted envelope');
  assert.equal(h.ctx.controller().response('session_a').approval.lookup.receipt.recovery.run_generation, 'run_approved');
});

test('late 202 cannot overwrite an ordinary Continue generation', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  h.ctx.detail.state = { status: 'awaiting_input', run_generation: 'run_before_approval', updated_at: 'before' };
  const action = h.ctx.handlePlanApprove();
  await Promise.resolve();
  h.ctx.detail.state = { status: 'running', run_generation: 'run_ordinary_followup', updated_at: 'followup' };
  pending.resolve(admittedResponse(h.calls[0].payload, 'run_approved'));
  await action;
  assert.deepEqual(h.generating, [], 'old admission cannot present itself as the current follow-up run');
});

test('fresh accepted approval displays its new claim before the next detail refresh', async () => {
  const h = harness({ reply: (payload) => admittedResponse(payload, 'run_new_approval') });
  h.ctx.detail.state = { status: 'completed', run_generation: 'run_previous_turn', updated_at: 'before' };
  await h.ctx.handlePlanApprove();
  assert.deepEqual(h.generating, [true]);
  assert.ok(h.toasts.includes('approval.executionStarted'));
});

test('accepted response for the loaded running generation remains a legitimate execution control', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  const action = h.ctx.handlePlanApprove();
  await Promise.resolve();
  h.ctx.detail.state = { status: 'running', run_generation: 'run_approved' };
  pending.resolve(admittedResponse(h.calls[0].payload, 'run_approved'));
  await action;
  assert.deepEqual(h.generating, [true]);
});

test('a newly admitted receipt already returned as completed never manufactures a pending stage', async () => {
  const h = harness({ reply: (payload) => admittedResponse(payload, 'run_already_settled', 'completed') });
  await h.ctx.handlePlanApprove();
  assert.deepEqual(h.generating, []);
});

test('accepted envelope without a provable receipt generation never invents a running identity', async () => {
  const h = harness({ reply: (payload) => admittedResponse(payload, '') });
  await h.ctx.handlePlanApprove();
  assert.deepEqual(h.generating, []);
  assert.equal(canPresentApprovalAdmission(admittedResponse({}, 'g'), approvalViewToken({}), null), false);
});

for (const [cached, live, liveGeneration] of [['running', 'completed', 'run_approved'], ['completed', 'running', 'run_ordinary_followup']]) {
  test('receipt history ' + cached + ' never overrides the loaded current session ' + live, async () => {
    const h = harness({ reply: (payload) => {
      const result = admittedResponse(payload, 'run_approved', cached);
      result.status = 'admitted';
      result.approval.replay = true;
      return result;
    } });
    await h.ctx.handlePlanApprove();
    h.ctx.detail.state = { status: live, run_generation: liveGeneration };
    const controller = h.ctx.controller();
    const notice = approvalNoticeState({ record: controller.get('session_a'), response: controller.response('session_a'), busy: controller.busy('session_a'), loadedStatus: h.ctx.detail.state.status });
    assert.equal(notice.currentStatus.status, live);
    assert.equal(notice.phase, 'admitted');
    assert.deepEqual(notice.message, { key: 'approval.notice.admitted' });
    assert.equal(controller.response('session_a').current_state.status, cached);
    assert.deepEqual(h.generating, []);
    assert.ok(h.toasts.includes('approval.alreadyAdmitted'));
  });
}

test('Approve persists an ID and immutable target before its first request', async () => {
  const h = harness();
  await h.ctx.handlePlanApprove();
  assert.equal(h.calls.length, 1);
  assert.ok(h.calls[0].payload.approval_request_id);
  assert.deepEqual(Object.fromEntries(Object.keys(target).map((key) => [key, h.calls[0].payload[key]])), target);
  assert.ok(h.calls[0].stored.some((value) => value.includes(h.calls[0].payload.approval_request_id)), 'identity must be saved before POST');
});

test('Approve does not mark an admitted replay as generating', async () => {
  const h = harness({ reply: response(true) });
  await h.ctx.handlePlanApprove();
  assert.deepEqual(h.generating, []);
  assert.equal(JSON.parse(h.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.phase, 'admitted');
});

// ---------------------------------------------------------------------------
// Controller (ported verbatim, error messages are now i18n keys)
// ---------------------------------------------------------------------------

test('two visible aliases share one in-flight POST and persisted ID', async () => {
  const pending = deferred();
  const h = controllerHarness({ submit: () => pending.promise });
  const a = h.controller.start('session_a', 'planmode', target);
  const b = h.controller.start('session_a', 'mission', target);
  assert.equal(a, b);
  await Promise.resolve();
  assert.equal(h.posts.length, 1);
  assert.deepEqual(h.posts[0].stored.sessions.session_a.parameters, { ...target, override_coverage: false });
  pending.resolve(receipt(h.posts[0].payload));
  await a;
});

test('unknown delivery queries before any retry and 404 exposes only original-request retry', async () => {
  const h = controllerHarness({ submit: () => { throw new TypeError('network lost'); } });
  await h.controller.start('session_a', 'planmode', target);
  assert.equal(h.posts.length, 1);
  assert.equal(h.queries.length, 1);
  assert.equal(h.controller.get('session_a').phase, 'not_found');
  await h.controller.check('session_a');
  assert.equal(h.posts.length, 1);
  await h.controller.retry('session_a');
  assert.equal(h.posts.length, 2);
  assert.deepEqual(h.posts[1].payload, h.posts[0].payload);
});

test('unknown response after server admission resolves through readonly receipt without another POST', async () => {
  const h = controllerHarness({ submit: () => { throw new TypeError('response lost'); }, query: (sessionID, id) => receipt({ approval_request_id: id }) });
  const result = await h.controller.start('session_a', 'mission', target);
  assert.equal(result.approval.replay, true);
  assert.equal(h.controller.get('session_a').phase, 'admitted');
  assert.equal(h.posts.length, 1);
  assert.equal(h.queries.length, 1);
});

test('reload restores original ID and parameters through a query without executing', async () => {
  const a = controllerHarness({ submit: () => { throw new TypeError('network lost'); } });
  await a.controller.start('session_a', 'mission', { ...target, system: 'reviewed system', provider_options: { temperature: 0.2 } });
  const b = controllerHarness({ values: a.values });
  await b.controller.restore('session_a');
  assert.equal(b.posts.length, 0);
  assert.equal(b.queries[0].id, a.posts[0].payload.approval_request_id);
  await b.controller.retry('session_a');
  assert.deepEqual(b.posts[0].payload, a.posts[0].payload);
});

test('prepared query without a recovery flag remains unadmitted until explicit POST proves admission', async () => {
  const h = controllerHarness({ submit: (sessionID, payload) => receipt(payload, 'prepared'), query: (sessionID, id) => receipt({ approval_request_id: id }, 'prepared') });
  await h.controller.start('session_a', 'planmode', target);
  await h.controller.check('session_a');
  assert.equal(h.controller.get('session_a').phase, 'prepared');
  assert.equal(h.posts.length, 1);
  await h.controller.retry('session_a');
  assert.equal(h.posts.length, 2);
  assert.deepEqual(h.posts[1].payload, h.posts[0].payload);
});

test('coverage confirmation creates a new ID with the same captured target and changes only override', async () => {
  const h = controllerHarness({ submit: (sessionID, payload) => receipt(payload, payload.override_coverage ? 'admitted' : 'rejected') });
  await h.controller.start('session_a', 'planmode', target);
  await h.controller.override('session_a', target);
  assert.notEqual(h.posts[0].payload.approval_request_id, h.posts[1].payload.approval_request_id);
  assert.deepEqual({ ...h.posts[1].payload, approval_request_id: h.posts[0].payload.approval_request_id, override_coverage: false }, h.posts[0].payload);
});

test('caller mutation cannot change captured nested explicit parameters on retry', async () => {
  const params = { ...target, provider_options: { temperature: 0.2 }, system: 'first' };
  const h = controllerHarness({ submit: () => { throw new TypeError('network lost'); } });
  const initial = h.controller.start('session_a', 'planmode', params);
  params.provider_options.temperature = 0.9;
  params.system = 'second';
  params.expected_revision = 'new';
  await initial;
  await h.controller.retry('session_a');
  assert.deepEqual(h.posts[1].payload, h.posts[0].payload);
  assert.equal(h.posts[1].payload.provider_options.temperature, 0.2);
});

test('late session A response preserves session B pending identity and parameters', async () => {
  const a = deferred();
  const b = deferred();
  const h = controllerHarness({ submit: (sessionID) => (sessionID === 'session_a' ? a.promise : b.promise) });
  const taskA = h.controller.start('session_a', 'planmode', target);
  const taskB = h.controller.start('session_b', 'mission', { ...target, expected_revision: 'revision_b' });
  await Promise.resolve();
  const beforeB = h.controller.get('session_b');
  a.resolve(receipt(h.posts[0].payload));
  await taskA;
  assert.deepEqual(h.controller.get('session_b'), beforeB);
  b.resolve(receipt(h.posts[1].payload));
  await taskB;
});

test('late A response after A to B to A navigation never changes generating', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  const task = h.ctx.handlePlanApprove();
  await Promise.resolve();
  h.ctx.sessionId = 'session_b'; h.ctx.epoch += 1;
  h.ctx.sessionId = 'session_a'; h.ctx.epoch += 1;
  const payload = h.calls[0].payload;
  pending.resolve(receipt(payload, 'admitted', { status: 'accepted', approval: { ...receipt(payload).approval, replay: false } }));
  await task;
  assert.deepEqual(h.generating, []);
});

test('old handler ignores its late admission after another tab saved a newer operation', async () => {
  const pending = deferred();
  const a = harness({ reply: () => pending.promise });
  const oldAction = a.ctx.handlePlanApprove();
  await Promise.resolve();
  const oldPayload = a.calls[0].payload;
  const b = harness({ storage: a.values, reply: response(true) });
  b.ctx.crypto.randomUUID = () => 'peer_new_operation';
  b.ctx.getApprovalReceipt = async () => receipt(oldPayload);
  await b.ctx.controller().check('session_a');
  await b.ctx.handlePlanApprove();
  assert.equal(b.calls[0].payload.approval_request_id, 'peer_new_operation');
  pending.resolve(receipt(oldPayload, 'admitted', { status: 'accepted', approval: { ...receipt(oldPayload).approval, replay: false } }));
  await oldAction;
  assert.deepEqual(a.generating, [], 'old accepted response must not impersonate the new pending identity');
  assert.equal(JSON.parse(a.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.approval_request_id, 'peer_new_operation');
});

test('local storage failure prevents all approval POSTs', async () => {
  let posts = 0;
  const controller = new ApprovalOperationController({ storage: { getItem: () => null, setItem() { throw new Error('disabled'); } },
    requestID: () => 'saved_request', submit: async () => { posts += 1; }, query: async () => {} });
  await assert.rejects(controller.start('session_a', 'planmode', target), (err) => err.key === 'approval.error.saveFailed' && err.message === 'approval.error.saveFailed');
  assert.equal(posts, 0);
});

test('linked mission alias also persists identity and leaves replay generation unchanged', async () => {
  const h = harness({ linked: true, reply: response(true) });
  await h.ctx.handleGoalApprovePlan();
  assert.equal(h.calls.length, 1);
  assert.ok(h.calls[0].payload.approval_request_id);
  assert.equal(JSON.parse(h.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.entrypoint, 'mission');
  assert.deepEqual(h.generating, []);
});

test('legacy recovery outcome offers an ordinary Continue choice and never starts generation', async () => {
  const h = harness({ reply: () => { const err = new Error('explicit recovery required'); err.status = 409; err.code = 'APPROVAL_RECOVERY_REQUIRED'; throw err; } });
  await h.ctx.handlePlanApprove();
  const notice = approvalNoticeState({ record: h.ctx.controller().get('session_a') });
  assert.equal(notice.visible, true);
  assert.equal(notice.phase, 'recovery_required');
  assert.deepEqual(notice.message, { key: 'approval.notice.recovery_required' });
  assert.deepEqual(notice.serverNotice, { raw: 'explicit recovery required' });
  assert.deepEqual(notice.actions.map((action) => action.action), ['check', 'continue']);
  assert.deepEqual(h.generating, []);
});

test('unknown 404 notice has Check and Retry controls bound to the original identity', async () => {
  const h = harness({ reply: () => { throw new TypeError('network lost'); } });
  await h.ctx.handlePlanApprove();
  const controller = h.ctx.controller();
  const notice = approvalNoticeState({ record: controller.get('session_a'), response: controller.response('session_a'), busy: controller.busy('session_a') });
  assert.equal(notice.phase, 'not_found');
  assert.deepEqual(notice.requestID, { raw: h.calls[0].payload.approval_request_id });
  assert.deepEqual(notice.actions.map((action) => [action.action, action.disabled]), [['check', false], ['retry', false]]);
  assert.deepEqual(notice.message, { key: 'approval.notice.not_found' });
  assert.deepEqual(h.generating, []);
});

test('prepared response with recovery_required false never marks the view as generating', async () => {
  const h = harness({ reply: (payload) => receipt(payload, 'prepared') });
  await h.ctx.handlePlanApprove();
  assert.equal(JSON.parse(h.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.phase, 'prepared');
  assert.deepEqual(h.generating, []);
});

test('navigation restoration never attaches a new view to an old in-flight POST', async () => {
  const pending = deferred();
  const h = controllerHarness({ submit: () => pending.promise });
  const old = h.controller.start('session_a', 'planmode', target);
  await Promise.resolve();
  assert.equal(h.controller.restore('session_a'), null);
  assert.equal(h.queries.length, 0);
  pending.resolve(receipt(h.posts[0].payload));
  await old;
});

test('definitive stale rejection survives restore 404 and permits a newly reviewed target', async () => {
  const h = harness({ reply: (payload) => {
    if (payload.plan_version === 1) { const err = new Error('approval target no longer matches'); err.status = 409; err.code = 'APPROVAL_TARGET_CONFLICT'; throw err; }
    return receipt(payload);
  } });
  await h.ctx.handlePlanApprove();
  assert.equal(h.ctx.controller().get('session_a').phase, 'failed');
  assert.deepEqual(h.toasts, ['approval.planChanged']);
  const reloaded = harness({ storage: h.values, reply: (payload) => receipt(payload) });
  await reloaded.ctx.controller().restore('session_a');
  const restoredPhase = reloaded.ctx.controller().get('session_a').phase;
  Object.assign(reloaded.ctx.detail.plan_mode, { plan_version: 2, approval_revision: 'reviewed_revision_v2' });
  reloaded.ctx.crypto.randomUUID = () => 'request_new_review_v2';
  await reloaded.ctx.handlePlanApprove();
  assert.equal(reloaded.calls.length, 1, 'fresh visible V2 approval must POST after the definite V1 rejection');
  assert.equal(restoredPhase, 'failed', 'a readonly miss cannot erase definitive non-admission');
  assert.notEqual(reloaded.calls[0].payload.approval_request_id, h.calls[0].payload.approval_request_id);
  assert.equal(reloaded.calls[0].payload.expected_revision, 'reviewed_revision_v2');
  assert.deepEqual(reloaded.generating, []);
});

test('unknown transport delivery plus 404 preserves the original ID even when the displayed target changes', async () => {
  const h = harness({ reply: () => { throw new TypeError('response lost'); } });
  await h.ctx.handlePlanApprove();
  const original = h.ctx.controller().get('session_a');
  Object.assign(h.ctx.detail.plan_mode, { plan_version: 2, approval_revision: 'reviewed_revision_v2' });
  await h.ctx.handlePlanApprove();
  assert.equal(h.calls.length, 1);
  assert.equal(h.ctx.controller().get('session_a').approval_request_id, original.approval_request_id);
  assert.deepEqual(h.ctx.controller().get('session_a').parameters, original.parameters);
  assert.deepEqual(h.generating, []);
});

const repairedGoal = { schema_version: 1, session_id: 'session_a', goal_id: 'goal_a', mode: 'mission',
  objective: 'Reviewed linked mission', status: 'active', mission: { plan_status: 'approved', approved_at: '2026-10-03T01:00:00Z' } };

test('linked mission releases a successful facts-only intent without a receipt or generation', async () => {
  const h = harness({ linked: true, reply: repairedGoal });
  await h.ctx.handleGoalApprovePlan();
  assert.equal(h.calls.length, 1);
  assert.ok(h.calls[0].payload.approval_request_id);
  assert.equal(h.ctx.controller().get('session_a'), null, 'HTTP200 SessionGoal completes facts-only intent');
  assert.equal(h.queries.length, 0, 'confirmed facts response needs no receipt lookup');
  assert.deepEqual(h.generating, []);
  assert.deepEqual(h.toasts, ['approval.goalPlanUpdated']);
});

test('a newly reviewed target can execute after linked facts-only completion', async () => {
  const h = harness({ linked: true, reply: (payload) => (payload.plan_version === 1 ? repairedGoal : receipt(payload)) });
  await h.ctx.handleGoalApprovePlan();
  Object.assign(h.ctx.detail.plan_mode, { plan_version: 2, approval_revision: 'reviewed_revision_v2' });
  await h.ctx.handleGoalApprovePlan();
  assert.equal(h.calls.length, 2);
  assert.notEqual(h.calls[0].payload.approval_request_id, h.calls[1].payload.approval_request_id);
  assert.equal(h.calls[1].payload.plan_version, 2);
  assert.deepEqual(h.generating, []);
});

test('direct approval never mistakes a receipt-free Goal response for admission or mission facts', async () => {
  const h = harness({ reply: repairedGoal });
  await h.ctx.handlePlanApprove();
  assert.equal(h.ctx.controller().get('session_a').phase, 'not_found');
  assert.equal(h.queries.length, 1);
  assert.deepEqual(h.generating, []);
});

test('late linked facts completion cannot release a newer operation saved by a peer tab', async () => {
  const pending = deferred();
  const a = harness({ linked: true, reply: () => pending.promise });
  const oldAction = a.ctx.handleGoalApprovePlan();
  await Promise.resolve();
  const oldID = a.calls[0].payload.approval_request_id;
  const b = harness({ linked: true, storage: a.values, reply: (payload) => (payload.plan_version === 1 ? repairedGoal : receipt(payload)) });
  await b.ctx.controller().check('session_a');
  await b.ctx.controller().retry('session_a');
  assert.equal(b.calls[0].payload.approval_request_id, oldID);
  Object.assign(b.ctx.detail.plan_mode, { plan_version: 2, approval_revision: 'peer_reviewed_revision_v2' });
  b.ctx.crypto.randomUUID = () => 'peer_reviewed_operation_v2';
  await b.ctx.handleGoalApprovePlan();
  pending.resolve(repairedGoal);
  await oldAction;
  assert.equal(a.ctx.controller().get('session_a').approval_request_id, 'peer_reviewed_operation_v2');
  assert.equal(a.ctx.controller().get('session_a').parameters.expected_revision, 'peer_reviewed_revision_v2');
  assert.deepEqual(a.generating, []);
  assert.deepEqual(a.toasts, []);
});

test('ordinary unlinked mission facts control keeps its target-free approval', async () => {
  const h = harness({ reply: { status: 'approved' } });
  h.ctx.detail.plan_mode = null;
  await h.ctx.handleGoalApprovePlan();
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.calls[0].payload, {});
  assert.deepEqual(h.generating, []);
  assert.deepEqual(h.toasts, ['approval.goalPlanApproved']);
});

test('unlinked mission coverage block retries once with an explicit override', async () => {
  const h = harness();
  h.ctx.detail.plan_mode = null;
  let first = true;
  h.ctx.unlinkedApprove = async () => {
    if (first) { first = false; throw Object.assign(new Error('validation coverage blocks approval'), { status: 409 }); }
    return { status: 'approved' };
  };
  await h.ctx.handleGoalApprovePlan();
  assert.deepEqual(h.calls.map((call) => call.payload), [{}, { override_coverage: true }]);
  h.ctx.confirm = async () => false;
  first = true;
  const declined = await approveUnlinkedMission({ approve: h.ctx.unlinkedApprove, isCurrent: () => true, confirmCoverageOverride: h.ctx.confirm });
  assert.equal(declined.outcome, 'declined');
});

// ---------------------------------------------------------------------------
// Reviewed targets (ported from webconsole_utils_test.mjs)
// ---------------------------------------------------------------------------

function coverageRejecting(payload) {
  if (!payload.override_coverage) {
    const err = Object.assign(new Error('validation coverage blocks approval'), { status: 409 });
    err.payload = { error: err.message, code: 'APPROVAL_REJECTED', session_id: 'session_a', approval: { replay: false, recovery_required: false,
      lookup: { found: true, binding: { approval_request_id: payload.approval_request_id }, receipt: { stage: 'rejected', rejection: 'validation coverage blocks approval' } } } };
    throw err;
  }
  return receipt(payload);
}

for (const route of ['plan', 'linked-mission']) {
  test('reviewed approval target (' + route + ') carries the displayed scope through coverage override', async () => {
    const h = harness({ linked: route !== 'plan', reply: coverageRejecting });
    await (route === 'plan' ? h.ctx.handlePlanApprove() : h.ctx.handleGoalApprovePlan());
    assert.equal(h.calls.length, 2);
    const firstPayload = h.calls[0].payload;
    assert.deepEqual(firstPayload, { ...target, override_coverage: false, approval_request_id: firstPayload.approval_request_id });
    const nextPayload = h.calls[1].payload;
    assert.notEqual(nextPayload.approval_request_id, firstPayload.approval_request_id);
    assert.deepEqual(nextPayload, { ...target, override_coverage: true, approval_request_id: nextPayload.approval_request_id });
  });
}

test('declining the coverage override keeps the rejected receipt and does not retry', async () => {
  const h = harness({ reply: coverageRejecting });
  h.ctx.confirm = async () => false;
  await h.ctx.handlePlanApprove();
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.toasts, ['approval.notOverridden']);
  assert.equal(h.ctx.controller().get('session_a').phase, 'rejected');
  assert.deepEqual(coverageOverrideConfirmation(), { title: { key: 'approval.coverage.title' }, message: { key: 'approval.coverage.message' }, confirm: { key: 'approval.coverage.confirm' }, tone: 'danger' });
});

test('stale target conflict shows the review message without retry', async () => {
  const h = harness({ reply: () => { throw Object.assign(new Error('approval target no longer matches'), { status: 409, code: 'approval_target_stale' }); } });
  await h.ctx.handlePlanApprove();
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.toasts, ['approval.planChanged']);
});

test('revision change during coverage confirmation requires review', async () => {
  const confirm = deferred();
  const h = harness({ reply: coverageRejecting });
  h.ctx.confirm = () => confirm.promise;
  h.ctx.detail.plan_mode.approval_revision = 'rev_old';
  h.ctx.detail.plan_mode.expected_revision = undefined;
  const action = h.ctx.handlePlanApprove();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(h.calls.length, 1);
  h.ctx.detail.plan_mode.approval_revision = 'rev_new';
  confirm.resolve(true);
  await action;
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.toasts, ['approval.planChanged']);
});

test('missing revision requires reload without sending', async () => {
  const h = harness();
  delete h.ctx.detail.plan_mode.approval_revision;
  await h.ctx.handlePlanApprove();
  assert.equal(h.calls.length, 0);
  assert.deepEqual(h.toasts, ['approval.error.reloadPlan']);
  assert.throws(() => displayedApprovalTarget({ plan_mode_id: 'p', plan_version: 0, approval_revision: 'r' }), (err) => err.key === 'approval.error.reloadPlan');
});

test('linked mission semantic change during confirmation requires review', async () => {
  const confirm = deferred();
  const h = harness({ linked: true, reply: coverageRejecting });
  h.ctx.confirm = () => confirm.promise;
  const action = h.ctx.handleGoalApprovePlan();
  await new Promise((resolve) => setImmediate(resolve));
  h.ctx.detail.plan_mode.approval_revision = 'rev_new';
  h.ctx.detail.goal.updated_at = 'new';
  confirm.resolve(true);
  await action;
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.toasts, ['approval.planChanged']);
});

test('goal identity change during confirmation drops the override silently', async () => {
  const confirm = deferred();
  const h = harness({ linked: true, reply: coverageRejecting });
  h.ctx.confirm = () => confirm.promise;
  const action = h.ctx.handleGoalApprovePlan();
  await new Promise((resolve) => setImmediate(resolve));
  h.ctx.detail.goal.updated_at = 'new';
  confirm.resolve(true);
  await action;
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.toasts, []);
});

test('busy sessions report the pending request instead of starting another', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  const first = h.ctx.handlePlanApprove();
  await Promise.resolve();
  await h.ctx.handlePlanApprove();
  assert.deepEqual(h.toasts, ['approval.alreadyPending']);
  pending.resolve(receipt(h.calls[0].payload));
  await first;
});

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

test('stale target and coverage block detection', () => {
  assert.equal(isStaleApprovalTarget({ status: 409, code: 'APPROVAL_TARGET_CONFLICT' }), true);
  assert.equal(isStaleApprovalTarget({ status: 409, message: 'The plan has changed' }), true);
  assert.equal(isStaleApprovalTarget({ status: 400, code: 'APPROVAL_TARGET_CONFLICT' }), false);
  assert.equal(isStaleApprovalTarget(new ApprovalOperationError('approval.planChanged')), true);
  assert.equal(isCoverageApprovalBlock({ status: 409, message: 'validation coverage blocks approval' }), true);
  assert.equal(isCoverageApprovalBlock({ status: 409, detail: 'coverage', action: 'approval blocked: coverage approval block' }), true);
  assert.equal(isCoverageApprovalBlock({ status: 409, message: 'coverage ok' }), false);
  assert.equal(isCoverageApprovalBlock({ status: 400, message: 'validation coverage blocks approval' }), false);
  assert.deepEqual(approvalActionError({ status: 500, message: 'server boom' }), { raw: 'server boom' });
  assert.deepEqual(approvalActionError({}, 'plan.actionFailed'), { key: 'plan.actionFailed' });
});

test('targets, identities and view tokens', () => {
  const planMode = { plan_mode_id: 'p', plan_version: 2, approval_revision: 'r', status: 'awaiting_approval', pending_request: { request_id: 'req' } };
  assert.deepEqual(displayedApprovalTarget(planMode), { plan_mode_id: 'p', plan_version: 2, expected_revision: 'r' });
  assert.equal(isDisplayedApprovalTarget(planMode, { plan_mode_id: 'p', plan_version: 2, expected_revision: 'r' }), true);
  assert.equal(isDisplayedApprovalTarget(planMode, { plan_mode_id: 'p', plan_version: 2, expected_revision: 'x' }), false);
  assert.notEqual(planModeActionIdentity(planMode), planModeActionIdentity({ ...planMode, approval_revision: 'r2' }));
  assert.equal(planInputActionIdentity(planMode, 'other'), '');
  assert.ok(planInputActionIdentity(planMode, 'req').endsWith('\nreq'));
  assert.equal(goalActionIdentity(null), '');
  const token = approvalViewToken({ sessionID: 's', epoch: 3, state: { run_generation: 'g', status: 'awaiting_input', updated_at: 'u' } });
  assert.deepEqual(token, { sessionID: 's', epoch: 3, observed: { generation: 'g', status: 'awaiting_input', updatedAt: 'u' } });
  assert.equal(isCurrentApprovalViewToken(token, { sessionID: 's', epoch: 3 }), true);
  assert.equal(isCurrentApprovalViewToken(token, { sessionID: 's', epoch: 4 }), false);
  assert.equal(isNewApprovalAdmission(admittedResponse({}, 'g')), true);
  assert.equal(isNewApprovalAdmission(response(true)), false);
});

test('flow outcomes map to notice keys', () => {
  assert.deepEqual(approvalOutcomeNotice('busy'), { key: 'approval.alreadyPending', tone: 'info' });
  assert.deepEqual(approvalOutcomeNotice('override_declined'), { key: 'approval.notOverridden', tone: 'info' });
  assert.deepEqual(approvalOutcomeNotice('approved'), { key: 'approval.goalPlanApproved', tone: 'success' });
  assert.deepEqual(approvalOutcomeNotice('continue'), { key: 'approval.continueHint', tone: 'info' });
  assert.equal(approvalOutcomeNotice('done'), null);
  assert.equal(approvalOutcomeNotice('stale_view'), null);
});

test('presentation ignores stale views and foreign request ids', () => {
  const r = admittedResponse({ approval_request_id: 'id_1' }, 'g');
  assert.equal(approvalPresentation(r, { viewCurrent: false }).kind, 'ignore');
  assert.equal(approvalPresentation(r, { viewCurrent: true, savedRequestID: 'id_2' }).kind, 'ignore');
  assert.equal(approvalPresentation(null, { viewCurrent: true }).kind, 'none');
});

test('every receipt phase maps to its own notice key', () => {
  const phases = ['pending', 'unknown', 'not_found', 'prepared', 'admitted', 'rejected', 'recovery_required', 'failed'];
  const keys = phases.map((phase) => approvalNoticeState({ record: { phase, approval_request_id: 'x' } }).message.key);
  assert.deepEqual(keys, phases.map((phase) => 'approval.notice.' + phase));
  assert.equal(approvalNoticeState({ record: { phase: 'weird', approval_request_id: 'x' } }).message.key, 'approval.notice.unknown');
  assert.deepEqual(approvalNoticeState({ record: null }), { visible: false });
  const busy = approvalNoticeState({ record: { phase: 'prepared', approval_request_id: 'x' }, busy: true });
  assert.deepEqual(busy.actions.map((action) => action.disabled), [true, true]);
});

test('saved approval errors are i18n keys', () => {
  const values = new Map([[APPROVAL_STORAGE_KEY, '{not json']]);
  const h = controllerHarness({ values });
  assert.throws(() => h.controller.get('session_a'), (err) => err.key === 'approval.error.savedUnreadable');
  values.set(APPROVAL_STORAGE_KEY, JSON.stringify({ version: 1, sessions: {} }));
  return assert.rejects(h.controller.check('session_a'), (err) => err.key === 'approval.error.noSaved')
    .then(() => assert.rejects(h.controller.retry('session_a'), (err) => err.key === 'approval.error.checkBeforeRetry'))
    .then(() => assert.rejects(h.controller.override('session_a', target), (err) => err.key === 'approval.planChanged'));
});

test('the module reads crypto lazily and never touches DOM or network globals at import', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/lib/approval-operations.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage)\b/);
  assert.equal(APPROVAL_STORAGE_KEY, 'aegis-agent.webconsole.approval-operations.v1');
});
