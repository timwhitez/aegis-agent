import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { approvalE2EFixtures as fixture } from './webconsole_approval_cas_e2e.mjs';

const profiles = [
  { locale: 'zh-CN', size: 'desktop', viewport: { width: 1440, height: 1000 } },
  { locale: 'zh-CN', size: 'mobile', viewport: { width: 390, height: 844 } },
  { locale: 'en', size: 'desktop', viewport: { width: 1440, height: 1000 } },
  { locale: 'en', size: 'mobile', viewport: { width: 390, height: 844 } }
];
const selectors = {
  approve: '#inspector-slide-out [data-plan-action="approve"]',
  goalApprove: '#inspector-slide-out [data-goal-action="approve-plan"]',
  notice: '#approval-operation-notice',
  check: '[data-approval-operation-action="check"]',
  retry: '[data-approval-operation-action="retry"]'
};
const pendingStorageKey = 'aegis-agent.webconsole.approval-operations.v1';

// Only transport failures and an old, real detail GET are intercepted. All
// approval POSTs that reach the server, receipt queries, and provider runs use
// the real service. API-only protocol checks are labelled in the evidence.
export async function runApprovalReceiptsE2E({ browser, baseURL, sessionRoot, providerLogPath, check, capture }) {
  assert.ok(browser && baseURL && sessionRoot && providerLogPath);
  assert.equal(typeof check, 'function');
  assert.equal(typeof capture, 'function');
  const evidence = [];
  const browserErrors = { page: [], console: [], request: [] };
  const expectedTransportFailures = [];
  for (const profile of profiles) {
    const suffix = `${profile.locale}-${profile.size}`;
    const contexts = [];
    const pages = [];
    const expectedFailures = new Set();
    try {
      // Separate browser storage makes the old tab's new-ID alias a real
      // independent client operation rather than an inherited pending ID.
      for (const tab of ['A', 'B']) {
        const context = await browser.newContext({ viewport: profile.viewport });
        contexts.push(context);
        await context.addInitScript((locale) => localStorage.setItem('aegis-agent.locale.v1', locale), profile.locale);
        const page = await context.newPage();
        pages.push(page);
        collectErrors(page, suffix, tab, expectedFailures, browserErrors, expectedTransportFailures);
        await page.goto(baseURL, { waitUntil: 'networkidle' });
        await page.waitForFunction((locale) => window.AegisI18n?.locale?.() === locale, profile.locale);
        assert.equal(await page.locator('html').getAttribute('lang'), profile.locale);
      }
      const [a, b] = pages;
      const context = contexts[0];
      const shared = { a, b, context, baseURL, sessionRoot, providerLogPath, profile, suffix,
        expectedFailures, browserErrors, expectedTransportFailures };
      const run = async (name, fn) => check(`approval receipts: ${name} (${suffix})`, async () => {
        const result = await fn(shared);
        evidence.push({ scenario: name, locale: profile.locale, viewport: profile.size, ...result });
        await capture(a, `approval-receipts-${name}-${suffix}.png`, { retainToasts: true });
      });
      await run('unknown-response', unknownResponse);
      await run('reload-pending', reloadPending);
      await run('late-response-isolation', lateResponseIsolation);
      await run('coverage-new-id', coverageNewID);
      await run('completed-alias', completedAlias);
      await run('legacy-recovery', legacyRecovery);
    } finally {
      for (const context of contexts.reverse()) await context.close();
    }
  }
  assert.deepEqual(browserErrors, { page: [], console: [], request: [] }, 'unexpected approval receipt browser errors');
  return { scenarios: evidence.length, profiles: profiles.length, evidence,
    browser_errors: browserErrors, expected_transport_failures: expectedTransportFailures };
}

async function unknownResponse(env) {
  const { a, context, baseURL, sessionRoot, providerLogPath, expectedFailures } = env;
  const id = await create(env, 'unknown-after-commit');
  await openSessionUI(a, id, 'plan');
  const reviewed = await fixture.displayedTarget(a);
  const beforeCalls = await providerCalls(providerLogPath, id);
  const traffic = observe(a, baseURL, id);
  const lost = await loseNextApproval(a, baseURL, id, expectedFailures, async () => {
    await completed(context, baseURL, id);
  });
  try {
    await a.locator(selectors.approve).click();
    const delivered = await lost.done;
    assert.equal(delivered.status, 202, delivered.text);
    fixture.assertApprovalPost(delivered.body, reviewed);
    await until(() => traffic.queries.some((query) => query.id === delivered.body.approval_request_id && query.status === 200),
      'unknown result must query the original request receipt');
    const receipt = await queryReceipt(context, baseURL, id, delivered.body.approval_request_id);
    assertReceipt(receipt, delivered.body, 'admitted');
    assert.equal(traffic.posts.length, 1, 'unknown success must not automatically POST again');
    await assertNotGenerating(a);
    await assertNotice(a, env.profile.locale, delivered.body.approval_request_id);
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
    const beforeReplay = await fixture.durableFacts(sessionRoot, id);
    const replay = await apiApproval(context, baseURL, id, delivered.body);
    assert.equal(replay.status, 200, replay.text);
    assertReceipt(replay.json, delivered.body, 'admitted', true);
    assert.equal(replay.json.approval.lookup.receipt.operation_id, receipt.approval.lookup.receipt.operation_id);
    assert.deepEqual(await fixture.durableFacts(sessionRoot, id), beforeReplay, 'same-ID replay must not write facts');
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
    return { session_id: id, request_id: delivered.body.approval_request_id, reviewed,
      operation_id: receipt.approval.lookup.receipt.operation_id, provider_before: beforeCalls,
      provider_after: beforeCalls + 1, ui_approval_posts: traffic.posts.length, receipt_queries: traffic.queries,
      api_checks: ['same-ID completed replay: HTTP 200, unchanged durable facts and provider count'] };
  } finally {
    traffic.dispose();
    await lost.dispose();
  }
}

