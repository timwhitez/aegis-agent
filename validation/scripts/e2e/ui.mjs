// Common UI helpers for the Web console browser suite. Selectors use the
// data-testid contract from spec/17-web-console-frontend.md §5.1.
import assert from 'node:assert/strict';

export const tid = (page, id) => page.getByTestId(id);

/** Reads a JSON copy of one store path, e.g. 'session.detail.plan_mode' (read-only). */
export function storeState(page, dottedPath = '') {
  return page.evaluate(async (keys) => {
    const { getState } = await import('/ui/src/state/store.js');
    const value = keys.reduce((current, key) => (current == null ? undefined : current[key]), getState());
    return value === undefined ? null : JSON.parse(JSON.stringify(value));
  }, dottedPath ? dottedPath.split('.') : []);
}

export async function gotoApp(h, page, pathname = '/') {
  await page.goto(h.baseURL + pathname, { waitUntil: 'domcontentloaded' });
  await page.getByTestId('topbar').waitFor();
}

/** Client-side navigation through the History API (what in-app links do). */
export async function spaGo(page, pathname) {
  await page.evaluate((target) => { history.pushState(null, '', target); dispatchEvent(new PopStateEvent('popstate')); }, pathname);
}

export const currentSessionId = (page) => decodeURIComponent(new URL(page.url()).pathname.match(/^\/s\/([^/]+)/)?.[1] || '');

export async function setLocale(page, locale) {
  if ((await page.locator('html').getAttribute('lang')) === locale) return;
  await page.getByTestId('language-toggle').click();
  await page.waitForFunction((value) => document.documentElement.lang === value, locale);
}

export async function clickSafe(locator) {
  try {
    await locator.click({ timeout: 4000 });
  } catch {
    await locator.dispatchEvent('click');
  }
}

/** Polls an async page predicate (page.waitForFunction does not await promises). */
export async function pollPage(page, predicate, arg, { timeout = 60000, interval = 60, label = 'page condition' } = {}) {
  const deadline = Date.now() + timeout;
  for (;;) {
    if (await page.evaluate(predicate, arg)) return;
    if (Date.now() > deadline) throw new Error('timed out waiting for ' + label);
    await new Promise((resolve) => setTimeout(resolve, interval));
  }
}

/** Waits until the session view shows id and its detail is loaded. */
export async function waitSessionLoaded(page, id) {
  await page.locator('[data-testid="session-view"][data-session-id="' + id + '"]').waitFor();
  await pollPage(page, async (sid) => {
    const { getState } = await import('/ui/src/state/store.js');
    const session = getState().session;
    return session.id === sid && session.detail?.metadata?.id === sid && !session.loading;
  }, id, { label: 'session ' + id + ' detail loaded' });
}

/** Opens a session from the sidebar row when it is on screen, else via the router. */
export async function openSession(page, id) {
  const row = page.locator('[data-testid="sidebar"] [data-testid="session-row"][data-session-id="' + id + '"]');
  if (await row.isVisible().catch(() => false)) await row.click();
  else await spaGo(page, '/s/' + encodeURIComponent(id));
  await page.waitForFunction((sid) => location.pathname === '/s/' + encodeURIComponent(sid), id);
  await waitSessionLoaded(page, id);
}

export async function newSessionUI(page) {
  const button = page.locator('[data-testid="sidebar"] [data-testid="new-session"]');
  if (await button.isVisible().catch(() => false)) await button.click();
  else await spaGo(page, '/');
  await page.waitForURL((url) => url.pathname === '/');
  await page.getByTestId('composer-input').waitFor();
}

/** Starts a run from the new-session composer; returns { id, payload }. */
export async function startFromComposer(page, text, { goal = false, plan = false } = {}) {
  await newSessionUI(page);
  if (goal) await setPressed(page.getByTestId('goal-toggle'), true);
  if (plan) await setPressed(page.getByTestId('plan-toggle'), true);
  await page.getByTestId('composer-input').fill(text);
  const request = page.waitForRequest((req) => req.url().endsWith('/api/sessions/start') && req.method() === 'POST');
  const response = page.waitForResponse((res) => res.url().endsWith('/api/sessions/start') && res.request().method() === 'POST');
  await page.getByTestId('composer-input').press('Enter');
  const payload = (await request).postDataJSON();
  const result = await response;
  assert.equal(result.status(), 202, 'start accepted');
  const id = (await result.json()).session_id;
  await page.waitForURL((url) => url.pathname === '/s/' + encodeURIComponent(id));
  return { id, payload };
}

export async function setPressed(locator, pressed) {
  if ((await locator.getAttribute('aria-pressed')) !== String(pressed)) await locator.click();
  assert.equal(await locator.getAttribute('aria-pressed'), String(pressed));
}

/** Fills the composer and presses Enter once the send button is enabled. */
export async function sendText(h, page, text) {
  await page.getByTestId('composer-input').fill(text);
  await h.until(async () => (await page.getByTestId('send-button').count()) === 1 && !(await page.getByTestId('send-button').isDisabled()), 'send enabled', 10000, 50);
  await page.getByTestId('composer-input').press('Enter');
}

export async function ensureInspector(page) {
  if (!(await page.getByTestId('inspector').count())) await page.getByTestId('inspector-toggle').click();
  await page.getByTestId('inspector').waitFor();
}

export async function openInspectorTab(page, tab) {
  await ensureInspector(page);
  const selected = page.locator('[data-testid="inspector-tab-' + tab + '"][aria-selected="true"]');
  if (!(await selected.count())) await page.getByTestId('inspector-tab-' + tab).click();
  await selected.waitFor();
}

export async function closeInspector(page) {
  if (await page.getByTestId('inspector').count()) {
    await page.getByTestId('inspector-close').click();
    await page.getByTestId('inspector').waitFor({ state: 'detached' });
  }
  await page.waitForFunction(() => !document.getElementById('app-layout')?.inert);
}

export const toastTexts = (page) => page.locator('[data-testid="toast"]').allInnerTexts();

export async function waitToast(h, page, pattern, timeout = 10000) {
  return h.until(async () => (await toastTexts(page)).find((text) => pattern.test(text)), 'toast ' + pattern, timeout, 60);
}

export async function dismissToasts(page) {
  for (const button of await page.locator('[data-testid="toast"] .toast-close').all()) await button.dispatchEvent('click', undefined, { timeout: 500 }).catch(() => {});
}

export async function htmlLang(page) {
  return page.locator('html').getAttribute('lang');
}
