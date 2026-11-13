// Approval receipt browser acceptance (spec/17-web-console.md §7.13): the
// browser keeps a pending approval intent (request id + reviewed target +
// parameters), checks the real receipt endpoint when a response is unknown,
// retries only the same id, never lets a late or peer response revive a
// generating state, uses a new id for a coverage override, replays completed
// admissions as aliases, and asks for explicit recovery for legacy facts.
//
// Only transport is manipulated (a lost POST, a held response, one aborted
// detail refresh, frozen/held detail GETs for the reviewed view); approval
// POSTs that reach the server, receipt queries and provider runs are real.
// API-only protocol checks are labelled in the evidence. Runs as the
// "receipts" area of webconsole_e2e.mjs (zh-CN desktop + en mobile by
// default; E2E_APPROVAL_PROFILES=all for zh-CN/en × desktop/mobile).
import assert from 'node:assert/strict';
import path from 'node:path';
import {
  abortNextDetailRefresh, apiApproval, approvalProfiles, assertApprovalPost, assertCurrentNoticeStatus, assertNotGenerating, assertNotice,
  assertReceipt, assertStoredPending, clickApprove, coherentSnapshot, completed, createFixture, delayNextApproval, displayedTarget,
  durableFacts, freezeDetail, holdDetailPolling, loadedState, loseNextApproval, missionPlan, nextPost, noticeIdentity, observe,
  openApproval, ordinaryContinue, patchMission, providerCalls, providerExecution, queryReceipt, readLedger, readOptional, refreshOpenSession,
  seedLegacyExecuting, seedNewTarget, settleToasts, stableReceipt, suffixOf, target, watchGenerating, withoutLedger,
} from './e2e/approvals.mjs';
import { gotoApp, storeState, tid, waitSessionLoaded } from './e2e/ui.mjs';

const FIVE_TOOLS = ['get_goal', 'shell', 'record_goal_progress', 'update_goal', 'finish'];

export async function runApprovalReceiptsE2E(h) {
  const evidence = [];
  // Default: both locales and both viewports (zh-CN desktop, en mobile);
  // E2E_APPROVAL_PROFILES=all runs every scenario in all four profiles.
  for (const profile of approvalProfiles('zh-desktop,en-mobile')) {
    const suffix = suffixOf(profile);
    const contexts = [];
    const pages = [];
    // Separate storage per tab: tab B is an independent client, not a peer of A.
    for (const tab of ['A', 'B']) {
      const context = await h.newContext({ locale: profile.locale, mobile: profile.mobile });
      contexts.push(context);
      const page = await h.newPage(context, 'receipts-' + tab + '-' + suffix);
      pages.push(page);
      await gotoApp(h, page, '/');
      assert.equal(await page.locator('html').getAttribute('lang'), profile.locale);
    }
    const env = { h, a: pages[0], b: pages[1], profile, locale: profile.locale, suffix };
    const run = async (name, fn) => h.check('approval receipts: ' + name + ' (' + suffix + ')', async () => {
      await settleToasts(env.a);
      const result = await fn(env);
      evidence.push({ scenario: name, locale: profile.locale, viewport: profile.size, ...result });
      await h.capture(env.a, 'approval-receipts-' + name + '-' + suffix, { retainToasts: true });
    }, { page: env.a });
    await run('unknown-response', unknownResponse);
    await run('reload-pending', reloadPending);
    await run('late-response-isolation', lateResponseIsolation);
    await run('coverage-new-id', coverageNewID);
    await run('completed-alias', completedAlias);
    await run('legacy-recovery', legacyRecovery);
    for (const context of contexts) await context.close();
  }
  h.evidence.approval_receipts = { scenarios: evidence.length, evidence };
}

const create = (env, label, linked = false) => createFixture(env.h, 'receipt ' + label + ' ' + env.suffix,
  { linked, marker: linked ? 'E2E_UI_PLAN_RECEIPT_MISSION' : 'E2E_UI_PLAN' });

/** Lets the page finish handling a response that just arrived (macrotask + two frames). */
async function settlePage(page) {
  await page.evaluate(() => new Promise((resolve) => setTimeout(() => requestAnimationFrame(() => requestAnimationFrame(resolve)), 0)));
}

async function checkEnabled(h, page) {
  await h.until(async () => (await tid(page, 'approval-check').count()) > 0 && await tid(page, 'approval-check').first().isEnabled(), 'receipt check control enabled', 30000, 40);
}

