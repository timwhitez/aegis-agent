// Shell: routes, sidebar session list, collapse, mobile drawer, command
// palette, global shortcuts, language persistence and theme.
import assert from 'node:assert/strict';
import { readFile, rename, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { assertDialogContract } from './audits.mjs';
import { gotoApp, htmlLang, setLocale, spaGo, tid, waitSessionLoaded } from './ui.mjs';

const DAY = 86400000;

async function settledSession(h, prompt) {
  const id = await h.startSession(prompt);
  await h.waitDetail(id, (d) => d.state?.status === 'awaiting_input' && !d.active_handle, 'awaiting input');
  return id;
}

async function completedSession(h, prompt) {
  const id = await settledSession(h, prompt);
  const result = await h.api('POST', '/api/sessions/' + encodeURIComponent(id) + '/continue', { message: 'finish the shell fixture' });
  assert.equal(result.status, 202, result.text);
  await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed');
  return id;
}

/** Moves a settled session's durable updated_at back in time (fixture only). */
async function backdate(h, id, when) {
  const file = path.join(h.paths.sessions, id, 'state.json');
  const state = JSON.parse(await readFile(file, 'utf8'));
  state.updated_at = new Date(when).toISOString();
  await writeFile(file + '.tmp', JSON.stringify(state, null, 2) + '\n', { mode: 0o600 });
  await rename(file + '.tmp', file);
}

export async function runShell(h) {
  const startOfToday = new Date(); startOfToday.setHours(0, 0, 0, 0);
  const [attentionId, todayId, yesterdayId, weekId, olderId] = await Promise.all([
    h.fixture('shell-attention', () => settledSession(h, 'E2E_UI_AWAIT 侧栏待处理会话 palette-target')),
    completedSession(h, 'E2E_UI_AWAIT 今天完成 shell-today'),
    completedSession(h, 'E2E_UI_AWAIT 昨天完成 shell-yesterday'),
    completedSession(h, 'E2E_UI_AWAIT 本周完成 shell-week'),
    completedSession(h, 'E2E_UI_AWAIT 更早完成 shell-older'),
  ]);
  await backdate(h, yesterdayId, startOfToday.getTime() - DAY / 2);
  await backdate(h, weekId, startOfToday.getTime() - 3 * DAY);
  await backdate(h, olderId, startOfToday.getTime() - 30 * DAY);
  h.evidence.shell = { attentionId, todayId, yesterdayId, weekId, olderId };

  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'shell-desktop');
  await gotoApp(h, page, '/');

  await h.check('default route / is the zh-CN new-session composer, light theme under a dark OS scheme', async () => {
    assert.equal(await htmlLang(page), 'zh-CN');
    await tid(page, 'empty-session').waitFor();
    await tid(page, 'composer-input').waitFor();
    assert.equal(await tid(page, 'session-actions').count(), 0, 'no session actions (and no fake id) on a new session');
    assert.equal(await tid(page, 'status-badge').count(), 0);
    assert.equal((await tid(page, 'nav-settings').innerText()).trim(), '设置');
    const theme = await page.evaluate(() => {
      const rgb = (value) => (value.match(/\d+(\.\d+)?/g) || []).slice(0, 3).map(Number);
      const lum = ([r, g, b]) => (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
      return { scheme: getComputedStyle(document.documentElement).colorScheme, dark: matchMedia('(prefers-color-scheme: dark)').matches,
        body: lum(rgb(getComputedStyle(document.body).backgroundColor)), text: lum(rgb(getComputedStyle(document.body).color)) };
    });
    assert.equal(theme.dark, true, 'the browser reports a dark OS preference');
    assert.equal(theme.scheme, 'light');
    assert.ok(theme.body > 0.85 && theme.text < 0.35, 'light surface with dark text: ' + JSON.stringify(theme));
  }, { page });
  await h.capture(page, 'shell-new-session-zh-desktop');

  await h.check('sidebar: in-progress group pinned first, date groups, status dots, raw titles', async () => {
    await page.locator('[data-testid="session-row"][data-session-id="' + olderId + '"]').waitFor({ timeout: 10000 });
    const groups = await page.locator('[data-testid="session-list"] [data-group]').evaluateAll((nodes) => nodes.map((node) => ({
      key: node.dataset.group,
      ids: [...node.querySelectorAll('[data-testid="session-row"]')].map((row) => row.dataset.sessionId),
    })));
    assert.equal(groups[0].key, 'attention', 'attention group first: ' + JSON.stringify(groups.map((g) => g.key)));
    const groupOf = (id) => groups.find((group) => group.ids.includes(id))?.key;
    assert.equal(groupOf(attentionId), 'attention');
    assert.equal(groupOf(todayId), 'today');
    assert.equal(groupOf(yesterdayId), 'yesterday');
    assert.equal(groupOf(weekId), 'week');
    assert.equal(groupOf(olderId), 'older');
    assert.deepEqual(groups.map((g) => g.key).filter((key) => key !== 'attention'), ['today', 'yesterday', 'week', 'older']);
    const labels = await page.locator('[data-testid="session-list"] .session-group-label').allInnerTexts();
    assert.equal(labels.length, 5);
    const row = page.locator('[data-testid="session-row"][data-session-id="' + attentionId + '"]');
    assert.equal(await row.getAttribute('data-status'), 'awaiting_input');
    assert.equal(await row.locator('.status-dot').getAttribute('data-status'), 'awaiting_input');
    assert.ok((await row.locator('.status-dot').getAttribute('aria-label')).length > 0, 'status dot is labelled');
    assert.equal(await page.locator('[data-testid="session-row"][data-session-id="' + todayId + '"]').getAttribute('data-status'), 'completed');
    const title = row.locator('.session-row-title');
    assert.equal(await title.getAttribute('translate'), 'no');
    assert.equal((await title.innerText()).trim(), 'E2E_UI_AWAIT 侧栏待处理会话 palette-target');
  }, { page });

  await h.check('sidebar search filters titles and ids; Esc clears', async () => {
    const search = tid(page, 'session-search');
    await search.fill('shell-week');
    await page.waitForFunction(() => document.querySelectorAll('[data-testid="session-row"]').length === 1);
    assert.equal(await tid(page, 'session-row').getAttribute('data-session-id'), weekId);
    await search.fill(olderId.slice(-10));
    await page.waitForFunction((id) => [...document.querySelectorAll('[data-testid="session-row"]')].map((row) => row.dataset.sessionId).join() === id, olderId);
    await search.fill('无匹配的会话 zzz');
    await page.locator('.session-nav-note').waitFor();
    await search.press('Escape');
    assert.equal(await search.inputValue(), '');
    await page.locator('[data-testid="session-row"][data-session-id="' + attentionId + '"]').waitFor();
  }, { page });
  await h.capture(page, 'shell-sidebar-groups-zh-desktop');

  await h.check('deep link /s/<id> opens the session; unknown route returns to /', async () => {
    await gotoApp(h, page, '/s/' + encodeURIComponent(attentionId));
    await waitSessionLoaded(page, attentionId);
    await h.until(async () => (await tid(page, 'session-title').innerText()).trim() === 'E2E_UI_AWAIT 侧栏待处理会话 palette-target', 'session title from the first prompt', 8000, 50);
    assert.equal(await tid(page, 'session-title').locator('[translate="no"]').count(), 1);
    assert.equal((await tid(page, 'status-badge').innerText()).trim(), '等待输入');
    assert.equal(await page.locator('[data-testid="session-row"][aria-current="page"]').getAttribute('data-session-id'), attentionId);
    await page.goto(h.baseURL + '/not/a/route');
    await page.waitForURL(h.baseURL + '/');
    await tid(page, 'empty-session').waitFor();
  }, { page });

  await h.check('missing session: one toast, back to /, no further polling', async () => {
    const missing = 'e2e-missing-session';
    h.allowHTTP('deliberately missing session', { url: '/api/sessions/' + missing, status: 404 });
    let requests = 0;
    const onRequest = (request) => { if (request.url().includes('/api/sessions/' + missing)) requests += 1; };
    page.on('request', onRequest);
    try {
      await page.goto(h.baseURL + '/s/' + missing);
      await page.waitForURL(h.baseURL + '/');
      await page.locator('[data-testid="toast"]').filter({ hasText: missing }).waitFor();
      await h.capture(page, 'shell-missing-session-zh-desktop', { retainToasts: true });
      const seen = requests;
      assert.ok(seen >= 1);
      // Observe two session polling intervals (1.5s) for a stray request.
      const deadline = Date.now() + 3300;
      while (Date.now() < deadline) {
        assert.equal(requests, seen, 'no polling of a missing session');
        await h.delay(150);
      }
      assert.equal(await page.locator('[data-testid="toast"]').filter({ hasText: missing }).count(), 1, 'exactly one toast');
    } finally {
      page.off('request', onRequest);
    }
  }, { page });

  await h.check('mod+B collapses the sidebar, the preference survives reload, mod+B restores it', async () => {
    await page.locator('body').click({ position: { x: 900, y: 300 } });
    await page.keyboard.press('ControlOrMeta+b');
    await tid(page, 'sidebar').waitFor({ state: 'detached' });
    await tid(page, 'sidebar-toggle').waitFor();
    await page.reload();
    await tid(page, 'topbar').waitFor();
    await tid(page, 'sidebar-toggle').waitFor();
    assert.equal(await tid(page, 'sidebar').count(), 0, 'collapsed after reload');
    await h.capture(page, 'shell-sidebar-collapsed-zh-desktop');
    await page.locator('body').click({ position: { x: 900, y: 300 } });
    await page.keyboard.press('ControlOrMeta+b');
    await tid(page, 'sidebar').waitFor();
  }, { page });

  await h.check('command palette: combobox/listbox semantics, keyboard selection, focus trap, Esc restores focus', async () => {
    const opener = tid(page, 'session-search');
    await opener.focus();
    await page.keyboard.press('ControlOrMeta+k');
    const palette = tid(page, 'command-palette');
    await palette.waitFor();
    const input = tid(page, 'command-palette-input');
    assert.equal(await input.evaluate((node) => document.activeElement === node), true, 'input focused');
    assert.equal(await input.getAttribute('role'), 'combobox');
    assert.equal(await input.getAttribute('aria-expanded'), 'true');
    const listId = await input.getAttribute('aria-controls');
    assert.equal(await page.locator('#' + listId).getAttribute('role'), 'listbox');
    assert.ok(await page.locator('#' + listId + ' [role="option"]').count() >= 8);
    const active = () => input.getAttribute('aria-activedescendant');
    const first = await active();
    assert.equal(await page.locator('#' + first).getAttribute('aria-selected'), 'true');
    await page.keyboard.press('ArrowDown');
    const second = await active();
    assert.notEqual(second, first);
    assert.equal(await page.locator('#' + second).getAttribute('aria-selected'), 'true');
    assert.equal(await page.locator('#' + first).getAttribute('aria-selected'), 'false');
    await page.keyboard.press('ArrowUp');
    assert.equal(await active(), first);
    await h.capture(page, 'shell-palette-zh-desktop');
    await assertDialogContract(page, palette, { returnTo: opener });
  }, { page });

  await h.check('command palette: navigate, new session and recent-session jump', async () => {
    const palette = tid(page, 'command-palette');
    const input = tid(page, 'command-palette-input');
    await page.keyboard.press('ControlOrMeta+k');
    await input.fill('设置');
    await page.keyboard.press('Enter');
    await page.waitForURL(h.baseURL + '/settings');
    await palette.waitFor({ state: 'detached' });
    assert.equal(await page.locator('#app-layout').evaluate((node) => node.inert), false);
    await tid(page, 'page-settings').waitFor();
    await page.keyboard.press('ControlOrMeta+k');
    await input.fill('新建会话');
    assert.equal(await page.locator('[role="option"][aria-selected="true"]').getAttribute('data-command-id'), 'new-session');
    await page.keyboard.press('Enter');
    await page.waitForURL(h.baseURL + '/');
    await page.waitForFunction(() => document.activeElement?.id === 'composer-input');
    await page.keyboard.press('ControlOrMeta+k');
    await input.fill('palette-target');
    const option = page.locator('[role="option"][data-command-id="session:' + attentionId + '"]');
    await option.waitFor();
    assert.equal(await option.getAttribute('aria-selected'), 'true', 'best match is the recent session');
    assert.equal(await option.locator('.palette-option-meta').getAttribute('translate'), 'no');
    await page.keyboard.press('Enter');
    await waitSessionLoaded(page, attentionId);
  }, { page });

  await h.check('shortcuts: / focuses the composer, ? opens help (dialog contract), mod+. toggles the inspector, mod+Shift+O starts a new session', async () => {
    await tid(page, 'session-title').click();
    await page.keyboard.press('/');
    await page.waitForFunction(() => document.activeElement?.id === 'composer-input');
    assert.equal(await tid(page, 'composer-input').inputValue(), '', '/ is not typed into the composer');
    await page.keyboard.press('Shift+?');
    assert.equal(await tid(page, 'shortcut-help').count(), 0, '? inside the composer types instead of opening help');
    await tid(page, 'composer-input').fill('');
    const opener = tid(page, 'inspector-toggle');
    await opener.focus();
    await page.keyboard.press('Shift+?');
    const help = tid(page, 'shortcut-help');
    await help.waitFor();
    await h.capture(page, 'shell-shortcut-help-zh-desktop');
    await assertDialogContract(page, help, { returnTo: opener });
    await page.keyboard.press('ControlOrMeta+.');
    await tid(page, 'inspector').waitFor();
    assert.equal(await page.locator('#app-layout').getAttribute('data-inspector'), 'inline', 'side-by-side at 1440px');
    await page.keyboard.press('ControlOrMeta+.');
    await tid(page, 'inspector').waitFor({ state: 'detached' });
    await page.keyboard.press('ControlOrMeta+Shift+O');
    await page.waitForURL(h.baseURL + '/');
    await page.waitForFunction(() => document.activeElement?.id === 'composer-input');
  }, { page });

  await h.check('language toggle switches copy, html lang and persists across reload', async () => {
    await setLocale(page, 'en');
    assert.equal(await htmlLang(page), 'en');
    assert.equal((await tid(page, 'nav-settings').innerText()).trim(), 'Settings');
    assert.match(await page.locator('[data-testid="sidebar"] [data-testid="new-session"]').innerText(), /New session/);
    await page.reload();
    await tid(page, 'topbar').waitFor();
    assert.equal(await htmlLang(page), 'en', 'locale persisted');
    assert.equal(await page.evaluate((key) => localStorage.getItem(key), 'aegis-agent.locale.v1'), 'en');
    await tid(page, 'empty-session').filter({ hasText: 'What should Aegis work on?' }).waitFor();
  }, { page });
  await h.capture(page, 'shell-new-session-en-desktop');
  await spaGo(page, '/s/' + encodeURIComponent(attentionId));
  await waitSessionLoaded(page, attentionId);
  await h.capture(page, 'shell-session-en-desktop');
  await setLocale(page, 'zh-CN');
  await context.close();

  for (const locale of ['zh-CN', 'en']) {
    const tag = locale === 'en' ? 'en' : 'zh';
    const mobile = await h.newContext({ locale, mobile: true });
    const m = await h.newPage(mobile, 'shell-mobile-' + tag);
    await gotoApp(h, m, '/');
    await h.check('mobile ' + tag + ': sidebar is a drawer with the dialog contract; row navigation closes it', async () => {
      assert.equal(await htmlLang(m), locale);
      assert.equal(await tid(m, 'sidebar').count(), 0, 'no inline sidebar at 390px');
      await h.capture(m, 'shell-new-session-' + tag + '-mobile');
      const toggle = tid(m, 'sidebar-toggle');
      await toggle.click();
      const drawer = tid(m, 'sidebar-drawer');
      await drawer.waitFor();
      await m.locator('[data-testid="sidebar-drawer"] [data-testid="session-row"][data-session-id="' + attentionId + '"]').waitFor();
      await h.capture(m, 'shell-drawer-' + tag + '-mobile');
      await assertDialogContract(m, drawer, { returnTo: toggle });
      await toggle.click();
      await m.locator('[data-testid="sidebar-drawer"] [data-testid="session-row"][data-session-id="' + attentionId + '"]').click();
      await waitSessionLoaded(m, attentionId);
      await drawer.waitFor({ state: 'detached' });
      assert.equal(await m.locator('#app-layout').evaluate((node) => node.inert), false);
    }, { page: m });
    await h.capture(m, 'shell-session-' + tag + '-mobile');
    await mobile.close();
  }
}
