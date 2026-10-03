import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';

const staleCopy = {
  'zh-CN': '计划已更新，请重新审阅。',
  en: 'The plan has changed. Please review it again.'
};
const viewports = {
  desktop: { width: 1440, height: 1000 },
  mobile: { width: 390, height: 844 }
};
const protocolAudits = new WeakMap();

// The only intercepted requests are tab A's detail GETs. Approval, revision,
// mission mutations, and provider execution use the real deterministic service.
export async function runApprovalCASE2E({ browser, baseURL, sessionRoot, check, capture }) {
  assert.ok(browser && baseURL && sessionRoot, 'browser, baseURL, and sessionRoot are required');
  assert.equal(typeof check, 'function');
  assert.equal(typeof capture, 'function');
  const evidence = [];
  const browserErrors = { page: [], console: [], request: [] };
  const expectedProtocolErrors = [];
  const unexpectedHTTP = [];
  const protocolResponses = [];
  for (const locale of ['zh-CN', 'en']) {
    for (const [size, viewport] of Object.entries(viewports)) {
      const context = await browser.newContext({ viewport });
      await context.addInitScript((value) => localStorage.setItem('aegis-agent.locale.v1', value), locale);
      const a = await context.newPage();
      const b = await context.newPage();
      const pageErrors = [];
      const suffix = `${locale}-${size}`;
      const audits = [];
      for (const [tab, page] of [['A', a], ['B', b]]) {
        audits.push(createApprovalProtocolAudit({ page, baseURL, scenario: suffix, tab }));
        page.on('pageerror', (error) => {
          pageErrors.push(error.message);
          browserErrors.page.push({ scenario: suffix, tab, message: error.message });
        });
        page.on('requestfailed', (request) => {
          browserErrors.request.push({ scenario: suffix, tab, url: request.url(), error: request.failure()?.errorText });
        });
      }
      try {
        await Promise.all([a.goto(baseURL), b.goto(baseURL)]);
        for (const page of [a, b]) {
          await page.waitForFunction((value) => window.AegisI18n?.locale?.() === value, locale);
          assert.equal(await page.locator('html').getAttribute('lang'), locale);
        }

        await check(`approval CAS: stale V1 rejects real V2 before execution (${suffix})`, async () => {
          await settleToasts(a);
          const id = await createFixture(context, baseURL, `version ${suffix}`);
          const v1 = await coherentSnapshot(context, baseURL, id);
          assert.equal(v1.plan_mode.plan_version, 1);
          let staleRequestID;
          const thaw = await freezeDetail(a, baseURL, id, v1);
          try {
            await openSession(a, id, 'plan');
            await a.locator('.plan-panel').filter({ hasText: 'Original plan' }).waitFor();
            const reviewed = await displayedTarget(a);
            assert.deepEqual(reviewed, target(v1));
            await openSession(b, id, 'plan');
            await b.locator('#inspector-slide-out [data-plan-action="revise"]').click();
            await b.locator('#chat-input').fill('Use the narrower verified scope for the browser CAS test.');
            const revised = nextPost(b, baseURL, id, 'planmode/revise');
            await b.locator('#send-btn').click();
            assert.equal((await revised).status(), 202);
            const v2 = await waitForPlan(context, baseURL, id, 2);
            await openSession(b, id, 'plan');
            await b.locator('.plan-panel').filter({ hasText: 'Revised plan' }).waitFor();
            assert.notEqual(v2.plan_mode.approval_revision, reviewed.expected_revision);
            assert.deepEqual(await displayedTarget(a), reviewed);
            const before = await durableFacts(sessionRoot, id);
            const result = await clickStaleApproval(a, baseURL, id, 'planmode/approve', reviewed,
              '#inspector-slide-out [data-plan-action="approve"]', locale);
            staleRequestID = result.request().postDataJSON().approval_request_id;
            await assertUnaccepted(context, baseURL, sessionRoot, id, before, 2);
            evidence.push({ scenario: 'version', locale, viewport: size, session_id: id, reviewed, status: result.status(), code: (await result.json()).code });
            await capture(a, `approval-cas-version-${suffix}.png`, { retainToasts: true });
          } finally {
            await thaw();
          }
          // A fresh review is a positive control: the same real button and
          // service accept V2 and preserve the revision actually displayed.
          await openSession(a, id, 'plan');
          const reviewedV2 = await displayedTarget(a);
          const approved = nextPost(a, baseURL, id, 'planmode/approve');
          await a.locator('#inspector-slide-out [data-plan-action="approve"]').click();
          const response = await approved;
          assert.equal(response.status(), 202, await response.text());
          assertApprovalPost(response.request().postDataJSON(), reviewedV2);
          assert.notEqual(response.request().postDataJSON().approval_request_id, staleRequestID,
            'a freshly reviewed V2 must start a new request instead of querying the rejected V1 forever');
          const completed = await waitForDetail(context, baseURL, id,
            (detail) => detail.state?.status === 'completed' && !detail.active_handle);
          assert.equal(completed.plan_mode.approved_revision, reviewedV2.expected_revision);
          assert.ok(completed.messages.some((message) => message.meta?.source === 'planmode_approval' &&
            message.meta.approved_revision === reviewedV2.expected_revision));
        });

        await check(`approval CAS: linked mission change rejects the same plan version (${suffix})`, async () => {
          await settleToasts(a);
          const id = await createFixture(context, baseURL, `linked ${suffix}`, true);
          await patchMission(b, baseURL, id, missionPlan(true));
          const v1 = await coherentSnapshot(context, baseURL, id);
          assert.equal(v1.plan_mode.linked_goal_id, v1.goal.goal_id);
          const thaw = await freezeDetail(a, baseURL, id, v1);
          try {
            await openSession(a, id, 'goal');
            await a.locator('.goal-panel').filter({ hasText: 'Reviewed linked scope' }).waitFor();
            const reviewed = await displayedTarget(a);
            await openSession(b, id, 'goal');
            await patchMission(b, baseURL, id, {
              requirements: [{ id: 'requirement_scope', text: 'The linked mission now includes an additional deliverable.' }],
              features: [{ id: 'feature_scope', title: 'Changed linked scope', status: 'pending', claimed_assertions: ['validation_scope'] }]
            });
            const changed = await coherentSnapshot(context, baseURL, id);
            assert.equal(changed.plan_mode.plan_mode_id, reviewed.plan_mode_id);
            assert.equal(changed.plan_mode.plan_version, reviewed.plan_version);
            assert.notEqual(changed.plan_mode.approval_revision, reviewed.expected_revision);
            await openSession(b, id, 'goal');
            await b.locator('.goal-panel').filter({ hasText: 'Changed linked scope' }).waitFor();
            assert.deepEqual(await displayedTarget(a), reviewed);
            const before = await durableFacts(sessionRoot, id);
            const result = await clickStaleApproval(a, baseURL, id, 'mission/plan/approve', reviewed,
              '#inspector-slide-out [data-goal-action="approve-plan"]', locale);
            await assertUnaccepted(context, baseURL, sessionRoot, id, before, reviewed.plan_version);
            evidence.push({ scenario: 'linked_mission', locale, viewport: size, session_id: id, reviewed, status: result.status(), code: (await result.json()).code });
            await capture(a, `approval-cas-linked-mission-${suffix}.png`, { retainToasts: true });
          } finally {
            await thaw();
          }
        });

        await check(`approval CAS: coverage confirmation retains the reviewed target (${suffix})`, async () => {
          await settleToasts(a);
          const id = await createFixture(context, baseURL, `coverage ${suffix}`, true);
          await patchMission(b, baseURL, id, missionPlan(false));
          const v1 = await coherentSnapshot(context, baseURL, id);
          assert.equal(v1.goal_facts.coverage.approval_blocked, true);
          const thaw = await freezeDetail(a, baseURL, id, v1);
          try {
            await openSession(a, id, 'plan');
            const reviewed = await displayedTarget(a);
            const posts = approvalPosts(a, baseURL, id);
            const first = nextPost(a, baseURL, id, 'planmode/approve');
            await a.locator('#inspector-slide-out [data-plan-action="approve"]').click();
            const blocked = await first;
            assert.equal(blocked.status(), 409);
            const blockedRequest = blocked.request().postDataJSON();
            assertApprovalPost(blockedRequest, reviewed);
            assert.equal((await blocked.json()).approval.lookup.receipt.stage, 'rejected');
            await allowApprovalProtocolError(blocked, { status: 409, code: 'APPROVAL_REJECTED', classification: 'coverage_rejected' });
            assert.match(JSON.stringify(await blocked.json()), /coverage/i);
            const dialog = a.locator('.confirm-dialog');
            await dialog.waitFor();
            assert.match(await dialog.innerText(), locale === 'zh-CN' ? /覆盖|验证/ : /validation coverage/i);
            await openSession(b, id, 'goal');
            // These mappings also change approval scope when written through
            // RecordGoalProgress; the browser exercises the public PATCH path.
            await patchMission(b, baseURL, id, {
              features: [{ id: 'feature_scope', title: 'Changed coverage scope', status: 'pending', claimed_assertions: ['validation_scope'] }],
              milestones: [{ id: 'milestone_scope', title: 'Coverage milestone', status: 'pending', feature_ids: ['feature_scope'], validation_ids: ['validation_scope'] }]
            });
            const changed = await coherentSnapshot(context, baseURL, id);
            assert.equal(changed.plan_mode.plan_mode_id, reviewed.plan_mode_id);
            assert.equal(changed.plan_mode.plan_version, reviewed.plan_version);
            assert.notEqual(changed.plan_mode.approval_revision, reviewed.expected_revision);
            assert.equal(changed.goal_facts.coverage.approval_blocked, false);
            const before = await durableFacts(sessionRoot, id);
            const retry = nextPost(a, baseURL, id, 'planmode/approve');
            const previousToasts = await toastIDs(a);
            await dialog.locator('.confirm-dialog-confirm').click();
            const rejected = await retry;
            await assertStaleResponse(rejected, { ...reviewed, override_coverage: true });
            await assertStaleUI(a, locale, previousToasts);
            await assertUnaccepted(context, baseURL, sessionRoot, id, before, reviewed.plan_version);
            assert.equal(posts.payloads.length, 2, 'coverage must not automatically approve another target');
            assertApprovalPost(posts.payloads[0], reviewed);
            assertApprovalPost(posts.payloads[1], reviewed, true);
            assert.notEqual(posts.payloads[1].approval_request_id, blockedRequest.approval_request_id,
              'coverage confirmation changes parameters and must use a new request ID');
            posts.dispose();
            evidence.push({ scenario: 'coverage_confirmation', locale, viewport: size, session_id: id, reviewed, status: rejected.status(), code: (await rejected.json()).code });
            await capture(a, `approval-cas-coverage-${suffix}.png`, { retainToasts: true });
          } finally {
            await thaw();
          }
          // With polling resumed, the dialog must also detect a newly displayed
          // target locally and require another review without a second POST.
          await settleToasts(a);
          await patchMission(b, baseURL, id, missionPlan(false));
          await openSession(a, id, 'plan');
          const reviewed = await displayedTarget(a);
          const posts = approvalPosts(a, baseURL, id);
          const first = nextPost(a, baseURL, id, 'planmode/approve');
          await a.locator('#inspector-slide-out [data-plan-action="approve"]').click();
          const blocked = await first;
          assert.equal(blocked.status(), 409);
          assert.equal((await blocked.json()).approval.lookup.receipt.stage, 'rejected');
          await allowApprovalProtocolError(blocked, { status: 409, code: 'APPROVAL_REJECTED', classification: 'coverage_rejected' });
          const dialog = a.locator('.confirm-dialog');
          await dialog.waitFor();
          await patchMission(b, baseURL, id, {
            requirements: [{ id: 'requirement_scope', text: 'This scope changed while the coverage dialog was open.' }]
          });
          await a.evaluate(async () => { await refreshCurrentSession(); });
          assert.notEqual((await displayedTarget(a)).expected_revision, reviewed.expected_revision);
          const before = await durableFacts(sessionRoot, id);
          const previousToasts = await toastIDs(a);
          await dialog.locator('.confirm-dialog-confirm').click();
          await assertStaleUI(a, locale, previousToasts);
          await assertUnaccepted(context, baseURL, sessionRoot, id, before, reviewed.plan_version);
          assert.equal(posts.payloads.length, 1, 'a changed display must not automatically approve or retry');
          assertApprovalPost(posts.payloads[0], reviewed);
          posts.dispose();
          evidence.push({ scenario: 'coverage_display_changed', locale, viewport: size, session_id: id, reviewed, approval_requests: 1 });
          await capture(a, `approval-cas-coverage-refreshed-${suffix}.png`, { retainToasts: true });
        });
        assert.deepEqual(pageErrors, [], `browser runtime errors (${suffix})`);
      } finally {
        for (const audit of audits) {
          const result = await audit.complete();
          browserErrors.console.push(...result.console);
          expectedProtocolErrors.push(...result.expected);
          unexpectedHTTP.push(...result.unexpected_http);
          protocolResponses.push(...result.responses);
        }
        await context.close();
      }
    }
  }
  process.stdout.write(`approval CAS HTTP 404 diagnostics: ${JSON.stringify(protocolResponses.filter((item) => item.status === 404))}\n`);
  assert.deepEqual(unexpectedHTTP, [], 'approval CAS unexpected HTTP protocol errors');
  assert.deepEqual(browserErrors, { page: [], console: [], request: [] }, 'approval CAS browser runtime errors');
  return { scenarios: evidence.length, evidence, browser_errors: browserErrors,
    expected_protocol_errors: expectedProtocolErrors, http_error_responses: protocolResponses };
}

