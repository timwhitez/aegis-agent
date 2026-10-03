import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
import { ApprovalOperationController, APPROVAL_STORAGE_KEY } from '../../internal/webconsole/assets/approval-operations.mjs';

const appSource = readFileSync(new URL('../../internal/webconsole/assets/app.js', import.meta.url), 'utf8');
function sourceFunction(name) {
  const start = appSource.search(new RegExp(`(?:async )?function ${name}\\(`));
  if (start < 0) return '';
  const tail = appSource.slice(start);
  const end = tail.search(/\n(?:async )?function /);
  return end < 0 ? tail : tail.slice(0, end);
}
const target = { plan_mode_id: 'plan_reviewed', plan_version: 1, expected_revision: 'revision_reviewed' };
function response(replay = false) {
  return { session_id: 'session_a', status: replay ? 'admitted' : 'accepted', approval: {
    replay, recovery_required: false, lookup: { found: true,
      binding: { approval_request_id: 'request_fixture', operation_id: 'request_fixture', parameters: target },
      receipt: { stage: 'admitted', operation_id: 'request_fixture', target }
    }
  }, current_state: { status: 'completed', run_generation: 1 } };
}
function harness({ linked = false, reply = response(), storage } = {}) {
  const values = storage || new Map();
  const calls = [], generating = [], toasts = [], queries = [];
  const ctx = {
    state: { sessionId: 'session_a', sessionBacked: true, sessionDetail: {
      state: { status: 'awaiting_input' }, goal: { goal_id: 'goal_a', updated_at: 'same' },
      plan_mode: { ...target, approval_revision: target.expected_revision, status: 'awaiting_approval', linked_goal_id: linked ? 'goal_a' : '' }
    } },
    approvalViewEpoch: 0, approvalOperationController: null, approvalOperationsModule: null,
    localStorage: { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value) },
    crypto: { randomUUID: () => `request_${calls.length + 1}` },
    window: { AegisApprovalOperations: { ApprovalOperationController } }, document: { body: { contains: () => true }, getElementById: () => null },
    nodes: {},
    hasDurableSession: () => true,
    currentPlanMode() { return ctx.state.sessionDetail.plan_mode; },
    setGenerating(value) { generating.push(value); },
    showToast(message) { toasts.push(message); },
    queueSessionRefresh() {}, queueOverviewRefresh() {}, renderCurrentSession() {}, updateUI() {},
    refreshCurrentSession: async () => {},
    isAcceptedLaunchResponse: result => result?.status === 'accepted',
    approvePlanMode: async (sessionID, payload) => { calls.push({ sessionID, payload: structuredClone(payload), stored: [...values.values()] }); return replyFor(payload); },
    approveMissionPlan: async (sessionID, payload) => { calls.push({ sessionID, payload: structuredClone(payload), stored: [...values.values()] }); return replyFor(payload); },
    getApprovalReceipt: async (sessionID, requestID) => { queries.push({ sessionID, requestID }); const err = new Error('Not found'); err.status = 404; throw err; },
    renderApprovalOperationNotice() {}, console
  };
  function replyFor(payload) {
    if (typeof reply === 'function') return reply(payload);
    const result = structuredClone(reply);
    if (result.approval) result.approval.lookup.binding.approval_request_id = payload.approval_request_id;
    return result;
  }
  vm.createContext(ctx);
  for (const name of ['displayedApprovalTarget', 'isDisplayedApprovalTarget', 'isStaleApprovalTarget', 'approvalActionError', 'isCoverageApprovalBlock', 'currentGoalActionIdentity', 'isCurrentGoalActionIdentity', 'currentPlanModeActionIdentity', 'isCurrentPlanModeActionIdentity',
    'generateApprovalRequestID', 'approvalController', 'currentApprovalViewToken', 'isCurrentApprovalViewToken', 'isNewApprovalAdmission', 'canPresentApprovalAdmission', 'executeReviewedApproval', 'presentApprovalResponse', 'handlePlanModeAction', 'handleGoalAction']) {
    vm.runInContext(sourceFunction(name), ctx, { filename: `app.js:${name}` });
  }
  return { ctx, calls, generating, toasts, queries, values };
}
const button = action => ({ disabled: false, getAttribute: () => action });