async function reloadPending(env) {
  const { a, context, baseURL, providerLogPath, expectedFailures } = env;
  const id = await create(env, 'reload-before-delivery');
  await openSessionUI(a, id, 'plan');
  const reviewed = await fixture.displayedTarget(a);
  const beforeCalls = await providerCalls(providerLogPath, id);
  const traffic = observe(a, baseURL, id);
  const lost = await loseNextApproval(a, baseURL, id, expectedFailures);
  try {
    await a.locator(selectors.approve).click();
    const original = (await lost.done).body;
    fixture.assertApprovalPost(original, reviewed);
    await until(() => traffic.queries.some((query) => query.id === original.approval_request_id && query.status === 404),
      'a POST not delivered must be checked against the real receipt endpoint');
    await assertStoredPending(a, id, original);
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls);
    const queriesBeforeReload = traffic.queries.length;
    await a.reload({ waitUntil: 'networkidle' });
    await a.waitForFunction((value) => state.sessionId === value, id);
    await until(() => traffic.queries.length > queriesBeforeReload && traffic.queries.at(-1).id === original.approval_request_id,
      'reload must check the retained pending request');
    await assertStoredPending(a, id, original);
    assert.equal(traffic.posts.length, 1, 'reload must not automatically create another operation');
    await openInspector(a, 'plan');
    await assertNotice(a, env.profile.locale, original.approval_request_id);
    await a.locator(selectors.check).click();
    await until(() => traffic.queries.length > queriesBeforeReload + 1, 'visible Check control must query the receipt');
    const retry = fixture.nextPost(a, baseURL, id, 'planmode/approve');
    await a.locator(selectors.retry).click();
    const response = await retry;
    assert.equal(response.status(), 202, await response.text());
    assert.deepEqual(response.request().postDataJSON(), original, 'explicit retry must retain ID, target and parameters');
    await completed(context, baseURL, id);
    const receipt = await queryReceipt(context, baseURL, id, original.approval_request_id);
    assertReceipt(receipt, original, 'admitted');
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
    await assertNotGenerating(a);
    return { session_id: id, request_id: original.approval_request_id, reviewed,
      provider_before: beforeCalls, provider_after: beforeCalls + 1,
      ui_approval_posts: traffic.posts.length, receipt_queries: traffic.queries,
      delivery: 'first POST aborted before server; reload and explicit retry keep the same request' };
  } finally {
    traffic.dispose();
    await lost.dispose();
  }
}

async function lateResponseIsolation(env) {
  const { a, context, baseURL, providerLogPath, expectedFailures } = env;
  const idA = await create(env, 'delayed-A');
  const idB = await create(env, 'isolated-B');
  await openSessionUI(a, idA, 'plan');
  const trafficA = observe(a, baseURL, idA);
  const trafficB = observe(a, baseURL, idB);
  const delayed = await delayNextApproval(a, baseURL, idA);
  const lostB = await loseNextApproval(a, baseURL, idB, expectedFailures);
  const beforeA = await providerCalls(providerLogPath, idA);
  const beforeB = await providerCalls(providerLogPath, idB);
  try {
    await a.locator(selectors.approve).dblclick();
    const old = await delayed.fetched;
    assert.equal(old.status, 202, old.text);
    await completed(context, baseURL, idA);
    assert.equal(trafficA.posts.length, 1, 'double-click must not allocate a second operation');
    await openSessionUI(a, idB, 'plan');
    await a.locator(selectors.approve).click();
    const pendingB = (await lostB.done).body;
    await until(() => trafficB.queries.some((query) => query.id === pendingB.approval_request_id && query.status === 404),
      'B has its own checked, unaccepted pending operation');
    const draft = 'Settings must remain unchanged: session B draft.';
    await closeInspector(a);
    await a.locator('#chat-input').fill(draft);
    const operationB = await assertStoredPending(a, idB, pendingB);
    await assertNotGenerating(a);
    const generationWatch = await watchGenerating(a);
    delayed.release();
    await delayed.done;
    await a.waitForTimeout(150);
    assert.equal(await a.evaluate(() => state.sessionId), idB);
    assert.equal(await a.locator('#chat-input').inputValue(), draft);
    assert.equal((await assertStoredPending(a, idB, pendingB)).phase, operationB.phase,
      'late A response must not replace B pending phase');
    await assertNotGenerating(a);
    assert.deepEqual(await generationWatch.stop(), [], 'late response must never show B as generating');
    assert.equal(trafficA.posts.length, 1);
    assert.equal(trafficB.posts.length, 1);
    assert.equal(await providerCalls(providerLogPath, idA), beforeA + 1);
    assert.equal(await providerCalls(providerLogPath, idB), beforeB);
    await openInspector(a, 'plan');
    await assertNotice(a, env.profile.locale, pendingB.approval_request_id);
    const sameSessionPeer = await sameSessionPeerIsolation(env);
    return { session_id: idB, late_session_id: idA, request_id: pendingB.approval_request_id,
      late_request_id: old.body.approval_request_id, ui_approval_posts: { A: 1, B: 1 },
      provider_delta: { A: 1, B: 0 }, preserved_draft: draft,
      control_flow: 'double-click A, real history click B, B pending, release actual A response',
      shared_session_peer: sameSessionPeer };
  } finally {
    delayed.release();
    await delayed.dispose();
    await lostB.dispose();
    trafficA.dispose();
    trafficB.dispose();
  }
}

