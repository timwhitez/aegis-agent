// Shared fixtures, fact readers and UI probes for the approval CAS and
// approval receipt suites. Fixture mutation is limited to settled temporary
// sessions and never invents a receipt, claim or JSONL approval fact.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { readFile, rename, unlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { closeInspector, openInspectorTab, openSession, storeState, tid } from './ui.mjs';

export const STALE_COPY = { 'zh-CN': '计划已更新，请重新审阅', en: 'The plan has changed. Please review it again.' };
export const PENDING_KEY = 'aegis-agent.webconsole.approval-operations.v1';
export const PROFILES = [
  { locale: 'zh-CN', size: 'desktop', mobile: false },
  { locale: 'zh-CN', size: 'mobile', mobile: true },
  { locale: 'en', size: 'desktop', mobile: false },
  { locale: 'en', size: 'mobile', mobile: true },
];
/**
 * Locale × viewport profiles for an approval suite. E2E_APPROVAL_PROFILES
 * (all | comma list such as zh-desktop,en-mobile) overrides the suite default.
 */
export function approvalProfiles(defaults = 'all') {
  const wanted = String(process.env.E2E_APPROVAL_PROFILES || defaults).split(',').map((value) => value.trim()).filter(Boolean);
  return wanted.includes('all') ? PROFILES : PROFILES.filter((profile) => wanted.includes(suffixOf(profile)));
}
export const suffixOf = (profile) => (profile.locale === 'en' ? 'en' : 'zh') + '-' + profile.size;
const sessionURL = (h, id, suffix = '') => h.baseURL + '/api/sessions/' + encodeURIComponent(id) + suffix;
const isOverlay = async (page) => (await tid(page, 'inspector').count()) > 0 && (await tid(page, 'inspector').getAttribute('role')) === 'dialog';

export async function createFixture(h, label, { linked = false, marker = 'E2E_UI_PLAN_REVISE' } = {}) {
  const prompt = marker + ' browser approval ' + label;
  const id = await h.startSession({
    prompt,
    plan_mode: { enabled: true, objective: prompt },
    ...(linked ? { goal: { enabled: true, mode: 'mission', objective: prompt, require_plan_approval: true } } : {}),
  });
  await waitForPlan(h, id, 1);
  return id;
}

export function waitForPlan(h, id, version) {
  return h.waitDetail(id, (d) => d.plan_mode?.status === 'awaiting_approval' && d.plan_mode.plan_version === version
    && d.state?.status === 'awaiting_input' && !d.active_handle, 'plan v' + version);
}

export function missionPlan(covered) {
  return {
    plan_status: 'needs_approval',
    requirements: [{ id: 'requirement_scope', text: 'Review the linked scope before execution.' }],
    features: [{ id: 'feature_scope', title: 'Reviewed linked scope', status: 'pending', claimed_assertions: covered ? ['validation_scope'] : [] }],
    milestones: [{ id: 'milestone_scope', title: 'Coverage milestone', status: 'pending', feature_ids: ['feature_scope'], validation_ids: covered ? ['validation_scope'] : [] }],
    validation_contract: [{ id: 'validation_scope', kind: 'command', command: 'printf approval-cas', description: 'Validate the reviewed scope.', status: 'pending' }],
  };
}

export async function patchMission(h, id, data) {
  const result = await h.api('PATCH', '/api/sessions/' + encodeURIComponent(id) + '/mission/plan', data);
  assert.equal(result.status, 200, result.text);
  return result.json;
}

export const target = (detail) => ({
  plan_mode_id: detail.plan_mode.plan_mode_id,
  plan_version: detail.plan_mode.plan_version,
  expected_revision: detail.plan_mode.approval_revision,
});