async function createFixture(context, baseURL, label, linked = false, marker = 'E2E_UI_PLAN_REVISE') {
  const prompt = `${marker} browser approval ${label}`;
  const response = await context.request.post(`${baseURL}/api/sessions/start`, {
    headers: { 'X-Aegis-Agent-Web': '1' },
    data: {
      prompt,
      plan_mode: { enabled: true, objective: prompt },
      ...(linked ? { goal: { enabled: true, mode: 'mission', objective: prompt, require_plan_approval: true } } : {})
    }
  });
  assert.equal(response.status(), 202, await response.text());
  const { session_id: id } = await response.json();
  assert.ok(id);
  await waitForPlan(context, baseURL, id, 1);
  return id;
}

function missionPlan(covered) {
  return {
    plan_status: 'needs_approval',
    requirements: [{ id: 'requirement_scope', text: 'Review the linked scope before execution.' }],
    features: [{ id: 'feature_scope', title: 'Reviewed linked scope', status: 'pending', claimed_assertions: covered ? ['validation_scope'] : [] }],
    milestones: [{ id: 'milestone_scope', title: 'Coverage milestone', status: 'pending', feature_ids: ['feature_scope'], validation_ids: covered ? ['validation_scope'] : [] }],
    validation_contract: [{ id: 'validation_scope', kind: 'command', command: 'printf approval-cas', description: 'Validate the reviewed scope.', status: 'pending' }]
  };
}