async function sameSessionPeerIsolation(env) {
  const { a, context, baseURL, sessionRoot, providerLogPath } = env;
  const id = await create(env, 'shared-storage-peer');
  await openSessionUI(a, id, 'plan');
  const originalTarget = await fixture.displayedTarget(a);
  const beforeCalls = await providerCalls(providerLogPath, id);
  const trafficA = observe(a, baseURL, id);
  const delayed = await delayNextApproval(a, baseURL, id);
  let peer, trafficPeer;
  try {
    await a.locator(selectors.approve).click();
    const old = await delayed.fetched;
    assert.equal(old.status, 202, old.text);
    fixture.assertApprovalPost(old.body, originalTarget);
    await completed(context, baseURL, id);
    // This page shares the actual browser context and localStorage with A.
    // Its restored intent and Check control query the real old receipt.
    peer = await context.newPage();
    collectErrors(peer, `${env.suffix}-shared-session`, 'Peer', env.expectedFailures,
      env.browserErrors, env.expectedTransportFailures);
    trafficPeer = observe(peer, baseURL, id);
    await peer.goto(baseURL, { waitUntil: 'networkidle' });
    await openSessionUI(peer, id, 'plan');
    await assertNotice(peer, env.profile.locale, old.body.approval_request_id);
    const checked = peer.waitForResponse((response) => response.request().method() === 'GET' &&
      response.url() === `${baseURL}/api/sessions/${id}/approval-receipts/${old.body.approval_request_id}`);
    await peer.locator(selectors.check).click();
    const checkedResponse = await checked;
    assert.equal(checkedResponse.status(), 200, await checkedResponse.text());
    assertReceipt(await checkedResponse.json(), old.body, 'admitted');
    await until(async () => (await assertStoredPending(peer, id, old.body)).phase === 'admitted',
      'peer must confirm the old admission before starting a new reviewed operation');
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
    await seedNewTarget(sessionRoot, id);
    await openSessionUI(peer, id, 'plan');
    const freshTarget = await fixture.displayedTarget(peer);
    assert.notEqual(freshTarget.expected_revision, originalTarget.expected_revision);
    const accepted = fixture.nextPost(peer, baseURL, id, 'planmode/approve');
    await peer.locator(selectors.approve).click();
    const response = await accepted;
    assert.equal(response.status(), 202, await response.text());
    const fresh = response.request().postDataJSON();
    fixture.assertApprovalPost(fresh, freshTarget);
    assert.notEqual(fresh.approval_request_id, old.body.approval_request_id);
    await completed(context, baseURL, id);
    await assertNotGenerating(peer);
    await assertNotice(peer, env.profile.locale, fresh.approval_request_id);
    const peerRecord = await assertStoredPending(peer, id, fresh);
    assert.equal(peerRecord.phase, 'admitted');
    // A is still in the same session/view while its old response is held.
    assert.equal(await a.evaluate(() => state.sessionId), id);
    await assertStoredPending(a, id, fresh);
    await assertNotGenerating(a);
    const generationWatch = await watchGenerating(a);
    delayed.release();
    await delayed.done;
    await closeInspector(a);
    await until(() => a.locator(selectors.check).isEnabled(), 'old A response must finish processing');
    await assertNotGenerating(a);
    assert.deepEqual(await generationWatch.stop(), [], 'old accepted response must never show a fresh generating state');
    const after = await assertStoredPending(a, id, fresh);
    assert.equal(after.phase, peerRecord.phase, 'old accepted response cannot replace the peer operation phase');
    await assertNotice(a, env.profile.locale, fresh.approval_request_id);
    assert.equal(trafficA.posts.length, 1, 'A must not post again after its late response');
    assert.equal(trafficPeer.posts.length, 1, 'peer performs exactly one new-target approval');
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 2);
    const oldReceipt = await queryReceipt(context, baseURL, id, old.body.approval_request_id);
    const freshReceipt = await queryReceipt(context, baseURL, id, fresh.approval_request_id);
    assertReceipt(oldReceipt, old.body, 'admitted');
    assertReceipt(freshReceipt, fresh, 'admitted');
    assert.notEqual(oldReceipt.approval.lookup.receipt.operation_id, freshReceipt.approval.lookup.receipt.operation_id);
    assert.notEqual(oldReceipt.approval.lookup.receipt.recovery.run_generation,
      freshReceipt.approval.lookup.receipt.recovery.run_generation);
    const ledger = await readLedger(sessionRoot, id);
    assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'admitted').length, 2);
    assert.equal(Object.keys(ledger.target_admissions).length, 2);
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 2);
    return { session_id: id, old_request_id: old.body.approval_request_id,
      peer_request_id: fresh.approval_request_id, original_target: originalTarget, peer_target: freshTarget,
      provider_before: beforeCalls, provider_after: beforeCalls + 2,
      ui_approval_posts: { A: 1, peer: 1 }, peer_receipt_queries: trafficPeer.queries,
      preserved_storage_identity: after.approval_request_id,
      control_flow: 'same-context peer Check, fresh-target real Approve, release old A accepted response',
      api_checks: ['query both canonical receipts: distinct operation IDs and generations, provider delta remains two'] };
  } finally {
    delayed.release();
    await delayed.dispose();
    trafficA.dispose();
    trafficPeer?.dispose();
    if (peer) await peer.close();
  }
}