for (const linked of [false, true]) {
  test(`actual ${linked ? 'mission' : 'planmode'} approval supports secure random IDs without randomUUID on trusted HTTP`, async () => {
    const h = harness({ linked });
    let randomCalls = 0;
    h.ctx.crypto = { getRandomValues(bytes) {
      randomCalls++;
      bytes.fill(randomCalls);
      return bytes;
    } };
    await (linked ? h.ctx.handleGoalAction(button('approve-plan')) : h.ctx.handlePlanModeAction(button('approve')));
    assert.equal(h.calls.length, 1);
    assert.equal(randomCalls, 1);
    const id = h.calls[0].payload.approval_request_id;
    assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    assert.ok(h.calls[0].stored.some(value => value.includes(id)), 'fallback ID must be persisted before POST');
    assert.notEqual(h.ctx.generateApprovalRequestID(), id, 'each new identity consumes fresh secure randomness');
  });
}

test('approval ID retains native randomUUID when available and fails before POST without secure randomness', async () => {
  const native = harness();
  native.ctx.crypto.getRandomValues = () => { throw new Error('native UUID should be preferred'); };
  assert.equal(native.ctx.generateApprovalRequestID(), 'request_1');
  const absent = harness();
  absent.ctx.crypto = {};
  await absent.ctx.handlePlanModeAction(button('approve'));
  assert.equal(absent.calls.length, 0);
  assert.equal(absent.values.size, 0);
  assert.ok(absent.toasts.some(message => message.includes('Secure randomness')));
});

function admittedResponse(payload, generation, status = 'running') {
  const result = receipt(payload);
  result.status = 'accepted';
  result.approval.replay = false;
  result.approval.lookup.receipt.recovery = { run_generation: generation };
  result.current_state = { status, run_generation: generation };
  return result;
}

test('late actual 202 never revives the same approval generation already loaded as completed', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  h.ctx.state.sessionDetail.state = { status: 'awaiting_input', run_generation: 'run_before_approval', updated_at: 'before' };
  const action = h.ctx.handlePlanModeAction(button('approve'));
  h.ctx.state.sessionDetail.state = { status: 'completed', run_generation: 'run_approved', updated_at: 'settled' };
  h.ctx.state.sessionDetail.plan_mode.status = 'executing';
  pending.resolve(admittedResponse(h.calls[0].payload, 'run_approved'));
  await action;
  assert.deepEqual(h.generating, [], 'loaded terminal facts must outrank the delayed accepted envelope');
  assert.equal(h.ctx.approvalController().response('session_a').approval.lookup.receipt.recovery.run_generation, 'run_approved');
});

test('late actual 202 cannot overwrite ordinary Continue generation even when the follow-up refresh fails', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  let refreshFailed = 0, activity = 'Ordinary follow-up';
  h.ctx.refreshCurrentSession = async () => { throw new TypeError('session refresh unavailable'); };
  h.ctx.queueSessionRefresh = () => { h.ctx.refreshCurrentSession().catch(() => refreshFailed++); };
  h.ctx.setGenerating = (value, next) => { h.generating.push(value); activity = next.title; };
  h.ctx.state.sessionDetail.state = { status: 'awaiting_input', run_generation: 'run_before_approval', updated_at: 'before' };
  const action = h.ctx.handlePlanModeAction(button('approve'));
  h.ctx.state.sessionDetail.state = { status: 'running', run_generation: 'run_ordinary_followup', updated_at: 'followup' };
  h.ctx.state.sessionDetail.plan_mode.status = 'executing';
  pending.resolve(admittedResponse(h.calls[0].payload, 'run_approved'));
  await action;
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(refreshFailed, 1);
  assert.deepEqual(h.generating, [], 'old admission cannot present itself as the current follow-up run');
  assert.equal(activity, 'Ordinary follow-up');
});

