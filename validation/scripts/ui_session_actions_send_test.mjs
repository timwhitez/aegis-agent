import test from 'node:test';
import assert from 'node:assert/strict';
import { t } from '../../internal/webconsole/ui/src/lib/i18n.js';
import { apiError, createHarness, deferred } from './ui_session_actions_harness.mjs';

test('new session: Goal start payload, navigate once accepted, optimistic prompt follows the session', async () => {
  const h = createHarness();
  h.navigate('/');
  h.setComposer({ text: '  Build the thing\n', goalEnabled: true, provider: 'openai', model: 'gpt-x' });
  const result = await h.actions.submitComposer();
  assert.equal(result.ok, true);
  assert.equal(result.kind, 'start');
  assert.equal(result.sessionId, 'sess_new');
  assert.deepEqual(h.calls[0], { name: 'startSession', args: [{ prompt: 'Build the thing', goal: { enabled: true }, planMode: undefined, provider: 'openai', model: 'gpt-x', workdir: undefined }] });
  assert.deepEqual(h.navigations.slice(-1), ['/s/sess_new']);
  assert.equal(h.state().composer.text, '');
  assert.equal(h.state().composer.submitting, false);
  const optimistic = h.state().session.optimistic;
  assert.equal(optimistic.length, 1);
  assert.equal(optimistic[0].text, 'Build the thing');
  assert.equal(optimistic[0].role, 'user');
  // Durable history reconciles the optimistic message.
  h.store.update('session', (session) => ({ ...session, detail: { messages: [] }, messages: [{ id: 'm1', role: 'user', text: 'Build the thing' }] }));
  assert.deepEqual(h.state().session.optimistic, []);
  // The new-session draft toggles were reset after the accepted start.
  h.navigate('/');
  assert.equal(h.state().composer.goalEnabled, false);
  assert.equal(h.state().composer.provider, '');
});

test('new session: Plan start sends plan_mode enabled and Goal/Plan never both', async () => {
  const h = createHarness();
  h.navigate('/');
  h.setComposer({ text: 'Plan it', planEnabled: true });
  await h.actions.submitComposer();
  assert.deepEqual(h.calls[0].args[0].planMode, { enabled: true });
  assert.equal(h.calls[0].args[0].goal, undefined);
});

for (const status of ['paused', 'awaiting_input', 'failed', 'completed']) {
  test('continue routes for ' + status + ' with plan_mode and provider/model', async () => {
    const h = createHarness();
    h.openSession('sess_a', { state: { status } });
    h.setComposer({ text: 'keep going', planEnabled: true, provider: 'p1', model: 'm1' });
    const result = await h.actions.submitComposer();
    assert.equal(result.kind, 'continue');
    assert.deepEqual(h.calls[0], { name: 'continueSession', args: ['sess_a', { message: 'keep going', provider: 'p1', model: 'm1', planMode: { enabled: true } }] });
    assert.equal(h.state().session.generating, true);
    assert.deepEqual(h.refreshes, ['sess_a'], 'accepted actions refresh immediately');
    assert.equal(h.toasts.at(-1).message, t('send.success.planStarted'));
    assert.equal(h.state().composer.planEnabled, false);
  });
}

test('running session steers with interrupt; the interrupt toggle resets after success', async () => {
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'running' }, steer_requests: [{ id: 'st_old', text: 'old' }] }, { generating: true, messages: [{ id: 'm1', role: 'user', text: 'hello' }] });
  h.setComposer({ text: 'change course', interrupt: true, provider: 'ignored' });
  const result = await h.actions.submitComposer();
  assert.equal(result.kind, 'steer');
  assert.deepEqual(h.calls[0], { name: 'steerSession', args: ['sess_a', { message: 'change course', interrupt: true }] });
  assert.equal(h.state().composer.interrupt, false);
  assert.equal(h.toasts.at(-1).message, t('send.success.interruptSteer'));
  assert.equal(h.state().session.optimistic[0].meta.interrupt, true);
  assert.equal(h.state().session.optimistic[0].meta.source, 'steer');
});

test('awaiting plan approval: composer text is sent as a plan revision', async () => {
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'awaiting_input' }, plan_mode: { status: 'awaiting_approval', plan_version: 2 } });
  h.setComposer({ text: 'narrow the scope', revising: true, planEnabled: true });
  const result = await h.actions.submitComposer();
  assert.equal(result.kind, 'revise');
  assert.deepEqual(h.calls[0], { name: 'revisePlanMode', args: ['sess_a', 'narrow the scope'] });
  assert.equal(h.state().composer.revising, false);
});