async function coverageNewID(env) {
  const { a, b, context, baseURL, providerLogPath } = env;
  const id = await create(env, 'coverage', true);
  await fixture.patchMission(b, baseURL, id, fixture.missionPlan(false));
  await openSessionUI(a, id, 'plan');
  const reviewed = await fixture.displayedTarget(a);
  const beforeCalls = await providerCalls(providerLogPath, id);
  const traffic = observe(a, baseURL, id);
  try {
    const first = fixture.nextPost(a, baseURL, id, 'planmode/approve');
    await a.locator(selectors.approve).click();
    const blocked = await first;
    assert.equal(blocked.status(), 409, await blocked.text());
    const original = blocked.request().postDataJSON();
    fixture.assertApprovalPost(original, reviewed);
    assertReceipt(await blocked.json(), original, 'rejected');
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls);
    const changedParams = await apiApproval(context, baseURL, id, { ...original, override_coverage: true });
    assert.equal(changedParams.status, 409, changedParams.text);
    assert.equal(changedParams.json.code, 'APPROVAL_REQUEST_CONFLICT');
    const dialog = a.locator('.confirm-dialog');
    await dialog.waitFor();
    assert.match(await dialog.innerText(), env.profile.locale === 'zh-CN' ? /覆盖|验证/ : /validation coverage/i);
    const confirmed = fixture.nextPost(a, baseURL, id, 'planmode/approve');
    await dialog.locator('.confirm-dialog-confirm').click();
    const approved = await confirmed;
    assert.equal(approved.status(), 202, await approved.text());
    const newRequest = approved.request().postDataJSON();
    fixture.assertApprovalPost(newRequest, reviewed, true);
    assert.notEqual(newRequest.approval_request_id, original.approval_request_id);
    assertReceipt(await approved.json(), newRequest, 'admitted');
    await completed(context, baseURL, id);
    const ledger = await readLedger(env.sessionRoot, id);
    assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'rejected').length, 1);
    assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'admitted').length, 1);
    assert.equal(Object.keys(ledger.target_admissions).length, 1);
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
    assert.equal(traffic.posts.length, 2);
    await assertNotGenerating(a);
    await assertNotice(a, env.profile.locale, newRequest.approval_request_id);
    return { session_id: id, rejected_request_id: original.approval_request_id,
      request_id: newRequest.approval_request_id, reviewed, provider_delta: 1,
      api_checks: ['same ID plus changed override conflicts'],
      companion_browser_scope_change_checks: 'approval_cas coverage_confirmation and coverage_display_changed' };
  } finally {
    traffic.dispose();
  }
}