async function patchMission(page, baseURL, id, data) {
  const result = await page.evaluate(async ({ url, data }) => {
    const response = await fetch(url, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-Aegis-Agent-Web': '1' }, body: JSON.stringify(data)
    });
    return { status: response.status, text: await response.text() };
  }, { url: `${baseURL}/api/sessions/${encodeURIComponent(id)}/mission/plan`, data });
  assert.equal(result.status, 200, result.text);
  return JSON.parse(result.text);
}

async function getJSON(context, url) {
  const response = await context.request.get(url);
  assert.equal(response.status(), 200, `${url}: ${await response.text()}`);
  return response.json();
}

async function waitForDetail(context, baseURL, id, predicate) {
  const deadline = Date.now() + 25_000;
  let detail;
  do {
    detail = await getJSON(context, `${baseURL}/api/sessions/${encodeURIComponent(id)}`);
    if (predicate(detail)) return detail;
    await new Promise((resolve) => setTimeout(resolve, 100));
  } while (Date.now() < deadline);
  throw new Error(`approval CAS fixture did not settle: ${JSON.stringify(detail)}`);
}

function waitForPlan(context, baseURL, id, version) {
  return waitForDetail(context, baseURL, id, (detail) => detail.plan_mode?.status === 'awaiting_approval' &&
    detail.plan_mode.plan_version === version && detail.state?.status === 'awaiting_input' && !detail.active_handle);
}