async function unknownResponse(env) {
  const { h, a, locale } = env;
  const id = await create(env, 'unknown-after-commit');
  await openApproval(a, id);
  const reviewed = await displayedTarget(a);
  const beforeCalls = await providerCalls(h, id);
  const traffic = observe(a, h, id);
  const lost = await loseNextApproval(a, h, id, async () => { await completed(h, id); });
  try {
    await clickApprove(h, a);
    const delivered = await lost.done;
    assert.equal(delivered.status, 202, delivered.text);
    assertApprovalPost(delivered.body, reviewed);
    await h.until(() => traffic.queries.some((query) => query.id === delivered.body.approval_request_id && query.status === 200),
      'an unknown result queries the original receipt', 30000);
    const receipt = await queryReceipt(h, id, delivered.body.approval_request_id);
    assertReceipt(receipt, delivered.body, 'admitted');
    assert.equal(traffic.posts.length, 1, 'an unknown success never posts again automatically');
    await assertNotGenerating(h, a);
    await assertNotice(a, locale, delivered.body.approval_request_id);
    assert.equal(await providerCalls(h, id), beforeCalls + 1);
    const beforeReplay = await durableFacts(h, id);
    const replay = await apiApproval(h, id, delivered.body);
    assert.equal(replay.status, 200, replay.text);
    assertReceipt(replay.json, delivered.body, 'admitted', true);
    assert.equal(replay.json.approval.lookup.receipt.operation_id, receipt.approval.lookup.receipt.operation_id);
    assert.deepEqual(await durableFacts(h, id), beforeReplay, 'same-id replay writes no facts');
    assert.equal(await providerCalls(h, id), beforeCalls + 1);
    return { session_id: id, request_id: delivered.body.approval_request_id, reviewed, operation_id: receipt.approval.lookup.receipt.operation_id,
      provider_delta: 1, ui_approval_posts: traffic.posts.length, receipt_queries: traffic.queries,
      api_checks: ['same-id completed replay: HTTP 200, unchanged durable facts and provider count'] };
  } finally {
    traffic.dispose();
    await lost.dispose();
  }
}

async function reloadPending(env) {
  const { h, a, locale } = env;
  const id = await create(env, 'reload-before-delivery');
  await openApproval(a, id);
  const reviewed = await displayedTarget(a);
  const beforeCalls = await providerCalls(h, id);
  const traffic = observe(a, h, id);
  const lost = await loseNextApproval(a, h, id);
  try {
    await clickApprove(h, a);
    const original = (await lost.done).body;
    assertApprovalPost(original, reviewed);
    await h.until(() => traffic.queries.some((query) => query.id === original.approval_request_id && query.status === 404),
      'an undelivered POST is checked against the real receipt endpoint', 30000);
    await page404Phase(h, a, 'not_found');
    await assertStoredPending(a, id, original);
    assert.equal(await providerCalls(h, id), beforeCalls);
    const queriesBeforeReload = traffic.queries.length;
    await a.reload();
    await waitSessionLoaded(a, id);
    await h.until(() => traffic.queries.length > queriesBeforeReload && traffic.queries.at(-1).id === original.approval_request_id,
      'reload checks the retained pending request', 30000);
    await assertStoredPending(a, id, original);
    assert.equal(traffic.posts.length, 1, 'reload never creates another operation');
    await assertNotice(a, locale, original.approval_request_id);
    await checkEnabled(h, a);
    await tid(a, 'approval-check').click();
    await h.until(() => traffic.queries.length > queriesBeforeReload + 1, 'the visible Check control queries the receipt', 30000);
    await h.until(async () => (await tid(a, 'approval-retry').count()) === 1 && await tid(a, 'approval-retry').isEnabled(), 'retry offered after a miss', 30000, 40);
    const retry = nextPost(a, h, id, 'planmode/approve');
    await tid(a, 'approval-retry').click();
    const response = await retry;
    assert.equal(response.status(), 202, await response.text());
    assert.deepEqual(response.request().postDataJSON(), original, 'explicit retry keeps the id, target and parameters');
    await completed(h, id);
    assertReceipt(await queryReceipt(h, id, original.approval_request_id), original, 'admitted');
    assert.equal(await providerCalls(h, id), beforeCalls + 1);
    await assertNotGenerating(h, a);
    return { session_id: id, request_id: original.approval_request_id, reviewed, provider_delta: 1,
      ui_approval_posts: traffic.posts.length, receipt_queries: traffic.queries,
      delivery: 'first POST aborted before the server; reload and explicit retry keep the same request' };
  } finally {
    traffic.dispose();
    await lost.dispose();
  }
}

async function page404Phase(h, page, phase) {
  await h.until(async () => (await page.locator('[data-testid="approval-receipt"]').first().getAttribute('data-approval-operation-phase').catch(() => '')) === phase,
    'receipt phase ' + phase, 30000, 40);
}