async function completedAlias(env) {
  const { a, b, context, baseURL, sessionRoot, providerLogPath } = env;
  const id = await create(env, 'linked-canonical-alias', true);
  await fixture.patchMission(b, baseURL, id, fixture.missionPlan(true));
  const originalDetail = await fixture.coherentSnapshot(context, baseURL, id);
  const reviewed = fixture.target(originalDetail);
  const thaw = await fixture.freezeDetail(a, baseURL, id, originalDetail);
  const beforeCalls = await providerCalls(providerLogPath, id);
  let canonicalRequest;
  let operationID;
  try {
    await openSessionUI(a, id, 'plan');
    await openSessionUI(b, id, 'goal');
    const accepted = fixture.nextPost(b, baseURL, id, 'mission/plan/approve');
    await b.locator(selectors.goalApprove).click();
    const response = await accepted;
    assert.equal(response.status(), 202, await response.text());
    canonicalRequest = response.request().postDataJSON();
    fixture.assertApprovalPost(canonicalRequest, reviewed);
    operationID = (await response.json()).approval.lookup.receipt.operation_id;
    await completed(context, baseURL, id);
    const originalReceipt = (await queryReceipt(context, baseURL, id,
      canonicalRequest.approval_request_id)).approval.lookup.receipt;
    const beforeAlias = await fixture.durableFacts(sessionRoot, id);
    await assertNotGenerating(a);
    const generationWatch = await watchGenerating(a);
    const aliasResponse = fixture.nextPost(a, baseURL, id, 'planmode/approve');
    await a.locator(selectors.approve).click();
    const alias = await aliasResponse;
    assert.equal(alias.status(), 200, await alias.text());
    const aliasRequest = alias.request().postDataJSON();
    fixture.assertApprovalPost(aliasRequest, reviewed);
    assert.notEqual(aliasRequest.approval_request_id, canonicalRequest.approval_request_id);
    const result = await alias.json();
    assertReceipt(result, aliasRequest, 'admitted', true);
    assert.equal(result.approval.lookup.receipt.operation_id, operationID);
    assert.deepEqual(stableReceipt(result.approval.lookup.receipt), stableReceipt(originalReceipt));
    assert.deepEqual(withoutLedger(await fixture.durableFacts(sessionRoot, id)), withoutLedger(beforeAlias));
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
    await assertNotGenerating(a);
    assert.deepEqual(await generationWatch.stop(), [], 'completed alias must never show a generating message');
    await assertNotice(a, env.profile.locale, aliasRequest.approval_request_id);
  } finally {
    await thaw();
  }
  // These real API checks prove lookup before current target and generation.
  // They are not counted as additional clicked UI scenarios.
  await openSessionUI(b, id, 'plan');
  await ordinaryContinue(b, baseURL, id, 'Explicit ordinary follow-up after the old approval.');
  const later = await completed(context, baseURL, id);
  const query = await queryReceipt(context, baseURL, id, canonicalRequest.approval_request_id);
  assert.equal(query.approval.lookup.receipt.operation_id, operationID);
  assert.notEqual(query.current_state.run_generation, query.approval.lookup.receipt.recovery.run_generation);
  assert.equal(query.current_state.run_generation, later.state.run_generation);
  assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 2);
  await fixture.patchMission(b, baseURL, id, { requirements: [{ id: 'requirement_scope', text: 'Settings must remain unchanged: later linked scope.' }] });
  const changed = await fixture.coherentSnapshot(context, baseURL, id);
  assert.notEqual(changed.plan_mode.approval_revision, reviewed.expected_revision);
  const beforeReplay = await fixture.durableFacts(sessionRoot, id);
  const replay = await apiApproval(context, baseURL, id, canonicalRequest, 'mission/plan/approve');
  assert.equal(replay.status, 200, replay.text);
  assertReceipt(replay.json, canonicalRequest, 'admitted', true);
  assert.equal(replay.json.approval.lookup.receipt.operation_id, operationID);
  assert.deepEqual(await fixture.durableFacts(sessionRoot, id), beforeReplay);
  assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 2);
  await seedNewTarget(sessionRoot, id);
  await openSessionUI(a, id, 'plan');
  const newTarget = await fixture.displayedTarget(a);
  assert.notEqual(newTarget.expected_revision, reviewed.expected_revision);
  const admitted = fixture.nextPost(a, baseURL, id, 'planmode/approve');
  await a.locator(selectors.approve).click();
  const fresh = await admitted;
  assert.equal(fresh.status(), 202, await fresh.text());
  const freshRequest = fresh.request().postDataJSON();
  fixture.assertApprovalPost(freshRequest, newTarget);
  assert.notEqual(freshRequest.approval_request_id, canonicalRequest.approval_request_id);
  await completed(context, baseURL, id);
  assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 3);
  const ledger = await readLedger(sessionRoot, id);
  assert.equal(Object.values(ledger.operations).filter((op) => op.stage === 'admitted').length, 2);
  assert.equal(Object.keys(ledger.target_admissions).length, 2);
  await assertNotGenerating(a);
  await assertNotice(a, env.profile.locale, freshRequest.approval_request_id);
  return { session_id: id, request_id: canonicalRequest.approval_request_id, operation_id: operationID,
    reviewed, new_target: newTarget, new_request_id: freshRequest.approval_request_id,
    provider_delta: { original_admission: 1, completed_alias_and_replay: 0, ordinary_continue: 1, new_target_admission: 1 },
    api_checks: ['same-ID linked replay after semantic change', 'old receipt query separates later generation'],
    fixture_setup: 'new pending plan seeded only after settled ordinary continue; existing historical revisions preserved' };
}