/** Detail + flat planmode snapshot must describe one reviewable target. */
export async function coherentSnapshot(h, id) {
  const detail = await h.detail(id);
  const flat = await h.api('GET', '/api/sessions/' + encodeURIComponent(id) + '/planmode');
  assert.equal(flat.status, 200, flat.text);
  assert.ok(flat.json.approval_revision, 'GET planmode returns a revision on the flat snapshot');
  assert.equal(flat.json.plan_mode, undefined, 'GET planmode contract is flat');
  assert.deepEqual(target({ plan_mode: flat.json }), target(detail));
  assert.equal(flat.json.plan_markdown, detail.plan_mode.plan_markdown);
  if (flat.json.linked_goal_id) assert.deepEqual(flat.json.linked_goal, detail.goal, 'plan and linked goal form one review snapshot');
  return detail;
}

/** Only the displayed detail GET is replaced; every control request is real. */
export async function freezeDetail(page, h, id, detail) {
  const pathname = new URL(sessionURL(h, id)).pathname;
  const matches = (url) => url.pathname === pathname;
  const handler = async (route) => {
    assert.equal(route.request().method(), 'GET', 'only displayed detail polling may be frozen');
    await route.fulfill({ status: 200, json: detail });
  };
  await page.route(matches, handler);
  return () => page.unroute(matches, handler);
}

/** Holds real detail GETs until released (they then reach the server). */
export async function holdDetailPolling(page, h, id) {
  const pathname = new URL(sessionURL(h, id)).pathname;
  const matches = (url) => url.pathname === pathname;
  let release;
  const released = new Promise((resolve) => { release = resolve; });
  const pending = new Set();
  let holding = true;
  const handler = (route) => {
    assert.equal(route.request().method(), 'GET', 'only real detail polling is delayed');
    const hold = holding;
    const task = (async () => {
      if (hold) await released;
      await route.continue();
    })();
    pending.add(task);
    task.finally(() => pending.delete(task)).catch(() => {});
    return task;
  };
  await page.route(matches, handler);
  return async () => {
    holding = false;
    release();
    while (pending.size) await Promise.all([...pending]);
    await page.unroute(matches, handler);
  };
}

export async function displayedTarget(page) {
  const plan = await storeState(page, 'session.detail.plan_mode');
  return { plan_mode_id: plan?.plan_mode_id, plan_version: plan?.plan_version, expected_revision: plan?.approval_revision };
}

export function nextPost(page, h, id, action) {
  const url = sessionURL(h, id, '/' + action);
  const pending = page.waitForResponse((response) => response.request().method() === 'POST' && response.url() === url, { timeout: 60000 });
  pending.catch(() => {});
  return pending;
}

export function approvalPosts(page, h, id) {
  const prefix = sessionURL(h, id, '/');
  const payloads = [];
  const listener = (request) => {
    if (request.method() === 'POST' && ['planmode/approve', 'mission/plan/approve'].some((action) => request.url() === prefix + action)) payloads.push(request.postDataJSON());
  };
  page.on('request', listener);
  return { payloads, dispose: () => page.off('request', listener) };
}

export function assertApprovalPost(payload, reviewed, overrideCoverage = false) {
  assert.equal(typeof payload.approval_request_id, 'string');
  assert.ok(payload.approval_request_id.trim(), 'Approve supplies an operation request id');
  assert.deepEqual({ plan_mode_id: payload.plan_mode_id, plan_version: payload.plan_version, expected_revision: payload.expected_revision },
    { plan_mode_id: reviewed.plan_mode_id, plan_version: reviewed.plan_version, expected_revision: reviewed.expected_revision },
    'Approve sends the target actually displayed');
  assert.equal(payload.override_coverage ?? false, overrideCoverage);
}

/** Declares the expected protocol errors of one stale approval click. */
export function allowStale(h, id, { action = 'planmode/approve', responses = 1, label = 'stale approval target' } = {}) {
  h.allowHTTP(label + ' (409) ' + id, { url: sessionURL(h, id, '/' + action), status: 409, method: 'POST', limit: 2 * responses });
  h.allowHTTP('receipt lookup of a rejected stale request ' + id, { url: sessionURL(h, id, '/approval-receipts/'), status: 404, method: 'GET' });
}