async function lateResponseIsolation(env) {
  const { h, a, locale } = env;
  const [idA, idB] = await Promise.all([create(env, 'delayed-A'), create(env, 'isolated-B')]);
  await openApproval(a, idA);
  const trafficA = observe(a, h, idA);
  const trafficB = observe(a, h, idB);
  const delayed = await delayNextApproval(a, h, idA);
  const lostB = await loseNextApproval(a, h, idB);
  const beforeA = await providerCalls(h, idA);
  const beforeB = await providerCalls(h, idB);
  try {
    await clickApprove(h, a, { dbl: true });
    const old = await delayed.fetched;
    assert.equal(old.status, 202, old.text);
    await completed(h, idA);
    assert.equal(trafficA.posts.length, 1, 'a double-click never allocates a second operation');
    await openApproval(a, idB);
    await clickApprove(h, a);
    const pendingB = (await lostB.done).body;
    await h.until(() => trafficB.queries.some((query) => query.id === pendingB.approval_request_id && query.status === 404),
      'B has its own checked, unaccepted pending operation', 30000);
    const draft = 'Settings must remain unchanged: session B draft.';
    await tid(a, 'composer-input').fill(draft);
    const operationB = await assertStoredPending(a, idB, pendingB);
    await assertNotGenerating(h, a);
    const watch = await watchGenerating(a);
    const finished = a.waitForEvent('requestfinished', { predicate: (request) => request.url().endsWith('/api/sessions/' + idA + '/planmode/approve'), timeout: 30000 });
    delayed.release();
    await delayed.done;
    await finished;
    await settlePage(a);
    assert.equal(await storeState(a, 'session.id'), idB);
    assert.equal(await tid(a, 'composer-input').inputValue(), draft, 'the late A response keeps the B draft');
    assert.equal((await assertStoredPending(a, idB, pendingB)).phase, operationB.phase, 'the late A response never replaces the B phase');
    await assertNotGenerating(h, a);
    assert.deepEqual(await watch.stop(), [], 'the late response never shows B as generating');
    assert.equal(trafficA.posts.length, 1);
    assert.equal(trafficB.posts.length, 1);
    assert.equal(await providerCalls(h, idA), beforeA + 1);
    assert.equal(await providerCalls(h, idB), beforeB);
    await assertNotice(a, locale, pendingB.approval_request_id);
    await tid(a, 'composer-input').fill('');
    const peer = await sameSessionPeerIsolation(env);
    const sameView = await sameViewLateResponseIsolation(env);
    return { session_id: idB, late_session_id: idA, request_id: pendingB.approval_request_id, late_request_id: old.body.approval_request_id,
      ui_approval_posts: { A: 1, B: 1 }, provider_delta: { A: 1, B: 0 }, preserved_draft: draft,
      control_flow: 'double-click A, open B, B pending, release the actual A response',
      shared_session_peer: peer, same_view_late_response: sameView };
  } finally {
    delayed.release();
    await delayed.dispose();
    await lostB.dispose();
    trafficA.dispose();
    trafficB.dispose();
  }
}

async function sameSessionPeerIsolation(env) {
  const { h, a, locale, suffix } = env;
  const id = await create(env, 'shared-storage-peer');
  await openApproval(a, id);
  const originalTarget = await displayedTarget(a);
  const beforeCalls = await providerCalls(h, id);
  const trafficA = observe(a, h, id);
  const delayed = await delayNextApproval(a, h, id);
  let peer; let trafficPeer;
  try {
    await clickApprove(h, a);
    const old = await delayed.fetched;
    assert.equal(old.status, 202, old.text);
    assertApprovalPost(old.body, originalTarget);
    await completed(h, id);
    // The peer shares A's browser context and storage; its restored intent
    // and Check control query the real old receipt.
    peer = await h.newPage(a.context(), 'receipts-peer-' + suffix);
    trafficPeer = observe(peer, h, id);
    await gotoApp(h, peer, '/s/' + encodeURIComponent(id));
    await waitSessionLoaded(peer, id);
    await assertNotice(peer, locale, old.body.approval_request_id);
    await checkEnabled(h, peer);
    const checked = peer.waitForResponse((response) => response.request().method() === 'GET'
      && response.url() === h.baseURL + '/api/sessions/' + id + '/approval-receipts/' + old.body.approval_request_id);
    await tid(peer, 'approval-check').click();
    const checkedResponse = await checked;
    assert.equal(checkedResponse.status(), 200, await checkedResponse.text());
    assertReceipt(await checkedResponse.json(), old.body, 'admitted');
    await h.until(async () => (await assertStoredPending(peer, id, old.body)).phase === 'admitted', 'the peer confirms the old admission first', 30000);
    assert.equal(await providerCalls(h, id), beforeCalls + 1);
    await seedNewTarget(h, id);
    await peer.reload();
    await waitSessionLoaded(peer, id);
    const freshTarget = await displayedTarget(peer);
    assert.notEqual(freshTarget.expected_revision, originalTarget.expected_revision);
    const accepted = nextPost(peer, h, id, 'planmode/approve');
    await clickApprove(h, peer);
    const response = await accepted;
    assert.equal(response.status(), 202, await response.text());
    const fresh = response.request().postDataJSON();
    assertApprovalPost(fresh, freshTarget);
    assert.notEqual(fresh.approval_request_id, old.body.approval_request_id);
    await completed(h, id);
    await assertNotGenerating(h, peer);
    await assertNotice(peer, locale, fresh.approval_request_id);
    const peerRecord = await assertStoredPending(peer, id, fresh);
    assert.equal(peerRecord.phase, 'admitted');
    // A still shows the same session while its old response is held.
    assert.equal(await storeState(a, 'session.id'), id);
    await assertStoredPending(a, id, fresh);
    await assertNotGenerating(h, a);
    const watch = await watchGenerating(a);
    const finished = a.waitForEvent('requestfinished', { predicate: (request) => request.url().endsWith('/api/sessions/' + id + '/planmode/approve'), timeout: 30000 });
    delayed.release();
    await delayed.done;
    await finished;
    await settlePage(a);
    await assertNotGenerating(h, a);
    assert.deepEqual(await watch.stop(), [], 'an old accepted response never shows a fresh generating state');
    const after = await assertStoredPending(a, id, fresh);
    assert.equal(after.phase, peerRecord.phase, 'the old response never replaces the peer operation');
    await assertNotice(a, locale, fresh.approval_request_id);
    assert.equal(trafficA.posts.length, 1, 'A never posts again after its late response');
    assert.equal(trafficPeer.posts.length, 1, 'the peer performs exactly one new-target approval');
    assert.equal(await providerCalls(h, id), beforeCalls + 2);
    const oldReceipt = await queryReceipt(h, id, old.body.approval_request_id);
    const freshReceipt = await queryReceipt(h, id, fresh.approval_request_id);
    assertReceipt(oldReceipt, old.body, 'admitted');
    assertReceipt(freshReceipt, fresh, 'admitted');
    assert.notEqual(oldReceipt.approval.lookup.receipt.operation_id, freshReceipt.approval.lookup.receipt.operation_id);
    assert.notEqual(oldReceipt.approval.lookup.receipt.recovery.run_generation, freshReceipt.approval.lookup.receipt.recovery.run_generation);
    const ledger = await readLedger(h, id);
    assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'admitted').length, 2);
    assert.equal(Object.keys(ledger.target_admissions).length, 2);
    return { session_id: id, old_request_id: old.body.approval_request_id, peer_request_id: fresh.approval_request_id,
      provider_delta: 2, ui_approval_posts: { A: 1, peer: 1 }, peer_receipt_queries: trafficPeer.queries,
      control_flow: 'same-context peer Check, fresh-target real Approve, release the old A accepted response',
      api_checks: ['both canonical receipts: distinct operation ids and generations'] };
  } finally {
    delayed.release();
    await delayed.dispose();
    trafficA.dispose();
    trafficPeer?.dispose();
    if (peer) await peer.close();
  }
}