async function legacyRecovery(env) {
  const { a, b, context, baseURL, sessionRoot, providerLogPath } = env;
  const id = await create(env, 'legacy-direct');
  const old = await fixture.coherentSnapshot(context, baseURL, id);
  const thaw = await fixture.freezeDetail(a, baseURL, id, old);
  const beforeCalls = await providerCalls(providerLogPath, id);
  try {
    await openSessionUI(a, id, 'plan');
    await seedLegacyExecuting(sessionRoot, id);
    const before = await fixture.durableFacts(sessionRoot, id);
    const approval = fixture.nextPost(a, baseURL, id, 'planmode/approve');
    await a.locator(selectors.approve).click();
    const response = await approval;
    assert.equal(response.status(), 409, await response.text());
    assert.equal((await response.json()).code, 'APPROVAL_RECOVERY_REQUIRED');
    fixture.assertApprovalPost(response.request().postDataJSON(), fixture.target(old));
    assert.deepEqual(await fixture.durableFacts(sessionRoot, id), before);
    assert.equal(await providerCalls(providerLogPath, id), beforeCalls);
    const copy = a.locator('#toast-rack .toast, #approval-operation-notice').filter({ hasText: env.profile.locale === 'zh-CN' ? /恢复|继续/ : /recovery|ordinary continue/i }).last();
    await copy.waitFor();
    assert.match(await copy.innerText(), env.profile.locale === 'zh-CN' ? /恢复|继续/ : /recovery|ordinary continue/i);
    await assertNotGenerating(a);
  } finally {
    await thaw();
  }
  await openSessionUI(a, id, 'plan');
  await ordinaryContinue(a, baseURL, id, 'Explicit ordinary continue to recover the legacy session.');
  const recovered = await completed(context, baseURL, id);
  assert.equal(recovered.plan_mode.approved_revision || '', '', 'legacy recovery must not backfill a reviewed revision');
  assert.equal(await readOptional(path.join(sessionRoot, id, 'approval-operations.json')), null);
  assert.equal(await providerCalls(providerLogPath, id), beforeCalls + 1);
  const linked = await create(env, 'legacy-linked-facts', true);
  await fixture.patchMission(b, baseURL, linked, fixture.missionPlan(true));
  await seedLegacyExecuting(sessionRoot, linked);
  const detail = await fixture.coherentSnapshot(context, baseURL, linked);
  const linkedBefore = await providerCalls(providerLogPath, linked);
  const missing = await apiApproval(context, baseURL, linked, {}, 'mission/plan/approve');
  assert.equal(missing.status, 400, missing.text);
  const repair = await apiApproval(context, baseURL, linked, fixture.target(detail), 'mission/plan/approve');
  assert.equal(repair.status, 200, repair.text);
  assert.equal(repair.json.approval, undefined, 'linked executing repair is not an execution receipt');
  const repaired = await fixture.coherentSnapshot(context, baseURL, linked);
  assert.equal(repaired.goal.mission.plan_status, 'approved');
  assert.equal(repaired.goal.mission.approved_revision || '', '');
  assert.equal(repaired.plan_mode.approved_revision || '', '');
  assert.equal(await providerCalls(providerLogPath, linked), linkedBefore);
  assert.equal(await readOptional(path.join(sessionRoot, linked, 'approval-operations.json')), null);
  await assertNotGenerating(a);
  return { session_id: id, linked_session_id: linked, provider_delta: { legacy_approval: 0, explicit_ordinary_continue: 1, linked_fact_repair: 0 },
    ui_controls: ['old real Approve returns explicit recovery', 'composer Send performs ordinary continue'],
    api_checks: ['linked executing missing target: HTTP 400', 'linked executing complete target: HTTP 200, no receipt/provider, unknown revision remains unknown'],
    fixture_setup: 'stopped local fixture enters legacy executing without receipt or assigned historical revision' };
}

function create(env, label, linked = false) {
  return fixture.createFixture(env.context, env.baseURL, `receipt ${label} ${env.suffix}`, linked, 'E2E_UI_PLAN');
}

function completed(context, baseURL, id) {
  return fixture.waitForDetail(context, baseURL, id, (detail) => detail.state?.status === 'completed' && !detail.active_handle);
}

async function closeInspector(page) {
  if (await page.locator('#inspector-slide-out').getAttribute('aria-hidden') === 'false') {
    await page.locator('#inspector-slide-out [data-close-inspector]').click();
  }
}

async function openSessionUI(page, id, tab) {
  await closeInspector(page);
  await page.locator('[data-view="history"]').click();
  const row = page.locator(`#history-view [data-open-session="${id}"]`);
  await row.waitFor();
  await row.click();
  await page.waitForFunction((value) => state.sessionId === value && state.sessionDetail?.metadata?.id === value, id);
  await openInspector(page, tab);
}

async function openInspector(page, tab) {
  if (await page.locator('#inspector-slide-out').getAttribute('aria-hidden') !== 'false') {
    await page.locator('#inspector-toggle-btn').click();
  }
  const selected = page.locator(`[data-inspector-tab="${tab}"][aria-selected="true"]`);
  if (await selected.count() === 0) await page.locator(`[data-inspector-tab="${tab}"]`).click();
  await selected.waitFor();
}

async function ordinaryContinue(page, baseURL, id, message) {
  await closeInspector(page);
  await page.locator('#chat-input').fill(message);
  const continued = fixture.nextPost(page, baseURL, id, 'continue');
  await page.locator('#send-btn').click();
  const response = await continued;
  assert.equal(response.status(), 202, await response.text());
}