async function coherentSnapshot(context, baseURL, id) {
  const detail = await getJSON(context, `${baseURL}/api/sessions/${encodeURIComponent(id)}`);
  const flat = await getJSON(context, `${baseURL}/api/sessions/${encodeURIComponent(id)}/planmode`);
  assert.ok(flat.approval_revision, 'GET planmode must return a revision on the flat plan snapshot');
  assert.equal(flat.plan_mode, undefined, 'GET planmode contract is flat');
  assert.deepEqual(target({ plan_mode: flat }), target(detail));
  assert.equal(flat.plan_markdown, detail.plan_mode.plan_markdown);
  if (flat.linked_goal_id) assert.deepEqual(flat.linked_goal, detail.goal, 'plan and linked goal must form one coherent review snapshot');
  return detail;
}

async function freezeDetail(page, baseURL, id, detail) {
  const pathname = new URL(`${baseURL}/api/sessions/${encodeURIComponent(id)}`).pathname;
  const matches = (url) => url.pathname === pathname;
  await page.route(matches, async (route) => {
    assert.equal(route.request().method(), 'GET', 'only displayed detail polling may be frozen');
    await route.fulfill({ status: 200, json: detail });
  });
  return () => page.unroute(matches);
}

async function openSession(page, id, tab) {
  await page.evaluate(async (value) => {
    closeInspectorSlideOut({ restoreFocus: false });
    await openSession(value, { switchToChat: true });
  }, id);
  await page.waitForFunction((value) => state.sessionId === value && state.sessionDetail?.metadata?.id === value, id);
  await page.locator('#inspector-toggle-btn').click();
  const selected = page.locator(`[data-inspector-tab="${tab}"][aria-selected="true"]`);
  if (await selected.count() === 0) await page.locator(`[data-inspector-tab="${tab}"]`).click();
  await selected.waitFor();
}