async function sameViewLateResponseIsolation(env) {
  const { h, a, locale } = env;
  const results = [];
  for (const ordinaryFollowup of [false, true]) {
    const id = await create(env, ordinaryFollowup ? 'same-view-late-followup' : 'same-view-late-completed');
    await openApproval(a, id);
    const reviewed = await displayedTarget(a);
    const beforeCalls = await providerCalls(h, id);
    const traffic = observe(a, h, id);
    const delayed = await delayNextApproval(a, h, id);
    let refreshFailure;
    try {
      await clickApprove(h, a);
      const old = await delayed.fetched;
      assert.equal(old.status, 202, old.text);
      assertApprovalPost(old.body, reviewed);
      const originalView = await noticeIdentity(a);
      assert.deepEqual(originalView, { session_id: id, request_id: old.body.approval_request_id });
      const original = await completed(h, id);
      // An awaiting-input view does not poll before the held acceptance arrives;
      // re-read the facts in the same view through the app's refresh action
      // (what a relay refresh hint invokes; the old suite clicked History).
      await refreshOpenSession(a);
      await loadedState(h, a, id, original.state.run_generation, 'completed');
      assert.deepEqual(await noticeIdentity(a), originalView, 'the refresh keeps the original view and request identity');
      await assertNotGenerating(h, a);
      await assertNotice(a, locale, old.body.approval_request_id);
      await assertCurrentNoticeStatus(a, locale);
      const originalExecution = await providerExecution(h, id, original, beforeCalls, ['finish']);
      let current = original;
      let ordinaryExecution = null;
      if (ordinaryFollowup) {
        await ordinaryContinue(h, a, id, 'Explicit same-view ordinary follow-up while the old approval transport is delayed.');
        current = await completed(h, id);
        assert.notEqual(current.state.run_generation, original.state.run_generation);
        await loadedState(h, a, id, current.state.run_generation, 'completed');
        await assertNotGenerating(h, a);
        await assertCurrentNoticeStatus(a, locale);
        ordinaryExecution = await providerExecution(h, id, current, beforeCalls + 1, ['finish']);
        refreshFailure = await abortNextDetailRefresh(a, h, id);
      }
      const beforeLate = await durableFacts(h, id);
      const watch = await watchGenerating(a);
      const finished = a.waitForEvent('requestfinished', { predicate: (request) => request.url().endsWith('/api/sessions/' + id + '/planmode/approve'), timeout: 30000 });
      refreshFailure?.arm();
      delayed.release();
      refreshFailure?.markReleased();
      await delayed.done;
      await finished;
      let aborted = null;
      if (refreshFailure) {
        await h.until(() => refreshFailure.observed(), 'one exact post-release detail GET is aborted', 30000);
        aborted = refreshFailure.observed();
      }
      await settlePage(a);
      await checkEnabled(h, a);
      await loadedState(h, a, id, current.state.run_generation, 'completed');
      assert.deepEqual(await noticeIdentity(a), originalView, 'a late same-view acceptance keeps its view and request identity');
      await assertNotGenerating(h, a);
      assert.deepEqual(await watch.stop(), [], 'an old accepted envelope never revives a settled or later generation');
      await assertNotice(a, locale, old.body.approval_request_id);
      await assertCurrentNoticeStatus(a, locale);
      assert.deepEqual(await durableFacts(h, id), beforeLate, 'late transport delivery writes no execution facts');
      assert.equal(await providerCalls(h, id), beforeCalls + (ordinaryFollowup ? 2 : 1));
      assert.equal(traffic.posts.length, 1);
      const ledger = await readLedger(h, id);
      assert.equal(Object.keys(ledger.operations).length, 1);
      assert.equal(Object.keys(ledger.target_admissions).length, 1);
      const query = a.waitForResponse((response) => response.request().method() === 'GET'
        && response.url() === h.baseURL + '/api/sessions/' + id + '/approval-receipts/' + old.body.approval_request_id);
      await tid(a, 'approval-check').click();
      const checked = await query;
      assert.equal(checked.status(), 200, await checked.text());
      const receipt = await checked.json();
      assertReceipt(receipt, old.body, 'admitted');
      assert.equal(receipt.approval.lookup.receipt.recovery.run_generation, original.state.run_generation);
      assert.equal(receipt.current_state.run_generation, current.state.run_generation);
      assert.equal(receipt.current_state.status, 'completed');
      await checkEnabled(h, a);
      await assertNotGenerating(h, a);
      await assertCurrentNoticeStatus(a, locale);
      assert.equal(await providerCalls(h, id), beforeCalls + (ordinaryFollowup ? 2 : 1));
      results.push({ scenario: ordinaryFollowup ? 'loaded-later-generation-and-refresh-failure' : 'loaded-completed-same-generation',
        session_id: id, request_id: old.body.approval_request_id, original_execution: originalExecution, ordinary_execution: ordinaryExecution,
        current_generation: current.state.run_generation, original_view: originalView, canonical_operations: 1,
        ui_approval_posts: traffic.posts.length, receipt_queries: traffic.queries,
        ...(aborted ? { intentional_detail_refresh_failure: aborted } : {}),
        control_flow: 'real Approve admitted by the server, same-view refresh shows Completed, held transport released, real Check of the historical receipt' });
    } finally {
      delayed.release();
      await delayed.dispose();
      await refreshFailure?.dispose();
      traffic.dispose();
    }
  }
  return results;
}