export async function assertStaleResponse(response, reviewed) {
  assert.equal(response.status(), 409, await response.text());
  assert.equal((await response.json()).code, 'APPROVAL_TARGET_CONFLICT');
  assertApprovalPost(response.request().postDataJSON(), reviewed, reviewed.override_coverage === true);
}

export const staleToastCount = (page, locale) => page.locator('[data-testid="toast"] .toast-message')
  .evaluateAll((nodes, copy) => nodes.filter((node) => node.textContent.trim() === copy).length, STALE_COPY[locale]);

/** A new stale toast is visible, unobscured and in the active locale; no dialog remains. */
export async function assertStaleUI(h, page, locale, before) {
  await h.until(async () => (await staleToastCount(page, locale)) > before, 'stale toast "' + STALE_COPY[locale] + '"', 15000, 40);
  const toast = page.locator('[data-testid="toast"]').filter({ hasText: STALE_COPY[locale] }).last();
  assert.equal((await toast.locator('.toast-message').innerText()).trim(), STALE_COPY[locale]);
  assert.equal(await tid(page, 'confirm-dialog').count(), 0, 'a stale target requires a new review, not a dialog');
  const other = locale === 'en' ? STALE_COPY['zh-CN'] : STALE_COPY.en;
  assert.equal(await page.locator('[data-testid="toast"]').filter({ hasText: other }).count(), 0, 'no copy from the other locale');
  await toast.evaluate((element) => Promise.all(element.getAnimations().map((animation) => animation.finished)));
  const box = await toast.boundingBox();
  const viewport = page.viewportSize();
  assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width + 1 && box.y + box.height <= viewport.height + 1,
    'stale notice inside the viewport: ' + JSON.stringify(box));
  assert.equal(await toast.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return element.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
  }), true, 'stale notice is not obscured by another layer');
}

export async function settleToasts(page) {
  for (const button of await page.locator('[data-testid="toast"] .toast-close').all()) await button.dispatchEvent('click', undefined, { timeout: 500 }).catch(() => {});
  await page.locator('[data-testid="toast"]').first().waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
}

const DURABLE_FILES = ['session.json', 'planmode.json', 'goal.json', 'state.json', 'messages.jsonl', 'events.jsonl',
  'artifacts/planmode-history.jsonl', 'artifacts/goal-history.jsonl', 'artifacts/approval-preparation.json', 'approval-operations.json'];

export async function durableFacts(h, id) {
  const facts = {};
  for (const relative of DURABLE_FILES) facts[relative] = await readOptional(path.join(h.paths.sessions, id, relative));
  return facts;
}

export async function assertUnaccepted(h, id, before, version) {
  const detail = await h.detail(id);
  assert.equal(detail.state.status, 'awaiting_input', 'a CAS conflict preserves the resumable session');
  assert.equal(detail.active_handle, false);
  assert.equal(detail.plan_mode.status, 'awaiting_approval');
  assert.equal(detail.plan_mode.plan_version, version);
  assert.equal(detail.plan_mode.approved_version || 0, 0);
  assert.equal(detail.plan_mode.approved_revision || '', '');
  assert.equal(detail.plan_mode.approvals?.length || 0, 0);
  if (detail.goal?.mission) assert.equal(detail.goal.mission.plan_status, 'needs_approval');
  assert.deepEqual(await durableFacts(h, id), before, 'a CAS rejection writes no approval/history/replay/acceptance/provider facts');
  assert.equal(detail.messages.some((message) => message.meta?.source === 'planmode_approval'), false);
}

export async function readOptional(file) {
  try { return await readFile(file, 'utf8'); } catch (error) { if (error.code === 'ENOENT') return null; throw error; }
}

export async function readLedger(h, id) {
  return JSON.parse(await readFile(path.join(h.paths.sessions, id, 'approval-operations.json'), 'utf8'));
}

