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

// The only intercepted requests are tab A's detail GETs. Approval, revision,
// mission mutations, and provider execution use the real deterministic service.
export async function runApprovalCASE2E({ browser, baseURL, sessionRoot, check, capture }) {
  assert.ok(browser && baseURL && sessionRoot, 'browser, baseURL, and sessionRoot are required');
  assert.equal(typeof check, 'function');
  assert.equal(typeof capture, 'function');
  const evidence = [];
  const browserErrors = { page: [], console: [], request: [] };
  for (const locale of ['zh-CN', 'en']) {
    for (const [size, viewport] of Object.entries(viewports)) {
      const context = await browser.newContext({ viewport });
      await context.addInitScript((value) => localStorage.setItem('aegis-agent.locale.v1', value), locale);
      const a = await context.newPage();
      const b = await context.newPage();
      const pageErrors = [];
      const suffix = `${locale}-${size}`;
      for (const [tab, page] of [['A', a], ['B', b]]) {
        page.on('pageerror', (error) => {
          pageErrors.push(error.message);
          browserErrors.page.push({ scenario: suffix, tab, message: error.message });
        });
        page.on('console', (message) => {
          if (message.type() === 'error' && !/^Failed to load resource: the server responded with a status of 409\b/.test(message.text())) {
            browserErrors.console.push({ scenario: suffix, tab, message: message.text() });
          }
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
          assert.equal((await first).status(), 409);
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
        await context.close();
      }
    }
  }
  assert.deepEqual(browserErrors, { page: [], console: [], request: [] }, 'approval CAS browser runtime errors');
  return { scenarios: evidence.length, evidence, browser_errors: browserErrors };
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