async function coverageNewID(env) {
  const { h, a, locale } = env;
  const id = await create(env, 'coverage', true);
  await patchMission(h, id, missionPlan(false));
  const snapshot = await coherentSnapshot(h, id);
  assert.equal(snapshot.goal_facts.coverage.approval_blocked, true);
  await openApproval(a, id);
  const reviewed = await displayedTarget(a);
  const beforeCalls = await providerCalls(h, id);
  const traffic = observe(a, h, id);
  h.allowHTTP('coverage rejection (409) ' + id, { url: h.baseURL + '/api/sessions/' + id + '/planmode/approve', status: 409, method: 'POST', limit: 2 });
  try {
    const first = nextPost(a, h, id, 'planmode/approve');
    await clickApprove(h, a);
    const blocked = await first;
    assert.equal(blocked.status(), 409, await blocked.text());
    const original = blocked.request().postDataJSON();
    assertApprovalPost(original, reviewed);
    const blockedJSON = await blocked.json();
    assert.equal(blockedJSON.code, 'APPROVAL_REJECTED');
    assertReceipt(blockedJSON, original, 'rejected');
    assert.equal(await providerCalls(h, id), beforeCalls);
    const changedParams = await apiApproval(h, id, { ...original, override_coverage: true });
    assert.equal(changedParams.status, 409, changedParams.text);
    assert.equal(changedParams.json.code, 'APPROVAL_REQUEST_CONFLICT');
    const dialog = tid(a, 'confirm-dialog');
    await dialog.waitFor();
    assert.match(await dialog.innerText(), locale === 'zh-CN' ? /覆盖率|验证/ : /validation coverage/i);
    const confirmed = nextPost(a, h, id, 'planmode/approve');
    await tid(a, 'confirm-dialog-confirm').click();
    const approved = await confirmed;
    assert.equal(approved.status(), 202, await approved.text());
    const newRequest = approved.request().postDataJSON();
    assertApprovalPost(newRequest, reviewed, true);
    assert.notEqual(newRequest.approval_request_id, original.approval_request_id);
    assertReceipt(await approved.json(), newRequest, 'admitted');
    const settled = await completed(h, id);
    const ledger = await readLedger(h, id);
    assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'rejected').length, 1);
    assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'admitted').length, 1);
    assert.equal(Object.keys(ledger.target_admissions).length, 1);
    assert.equal(await providerCalls(h, id), beforeCalls + 5);
    const execution = await providerExecution(h, id, settled, beforeCalls, FIVE_TOOLS);
    assert.equal(traffic.posts.length, 2);
    await assertNotGenerating(h, a);
    await assertNotice(a, locale, newRequest.approval_request_id);
    return { session_id: id, rejected_request_id: original.approval_request_id, request_id: newRequest.approval_request_id, reviewed,
      provider_delta: 5, execution, api_checks: ['same id plus a changed override conflicts (APPROVAL_REQUEST_CONFLICT)'] };
  } finally {
    traffic.dispose();
  }
}