function target(detail) {
  return {
    plan_mode_id: detail.plan_mode.plan_mode_id,
    plan_version: detail.plan_mode.plan_version,
    expected_revision: detail.plan_mode.approval_revision
  };
}

async function displayedTarget(page) {
  return page.evaluate(() => ({
    plan_mode_id: state.sessionDetail.plan_mode.plan_mode_id,
    plan_version: state.sessionDetail.plan_mode.plan_version,
    expected_revision: state.sessionDetail.plan_mode.approval_revision
  }));
}

function nextPost(page, baseURL, id, action) {
  const url = `${baseURL}/api/sessions/${encodeURIComponent(id)}/${action}`;
  return page.waitForResponse((response) => response.request().method() === 'POST' && response.url() === url);
}

function approvalPosts(page, baseURL, id) {
  const prefix = `${baseURL}/api/sessions/${encodeURIComponent(id)}/`;
  const payloads = [];
  const listener = (request) => {
    if (request.method() === 'POST' && ['planmode/approve', 'mission/plan/approve'].some((action) => request.url() === prefix + action)) {
      payloads.push(request.postDataJSON());
    }
  };
  page.on('request', listener);
  return { payloads, dispose: () => page.off('request', listener) };
}

async function clickStaleApproval(page, baseURL, id, action, reviewed, selector, locale) {
  const posts = approvalPosts(page, baseURL, id);
  try {
    const pending = nextPost(page, baseURL, id, action);
    const previousToasts = await toastIDs(page);
    await page.locator(selector).click();
    const response = await pending;
    await assertStaleResponse(response, reviewed);
    await assertStaleUI(page, locale, previousToasts);
    await page.waitForTimeout(200);
    assert.equal(posts.payloads.length, 1, 'stale approval must not automatically retry with latest');
    assertApprovalPost(posts.payloads[0], reviewed);
    return response;
  } finally {
    posts.dispose();
  }
}

async function assertStaleResponse(response, reviewed) {
  assert.equal(response.status(), 409, await response.text());
  assert.equal((await response.json()).code, 'APPROVAL_TARGET_CONFLICT');
  assertApprovalPost(response.request().postDataJSON(), reviewed, reviewed.override_coverage === true);
  await allowApprovalProtocolError(response, { status: 409, code: 'APPROVAL_TARGET_CONFLICT', classification: 'stale_target' });
  const request = response.request();
  const sessionID = decodeURIComponent(new URL(request.url()).pathname.split('/')[3]);
  allowMissingApprovalReceipt(request.frame().page(), sessionID, request.postDataJSON().approval_request_id,
    'this exact stale target was rejected before any receipt admission');
}