async function providerRows(h, id) {
  const text = await readOptional(h.paths.providerLog);
  return (text || '').split('\n').filter(Boolean).map((line) => JSON.parse(line)).filter((row) => row.session_id === id);
}
export const providerCalls = async (h, id) => (await providerRows(h, id)).length;

const completionObservations = new WeakMap();

/**
 * The current generation is completed, has no active handle, and the exact
 * web owner that acquired it recorded one normal release.
 */
export async function completed(h, id) {
  let generation; let acquired; let owner; let settled; let release;
  await h.until(async () => {
    const detail = await h.detail(id);
    if (!generation && (detail.state?.status !== 'completed' || detail.active_handle)) return false;
    if (!generation) {
      generation = detail.state.run_generation;
      assert.ok(generation, 'completion requires the current execution generation');
    }
    assert.equal(detail.state.run_generation, generation, 'a later generation cannot satisfy the original completion');
    assert.equal(detail.state.status, 'completed');
    const events = (await readFile(path.join(h.paths.sessions, id, 'events.jsonl'), 'utf8')).split('\n').filter(Boolean).map((line) => JSON.parse(line));
    const latest = events.findLast((event) => event.type === 'webconsole.handle.acquired');
    assert.ok(latest, 'the completed execution has a real acquired web owner');
    if (!acquired) {
      acquired = latest;
      owner = { pid: acquired.data.pid, process_start_id: acquired.data.process_start_id, started_at: acquired.data.started_at };
      assert.ok(Number.isInteger(owner.pid) && owner.pid > 0 && owner.process_start_id && owner.started_at);
    }
    assert.equal(latest.id, acquired.id, 'a replaced owner cannot satisfy the original completion');
    const releases = events.filter((event) => event.type === 'webconsole.handle.released' && event.data.pid === owner.pid
      && event.data.process_start_id === owner.process_start_id && event.data.started_at === owner.started_at
      && event.data.result_session_id === id && event.data.reason === undefined && event.data.error === undefined);
    if (!releases.length) return false;
    assert.equal(releases.length, 1, 'one normal release for this owner');
    release = releases[0];
    settled = await h.detail(id);
    assert.equal(settled.state.run_generation, generation);
    assert.equal(settled.state.status, 'completed');
    assert.ok(!settled.active_handle);
    return true;
  }, 'completed generation of ' + id + ' and its owner release', 120000, 120);
  completionObservations.set(settled, { run_generation: generation, owner, acquired_event_id: acquired.id, normal_release_event_id: release.id });
  return settled;
}

export async function providerExecution(h, id, detail, beforeCalls, expectedTools) {
  const turns = (await providerRows(h, id)).slice(beforeCalls);
  assert.deepEqual(turns.map((row) => row.tool), expectedTools, 'provider tool sequence for this generation');
  assert.ok(detail.state.run_generation);
  return { run_generation: detail.state.run_generation, provider_turns: turns.length, tool_sequence: turns.map((row) => row.tool),
    session_status: detail.state.status, goal_status: detail.goal?.status || null, completion_boundary: completionObservations.get(detail) };
}

export function stableReceipt(receipt) {
  return { schema_version: receipt.schema_version, session_id: receipt.session_id, operation_id: receipt.operation_id, target: receipt.target,
    parameters: receipt.parameters, fingerprint: receipt.fingerprint, run_generation: receipt.recovery.run_generation };
}

export function withoutLedger(facts) {
  const copy = { ...facts };
  delete copy['approval-operations.json'];
  return copy;
}