async function completedAlias(env) {
  const { h, a, b, locale } = env;
  const id = await create(env, 'linked-canonical-alias', true);
  await patchMission(h, id, missionPlan(true));
  const originalDetail = await coherentSnapshot(h, id);
  const reviewed = target(originalDetail);
  const thaw = await freezeDetail(a, h, id, originalDetail);
  const beforeCalls = await providerCalls(h, id);
  let canonicalRequest; let operationID; let originalExecution; let originalReceipt; let aliasRequestID;
  try {
    await openApproval(a, id);
    await openApproval(b, id);
    const accepted = nextPost(b, h, id, 'planmode/approve');
    await clickApprove(h, b);
    const response = await accepted;
    assert.equal(response.status(), 202, await response.text());
    canonicalRequest = response.request().postDataJSON();
    assertApprovalPost(canonicalRequest, reviewed);
    operationID = (await response.json()).approval.lookup.receipt.operation_id;
    const settled = await completed(h, id);
    originalExecution = await providerExecution(h, id, settled, beforeCalls, FIVE_TOOLS);
    originalReceipt = (await queryReceipt(h, id, canonicalRequest.approval_request_id)).approval.lookup.receipt;
    const beforeAlias = await durableFacts(h, id);
    await assertNotGenerating(h, a);
    const watch = await watchGenerating(a);
    const aliasResponse = nextPost(a, h, id, 'planmode/approve');
    await clickApprove(h, a);
    const alias = await aliasResponse;
    assert.equal(alias.status(), 200, await alias.text());
    const aliasRequest = alias.request().postDataJSON();
    aliasRequestID = aliasRequest.approval_request_id;
    assertApprovalPost(aliasRequest, reviewed);
    assert.notEqual(aliasRequest.approval_request_id, canonicalRequest.approval_request_id);
    const result = await alias.json();
    assertReceipt(result, aliasRequest, 'admitted', true);
    assert.equal(result.approval.lookup.receipt.operation_id, operationID);
    assert.deepEqual(stableReceipt(result.approval.lookup.receipt), stableReceipt(originalReceipt));
    assert.deepEqual(withoutLedger(await durableFacts(h, id)), withoutLedger(beforeAlias));
    assert.equal(await providerCalls(h, id), beforeCalls + 5);
    await settlePage(a);
    await assertNotGenerating(h, a);
    assert.deepEqual(await watch.stop(), [], 'a completed alias never shows a generating state');
    await assertNotice(a, locale, aliasRequest.approval_request_id);
  } finally {
    await thaw();
  }
  // Real API checks: lookup precedes the current target and generation.
  await b.reload();
  await waitSessionLoaded(b, id);
  await ordinaryContinue(h, b, id, 'Explicit ordinary follow-up after the old approval.');
  const later = await completed(h, id);
  const query = await queryReceipt(h, id, canonicalRequest.approval_request_id);
  assert.equal(query.approval.lookup.receipt.operation_id, operationID);
  assert.notEqual(query.current_state.run_generation, query.approval.lookup.receipt.recovery.run_generation);
  assert.equal(query.current_state.run_generation, later.state.run_generation);
  assert.equal(await providerCalls(h, id), beforeCalls + 6);
  const ordinaryExecution = await providerExecution(h, id, later, beforeCalls + 5, ['finish']);
  assert.notEqual(ordinaryExecution.run_generation, originalExecution.run_generation);
  await patchMission(h, id, { requirements: [{ id: 'requirement_scope', text: 'Settings must remain unchanged: later linked scope.' }] });
  const changed = await coherentSnapshot(h, id);
  assert.equal(changed.state.status, 'completed');
  assert.equal(changed.state.run_generation, later.state.run_generation);
  assert.ok(!changed.active_handle);
  assert.equal(changed.goal.mission.plan_status, 'needs_approval');
  assert.equal(changed.plan_mode.status, 'planning');
  assert.equal(changed.plan_mode.plan_version || 0, 0);
  assert.notEqual(changed.plan_mode.plan_mode_id, reviewed.plan_mode_id, 'editing the approved linked scope creates a replacement gate');
  assert.equal(changed.plan_mode.linked_goal_id, changed.goal.goal_id);
  assert.equal(changed.goal_facts.coverage.approval_blocked, false);
  assert.notEqual(changed.plan_mode.approval_revision, reviewed.expected_revision);
  const beforeReplay = await durableFacts(h, id);
  for (const action of ['planmode/approve', 'mission/plan/approve']) {
    const replay = await apiApproval(h, id, canonicalRequest, action);
    assert.equal(replay.status, 200, action + ' ' + replay.text);
    assertReceipt(replay.json, canonicalRequest, 'admitted', true);
    assert.equal(replay.json.approval.lookup.receipt.operation_id, operationID);
    assert.deepEqual(stableReceipt(replay.json.approval.lookup.receipt), stableReceipt(originalReceipt));
  }
  assert.deepEqual(await durableFacts(h, id), beforeReplay);
  assert.equal(await providerCalls(h, id), beforeCalls + 6);
  await seedNewTarget(h, id, { expectedStatus: 'planning', runGeneration: later.state.run_generation });
  const freshDetail = await coherentSnapshot(h, id);
  assert.equal(freshDetail.plan_mode.status, 'awaiting_approval');
  assert.equal(freshDetail.plan_mode.plan_version, 1);
  assert.equal(freshDetail.state.status, 'completed');
  assert.ok(!freshDetail.active_handle);
  await a.reload();
  await waitSessionLoaded(a, id);
  const newTarget = await displayedTarget(a);
  assert.equal(newTarget.plan_mode_id, changed.plan_mode.plan_mode_id);
  assert.equal(newTarget.plan_version, 1);
  assert.equal(newTarget.expected_revision, freshDetail.plan_mode.approval_revision);
  assert.notEqual(newTarget.expected_revision, reviewed.expected_revision);
  const admitted = nextPost(a, h, id, 'planmode/approve');
  await clickApprove(h, a);
  const fresh = await admitted;
  assert.equal(fresh.status(), 202, await fresh.text());
  const freshRequest = fresh.request().postDataJSON();
  assertApprovalPost(freshRequest, newTarget);
  assert.notEqual(freshRequest.approval_request_id, canonicalRequest.approval_request_id);
  assert.notEqual(freshRequest.approval_request_id, aliasRequestID);
  const freshSettled = await completed(h, id);
  assert.equal(await providerCalls(h, id), beforeCalls + 7);
  const freshExecution = await providerExecution(h, id, freshSettled, beforeCalls + 6, ['finish']);
  assert.notEqual(freshExecution.run_generation, originalExecution.run_generation);
  assert.notEqual(freshExecution.run_generation, ordinaryExecution.run_generation);
  const ledger = await readLedger(h, id);
  assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'admitted').length, 2);
  assert.equal(Object.keys(ledger.target_admissions).length, 2);
  await assertNotGenerating(h, a);
  await assertNotice(a, locale, freshRequest.approval_request_id);
  return { session_id: id, request_id: canonicalRequest.approval_request_id, alias_request_id: aliasRequestID, operation_id: operationID,
    reviewed, new_target: newTarget, new_request_id: freshRequest.approval_request_id,
    provider_delta: { original_admission: 5, completed_alias_and_replay: 0, ordinary_continue: 1, new_target_admission: 1 },
    executions: { original_admission: originalExecution, ordinary_continue: ordinaryExecution, new_target_admission: freshExecution },
    api_checks: ['same-id replay through both approval entrypoints after a semantic change', 'old receipt query separates the later generation'],
    fixture_setup: 'the real semantic mission edit creates a replacement planning gate; only its fresh pending version one is seeded' };
}