function assertApprovalPost(payload, reviewed, overrideCoverage = false) {
  assert.equal(typeof payload.approval_request_id, 'string');
  assert.ok(payload.approval_request_id.trim(), 'Approve must supply an operation request ID');
  assert.deepEqual({ plan_mode_id: payload.plan_mode_id, plan_version: payload.plan_version,
    expected_revision: payload.expected_revision }, { plan_mode_id: reviewed.plan_mode_id,
    plan_version: reviewed.plan_version, expected_revision: reviewed.expected_revision },
  'Approve must send the target actually displayed');
  assert.equal(payload.override_coverage ?? false, overrideCoverage);
}

function toastIDs(page) {
  return page.locator('#toast-rack .toast').evaluateAll((nodes) => nodes.map((node) => node.id));
}

async function settleToasts(page) {
  await page.locator('#toast-rack .toast').last().waitFor({ state: 'detached', timeout: 5_000 });
}

async function assertStaleUI(page, locale, previousToasts = []) {
  const copy = staleCopy[locale];
  await page.waitForFunction(({ copy, previousToasts }) => Array.from(document.querySelectorAll('#toast-rack .toast'))
    .some((node) => node.textContent.trim() === copy && !previousToasts.includes(node.id)), { copy, previousToasts });
  const toast = page.locator('#toast-rack .toast').filter({ hasText: copy }).last();
  await toast.waitFor();
  assert.equal((await toast.innerText()).trim(), copy);
  assert.equal(await page.locator('.confirm-dialog').count(), 0, 'stale target must require a new review');
  assert.equal(await page.locator('#toast-rack').getByText(locale === 'en' ? staleCopy['zh-CN'] : staleCopy.en, { exact: true }).count(), 0);
  const box = await toast.boundingBox();
  const viewport = page.viewportSize();
  assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width + 1 && box.y + box.height <= viewport.height + 1,
    `stale notice must be visible inside the ${locale} viewport: ${JSON.stringify(box)}`);
  const layers = await page.evaluate(() => ({
    toast: Number(getComputedStyle(document.getElementById('toast-rack')).zIndex),
    inspector: document.getElementById('inspector-slide-out').getAttribute('aria-hidden') === 'false'
      ? Number(getComputedStyle(document.getElementById('inspector-slide-out')).zIndex) : 0
  }));
  assert.ok(layers.toast > layers.inspector, `stale toast is obscured by the open inspector: ${JSON.stringify(layers)}`);
  await toast.evaluate((element) => Promise.all(element.getAnimations().map((animation) => animation.finished)));
}

async function durableFacts(root, id) {
  const facts = {};
  for (const relative of ['session.json', 'planmode.json', 'goal.json', 'state.json', 'messages.jsonl', 'events.jsonl',
    'artifacts/planmode-history.jsonl', 'artifacts/goal-history.jsonl', 'artifacts/approval-preparation.json', 'approval-operations.json']) {
    try {
      facts[relative] = await readFile(path.join(root, id, relative), 'utf8');
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
      facts[relative] = null;
    }
  }
  return facts;
}

// Shared deterministic setup and fact readers; receipt scenarios still drive
// user controls themselves and never call the application's approval handlers.
export const approvalE2EFixtures = {
  createFixture, missionPlan, patchMission, getJSON, waitForDetail, waitForPlan,
  coherentSnapshot, freezeDetail, target, displayedTarget, nextPost,
  approvalPosts, assertApprovalPost, durableFacts, assertStaleUI, toastIDs
};