async function assertNotGenerating(page) {
  await page.waitForFunction(() => !runViewState.generating);
  assert.equal(await page.locator('.message.assistant.pending').count(), 0, 'receipt replay must not display a generating message');
}

async function watchGenerating(page) {
  const key = '__approvalReceiptsE2EGenerationWatch';
  await page.evaluate((key) => {
    const observations = [];
    const pendingSelector = '.message.assistant.pending';
    const observer = new MutationObserver((records) => {
      if (observations.length >= 16) return;
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (node.nodeType !== Node.ELEMENT_NODE) continue;
          const pending = node.matches(pendingSelector) ? node : node.querySelector(pendingSelector);
          if (pending) observations.push({ kind: 'pending-message-added', text: pending.textContent.slice(0, 300) });
        }
      }
      if (runViewState.generating) observations.push({ kind: 'generating', pending: Boolean(document.querySelector(pendingSelector)) });
    });
    observer.observe(document.body, { childList: true, subtree: true, attributes: true });
    window[key] = { observations, observer };
  }, key);
  return { stop: async () => {
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    return page.evaluate((key) => {
      const watch = window[key];
      watch.observer.disconnect();
      delete window[key];
      return watch.observations;
    }, key);
  } };
}

async function assertNotice(page, locale, requestID) {
  await closeInspector(page);
  const notice = page.locator(`${selectors.notice}:visible`).first();
  await notice.waitFor();
  await notice.scrollIntoViewIfNeeded();
  const text = await notice.innerText();
  assert.match(text, locale === 'zh-CN' ? /审批|批准|回执|请求/ : /approval|receipt|request/i);
  if (requestID) assert.equal(await notice.getAttribute('data-approval-request-id'), requestID);
  const box = await notice.boundingBox();
  const viewport = page.viewportSize();
  assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width + 1 &&
    box.y + box.height <= viewport.height + 1, `approval notice must fit viewport: ${JSON.stringify(box)}`);
  assert.equal(await notice.evaluate((element) => {
    const box = element.getBoundingClientRect();
    return element.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
  }), true, 'approval receipt notice must not be obscured by another UI layer');
}

function stableReceipt(receipt) {
  return { schema_version: receipt.schema_version, session_id: receipt.session_id,
    operation_id: receipt.operation_id, target: receipt.target, parameters: receipt.parameters,
    fingerprint: receipt.fingerprint, run_generation: receipt.recovery.run_generation };
}

async function assertStoredPending(page, id, body) {
  const value = await page.evaluate((key) => JSON.parse(localStorage.getItem(key) || 'null'), pendingStorageKey);
  assert.ok(value, 'pending operation must survive in browser storage');
  assert.equal(value.version, 1);
  const operation = value.sessions?.[id];
  assert.ok(operation, 'pending operation must remain session-scoped');
  assert.equal(operation.session_id, id);
  assert.equal(operation.approval_request_id, body.approval_request_id);
  const { approval_request_id, ...parameters } = body;
  assert.deepEqual(operation.parameters, { ...parameters, override_coverage: parameters.override_coverage ?? false },
    'pending storage must retain the exact captured target and explicit parameters');
  return operation;
}

function assertReceipt(response, request, stage, replay) {
  assert.ok(response.approval?.lookup?.found, JSON.stringify(response));
  const { binding, receipt } = response.approval.lookup;
  assert.equal(binding.approval_request_id, request.approval_request_id);
  assert.equal(receipt.stage, stage);
  assert.equal(binding.operation_id, receipt.operation_id);
  const expected = { plan_mode_id: request.plan_mode_id, plan_version: request.plan_version, expected_revision: request.expected_revision };
  assert.deepEqual(binding.parameters.target, expected);
  assert.deepEqual(receipt.target, expected);
  assert.equal(binding.parameters.override_coverage, request.override_coverage ?? false);
  assert.ok(binding.fingerprint && receipt.fingerprint);
  if (replay !== undefined) assert.equal(response.approval.replay, replay);
}

async function apiApproval(context, baseURL, id, data, action = 'planmode/approve') {
  const response = await context.request.post(`${baseURL}/api/sessions/${id}/${action}`, {
    headers: { 'X-Aegis-Agent-Web': '1' }, data
  });
  const text = await response.text();
  return { status: response.status(), text, json: JSON.parse(text) };
}

async function queryReceipt(context, baseURL, id, requestID) {
  return fixture.getJSON(context, `${baseURL}/api/sessions/${id}/approval-receipts/${encodeURIComponent(requestID)}`);
}

async function readLedger(root, id) {
  return JSON.parse(await readFile(path.join(root, id, 'approval-operations.json'), 'utf8'));
}