async function legacyRecovery(env) {
  const { h, a, locale } = env;
  const id = await create(env, 'legacy-direct');
  const old = await coherentSnapshot(h, id);
  await openApproval(a, id);
  const release = await holdDetailPolling(a, h, id);
  const beforeCalls = await providerCalls(h, id);
  try {
    await seedLegacyExecuting(h, id);
    const before = await durableFacts(h, id);
    h.allowHTTP('legacy executing needs explicit recovery (409) ' + id, { url: h.baseURL + '/api/sessions/' + id + '/planmode/approve', status: 409, method: 'POST', limit: 2 });
    h.allowHTTP('legacy fixture has no receipt ledger (404) ' + id, { url: h.baseURL + '/api/sessions/' + id + '/approval-receipts/', status: 404, method: 'GET' });
    const approval = nextPost(a, h, id, 'planmode/approve');
    await clickApprove(h, a);
    const response = await approval;
    assert.equal(response.status(), 409, await response.text());
    assert.equal((await response.json()).code, 'APPROVAL_RECOVERY_REQUIRED');
    assertApprovalPost(response.request().postDataJSON(), target(old));
    assert.deepEqual(await durableFacts(h, id), before);
    assert.equal(await providerCalls(h, id), beforeCalls);
    const copy = a.locator('[data-testid="toast"], [data-testid="approval-receipt"]').filter({ hasText: locale === 'zh-CN' ? /恢复|继续/ : /recovery|continue/i }).last();
    await copy.waitFor();
    await assertNotGenerating(h, a);
  } finally {
    await release();
  }
  await a.reload();
  await waitSessionLoaded(a, id);
  await ordinaryContinue(h, a, id, 'Explicit ordinary continue to recover the legacy session.');
  const recovered = await completed(h, id);
  assert.equal(recovered.plan_mode.approved_revision || '', '', 'legacy recovery never backfills a reviewed revision');
  assert.equal(await readOptional(path.join(h.paths.sessions, id, 'approval-operations.json')), null);
  assert.equal(await providerCalls(h, id), beforeCalls + 1);

  // Linked legacy executing: the single inline approval path asks for recovery;
  // the mission fact-repair alias stays an API-level contract.
  const linked = await create(env, 'legacy-linked-facts', true);
  await patchMission(h, linked, missionPlan(true));
  const linkedReview = await coherentSnapshot(h, linked);
  await openApproval(a, linked);
  const releaseLinked = await holdDetailPolling(a, h, linked);
  const linkedBefore = await providerCalls(h, linked);
  const linkedTraffic = observe(a, h, linked);
  let uiRequest;
  try {
    await seedLegacyExecuting(h, linked);
    await assertNotGenerating(h, a);
    const watch = await watchGenerating(a);
    h.allowHTTP('linked legacy executing needs explicit recovery (409) ' + linked, { url: h.baseURL + '/api/sessions/' + linked + '/planmode/approve', status: 409, method: 'POST', limit: 2 });
    h.allowHTTP('linked legacy fixture has no receipt ledger (404) ' + linked, { url: h.baseURL + '/api/sessions/' + linked + '/approval-receipts/', status: 404, method: 'GET' });
    const approval = nextPost(a, h, linked, 'planmode/approve');
    await clickApprove(h, a);
    const response = await approval;
    assert.equal(response.status(), 409, await response.text());
    assert.equal((await response.json()).code, 'APPROVAL_RECOVERY_REQUIRED', 'the direct execution entrypoint never treats a fact repair as admission');
    uiRequest = response.request().postDataJSON();
    assertApprovalPost(uiRequest, target(linkedReview));
    await settlePage(a);
    await assertNotGenerating(h, a);
    assert.deepEqual(await watch.stop(), []);
    assert.equal(await providerCalls(h, linked), linkedBefore);
    const missing = await apiApproval(h, linked, {}, 'mission/plan/approve');
    assert.equal(missing.status, 400, missing.text);
    const repair = await apiApproval(h, linked, { ...target(linkedReview), approval_request_id: crypto.randomUUID() }, 'mission/plan/approve');
    assert.equal(repair.status, 200, repair.text);
    assert.equal(repair.json.approval, undefined, 'a linked executing fact repair is not an execution receipt');
    const repaired = await coherentSnapshot(h, linked);
    assert.equal(repaired.goal.mission.plan_status, 'approved');
    assert.equal(repaired.goal.mission.approved_revision || '', '');
    assert.equal(repaired.plan_mode.approved_revision || '', '');
    assert.equal(await providerCalls(h, linked), linkedBefore);
    assert.equal(await readOptional(path.join(h.paths.sessions, linked, 'approval-operations.json')), null);
    const receiptMiss = await h.api('GET', '/api/sessions/' + linked + '/approval-receipts/' + encodeURIComponent(uiRequest.approval_request_id));
    assert.equal(receiptMiss.status, 404, 'no receipt is fabricated for the refused request');
  } finally {
    await releaseLinked();
  }
  await seedNewTarget(h, linked);
  await a.reload();
  await waitSessionLoaded(a, linked);
  const freshTarget = await displayedTarget(a);
  assert.equal(freshTarget.plan_version, linkedReview.plan_mode.plan_version + 1);
  assert.notEqual(freshTarget.expected_revision, linkedReview.plan_mode.approval_revision);
  // Approving the newly reviewed target starts a new operation in this browser.
  const postsBefore = linkedTraffic.posts.length;
  const queriesBefore = linkedTraffic.queries.length;
  const accepted = nextPost(a, h, linked, 'planmode/approve');
  accepted.catch(() => {});
  await clickApprove(h, a);
  const outcome = await h.until(async () => {
    if (linkedTraffic.posts.length > postsBefore) return 'posted';
    if (linkedTraffic.queries.length > queriesBefore && !(await storeState(a, 'session.pendingAction'))) return 'rechecked-old-id';
    return null;
  }, 'Approve on the fresh target posts', 30000, 50);
  assert.equal(outcome, 'posted', 'Approve on the reviewed v' + freshTarget.plan_version + ' posts a new operation instead of re-checking ' + uiRequest.approval_request_id);
  const response = await accepted;
  assert.equal(response.status(), 202, await response.text());
  const freshRequest = response.request().postDataJSON();
  assertApprovalPost(freshRequest, freshTarget);
  assert.notEqual(freshRequest.approval_request_id, uiRequest.approval_request_id, 'a newly reviewed target starts a new operation');
  assertReceipt(await response.json(), freshRequest, 'admitted');
  const linkedSettled = await completed(h, linked);
  const ledger = await readLedger(h, linked);
  assert.equal(Object.keys(ledger.operations).length, 1);
  assert.equal(Object.keys(ledger.target_admissions).length, 1);
  assert.equal(ledger.requests[uiRequest.approval_request_id], undefined);
  assert.equal(await providerCalls(h, linked), linkedBefore + 5);
  const linkedExecution = await providerExecution(h, linked, linkedSettled, linkedBefore, FIVE_TOOLS);
  assert.equal(linkedTraffic.posts.length, 2, 'the refused request never retries automatically');
  linkedTraffic.dispose();
  await assertNotGenerating(h, a);
  await assertNotice(a, locale, freshRequest.approval_request_id);
  return { session_id: id, linked_session_id: linked, linked_refused_request_id: uiRequest.approval_request_id,
    linked_fresh_request_id: freshRequest.approval_request_id, linked_execution: linkedExecution,
    provider_delta: { legacy_approval: 0, explicit_ordinary_continue: 1, linked_refused_approval: 0, linked_api_fact_repair: 0, linked_fresh_target_admission: 5 },
    ui_controls: ['old Approve returns explicit recovery', 'composer Send performs the ordinary continue',
      'linked legacy Approve on the inline card returns explicit recovery', 'newly reviewed linked target starts a new operation'],
    api_checks: ['linked executing missing target: HTTP 400', 'linked executing fact repair: HTTP 200 without receipt or provider call',
      'refused UI request id has no fabricated receipt: HTTP 404'] };
}