// Match resource-console diagnostics to the exact observed response. A receipt
// miss is permitted only for an explicitly declared session/request identity
// and the server's structured missing-binding error, never for arbitrary 404s.
export function createApprovalProtocolAudit({ page, baseURL, scenario, tab }) {
  const consoleMessages = [], responses = [], failed = [], pendingReads = new Set();
  const allowedHTTP = new Map(), allowedMissing = new Map(), allowedTransport = new Map();
  let completedResult;
  const onConsole = (message) => {
    if (message.type() === 'error') consoleMessages.push({ scenario, tab, message: message.text(), location: message.location() });
  };
  const onResponse = (response) => {
    if (response.status() < 400) return;
    const item = { scenario, tab, request: response.request(), url: response.url(),
      method: response.request().method(), status: response.status(), content_type: response.headers()['content-type'] || '' };
    responses.push(item);
    const read = (async () => {
      try {
        const text = await response.text();
        try { item.json = JSON.parse(text); }
        catch { item.body_text = text.slice(0, 1000); }
      } catch (error) { item.body_read_error = error.message; }
    })();
    pendingReads.add(read);
    read.finally(() => pendingReads.delete(read)).catch(() => {});
  };
  const onFailed = (request) => failed.push({ request, url: request.url(), method: request.method(),
    error: request.failure()?.errorText, intent: allowedTransport.get(request) });
  page.on('console', onConsole);
  page.on('response', onResponse);
  page.on('requestfailed', onFailed);
  const classify = (item) => {
    const declared = allowedHTTP.get(item.request);
    if (declared && item.status === declared.status && item.json?.code === declared.code) return declared.classification;
    const missing = allowedMissing.get(item.url);
    if (missing && item.method === 'GET' && item.status === 404 && item.content_type.startsWith('application/json') &&
      item.json?.error === `approval receipt ${missing.request_id}: file does not exist` &&
      item.json.code === undefined && item.json.approval === undefined) return 'receipt_missing';
    return null;
  };
  const audit = {
    allowHTTP(request, declaration) {
      const knownStatus = { APPROVAL_TARGET_CONFLICT: 409, APPROVAL_REQUEST_CONFLICT: 409,
        APPROVAL_REJECTED: 409, APPROVAL_RECOVERY_REQUIRED: 409,
        APPROVAL_TARGET_REQUIRED: 400, APPROVAL_REQUEST_ID_REQUIRED: 400 };
      assert.equal(typeof declaration.code, 'string');
      assert.ok(Object.hasOwn(knownStatus, declaration.code), 'protocol errors require an explicit known code');
      assert.equal(declaration.status, knownStatus[declaration.code]);
      assert.ok(typeof declaration.classification === 'string' && declaration.classification);
      allowedHTTP.set(request, declaration);
    },
    allowMissing(sessionID, requestID, reason) {
      assert.ok(sessionID && requestID && reason);
      allowedMissing.set(`${baseURL}/api/sessions/${encodeURIComponent(sessionID)}/approval-receipts/${encodeURIComponent(requestID)}`,
        { session_id: sessionID, request_id: requestID, reason });
    },
    allowTransport(request) {
      assert.equal(request.method(), 'POST');
      const body = request.postDataJSON();
      assert.ok(body.approval_request_id);
      allowedTransport.set(request, { classification: 'intentional_transport_failure', method: 'POST',
        request_id: body.approval_request_id, reason: 'intentionally lost approval transport' });
    },
    allowDetailRefreshFailure(request, sessionID, reason) {
      assert.ok(sessionID && typeof reason === 'string' && reason.trim());
      assert.equal(request.method(), 'GET');
      assert.equal(request.url(), `${baseURL}/api/sessions/${encodeURIComponent(sessionID)}?limit=40`);
      allowedTransport.set(request, { classification: 'intentional_detail_refresh_failure', method: 'GET',
        session_id: sessionID, reason });
    },
    isExpectedTransport(request) { return allowedTransport.has(request); },
    transportEvidence(request) { return allowedTransport.get(request); },
    async complete() {
      if (completedResult) return completedResult;
      page.off('console', onConsole);
      page.off('response', onResponse);
      page.off('requestfailed', onFailed);
      await Promise.all([...pendingReads]);
      const unmatchedConsole = [], expected = [];
      const consumedResponses = new Set(), consumedFailures = new Set();
      for (const entry of consoleMessages) {
        const match = entry.message.match(/^Failed to load resource: the server responded with a status of (\d{3})\b/);
        const item = match && responses.find((value) => value.url === entry.location.url && value.status === Number(match[1]) && !consumedResponses.has(value));
        if (item && classify(item)) {
          consumedResponses.add(item);
          expected.push({ ...entry, classification: classify(item), response: protocolResponseEvidence(item) });
          continue;
        }
        const failure = /^Failed to load resource: net::ERR_FAILED\b/.test(entry.message) &&
          failed.find((value) => value.url === entry.location.url && value.intent?.method === value.method && !consumedFailures.has(value));
        if (failure) {
          consumedFailures.add(failure);
          expected.push({ ...entry, ...failure.intent });
          continue;
        }
        unmatchedConsole.push({ ...entry, ...(item ? { response: protocolResponseEvidence(item) } : {}) });
      }
      completedResult = { console: unmatchedConsole, expected,
        unexpected_http: responses.filter((item) => !classify(item)).map(protocolResponseEvidence),
        responses: responses.map((item) => ({ ...protocolResponseEvidence(item), classification: classify(item),
          ...(allowedMissing.has(item.url) ? { declared_missing: allowedMissing.get(item.url) } : {}),
          console_locations: consoleMessages.filter((entry) => entry.location.url === item.url).map((entry) => entry.location) })) };
      return completedResult;
    }
  };
  protocolAudits.set(page, audit);
  return audit;
}