async function readOptional(file) {
  try { return await readFile(file, 'utf8'); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
}

async function providerCalls(file, id) {
  const text = await readOptional(file);
  return (text || '').split('\n').filter(Boolean).map((line) => JSON.parse(line)).filter((row) => row.session_id === id).length;
}

function withoutLedger(facts) {
  const copy = { ...facts };
  delete copy['approval-operations.json'];
  return copy;
}

// Fixture mutation is limited to these settled temporary sessions. It never
// invents a receipt, claim, JSONL approval fact, or historical known revision.
async function seedNewTarget(root, id) {
  const file = path.join(root, id, 'planmode.json');
  const plan = JSON.parse(await readFile(file, 'utf8'));
  assert.equal(plan.status, 'executing');
  plan.status = 'awaiting_approval';
  plan.plan_version += 1;
  plan.summary = 'Settings must remain unchanged: new plan for a second reviewed admission.';
  plan.plan_markdown += '\n\n# Fresh review\n\nApprove this newly captured target explicitly.';
  plan.updated_at = new Date().toISOString();
  delete plan.approval_revision;
  await writeFile(file, JSON.stringify(plan) + '\n', { mode: 0o600 });
}

async function seedLegacyExecuting(root, id) {
  assert.equal(await readOptional(path.join(root, id, 'approval-operations.json')), null);
  const file = path.join(root, id, 'planmode.json');
  const plan = JSON.parse(await readFile(file, 'utf8'));
  assert.equal(plan.status, 'awaiting_approval');
  assert.equal(plan.approvals?.length || 0, 0);
  plan.status = 'executing';
  plan.approved_version = plan.plan_version;
  plan.updated_at = new Date().toISOString();
  delete plan.approval_revision;
  delete plan.approved_revision;
  await writeFile(file, JSON.stringify(plan) + '\n', { mode: 0o600 });
}

function observe(page, baseURL, id) {
  const posts = [];
  const queries = [];
  const prefix = `${baseURL}/api/sessions/${id}/`;
  const requestListener = (request) => {
    if (request.method() === 'POST' && ['planmode/approve', 'mission/plan/approve'].some((action) => request.url() === prefix + action)) {
      posts.push(request.postDataJSON());
    }
  };
  const responseListener = (response) => {
    if (response.request().method() === 'GET' && response.url().startsWith(prefix + 'approval-receipts/')) {
      queries.push({ id: decodeURIComponent(response.url().slice((prefix + 'approval-receipts/').length)), status: response.status() });
    }
  };
  page.on('request', requestListener);
  page.on('response', responseListener);
  return { posts, queries, dispose: () => { page.off('request', requestListener); page.off('response', responseListener); } };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

async function loseNextApproval(page, baseURL, id, expectedFailures, afterCommit) {
  const url = `${baseURL}/api/sessions/${id}/planmode/approve`;
  const result = deferred();
  let intercepted = false;
  const handler = async (route) => {
    if (intercepted || route.request().method() !== 'POST') return route.continue();
    intercepted = true;
    expectedFailures.add(url);
    try {
      const body = route.request().postDataJSON();
      let status = null, text = '';
      if (afterCommit) {
        const response = await route.fetch();
        status = response.status();
        text = await response.text();
        await afterCommit();
      }
      await route.abort('failed');
      result.resolve({ body, status, text });
    } catch (error) { result.reject(error); }
  };
  await page.route(url, handler);
  return { done: result.promise, dispose: () => page.unroute(url, handler) };
}

async function delayNextApproval(page, baseURL, id) {
  const url = `${baseURL}/api/sessions/${id}/planmode/approve`;
  const fetched = deferred(), released = deferred(), done = deferred();
  let intercepted = false;
  const handler = async (route) => {
    if (intercepted || route.request().method() !== 'POST') return route.continue();
    intercepted = true;
    try {
      const response = await route.fetch();
      fetched.resolve({ body: route.request().postDataJSON(), status: response.status(), text: await response.text() });
      await released.promise;
      await route.fulfill({ response });
      done.resolve();
    } catch (error) { fetched.reject(error); done.reject(error); }
  };
  await page.route(url, handler);
  return { fetched: fetched.promise, release: released.resolve, done: done.promise,
    dispose: () => page.unroute(url, handler) };
}

async function until(predicate, message, timeout = 25_000) {
  const deadline = Date.now() + timeout;
  do {
    if (await predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 80));
  } while (Date.now() < deadline);
  assert.fail(message);
}

function collectErrors(page, scenario, tab, expectedFailures, errors, expectedTransportFailures) {
  page.on('pageerror', (error) => errors.page.push({ scenario, tab, message: error.message }));
  page.on('requestfailed', (request) => {
    const entry = { scenario, tab, url: request.url(), error: request.failure()?.errorText };
    if (expectedFailures.has(request.url()) && request.method() === 'POST') expectedTransportFailures.push(entry);
    else errors.request.push(entry);
  });
  page.on('console', (message) => {
    if (message.type() !== 'error') return;
    const text = message.text();
    const url = message.location().url;
    if (/^Failed to load resource: the server responded with a status of (400|404|409)\b/.test(text) && /\/api\/sessions\//.test(url)) return;
    if (/^Failed to load resource: net::ERR_FAILED\b/.test(text) && expectedFailures.has(url)) return;
    errors.console.push({ scenario, tab, message: text, url });
  });
}
