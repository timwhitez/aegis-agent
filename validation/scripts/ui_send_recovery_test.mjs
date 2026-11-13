import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import {
  CONTINUABLE_STATUSES,
  SendRecoveryStore,
  classifySendFailure,
  failedDraftView,
  isAcceptedLaunchResponse,
  isEphemeralSessionId,
  nextEphemeralSessionId,
  prepareSend,
  resolveSendRoute,
  sameSessionRef,
  sendSuccessFeedback,
  sessionRef
} from '../../internal/webconsole/ui/src/lib/send-recovery.js';

function store() {
  let serial = 0;
  let clock = Date.parse('2026-11-13T00:00:00Z');
  return new SendRecoveryStore({ createId: () => 'req_' + (++serial), now: () => (clock += 1000) });
}

const rawPrompt = '  多行输入 <script>原文</script>\n  第二行  \n';
const routeContexts = {
  start: { sessionId: '0xA11CE0', backed: false, status: '', generating: false },
  continue: { sessionId: 'draft_a', backed: true, status: 'paused', generating: false },
  steer: { sessionId: 'draft_a', backed: true, status: 'running', generating: true },
  revise: { sessionId: 'draft_a', backed: true, status: 'paused', generating: false, planModeStatus: 'awaiting_approval' }
};

// Ported: "<route> <outcome> preserves the exact prompt for manual recovery".
for (const kind of ['start', 'continue', 'steer', 'revise']) {
  for (const outcome of ['failed', 'unconfirmed']) {
    test(kind + ' ' + outcome + ' preserves the exact prompt for manual recovery', () => {
      const s = store();
      const route = resolveSendRoute({ ...routeContexts[kind], rawText: rawPrompt });
      assert.equal(route.ok, true);
      assert.equal(route.kind, kind);
      assert.equal(route.text, rawPrompt.trim(), 'payload text is trimmed');
      const intent = s.begin({ session: route.session, kind: route.kind, rawText: route.rawText });
      assert.equal(intent.text, rawPrompt.trim());
      assert.equal(s.optimisticMessages(route.session).length, 1);
      const error = new Error('rejected or disconnected');
      if (outcome === 'failed') error.status = 400;
      const failure = s.fail(intent.id, error);
      assert.equal(s.optimisticMessages(route.session).length, 0, 'failed sends leave the optimistic stream');
      const [draft] = s.failedDrafts();
      assert.equal(draft.rawText, rawPrompt);
      assert.equal(draft.kind, kind);
      assert.equal(draft.status, outcome);
      assert.equal(draft.sessionId, routeContexts[kind].sessionId);
      assert.equal(failure.refresh.overview, outcome === 'unconfirmed');
      assert.equal(failure.refresh.session, outcome === 'unconfirmed' && kind !== 'start');
      const restored = s.restore(draft.id, { composerValue: '' });
      assert.equal(restored.ok, true);
      assert.equal(restored.rawText, rawPrompt);
      assert.deepEqual(restored.notice, outcome === 'unconfirmed' ? { key: 'send.restore.unconfirmed', tone: 'info' } : null);
      assert.equal(s.failedDrafts().length, 0);
      assert.equal(s.pendingIntents().length, 0, 'restoring never retries the API');
    });
  }
}

// Ported: "<validator> validation preserves composer before creating pending input".
for (const validator of ['goal', 'plan']) {
  test(validator + ' draft validation fails before any pending state is created', () => {
    const s = store();
    const result = prepareSend({ ...routeContexts.start, rawText: '  校验失败\n 原文  ' }, { [validator]: () => ({ error: { key: 'composer.invalidDraft' } }) });
    assert.equal(result.ok, false);
    assert.equal(result.reason, 'invalid');
    assert.deepEqual(result.notice, { key: 'composer.invalidDraft', tone: 'error' });
    assert.equal(s.pendingIntents().length, 0);
    assert.equal(s.optimisticMessages(sessionRef(routeContexts.start)).length, 0);
  });
}

test('prepareSend passes valid drafts and skips goal drafts for durable sessions', () => {
  const ok = prepareSend({ ...routeContexts.start, rawText: 'go' }, { goal: () => ({ enabled: true }), plan: () => null });
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.goalDraft, { enabled: true });
  let goalCalls = 0;
  const steer = prepareSend({ ...routeContexts.steer, rawText: 'go' }, { goal: () => { goalCalls += 1; return { error: 'x' }; }, plan: () => ({ error: 'x' }) });
  assert.equal(steer.ok, true, 'steer never runs goal/plan validators');
  assert.equal(goalCalls, 0);
});