function protocolResponseEvidence(item) {
  const body = item.json;
  const lookup = body?.approval?.lookup;
  return { scenario: item.scenario, tab: item.tab, method: item.method, url: item.url, status: item.status,
    content_type: item.content_type, server_code: body?.code ?? null,
    ...(body ? { json: { ...body, ...(body.approval ? { approval: {
      replay: body.approval.replay, recovery_required: body.approval.recovery_required,
      lookup: { found: lookup?.found, binding: { approval_request_id: lookup?.binding?.approval_request_id,
        operation_id: lookup?.binding?.operation_id }, receipt: { stage: lookup?.receipt?.stage,
        operation_id: lookup?.receipt?.operation_id, target: lookup?.receipt?.target } }
    } } : {}) } } : {}), ...(item.body_text ? { body_text: item.body_text } : {}),
    ...(item.body_read_error ? { body_read_error: item.body_read_error } : {}) };
}

export async function allowApprovalProtocolError(response, declaration) {
  assert.equal(response.status(), declaration.status);
  const json = await response.json();
  assert.equal(json.code, declaration.code);
  if (declaration.code === 'APPROVAL_REJECTED') {
    assert.equal(json.approval.lookup.receipt.stage, 'rejected');
    assert.equal(json.approval.lookup.binding.approval_request_id, response.request().postDataJSON().approval_request_id);
  }
  const request = response.request();
  const audit = protocolAudits.get(request.frame().page());
  assert.ok(audit, 'the actual response must belong to an audited browser page');
  audit.allowHTTP(request, declaration);
}

export function allowMissingApprovalReceipt(page, sessionID, requestID, reason) {
  const audit = protocolAudits.get(page);
  assert.ok(audit);
  audit.allowMissing(sessionID, requestID, reason);
}

export function allowApprovalTransportFailure(request) {
  const audit = protocolAudits.get(request.frame().page());
  assert.ok(audit);
  audit.allowTransport(request);
}

export function allowApprovalDetailRefreshFailure(request, sessionID, reason) {
  const audit = protocolAudits.get(request.frame().page());
  assert.ok(audit);
  audit.allowDetailRefreshFailure(request, sessionID, reason);
}

async function assertUnaccepted(context, baseURL, root, id, before, version) {
  const detail = await getJSON(context, `${baseURL}/api/sessions/${encodeURIComponent(id)}`);
  assert.equal(detail.state.status, 'awaiting_input', 'a CAS conflict must preserve the resumable session');
  assert.equal(detail.active_handle, false);
  assert.equal(detail.plan_mode.status, 'awaiting_approval');
  assert.equal(detail.plan_mode.plan_version, version);
  assert.equal(detail.plan_mode.approved_version || 0, 0);
  assert.equal(detail.plan_mode.approved_revision || '', '');
  assert.equal(detail.plan_mode.approvals?.length || 0, 0);
  if (detail.goal?.mission) assert.equal(detail.goal.mission.plan_status, 'needs_approval');
  const after = await durableFacts(root, id);
  assert.deepEqual(after, before, 'CAS rejection must not write approval/history/replay, acceptance or provider facts');
  assert.equal(detail.messages.some((message) => message.meta?.source === 'planmode_approval'), false);
}