test('a stale Plan toggle never starts Plan Mode while one is pending', async () => {
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'paused' }, plan_mode: { status: 'planning' } });
  h.setComposer({ text: 'continue', planEnabled: true });
  await h.actions.submitComposer();
  assert.equal(h.calls[0].name, 'continueSession');
  assert.equal(h.calls[0].args[1].planMode, undefined);
});

test('not-ready sessions are validated before the composer is cleared', async () => {
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'queued' } });
  h.setComposer({ text: 'hello' });
  const result = await h.actions.submitComposer();
  assert.equal(result.ok, false);
  assert.equal(result.reason, 'notReady');
  assert.equal(h.calls.length, 0);
  assert.equal(h.state().composer.text, 'hello');
  assert.equal(h.toasts[0].message, t('send.notReady'));
  assert.equal(h.toasts[0].tone, 'danger');
  assert.equal((await h.actions.submitComposer()).reason, 'notReady');
  h.setComposer({ text: '   ' });
  assert.equal((await h.actions.submitComposer()).reason, 'empty');
});

test('explicit 4xx rejection becomes a failed draft that keeps the raw text', async () => {
  const raw = '  first line\n  second line  ';
  const h = createHarness({ api: { continueSession: () => { throw apiError('session not resumable', { status: 409, detail: 'child session', action: 'use parent' }); } } });
  h.openSession('sess_a', { state: { status: 'paused' } });
  h.setComposer({ text: raw });
  const result = await h.actions.submitComposer();
  assert.equal(result.ok, false);
  assert.equal(h.calls[0].args[1].message, 'first line\n  second line');
  const drafts = h.state().composer.failedDrafts;
  assert.equal(drafts.length, 1);
  assert.equal(drafts[0].status, 'failed');
  assert.equal(drafts[0].rawText, raw);
  assert.equal(drafts[0].sessionId, 'sess_a');
  assert.equal(drafts[0].kind, 'continue');
  assert.equal(h.state().composer.text, '');
  assert.deepEqual(h.state().session.optimistic, []);
  assert.deepEqual(h.refreshes, [], 'explicit rejection does not need a refresh');
  assert.equal(drafts[0].error.message, 'session not resumable');
  assert.equal(drafts[0].errorDetail, 'child session', 'backend detail stays on the inline card');
  assert.equal(drafts[0].errorAction, 'use parent');
  assert.deepEqual(h.toasts, [], 'the inline card is the error; no duplicate toast');
  assert.equal(h.state().composer.submitting, false);
});

for (const [label, failure] of [
  ['HTTP 500', () => { throw apiError('gateway', { status: 500 }); }],
  ['network abort', () => { throw new TypeError('Failed to fetch'); }],
]) {
  test(label + ' is unconfirmed: refresh the session, never resend', async () => {
    const h = createHarness({ api: { steerSession: failure } });
    h.openSession('sess_a', { state: { status: 'running' } }, { generating: true });
    h.setComposer({ text: 'please check' });
    await h.actions.submitComposer();
    const [draft] = h.state().composer.failedDrafts;
    assert.equal(draft.status, 'unconfirmed');
    assert.deepEqual(h.refreshes, ['sess_a']);
    assert.equal(h.calls.filter((call) => call.name === 'steerSession').length, 1, 'no automatic resend');
    assert.deepEqual(h.toasts, []);
  });
}

test('a send that fails while a page (no composer) is shown also toasts', async () => {
  let fail;
  const gate = new Promise((_resolve, reject) => { fail = reject; });
  const h = createHarness({ api: { continueSession: () => gate } });
  h.openSession('sess_a', { state: { status: 'paused' } });
  h.setComposer({ text: 'background send' });
  const pending = h.actions.submitComposer();
  h.navigate('/settings');
  fail(apiError('gateway', { status: 504, detail: 'upstream timeout' }));
  await pending;
  assert.equal(h.toasts.length, 1);
  assert.equal(h.toasts[0].tone, 'warning');
  assert.equal(h.toasts[0].message, t('composer.send.unconfirmed'));
  assert.equal(h.toasts[0].detail, 'upstream timeout');
  assert.equal(h.state().composer.failedDrafts[0].sessionId, 'sess_a');
});