test('fresh accepted approval legitimately displays its new claim before the next detail refresh', async () => {
  const h = harness({ reply: payload => admittedResponse(payload, 'run_new_approval') });
  h.ctx.state.sessionDetail.state = { status: 'completed', run_generation: 'run_previous_turn', updated_at: 'before' };
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.deepEqual(h.generating, [true]);
});

test('accepted response for the actual loaded running generation remains a legitimate execution control', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  const action = h.ctx.handlePlanModeAction(button('approve'));
  h.ctx.state.sessionDetail.state = { status: 'running', run_generation: 'run_approved' };
  pending.resolve(admittedResponse(h.calls[0].payload, 'run_approved'));
  await action;
  assert.deepEqual(h.generating, [true]);
});

test('a newly admitted receipt already returned as completed never manufactures a pending execution stage', async () => {
  const h = harness({ reply: payload => admittedResponse(payload, 'run_already_settled', 'completed') });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.deepEqual(h.generating, []);
});

test('accepted envelope without a provable receipt generation never invents a new running identity', async () => {
  const h = harness({ reply: payload => admittedResponse(payload, '') });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.deepEqual(h.generating, []);
});

function noticeMarkup(h) {
  const attributes = {}, node = { setAttribute: (name, value) => attributes[name] = value, innerHTML: '', hidden: true };
  h.ctx.document.getElementById = () => node;
  h.ctx.escapeHTML = String;
  h.ctx.humanizeStatus = status => status.charAt(0).toUpperCase() + status.slice(1);
  vm.runInContext(sourceFunction('renderApprovalOperationNotice'), h.ctx);
  h.ctx.renderApprovalOperationNotice();
  return { html: node.innerHTML, attributes };
}

for (const [cached, live, liveGeneration] of [['running', 'completed', 'run_approved'], ['completed', 'running', 'run_ordinary_followup']]) {
  test(`receipt history ${cached} never overrides the loaded current session ${live}`, async () => {
    const h = harness({ reply: payload => {
      const result = admittedResponse(payload, 'run_approved', cached);
      result.status = 'admitted'; result.approval.replay = true;
      return result;
    } });
    await h.ctx.handlePlanModeAction(button('approve'));
    h.ctx.state.sessionDetail.state = { status: live, run_generation: liveGeneration };
    const notice = noticeMarkup(h);
    assert.match(notice.html, new RegExp(`<span>Current session</span>: <span>${live.charAt(0).toUpperCase() + live.slice(1)}</span>`));
    assert.equal(notice.attributes['data-approval-operation-phase'], 'admitted');
    assert.match(notice.html, /This approval was already admitted/);
    assert.equal(h.ctx.approvalController().response('session_a').current_state.status, cached);
    assert.equal(h.ctx.approvalController().response('session_a').approval.lookup.receipt.recovery.run_generation, 'run_approved');
    assert.deepEqual(h.generating, []);
  });
}

test('actual Approve handler persists an ID and immutable target before its first request', async () => {
  const h = harness();
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.equal(h.calls.length, 1);
  assert.ok(h.calls[0].payload.approval_request_id, 'request must carry a durable approval identity');
  assert.deepEqual(Object.fromEntries(Object.keys(target).map(key => [key, h.calls[0].payload[key]])), target);
  assert.ok(h.calls[0].stored.some(value => value.includes(h.calls[0].payload.approval_request_id)), 'identity must be saved before POST');
});