export function assertReceipt(response, request, stage, replay) {
  assert.ok(response.approval?.lookup?.found, JSON.stringify(response).slice(0, 600));
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

export async function apiApproval(h, id, data, action = 'planmode/approve') {
  const result = await h.api('POST', '/api/sessions/' + encodeURIComponent(id) + '/' + action, data);
  return { status: result.status, text: result.text, json: result.json };
}

export async function queryReceipt(h, id, requestID) {
  const result = await h.api('GET', '/api/sessions/' + encodeURIComponent(id) + '/approval-receipts/' + encodeURIComponent(requestID));
  assert.equal(result.status, 200, result.text);
  return result.json;
}

export async function assertStoredPending(page, id, body) {
  const value = await page.evaluate((key) => JSON.parse(localStorage.getItem(key) || 'null'), PENDING_KEY);
  assert.ok(value, 'the pending operation survives in browser storage');
  assert.equal(value.version, 1);
  const operation = value.sessions?.[id];
  assert.ok(operation, 'the pending operation is session-scoped');
  assert.equal(operation.session_id, id);
  assert.equal(operation.approval_request_id, body.approval_request_id);
  const { approval_request_id: _requestID, ...parameters } = body;
  assert.deepEqual(operation.parameters, { ...parameters, override_coverage: parameters.override_coverage ?? false },
    'storage keeps the exact captured target and explicit parameters');
  return operation;
}

// Fixture mutation for settled temporary sessions only (atomic publish).
async function publishPlanFixture(file, plan) {
  const temporary = file + '.' + randomUUID() + '.tmp';
  try {
    await writeFile(temporary, JSON.stringify(plan) + '\n', { flag: 'wx', mode: 0o600 });
    await rename(temporary, file);
  } finally {
    await unlink(temporary).catch((error) => { if (error.code !== 'ENOENT') throw error; });
  }
}

export async function seedNewTarget(h, id, { expectedStatus = 'executing', runGeneration } = {}) {
  const before = await durableFacts(h, id);
  const file = path.join(h.paths.sessions, id, 'planmode.json');
  const plan = JSON.parse(await readFile(file, 'utf8'));
  assert.equal(plan.status, expectedStatus);
  const version = plan.plan_version || 0;
  if (expectedStatus === 'planning') {
    assert.ok(runGeneration, 'replacement planning fixture requires a settled execution generation');
    const state = JSON.parse(before['state.json']);
    assert.equal(state.status, 'completed');
    assert.equal(state.run_generation, runGeneration);
    assert.equal(version, 0);
    assert.equal(plan.approved_version || 0, 0);
    assert.equal(plan.approved_revision || '', '');
    assert.equal(plan.approvals?.length || 0, 0);
    assert.equal(plan.plan_markdown || '', '');
    plan.verification = ['Browser approves this freshly reviewed target and observes completion'];
  } else {
    assert.ok(version > 0, 'executing controls retain a submitted plan version');
  }
  plan.status = 'awaiting_approval';
  plan.plan_version = version + 1;
  plan.summary = 'Settings must remain unchanged: new plan for a second reviewed admission.';
  plan.plan_markdown = (plan.plan_markdown || '# Fresh reviewed plan') + '\n\n# Fresh review\n\nApprove this newly captured target explicitly.';
  plan.updated_at = new Date().toISOString();
  delete plan.approval_revision;
  await publishPlanFixture(file, plan);
  const after = await durableFacts(h, id);
  delete before['planmode.json'];
  delete after['planmode.json'];
  assert.deepEqual(after, before, 'fixture setup preserves approval, ledger, goal, generation and JSONL facts');
}

export async function seedLegacyExecuting(h, id) {
  assert.equal(await readOptional(path.join(h.paths.sessions, id, 'approval-operations.json')), null);
  const file = path.join(h.paths.sessions, id, 'planmode.json');
  const plan = JSON.parse(await readFile(file, 'utf8'));
  assert.equal(plan.status, 'awaiting_approval');
  assert.equal(plan.approvals?.length || 0, 0);
  plan.status = 'executing';
  plan.approved_version = plan.plan_version;
  plan.updated_at = new Date().toISOString();
  delete plan.approval_revision;
  delete plan.approved_revision;
  await publishPlanFixture(file, plan);
}

// ---- browser probes ---------------------------------------------------------------

export function observe(page, h, id) {
  const posts = [];
  const queries = [];
  const prefix = sessionURL(h, id, '/');
  const onRequest = (request) => {
    if (request.method() === 'POST' && ['planmode/approve', 'mission/plan/approve'].some((action) => request.url() === prefix + action)) posts.push(request.postDataJSON());
  };
  const onResponse = (response) => {
    if (response.request().method() === 'GET' && response.url().startsWith(prefix + 'approval-receipts/')) {
      queries.push({ id: decodeURIComponent(response.url().slice((prefix + 'approval-receipts/').length)), status: response.status() });
    }
  };
  page.on('request', onRequest);
  page.on('response', onResponse);
  return { posts, queries, dispose: () => { page.off('request', onRequest); page.off('response', onResponse); } };
}

function deferred() {
  let resolve; let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

/** Aborts the next approval POST: before the server (afterCommit unset) or after a real admission. */
export async function loseNextApproval(page, h, id, afterCommit) {
  const url = sessionURL(h, id, '/planmode/approve');
  h.allowAbort('intentionally lost approval transport ' + id, { url, method: 'POST', limit: 2 });
  if (!afterCommit) h.allowHTTP('receipt not found for an undelivered approval ' + id, { url: sessionURL(h, id, '/approval-receipts/'), status: 404, method: 'GET' });
  const result = deferred();
  let intercepted = false;
  const handler = async (route) => {
    if (intercepted || route.request().method() !== 'POST') return route.continue();
    intercepted = true;
    try {
      const body = route.request().postDataJSON();
      let status = null; let text = '';
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

/** Delivers the next approval POST to the server but holds its response. */
export async function delayNextApproval(page, h, id) {
  const url = sessionURL(h, id, '/planmode/approve');
  const fetched = deferred(); const released = deferred(); const done = deferred();
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
  return { fetched: fetched.promise, release: released.resolve, done: done.promise, dispose: () => page.unroute(url, handler) };
}

/** Aborts exactly one detail GET observed after the release boundary. */
export async function abortNextDetailRefresh(page, h, id) {
  const url = sessionURL(h, id) + '?limit=40';
  let armed = false; let released = false; let intercepted = false; let observed = null; let failure = null;
  const handler = async (route) => {
    if (!armed || !released || intercepted) return route.continue();
    intercepted = true;
    try {
      h.allowAbort('one intentional detail refresh failure after the late response ' + id, { url, method: 'GET', limit: 2 });
      await route.abort('failed');
      observed = { method: 'GET', url, session_id: id, observed_after_release_boundary: true };
    } catch (error) { failure = error; }
  };
  await page.route(url, handler);
  return {
    arm: () => { armed = true; },
    markReleased: () => { assert.equal(armed, true); released = true; },
    observed: () => { if (failure) throw failure; return observed; },
    dispose: () => page.unroute(url, handler),
  };
}

export async function assertNotGenerating(h, page) {
  await h.until(async () => !(await storeState(page, 'session.generating')), 'session not generating', 20000, 50);
  assert.equal(await tid(page, 'live-activity').count(), 0, 'no live activity for a settled receipt');
}

/** Records any generating state or live-activity indicator until stop(). */
export async function watchGenerating(page) {
  await page.evaluate(async () => {
    const { store } = await import('/ui/src/state/store.js');
    const observations = [];
    const unsubscribe = store.subscribe((state) => {
      if (state.session.generating && observations.length < 16) observations.push({ kind: 'generating', session: state.session.id });
    });
    const observer = new MutationObserver(() => {
      if (document.querySelector('[data-testid="live-activity"]') && observations.length < 16) observations.push({ kind: 'live-activity' });
    });
    observer.observe(document.body, { childList: true, subtree: true });
    window.__approvalGenerationWatch = { observations, unsubscribe, observer };
  });
  return {
    stop: async () => {
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      return page.evaluate(() => {
        const watch = window.__approvalGenerationWatch;
        watch.unsubscribe();
        watch.observer.disconnect();
        delete window.__approvalGenerationWatch;
        return watch.observations;
      });
    },
  };
}

/** Visible, unobscured receipt notice for requestID (closes a mobile overlay first). */
export async function assertNotice(page, locale, requestID) {
  if (await isOverlay(page)) await closeInspector(page);
  const notice = page.locator('[data-testid="approval-receipt"]').first();
  await notice.waitFor({ timeout: 20000 });
  if (requestID) await page.waitForFunction((id) => document.querySelector('[data-testid="approval-receipt"]')?.dataset.approvalRequestId === id, requestID, { timeout: 20000 });
  await notice.scrollIntoViewIfNeeded();
  assert.match(await notice.innerText(), locale === 'zh-CN' ? /审批|批准|回执|请求/ : /approval|receipt|request/i);
  assert.equal(await notice.locator('.approval-receipt-id').getAttribute('translate'), 'no');
  const box = await notice.boundingBox();
  const viewport = page.viewportSize();
  assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width + 1 && box.y + box.height <= viewport.height + 1,
    'receipt notice fits the viewport: ' + JSON.stringify(box));
  assert.equal(await notice.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return element.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
  }), true, 'receipt notice is not obscured');
}

export async function assertCurrentNoticeStatus(page, locale) {
  const row = page.locator('[data-testid="approval-receipt"] .approval-receipt-status');
  assert.equal(await row.count(), 1);
  assert.equal((await row.innerText()).replace(/\s+/g, ' ').trim(), locale === 'zh-CN' ? '当前会话: 已完成' : 'Current session: Completed');
}

export async function noticeIdentity(page) {
  return {
    session_id: await storeState(page, 'session.id'),
    request_id: await page.locator('[data-testid="approval-receipt"]').first().getAttribute('data-approval-request-id', { timeout: 2000 }).catch(() => null),
  };
}

export async function loadedState(h, page, id, generation, status) {
  await h.until(async () => {
    const session = await storeState(page, 'session');
    return session.id === id && session.detail?.metadata?.id === id && session.detail?.state?.run_generation === generation
      && session.detail?.state?.status === status && !session.detail?.active_handle;
  }, 'loaded ' + status + ' generation ' + generation, 60000, 80);
}

/** Opens a session in the app and shows the requested surface ('card' | inspector tab). */
export async function openApproval(page, id, surface = 'card') {
  if (await isOverlay(page)) await closeInspector(page);
  await openSession(page, id);
  if (surface === 'card') {
    if (await isOverlay(page)) await closeInspector(page);
    return;
  }
  await openInspectorTab(page, surface);
}

export async function clickApprove(h, page, { dbl = false } = {}) {
  if (await isOverlay(page)) await closeInspector(page);
  const button = tid(page, 'approve-plan');
  await h.until(async () => (await button.count()) === 1 && !(await button.isDisabled()), 'approve enabled', 60000, 40);
  if (dbl) await button.dblclick();
  else await button.click();
}

export async function clickGoalApprove(h, page) {
  await openInspectorTab(page, 'goal');
  const button = tid(page, 'inspector-goal-approve-plan');
  await h.until(async () => (await button.count()) === 1 && !(await button.isDisabled()), 'goal approve enabled', 60000, 40);
  await button.click();
}

export async function ordinaryContinue(h, page, id, message) {
  if (await isOverlay(page)) await closeInspector(page);
  await tid(page, 'composer-input').fill(message);
  await h.until(async () => (await tid(page, 'send-button').count()) === 1 && !(await tid(page, 'send-button').isDisabled()), 'send enabled', 30000, 50);
  const continued = nextPost(page, h, id, 'continue');
  await tid(page, 'composer-input').press('Enter');
  const response = await continued;
  assert.equal(response.status(), 202, await response.text());
}

/** Asks the open page to re-read the session (what a relay refresh hint does). */
export async function refreshOpenSession(page) {
  await page.evaluate(async () => {
    const { refreshSession } = await import('/ui/src/state/actions.js');
    await refreshSession({ silent: true });
  });
}