// Ported: "failed requests stay bound to their session and request while newer drafts and sends survive".
test('failed requests stay bound to their session and request while newer drafts and sends survive', () => {
  const s = store();
  const a = { sessionId: 'draft_a', backed: true };
  const oldSend = s.begin({ session: a, kind: 'steer', rawText: ' old prompt\n ' });
  const newSend = s.begin({ session: a, kind: 'steer', rawText: 'new pending' });
  const composer = 'new editor draft';
  s.fail(oldSend.id, Object.assign(new Error('old reject'), { status: 400 }));
  assert.equal(s.failedDrafts().length, 1);
  assert.equal(s.failedDrafts()[0].sessionId, 'draft_a');
  assert.deepEqual(s.optimisticMessages(a).map((message) => message.text), ['new pending']);
  const refused = s.restore(oldSend.id, { composerValue: composer });
  assert.deepEqual(refused, { ok: false, reason: 'composerNotEmpty', notice: { key: 'send.restore.composerNotEmpty', tone: 'info' } });
  assert.equal(s.failedDrafts().length, 1);
  // Equal durable text cannot prove delivery of this specific failed request.
  s.reconcile(a, { messages: [{ id: 'm1', role: 'user', text: 'old prompt' }] });
  assert.equal(s.failedDrafts().length, 1);
  s.succeed(newSend.id);
  assert.equal(s.failedDrafts().length, 1, 'successful requests never create drafts');
});

test('failed draft view separates the notice key from raw text and session id', () => {
  const view = failedDraftView({ id: 'request_1', rawText: ' <script>alert(1)</script>\n ', status: 'unconfirmed', sessionId: 'session_x', kind: 'steer' });
  assert.deepEqual(view.notice, { key: 'send.draft.unconfirmed' });
  assert.deepEqual(view.text, { raw: ' <script>alert(1)</script>\n ' });
  assert.deepEqual(view.session, { key: 'send.draft.session', raw: 'session_x' });
  assert.deepEqual(view.restore, { key: 'send.draft.restore' });
  assert.deepEqual(failedDraftView({ status: 'failed', ephemeral: true, sessionId: '0xABC' }).notice, { key: 'send.draft.rejected' });
  assert.deepEqual(failedDraftView({ status: 'failed', ephemeral: true, sessionId: '0xABC' }).session, { key: 'send.draft.newSession', raw: '0xABC' });
});

test('explicit rejection versus unknown delivery', () => {
  assert.equal(classifySendFailure({ status: 400 }), 'failed');
  assert.equal(classifySendFailure({ status: 409 }), 'failed');
  assert.equal(classifySendFailure({ status: 404 }), 'failed');
  assert.equal(classifySendFailure(new TypeError('Failed to fetch')), 'unconfirmed');
  assert.equal(classifySendFailure({ status: 502 }), 'unconfirmed');
  assert.equal(classifySendFailure(undefined), 'unconfirmed');
});

test('stale responses after a session switch only touch their own session', () => {
  const s = store();
  const a = { sessionId: 'session_a', backed: true };
  const b = { sessionId: 'session_b', backed: true };
  const sendA = s.begin({ session: a, kind: 'continue', rawText: 'for A' });
  const sendB = s.begin({ session: b, kind: 'continue', rawText: 'for B' });
  assert.equal(s.isForSession(sendA.id, b), false);
  s.fail(sendA.id, new TypeError('network'));
  assert.deepEqual(s.optimisticMessages(b).map((message) => message.text), ['for B']);
  assert.equal(s.failedDrafts()[0].sessionId, 'session_a');
  assert.equal(s.fail(sendA.id, new TypeError('again')).draft, null, 'a second late response is ignored');
  assert.equal(s.succeed('unknown').ok, false);
  assert.equal(s.isForSession(sendB.id, b), true);
});

test('reconciliation never removes a newer pending entry with identical text', () => {
  const s = store();
  const a = { sessionId: 'session_a', backed: true };
  const baseline = { messageIds: ['m_old'], steerIds: [] };
  const first = s.begin({ session: a, kind: 'steer', rawText: 'continue', baseline });
  const second = s.begin({ session: a, kind: 'steer', rawText: 'continue', baseline });
  s.succeed(first.id);
  const removed = s.reconcile(a, { messages: [{ id: 'm_old', role: 'user', text: 'continue' }, { id: 'm_new', role: 'user', text: 'continue' }] });
  assert.deepEqual(removed, [first.id], 'each durable message reconciles one optimistic entry');
  assert.deepEqual(s.optimisticMessages(a).map((message) => message.id), [second.id]);
  assert.deepEqual(s.pendingIntents(a).map((intent) => intent.id), [second.id]);
});

test('baseline ids keep older identical durable text from hiding a new optimistic message', () => {
  const s = store();
  const a = { sessionId: 'session_a', backed: true };
  const intent = s.begin({ session: a, kind: 'continue', rawText: 'yes', baseline: { messageIds: ['m1'], steerIds: ['st1'] } });
  assert.deepEqual(s.reconcile(a, { messages: [{ id: 'm1', role: 'user', text: 'yes' }], steer_requests: [{ id: 'st1', text: 'yes' }] }), []);
  assert.deepEqual(s.reconcile(a, { messages: [{ id: 'm1', role: 'user', text: 'yes' }], steer_requests: [{ id: 'st2', text: ' yes ' }] }), [intent.id]);
});