test('actual Approve handler does not mark an admitted replay as Generating', async () => {
  const h = harness({ reply: response(true) });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.deepEqual(h.generating, []);
  assert.equal(JSON.parse(h.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.phase, 'admitted');
});

function controllerHarness({ submit, query, values = new Map() } = {}) {
  let serial = 0;
  const posts = [], queries = [];
  const controller = new ApprovalOperationController({
    storage: { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value) },
    requestID: () => `operation_${++serial}`,
    submit: async (sessionID, entrypoint, payload) => {
      posts.push({ sessionID, entrypoint, payload: structuredClone(payload), stored: JSON.parse(values.get(APPROVAL_STORAGE_KEY)) });
      return submit ? submit(sessionID, payload) : receipt(payload);
    },
    query: async (sessionID, id) => {
      queries.push({ sessionID, id });
      if (query) return query(sessionID, id);
      const err = new Error('receipt missing'); err.status = 404; throw err;
    }
  });
  return { controller, posts, queries, values };
}
function receipt(payload, stage = 'admitted', extra = {}) {
  return { ...response(true), ...extra, approval: { replay: true, recovery_required: false,
    lookup: { found: true, binding: { approval_request_id: payload.approval_request_id }, receipt: { stage, target } }, ...extra.approval } };
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test('two visible aliases share one in-flight POST and persisted ID', async () => {
  const pending = deferred();
  const h = controllerHarness({ submit: () => pending.promise });
  const a = h.controller.start('session_a', 'planmode', target);
  const b = h.controller.start('session_a', 'mission', target);
  assert.equal(a, b);
  assert.equal(h.posts.length, 1);
  const payload = h.posts[0].payload;
  assert.deepEqual(h.posts[0].stored.sessions.session_a.parameters, { ...target, override_coverage: false });
  pending.resolve(receipt(payload));
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
  const h = controllerHarness({ submit: () => { throw new TypeError('response lost'); },
    query: (sessionID, id) => receipt({ approval_request_id: id }) });
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
  const h = controllerHarness({ submit: (sessionID, payload) => receipt(payload, 'prepared'),
    query: (sessionID, id) => receipt({ approval_request_id: id }, 'prepared') });
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
  params.provider_options.temperature = 0.9; params.system = 'second'; params.expected_revision = 'new';
  await initial;
  await h.controller.retry('session_a');
  assert.deepEqual(h.posts[1].payload, h.posts[0].payload);
  assert.equal(h.posts[1].payload.provider_options.temperature, 0.2);
});

test('late session A response preserves session B pending identity and parameters', async () => {
  const a = deferred(), b = deferred();
  const h = controllerHarness({ submit: sessionID => sessionID === 'session_a' ? a.promise : b.promise });
  const taskA = h.controller.start('session_a', 'planmode', target);
  const taskB = h.controller.start('session_b', 'mission', { ...target, expected_revision: 'revision_b' });
  const beforeB = h.controller.get('session_b');
  a.resolve(receipt(h.posts[0].payload)); await taskA;
  assert.deepEqual(h.controller.get('session_b'), beforeB);
  b.resolve(receipt(h.posts[1].payload)); await taskB;
});

test('late A response after A to B to A navigation never changes Generating', async () => {
  const pending = deferred();
  const h = harness({ reply: () => pending.promise });
  const task = h.ctx.handlePlanModeAction(button('approve'));
  h.ctx.state.sessionId = 'session_b'; h.ctx.approvalViewEpoch++;
  h.ctx.state.sessionId = 'session_a'; h.ctx.approvalViewEpoch++;
  const payload = h.calls[0].payload;
  pending.resolve(receipt(payload, 'admitted', { status: 'accepted', approval: { ...receipt(payload).approval, replay: false } }));
  await task;
  assert.deepEqual(h.generating, []);
});

test('actual old handler ignores its late admission after another tab saved a newer operation', async () => {
  const pending = deferred();
  const a = harness({ reply: () => pending.promise });
  const oldAction = a.ctx.handlePlanModeAction(button('approve'));
  const oldPayload = a.calls[0].payload;
  const b = harness({ storage: a.values, reply: response(true) });
  b.ctx.crypto.randomUUID = () => 'peer_new_operation';
  b.ctx.getApprovalReceipt = async () => receipt(oldPayload);
  await b.ctx.approvalController().check('session_a');
  await b.ctx.handlePlanModeAction(button('approve'));
  assert.equal(b.calls[0].payload.approval_request_id, 'peer_new_operation');
  pending.resolve(receipt(oldPayload, 'admitted', { status: 'accepted', approval: { ...receipt(oldPayload).approval, replay: false } }));
  await oldAction;
  assert.deepEqual(a.generating, [], 'old accepted response must not impersonate the new pending identity');
  assert.equal(JSON.parse(a.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.approval_request_id, 'peer_new_operation');
});

test('local storage failure prevents all approval POSTs', async () => {
  let posts = 0;
  const controller = new ApprovalOperationController({ storage: { getItem: () => null, setItem() { throw new Error('disabled'); } },
    requestID: () => 'saved_request', submit: async () => { posts++; }, query: async () => {} });
  await assert.rejects(controller.start('session_a', 'planmode', target), /could not be saved/);
  assert.equal(posts, 0);
});

test('actual linked mission alias also persists identity and leaves replay generation unchanged', async () => {
  const h = harness({ linked: true, reply: response(true) });
  await h.ctx.handleGoalAction(button('approve-plan'));
  assert.equal(h.calls.length, 1);
  assert.ok(h.calls[0].payload.approval_request_id);
  assert.equal(JSON.parse(h.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.entrypoint, 'mission');
  assert.deepEqual(h.generating, []);
});

test('actual legacy recovery outcome has a visible ordinary Continue choice and never starts generation', async () => {
  const h = harness({ reply: () => { const err = new Error('explicit recovery required'); err.status = 409; err.code = 'APPROVAL_RECOVERY_REQUIRED'; throw err; } });
  await h.ctx.handlePlanModeAction(button('approve'));
  const node = { setAttribute() {}, innerHTML: '', hidden: true };
  h.ctx.document.getElementById = () => node;
  h.ctx.escapeHTML = String;
  h.ctx.humanizeStatus = status => status;
  vm.runInContext(sourceFunction('renderApprovalOperationNotice'), h.ctx);
  h.ctx.renderApprovalOperationNotice();
  assert.equal(node.hidden, false);
  assert.match(node.innerHTML, /data-approval-operation-action="continue"/);
  assert.match(node.innerHTML, /Stop an active run/);
  assert.deepEqual(h.generating, []);
});

test('unknown 404 notice has visible Check and Retry controls bound to the original identity', async () => {
  const h = harness({ reply: () => { throw new TypeError('network lost'); } });
  await h.ctx.handlePlanModeAction(button('approve'));
  const attrs = {}, node = { setAttribute: (key, value) => attrs[key] = value, innerHTML: '', hidden: true };
  h.ctx.document.getElementById = () => node;
  h.ctx.escapeHTML = String;
  h.ctx.humanizeStatus = status => status;
  vm.runInContext(sourceFunction('renderApprovalOperationNotice'), h.ctx);
  h.ctx.renderApprovalOperationNotice();
  assert.equal(attrs['data-approval-operation-phase'], 'not_found');
  assert.equal(attrs['data-approval-request-id'], h.calls[0].payload.approval_request_id);
  assert.match(node.innerHTML, /data-approval-operation-action="check"/);
  assert.match(node.innerHTML, /data-approval-operation-action="retry"/);
  assert.deepEqual(h.generating, []);
});

test('prepared response with recovery_required false never marks the actual Approve view as generating', async () => {
  const h = harness({ reply: payload => receipt(payload, 'prepared') });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.equal(JSON.parse(h.values.get(APPROVAL_STORAGE_KEY)).sessions.session_a.phase, 'prepared');
  assert.deepEqual(h.generating, []);
});

test('navigation restoration never attaches a new-view generation callback to an old in-flight POST', async () => {
  const pending = deferred();
  const h = controllerHarness({ submit: () => pending.promise });
  const old = h.controller.start('session_a', 'planmode', target);
  assert.equal(h.controller.restore('session_a'), null);
  assert.equal(h.queries.length, 0);
  pending.resolve(receipt(h.posts[0].payload)); await old;
});

test('API retains a coverage-rejected receipt on HTTP409 and queries receipts read-only', async () => {
  const calls = [];
  const rejected = receipt({ approval_request_id: 'request_rejected' }, 'rejected', { code: 'APPROVAL_REJECTED', error: 'validation coverage blocks approval' });
  const ctx = { fetch: async (url, options) => {
    calls.push({ url, options });
    return { ok: calls.length > 1, status: calls.length > 1 ? 200 : 409, json: async () => rejected };
  } };
  vm.createContext(ctx);
  vm.runInContext(readFileSync(new URL('../../internal/webconsole/assets/api.js', import.meta.url), 'utf8'), ctx);
  await assert.rejects(ctx.approvePlanMode('session/a', { ...target, approval_request_id: 'request_rejected' }), err => {
    assert.equal(err.payload.approval.lookup.receipt.stage, 'rejected');
    assert.equal(err.code, 'APPROVAL_REJECTED');
    return true;
  });
  await ctx.getApprovalReceipt('session/a', 'request/id');
  assert.equal(calls[1].url, '/api/sessions/session%2Fa/approval-receipts/request%2Fid');
  assert.deepEqual(Object.keys(calls[1].options.headers), []);
  assert.equal(calls[1].options.body, undefined);
});

test('definitive stale rejection survives restore 404 and permits a newly reviewed target', async () => {
  const h = harness({ reply: payload => {
    if (payload.plan_version === 1) {
      const err = new Error('approval target no longer matches'); err.status = 409; err.code = 'APPROVAL_TARGET_CONFLICT'; throw err;
    }
    return receipt(payload);
  } });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.equal(h.ctx.approvalController().get('session_a').phase, 'failed');
  const reloaded = harness({ storage: h.values, reply: payload => receipt(payload) });
  await reloaded.ctx.approvalController().restore('session_a');
  const restoredPhase = reloaded.ctx.approvalController().get('session_a').phase;
  Object.assign(reloaded.ctx.state.sessionDetail.plan_mode, { plan_version: 2, approval_revision: 'reviewed_revision_v2' });
  reloaded.ctx.crypto.randomUUID = () => 'request_new_review_v2';
  await reloaded.ctx.handlePlanModeAction(button('approve'));
  assert.equal(reloaded.calls.length, 1, 'fresh visible V2 approval must POST after the definite V1 rejection');
  assert.equal(restoredPhase, 'failed', 'a readonly miss cannot erase definitive non-admission');
  assert.notEqual(reloaded.calls[0].payload.approval_request_id, h.calls[0].payload.approval_request_id);
  assert.equal(reloaded.calls[0].payload.expected_revision, 'reviewed_revision_v2');
  assert.deepEqual(reloaded.generating, []);
});

test('unknown transport delivery plus 404 preserves the original ID even when the displayed target changes', async () => {
  const h = harness({ reply: () => { throw new TypeError('response lost'); } });
  await h.ctx.handlePlanModeAction(button('approve'));
  const original = h.ctx.approvalController().get('session_a');
  Object.assign(h.ctx.state.sessionDetail.plan_mode, { plan_version: 2, approval_revision: 'reviewed_revision_v2' });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.equal(h.calls.length, 1);
  assert.equal(h.ctx.approvalController().get('session_a').approval_request_id, original.approval_request_id);
  assert.deepEqual(h.ctx.approvalController().get('session_a').parameters, original.parameters);
  assert.deepEqual(h.generating, []);
});

const repairedGoal = { schema_version: 1, session_id: 'session_a', goal_id: 'goal_a', mode: 'mission',
  objective: 'Reviewed linked mission', status: 'active', mission: { plan_status: 'approved', approved_at: '2026-10-03T01:00:00Z' } };
test('actual linked mission handler releases successful facts-only intent without a receipt or generation', async () => {
  const h = harness({ linked: true, reply: repairedGoal });
  await h.ctx.handleGoalAction(button('approve-plan'));
  assert.equal(h.calls.length, 1);
  assert.ok(h.calls[0].payload.approval_request_id);
  assert.equal(h.ctx.approvalController().get('session_a'), null, 'HTTP200 SessionGoal completes facts-only intent');
  assert.equal(h.queries.length, 0, 'confirmed facts response needs no receipt lookup');
  assert.deepEqual(h.generating, []);
  assert.deepEqual(h.toasts, ['Goal plan updated']);
});

test('a newly reviewed target can execute after linked facts-only completion', async () => {
  const h = harness({ linked: true, reply: payload => payload.plan_version === 1 ? repairedGoal : receipt(payload) });
  await h.ctx.handleGoalAction(button('approve-plan'));
  Object.assign(h.ctx.state.sessionDetail.plan_mode, { plan_version: 2, approval_revision: 'reviewed_revision_v2' });
  await h.ctx.handleGoalAction(button('approve-plan'));
  assert.equal(h.calls.length, 2);
  assert.notEqual(h.calls[0].payload.approval_request_id, h.calls[1].payload.approval_request_id);
  assert.equal(h.calls[1].payload.plan_version, 2);
  assert.deepEqual(h.generating, []);
});

test('direct approval never mistakes a receipt-free Goal response for admission or mission facts completion', async () => {
  const h = harness({ reply: repairedGoal });
  await h.ctx.handlePlanModeAction(button('approve'));
  assert.equal(h.ctx.approvalController().get('session_a').phase, 'not_found');
  assert.equal(h.queries.length, 1);
  assert.deepEqual(h.generating, []);
});

test('late linked facts completion cannot release a newer operation saved by a peer tab', async () => {
  const pending = deferred();
  const a = harness({ linked: true, reply: () => pending.promise });
  const oldAction = a.ctx.handleGoalAction(button('approve-plan'));
  const oldID = a.calls[0].payload.approval_request_id;
  const b = harness({ linked: true, storage: a.values, reply: payload => payload.plan_version === 1 ? repairedGoal : receipt(payload) });
  await b.ctx.approvalController().check('session_a');
  await b.ctx.approvalController().retry('session_a');
  assert.equal(b.calls[0].payload.approval_request_id, oldID);
  Object.assign(b.ctx.state.sessionDetail.plan_mode, { plan_version: 2, approval_revision: 'peer_reviewed_revision_v2' });
  b.ctx.crypto.randomUUID = () => 'peer_reviewed_operation_v2';
  await b.ctx.handleGoalAction(button('approve-plan'));
  pending.resolve(repairedGoal);
  await oldAction;
  assert.equal(a.ctx.approvalController().get('session_a').approval_request_id, 'peer_reviewed_operation_v2');
  assert.equal(a.ctx.approvalController().get('session_a').parameters.expected_revision, 'peer_reviewed_revision_v2');
  assert.deepEqual(a.generating, []);
  assert.deepEqual(a.toasts, []);
});

test('visible approval recovery controls and receipt messages are translated in both locales', () => {
  const document = { readyState: 'loading', documentElement: { setAttribute() {} }, addEventListener() {}, getElementById: () => null };
  const window = { document, localStorage: { getItem: () => null, setItem() {} } };
  vm.runInNewContext(readFileSync(new URL('../../internal/webconsole/assets/i18n.js', import.meta.url), 'utf8'), { window });
  const labels = ['Approval receipt', 'Check approval', 'Retry approval', 'Continue session', 'Current session', 'Goal plan updated',
    'Approval delivery is unconfirmed. Check its receipt before retrying.',
    'No receipt was found. Retry only this saved request with its original parameters.',
    'Approval was prepared but has not been admitted. Retry the saved request to check whether it can resume.',
    'This approval was already admitted. The current session below may be a later turn.',
    'This approval was rejected. A coverage override requires confirmation and a new request ID.',
    'Approval needs explicit recovery. Stop an active run, then use the message box to continue the session normally.'];
  for (const label of labels) assert.notEqual(window.AegisI18n.t(label), label);
  window.AegisI18n.setLocale('en');
  for (const label of labels) assert.equal(window.AegisI18n.t(label), label);
});

test('ordinary unlinked mission facts control keeps its existing target-free approval', async () => {
  const h = harness({ reply: { status: 'approved' } });
  h.ctx.state.sessionDetail.plan_mode = null;
  await h.ctx.handleGoalAction(button('approve-plan'));
  assert.equal(h.calls.length, 1);
  assert.deepEqual(h.calls[0].payload, {});
  assert.deepEqual(h.generating, []);
});