test('restore only into an empty composer; dismiss drops the card', async () => {
  const h = createHarness({ api: { continueSession: () => { throw apiError('gateway', { status: 502 }); } } });
  h.openSession('sess_a', { state: { status: 'paused' } });
  h.setComposer({ text: 'original text' });
  await h.actions.submitComposer();
  const [draft] = h.state().composer.failedDrafts;
  h.setComposer({ text: 'new draft' });
  const refused = h.actions.restoreFailedDraft(draft.id);
  assert.equal(refused.ok, false);
  assert.equal(refused.reason, 'composerNotEmpty');
  assert.equal(h.toasts.at(-1).message, t('send.restore.composerNotEmpty'));
  assert.equal(h.state().composer.text, 'new draft');
  assert.equal(h.state().composer.failedDrafts.length, 1);
  h.setComposer({ text: '' });
  const focus = h.state().composer.focusToken;
  const restored = h.actions.restoreFailedDraft(draft.id);
  assert.equal(restored.ok, true);
  assert.equal(h.state().composer.text, 'original text');
  assert.equal(h.state().composer.focusToken, focus + 1);
  assert.equal(h.toasts.at(-1).message, t('send.restore.unconfirmed'));
  assert.equal(h.state().composer.failedDrafts.length, 0);
  assert.equal(h.calls.length, 1, 'restore never resends');
  h.setComposer({ text: 'again' });
  await h.actions.submitComposer();
  const second = h.state().composer.failedDrafts[0];
  assert.equal(h.actions.discardFailedDraft(second.id), true);
  assert.deepEqual(h.state().composer.failedDrafts, []);
});

test('per-session composer drafts are restored; the new-session draft is separate', () => {
  const h = createHarness();
  h.openSession('sess_a', { state: { status: 'paused' } });
  h.setComposer({ text: 'draft A', planEnabled: true });
  h.openSession('sess_b', { state: { status: 'paused' } });
  assert.equal(h.state().composer.text, '');
  assert.equal(h.state().composer.planEnabled, false);
  h.setComposer({ text: 'draft B' });
  h.navigate('/settings');
  assert.equal(h.state().composer.text, 'draft B', 'pages keep the owner draft');
  h.openSession('sess_a', { state: { status: 'paused' } });
  assert.equal(h.state().composer.text, 'draft A');
  assert.equal(h.state().composer.planEnabled, true);
  h.navigate('/');
  assert.equal(h.state().composer.text, '');
  h.setComposer({ text: 'new idea', goalEnabled: true });
  h.openSession('sess_b', { state: { status: 'paused' } });
  assert.equal(h.state().composer.text, 'draft B');
  h.navigate('/');
  assert.equal(h.state().composer.text, 'new idea');
  assert.equal(h.state().composer.goalEnabled, true);
  // newSession(): keeps other drafts, starts a fresh new-session identity.
  const before = h.drafts.newSessionIdentity();
  h.drafts.resetNewSession();
  assert.notEqual(h.drafts.newSessionIdentity(), before);
  h.navigate('/');
  h.openSession('sess_a', { state: { status: 'paused' } });
  assert.equal(h.state().composer.text, 'draft A');
});

test('double submit is blocked while a send is in flight; a late start does not hijack navigation', async () => {
  const gate = deferred();
  const h = createHarness({ api: { startSession: () => gate.promise } });
  h.navigate('/');
  h.setComposer({ text: 'first' });
  const pending = h.actions.submitComposer();
  assert.equal(h.state().composer.submitting, true);
  h.setComposer({ text: 'second' });
  assert.equal((await h.actions.submitComposer()).reason, 'busy');
  h.openSession('sess_other', { state: { status: 'paused' } });
  assert.equal(h.state().composer.submitting, false, 'submitting belongs to the new-session draft');
  gate.resolve({ status: 'accepted', session_id: 'sess_late' });
  const result = await pending;
  assert.equal(result.ok, true);
  assert.ok(!h.navigations.includes('/s/sess_late'), 'the user moved on; no navigation');
  assert.equal(h.state().session.id, 'sess_other');
});

test('a 202 without session_id is unconfirmed, not a success', async () => {
  const h = createHarness({ api: { startSession: () => ({ status: 'accepted' }) } });
  h.navigate('/');
  h.setComposer({ text: 'launch' });
  const result = await h.actions.submitComposer();
  assert.equal(result.ok, false);
  assert.equal(h.state().composer.failedDrafts[0].status, 'unconfirmed');
  assert.equal(h.state().composer.failedDrafts[0].ephemeral, true);
});