test('without a baseline reconciliation keeps the v1 text rule', () => {
  const s = store();
  const a = { sessionId: 'session_a', backed: true };
  s.begin({ session: a, kind: 'start', rawText: 'pending   launch prompt' });
  assert.equal(s.reconcile(a, { messages: [{ role: 'assistant', text: 'pending launch prompt' }] }).length, 1);
});

test('accepted ephemeral starts follow the adopted durable session', () => {
  const s = store();
  const ephemeral = { sessionId: '0xA11CE0', backed: false };
  const intent = s.begin({ session: ephemeral, kind: 'start', rawText: 'launch' });
  assert.equal(s.optimisticMessages(ephemeral).length, 1);
  s.succeed(intent.id, { durableSessionId: 'session_new' });
  assert.equal(s.optimisticMessages(ephemeral).length, 0);
  assert.equal(s.optimisticMessages({ sessionId: 'session_new', backed: true }).length, 1);
  const other = s.begin({ session: { sessionId: '0xBEEF01', backed: false }, kind: 'start', rawText: 'second' });
  assert.equal(s.adoptSession({ sessionId: '0xBEEF01', backed: false }, 'session_two'), 1);
  assert.equal(s.intent(other.id).sessionId, 'session_two');
  assert.equal(s.discardOptimistic({ sessionId: 'session_two', backed: true }), 1);
  assert.equal(s.optimisticMessages({ sessionId: 'session_two', backed: true }).length, 0);
});

test('ephemeral identities are part of the binding', () => {
  assert.notEqual(sessionRef({ sessionId: '0xAAA111', backed: false }).key, sessionRef({ sessionId: '0xBBB222', backed: false }).key);
  assert.equal(sessionRef({ sessionId: '0xAAA111', backed: true }).ephemeral, true, 'hex ids are ephemeral even when flagged backed');
  assert.equal(sameSessionRef(sessionRef({ sessionId: 's', backed: true }), { sessionId: 's', backed: true }), true);
  assert.equal(sameSessionRef({ sessionId: 's', backed: true }, { sessionId: 's', backed: false }), false);
  assert.equal(isEphemeralSessionId(nextEphemeralSessionId(() => 0.5)), true);
  assert.equal(nextEphemeralSessionId(() => 0), '0x000000');
});

test('route resolution mirrors the composer state machine', () => {
  assert.deepEqual(resolveSendRoute({ rawText: '   ' }), { ok: false, reason: 'empty', notice: null });
  assert.equal(resolveSendRoute({ rawText: 'x', sessionId: '0x1', backed: false, launchInFlight: true }).reason, 'launchInFlight');
  assert.equal(resolveSendRoute({ rawText: 'x', sessionId: 's', backed: true, status: 'running', generating: false }).reason, 'notReady');
  // Durable awaiting-input routes to continue even while the handle is tearing down.
  assert.equal(resolveSendRoute({ rawText: 'x', sessionId: 's', backed: true, status: 'awaiting_input', generating: true }).kind, 'continue');
  assert.equal(resolveSendRoute({ rawText: 'x', sessionId: 's', backed: true, status: 'completed' }).kind, 'continue');
  assert.deepEqual(CONTINUABLE_STATUSES, ['awaiting_input', 'paused', 'failed', 'completed']);
  const steer = resolveSendRoute(routeContexts.steer.rawText ? routeContexts.steer : { ...routeContexts.steer, rawText: 'x' });
  assert.equal(steer.allowsPlanDraft, false);
  assert.equal(steer.allowsGoalDraft, false);
  assert.equal(resolveSendRoute({ ...routeContexts.start, rawText: 'x' }).allowsGoalDraft, true);
});

test('success feedback is expressed as keys', () => {
  assert.deepEqual(sendSuccessFeedback('steer', { interrupt: true }).toast, { key: 'send.success.interruptSteer', tone: 'success' });
  assert.deepEqual(sendSuccessFeedback('steer').activity.title, { key: 'activity.steerQueued.title' });
  assert.deepEqual(sendSuccessFeedback('continue', { planDraft: true }).toast.key, 'send.success.planStarted');
  assert.equal(sendSuccessFeedback('start').toast, null);
});

test('isAcceptedLaunchResponse recognizes async launch responses only', () => {
  assert.equal(isAcceptedLaunchResponse({ session_id: 'session_123', status: 'accepted' }), true);
  assert.equal(isAcceptedLaunchResponse({ session_id: 'session_123', status: 'ACCEPTED' }), true);
  assert.equal(isAcceptedLaunchResponse({ session_id: 'session_123', status: 'completed' }), false);
  assert.equal(isAcceptedLaunchResponse({ goal_id: 'goal_123', status: 'active' }), false);
  assert.equal(isAcceptedLaunchResponse(null), false);
});

test('drafts stay in memory: the module never touches storage, DOM or network', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/lib/send-recovery.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:localStorage|sessionStorage|indexedDB|document|window|fetch)\b/);
  const s = store();
  const snapshot = s.snapshot();
  assert.deepEqual(snapshot, { pending: [], failedDrafts: [] });
  assert.doesNotThrow(() => JSON.stringify(snapshot));
});
