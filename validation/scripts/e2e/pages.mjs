// Pages: Sessions (search, filter, paging, open, delete, clear-history
// conflict while running then success), Skills (zip upload via input and
// drop, uninstall), Workspace (UTF-8 byte paging, upload, folder, rename,
// batch delete, download, 415 preview, mobile sheet) and Settings (load,
// validation, connection test success and typed failure, confirmed save
// round-trip). Runs last: it clears the session history and saves config.
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { assertDialogContract } from './audits.mjs';
import { storedZip } from './harness.mjs';
import { gotoApp, setLocale, tid, waitSessionLoaded, waitToast } from './ui.mjs';

const UNICODE = 'x'.repeat(262143) + '中🙂ée\u0301\r\nno final newline';
const entry = (p) => '[data-testid="workspace-entry"][data-path="' + p + '"] .ws-entry';
const PREVIEW = '[data-testid="workspace-preview-content"]';

export async function runPages(h) {
  await runSkills(h);
  await runWorkspace(h);
  await runSessions(h);
  await runSettings(h);
}

async function runSkills(h) {
  const dir = path.join(h.paths.runtime, 'uploads');
  await mkdir(dir, { recursive: true });
  const zip = path.join(dir, 'e2e-skill.zip');
  await writeFile(zip, storedZip({ 'e2e-skill/SKILL.md': '---\nname: e2e-skill\ndescription: Browser E2E managed skill\n---\n\nUse this fixture only for browser validation.\n' }));
  await writeFile(path.join(dir, 'not-a-skill.txt'), 'plain text');
  const dropZip = storedZip({ 'drop-skill/SKILL.md': '---\nname: drop-skill\ndescription: Skill uploaded by drag and drop\n---\n\nFixture.\n' });
  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'skills');
  await gotoApp(h, page, '/');
  await tid(page, 'nav-skills').click();
  await tid(page, 'page-skills').waitFor();

  await h.check('Skills: empty state, inline error for a non-zip, zip upload through the file input', async () => {
    await tid(page, 'skills-empty').waitFor();
    assert.match(await tid(page, 'skills-empty').innerText(), /skills\.dirs/);
    await h.capture(page, 'pages-skills-empty-zh-desktop');
    await tid(page, 'skills-upload-input').setInputFiles(path.join(dir, 'not-a-skill.txt'));
    await tid(page, 'skills-upload-error').waitFor();
    assert.match(await tid(page, 'skills-upload-error').innerText(), /请选择 \.zip 格式的技能包/);
    await tid(page, 'skills-upload-input').setInputFiles(zip);
    const card = page.locator('[data-testid="skill-card"][data-skill-id="e2e-skill"]');
    await card.waitFor({ timeout: 20000 });
    assert.equal(await card.locator('.skill-name').getAttribute('translate'), 'no');
    assert.equal(await card.locator('.skill-desc').getAttribute('translate'), 'no');
    assert.match(await card.locator('.skill-desc').innerText(), /Browser E2E managed skill/);
    assert.equal((await card.getByTestId('skill-author').innerText()).trim(), 'Local');
    assert.equal(await card.getByTestId('skill-author').getAttribute('translate'), 'no');
    await waitToast(h, page, /技能已安装/);
    assert.equal(await tid(page, 'skills-upload-error').count(), 0);
  }, { page });

  await h.check('Skills: drag-and-drop upload installs a second skill', async () => {
    const transfer = await page.evaluateHandle((b64) => {
      const bytes = Uint8Array.from(atob(b64), (char) => char.charCodeAt(0));
      const data = new DataTransfer();
      data.items.add(new File([bytes], 'drop-skill.zip', { type: 'application/zip' }));
      return data;
    }, dropZip.toString('base64'));
    await page.dispatchEvent('[data-testid="page-skills"]', 'dragenter', { dataTransfer: transfer });
    await page.dispatchEvent('[data-testid="page-skills"]', 'dragover', { dataTransfer: transfer });
    assert.match(await tid(page, 'skills-dropzone').getAttribute('class'), /is-active/);
    await h.capture(page, 'pages-skills-drop-zh-desktop');
    await page.dispatchEvent('[data-testid="page-skills"]', 'drop', { dataTransfer: transfer });
    await page.locator('[data-testid="skill-card"][data-skill-id="drop-skill"]').waitFor({ timeout: 20000 });
    const skills = (await h.api('GET', '/api/skills')).json;
    assert.ok(skills.some((item) => item.id === 'drop-skill'));
  }, { page });
  await h.capture(page, 'pages-skills-installed-zh-desktop');
  await setLocale(page, 'en');
  await h.check('Skills in English', async () => {
    assert.equal((await page.locator('.page-title').innerText()).trim(), 'Skills');
    assert.equal((await tid(page, 'skills-upload-button').innerText()).trim(), 'Upload skill');
    assert.equal((await page.locator('[data-skill-id="e2e-skill"] [data-testid="skill-uninstall"]').innerText()).trim(), 'Uninstall');
  }, { page });
  await h.capture(page, 'pages-skills-installed-en-desktop');
  await setLocale(page, 'zh-CN');
  await h.check('Skills: uninstall asks for confirmation (dialog contract) and removes the skill', async () => {
    const card = page.locator('[data-testid="skill-card"][data-skill-id="e2e-skill"]');
    const trigger = card.getByTestId('skill-uninstall');
    await trigger.click();
    const dialog = tid(page, 'confirm-dialog');
    await dialog.waitFor();
    assert.match(await dialog.innerText(), /卸载这个技能？/);
    await assertDialogContract(page, dialog, { returnTo: trigger });
    assert.equal(await card.count(), 1, 'Esc keeps the skill');
    await trigger.click();
    await tid(page, 'confirm-dialog-confirm').click();
    await card.waitFor({ state: 'detached', timeout: 20000 });
    assert.ok(!(await h.api('GET', '/api/skills')).json.some((item) => item.id === 'e2e-skill'));
  }, { page });
  const mobile = await h.newContext({ locale: 'zh-CN', mobile: true });
  const m = await h.newPage(mobile, 'skills-mobile');
  await gotoApp(h, m, '/skills');
  await m.locator('[data-testid="skill-card"]').first().waitFor();
  await h.capture(m, 'pages-skills-zh-mobile');
  await mobile.close();
  await context.close();
}

async function runWorkspace(h) {
  const files = h.paths.files;
  await mkdir(path.join(files, 'docs'), { recursive: true });
  await writeFile(path.join(files, 'e2e-unicode.txt'), UNICODE);
  await writeFile(path.join(files, 'e2e-preview.txt'), 'e2e workspace artifact\n');
  await writeFile(path.join(files, 'docs', 'readme.md'), '# Docs\r\n\r\nCRLF line one\r\nline two\r\n');
  await writeFile(path.join(files, 'binary.bin'), Buffer.from([0, 1, 2, 3, 0xff, 0xfe, 0, 9]));
  await writeFile(path.join(files, '.env'), 'SECRET=hidden\n');
  const uploads = path.join(h.paths.runtime, 'uploads');
  await mkdir(uploads, { recursive: true });
  await writeFile(path.join(uploads, 'upload-note.txt'), 'workspace upload from browser e2e\n');

  const context = await h.newContext({ locale: 'zh-CN', downloads: true });
  const page = await h.newPage(context, 'workspace');
  await gotoApp(h, page, '/');
  await tid(page, 'nav-workspace').click();
  await tid(page, 'page-workspace').waitFor();
  await page.locator(entry('e2e-preview.txt')).waitFor();

  await h.check('Workspace: listing hides sensitive files; preview keeps exact content; download returns the bytes', async () => {
    const listed = await page.locator('[data-testid="workspace-entry"]').evaluateAll((nodes) => nodes.map((node) => node.dataset.path));
    for (const name of ['docs', 'binary.bin', 'e2e-preview.txt', 'e2e-unicode.txt']) assert.ok(listed.includes(name), name + ' listed');
    assert.ok(!listed.includes('.env'), 'sensitive files are hidden');
    assert.ok(listed.indexOf('docs') < listed.indexOf('binary.bin'), 'folders first');
    await page.locator(entry('e2e-preview.txt')).click();
    await page.locator(PREVIEW).waitFor();
    assert.equal(await page.locator(PREVIEW).evaluate((node) => node.textContent), 'e2e workspace artifact\n');
    assert.equal(await page.locator(PREVIEW).getAttribute('translate'), 'no');
    const download = page.waitForEvent('download');
    await tid(page, 'workspace-download').first().click();
    const file = await download;
    const target = path.join(h.paths.runtime, 'downloaded.txt');
    await file.saveAs(target);
    assert.equal(await readFile(target, 'utf8'), 'e2e workspace artifact\n');
    assert.equal(file.suggestedFilename(), 'e2e-preview.txt');
  }, { page });
  await h.capture(page, 'pages-workspace-preview-zh-desktop');

  for (const locale of ['zh-CN', 'en']) {
    await setLocale(page, locale);
    const tag = locale === 'en' ? 'en' : 'zh';
    await h.check('Workspace: UTF-8 byte paging of e2e-unicode.txt is exact (' + tag + ')', async () => {
      await page.locator(entry('e2e-unicode.txt')).click();
      const more = tid(page, 'workspace-preview-more');
      await more.waitFor();
      assert.equal(await page.locator(PREVIEW).evaluate((node) => node.textContent), UNICODE.slice(0, 262144), 'first page ends on a full rune');
      assert.equal((await more.innerText()).trim(), locale === 'en' ? 'Load more' : '加载更多');
      const response = page.waitForResponse((res) => res.url().includes('/api/file/read') && res.url().includes('offset=262146'));
      await more.click();
      const json = await (await response).json();
      await more.waitFor({ state: 'detached' });
      assert.equal(await page.locator(PREVIEW).evaluate((node) => node.textContent), UNICODE, 'the concatenated pages equal the source');
      assert.equal(json.next_offset, Buffer.byteLength(UNICODE));
      assert.equal(json.eof, true);
    }, { page });
    await h.capture(page, 'pages-workspace-unicode-' + tag + '-desktop');
  }
  await setLocale(page, 'zh-CN');

  await h.check('Workspace: new folder (validation, prompt dialog contract), upload, rename', async () => {
    const trigger = tid(page, 'workspace-new-folder');
    await trigger.click();
    const prompt = tid(page, 'prompt-dialog');
    await prompt.waitFor();
    await assertDialogContract(page, prompt, { returnTo: trigger });
    await trigger.click();
    await prompt.locator('input').fill('bad/name');
    await prompt.locator('button[type="submit"]').click();
    await prompt.locator('.field-error').waitFor();
    assert.match(await prompt.locator('.field-error').innerText(), /不能包含/);
    await prompt.locator('input').fill('e2e-folder');
    await prompt.locator('button[type="submit"]').click();
    await page.locator(entry('e2e-folder')).waitFor();
    await page.waitForFunction(() => document.activeElement?.dataset.testid === 'workspace-new-folder');
    await tid(page, 'workspace-upload-input').setInputFiles(path.join(uploads, 'upload-note.txt'));
    await page.locator(entry('upload-note.txt')).waitFor();
    assert.equal(await readFile(path.join(files, 'upload-note.txt'), 'utf8'), 'workspace upload from browser e2e\n');
    await page.locator(entry('upload-note.txt')).click();
    await page.locator(PREVIEW).waitFor();
    await tid(page, 'workspace-rename').click();
    await prompt.waitFor();
    assert.equal(await prompt.locator('input').inputValue(), 'upload-note.txt');
    await prompt.locator('input').fill('e2e-upload-renamed.txt');
    await prompt.locator('button[type="submit"]').click();
    await page.locator(entry('e2e-upload-renamed.txt')).waitFor();
    assert.equal(await page.locator(entry('upload-note.txt')).count(), 0);
    await page.locator('[data-testid="workspace-preview"][data-path="e2e-upload-renamed.txt"][data-status="ready"]').waitFor();
  }, { page });

  await h.check('Workspace: batch delete asks for confirmation and removes the selection', async () => {
    await tid(page, 'workspace-select-toggle').click();
    await tid(page, 'workspace-selection-bar').waitFor();
    await page.locator(entry('e2e-folder')).click();
    await page.locator(entry('e2e-upload-renamed.txt')).click();
    assert.match(await page.locator('.ws-selection-count').innerText(), /已选择 2 项/);
    await h.capture(page, 'pages-workspace-selection-zh-desktop');
    await tid(page, 'workspace-delete-selected').click();
    await tid(page, 'confirm-dialog').waitFor();
    assert.match(await tid(page, 'confirm-dialog-confirm').getAttribute('class'), /btn-danger/);
    await tid(page, 'confirm-dialog-confirm').click();
    await page.locator(entry('e2e-folder')).waitFor({ state: 'detached' });
    await page.locator(entry('e2e-upload-renamed.txt')).waitFor({ state: 'detached' });
    const listing = (await h.api('GET', '/api/files?path=.')).json;
    assert.ok(!listing.some((item) => ['e2e-folder', 'e2e-upload-renamed.txt'].includes(item.name)));
  }, { page });

  await h.check('Workspace: unsupported (415) files offer a download instead of a preview', async () => {
    h.allowHTTP('unsupported binary preview (415)', { url: '/api/file/read?path=binary.bin', status: 415, method: 'GET', limit: 4 });
    await page.locator(entry('binary.bin')).click();
    await tid(page, 'workspace-preview-unsupported').waitFor();
    assert.match(await tid(page, 'workspace-preview-unsupported').innerText(), /无法预览，请下载/);
    assert.equal(await page.locator('[data-testid="workspace-preview-unsupported"] [data-testid="workspace-download"]').count(), 1);
  }, { page });
  await h.capture(page, 'pages-workspace-unsupported-zh-desktop');
  await setLocale(page, 'en');
  await page.locator(entry('e2e-preview.txt')).click();
  await page.locator(PREVIEW).waitFor();
  await h.check('Workspace in English', async () => {
    assert.equal((await tid(page, 'workspace-upload').innerText()).trim(), 'Upload');
    assert.equal(await tid(page, 'workspace-refresh').getAttribute('aria-label'), 'Refresh');
  }, { page });
  await h.capture(page, 'pages-workspace-en-desktop');
  await context.close();

  const mobile = await h.newContext({ locale: 'zh-CN', mobile: true });
  const m = await h.newPage(mobile, 'workspace-mobile');
  await gotoApp(h, m, '/workspace');
  await m.locator(entry('e2e-preview.txt')).waitFor();
  await h.capture(m, 'pages-workspace-zh-mobile');
  await h.check('Workspace mobile: the preview is a full-screen sheet with a back button', async () => {
    await m.locator(entry('e2e-preview.txt')).click();
    const sheet = m.locator('[data-testid="workspace-preview"].is-sheet');
    await sheet.waitFor();
    const box = await sheet.boundingBox();
    assert.ok(box && box.width >= 389 && box.height >= 843, 'the sheet covers the viewport ' + JSON.stringify(box));
    await m.waitForFunction(() => document.activeElement?.dataset.testid === 'workspace-preview-back');
    await h.capture(m, 'pages-workspace-preview-zh-mobile');
    await tid(m, 'workspace-preview-back').click();
    await sheet.waitFor({ state: 'detached' });
  }, { page: m });
  await mobile.close();
}

/** Every session converged: runners waiting on request_user_input are cancelled (they hold a live handle). */
async function settleAllSessions(h) {
  const list = (await h.api('GET', '/api/sessions?limit=1000')).json;
  const sessions = Array.isArray(list) ? list : list?.sessions || list?.items || [];
  for (const item of sessions) {
    const detail = await h.detail(item.id).catch(() => null);
    if (!detail || h.settled(detail)) continue;
    if (detail.plan_mode?.status === 'awaiting_user_input') {
      const result = await h.api('POST', '/api/sessions/' + encodeURIComponent(item.id) + '/planmode/cancel', {});
      assert.ok(result.status < 300, 'cancel pending input ' + item.id + ': ' + result.text);
    }
    await h.waitDetail(item.id, (d) => h.settled(d), 'settled before clearing', 60000);
  }
}

async function historyTotal(h) {
  return (await h.api('GET', '/api/history?page=1&page_size=1')).json?.total || 0;
}

async function runSessions(h) {
  // Paging needs more than one 20-row page of root sessions (only seeded when
  // this area runs on its own); the newest two below then sort onto page 1.
  for (let total = await historyTotal(h); total < 21; total = await historyTotal(h)) {
    const ids = await Promise.all(Array.from({ length: Math.min(6, 21 - total) }, (_, i) => h.startSession('E2E_UI_AWAIT paging filler ' + total + '-' + i)));
    for (const id of ids) await h.waitDetail(id, (d) => h.settled(d), 'settled');
  }
  const target = await h.startSession('E2E_UI_AWAIT 会话管理检索目标 history-target');
  await h.waitDetail(target, (d) => d.state?.status === 'awaiting_input' && !d.active_handle, 'awaiting');
  const victim = await h.startSession('E2E_UI_AWAIT 会话管理删除目标 history-victim');
  await h.waitDetail(victim, (d) => d.state?.status === 'awaiting_input' && !d.active_handle, 'awaiting');
  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'sessions');
  await gotoApp(h, page, '/');
  await tid(page, 'nav-sessions').click();
  await tid(page, 'page-sessions').waitFor();
  await tid(page, 'history-list').waitFor();
  const row = (id) => page.locator('[data-testid="history-row"][data-session-id="' + id + '"]');

  await h.check('Sessions: count, first page, raw titles, status and time', async () => {
    assert.equal((await page.locator('.page-title').innerText()).trim(), '会话管理');
    assert.match(await tid(page, 'history-count').innerText(), /^共 \d+ 个会话/);
    assert.match(await tid(page, 'history-page-label').innerText(), /^第 1 \/ \d+ 页$/);
    assert.equal(await tid(page, 'history-prev').isDisabled(), true);
    await row(target).waitFor();
    assert.equal(await row(target).locator('.history-row-title').getAttribute('translate'), 'no');
    assert.equal((await row(target).locator('.history-row-title').innerText()).trim(), 'E2E_UI_AWAIT 会话管理检索目标 history-target');
    assert.equal(await row(target).getAttribute('data-status'), 'awaiting_input');
    assert.equal((await row(target).getByTestId('status-badge').innerText()).trim(), '等待输入');
    assert.ok((await row(target).locator('time').getAttribute('title')).length > 6);
  }, { page });
  await h.capture(page, 'pages-sessions-zh-desktop');

  await h.check('Sessions: paging, search across pages, status filter', async () => {
    await tid(page, 'history-next').click();
    await page.waitForFunction(() => /^第 2 \//.test(document.querySelector('[data-testid="history-page-label"]')?.textContent || ''));
    await tid(page, 'history-prev').click();
    await page.waitForFunction(() => /^第 1 \//.test(document.querySelector('[data-testid="history-page-label"]')?.textContent || ''));
    await tid(page, 'history-search').fill('history-target');
    await row(target).waitFor();
    await h.until(async () => (await page.locator('[data-testid="history-row"]').count()) === 1, 'one search hit', 10000, 50);
    assert.match(await tid(page, 'history-count').innerText(), /找到 1 个匹配的会话/);
    await tid(page, 'history-search').fill('');
    await tid(page, 'history-filter-completed').click();
    assert.equal(await tid(page, 'history-filter-completed').getAttribute('aria-pressed'), 'true');
    const completed = (await h.api('GET', '/api/history?page=1&page_size=200')).json.items.some((item) => item.status === 'completed');
    await h.until(async () => {
      if (!completed) return (await tid(page, 'history-no-match').count()) === 1;
      const statuses = await page.locator('[data-testid="history-row"]').evaluateAll((nodes) => nodes.map((node) => node.dataset.status));
      return statuses.length > 0 && statuses.every((status) => status === 'completed');
    }, 'completed rows only (or the no-match state)', 15000, 60);
    await tid(page, 'history-filter-all').click();
    await row(target).waitFor();
  }, { page });

  await setLocale(page, 'en');
  await h.check('Sessions in English persists across reload', async () => {
    await page.reload();
    await tid(page, 'history-list').waitFor();
    assert.equal((await page.locator('.page-title').innerText()).trim(), 'Sessions');
    assert.match(await tid(page, 'history-count').innerText(), /^\d+ sessions/);
    assert.equal((await tid(page, 'history-clear').innerText()).trim(), 'Clear history');
  }, { page });
  await h.capture(page, 'pages-sessions-en-desktop');
  await setLocale(page, 'zh-CN');

  await h.check('Sessions: open a row, then "view in Sessions" reveals it', async () => {
    await row(target).getByTestId('history-row-link').click();
    await waitSessionLoaded(page, target);
    await tid(page, 'session-actions').locator('button').first().click();
    await tid(page, 'menu-view-history').click();
    await page.waitForURL(h.baseURL + '/sessions');
    await page.waitForFunction((id) => document.querySelector('[data-testid="history-row"][data-session-id="' + id + '"]')?.classList.contains('is-focused'), target);
  }, { page });

  await h.check('Sessions: delete asks for confirmation (dialog contract) and stays on the page', async () => {
    await row(victim).waitFor();
    const menu = row(victim).getByTestId('history-row-menu').locator('button').first();
    await menu.click();
    await tid(page, 'history-row-delete').click();
    const dialog = tid(page, 'confirm-dialog');
    await dialog.waitFor();
    assert.match(await dialog.innerText(), /删除这个会话？/);
    await assertDialogContract(page, dialog, {});
    assert.equal(await row(victim).count(), 1, 'Esc keeps the session');
    await menu.click();
    await tid(page, 'history-row-delete').click();
    await tid(page, 'confirm-dialog-confirm').click();
    await row(victim).waitFor({ state: 'detached' });
    await waitToast(h, page, /会话已删除/);
    assert.equal((await h.api('GET', '/api/sessions/' + victim)).status, 404);
    assert.equal(new URL(page.url()).pathname, '/sessions');
  }, { page });

  const mobile = await h.newContext({ locale: 'zh-CN', mobile: true });
  const m = await h.newPage(mobile, 'sessions-mobile');
  await gotoApp(h, m, '/sessions');
  await tid(m, 'history-list').waitFor();
  await h.capture(m, 'pages-sessions-zh-mobile');
  await mobile.close();

  await h.check('Sessions: clear history conflicts while a run is active (backend detail kept), then succeeds and stays', async () => {
    const slow = await h.startSession('E2E_UI_SLOW 清空冲突');
    await h.waitDetail(slow, (d) => d.state?.status === 'running', 'running', 60000);
    h.allowHTTP('clear history while running (409)', { url: h.baseURL + '/api/sessions/clear', status: 409, method: 'POST', limit: 2 });
    await tid(page, 'history-refresh').click();
    await tid(page, 'history-clear').click();
    await tid(page, 'confirm-dialog').waitFor();
    assert.match(await tid(page, 'confirm-dialog').innerText(), /清空全部历史？/);
    const response = page.waitForResponse((res) => res.url().endsWith('/api/sessions/clear'));
    await tid(page, 'confirm-dialog-confirm').click();
    assert.equal((await response).status(), 409, 'clear refused while running');
    const toast = await waitToast(h, page, /暂时无法清空/);
    assert.match(toast, /cannot clear history/);
    await h.capture(page, 'pages-sessions-clear-conflict-zh-desktop', { retainToasts: true });
    await h.waitDetail(slow, (d) => h.settled(d), 'slow settled', 60000);
    await settleAllSessions(h);
    await h.until(async () => {
      const queue = (await h.api('GET', '/api/queue/jobs')).json;
      const jobs = Array.isArray(queue) ? queue : queue?.jobs || [];
      return !jobs.some((job) => ['queued', 'running', 'blocked'].includes(job.status));
    }, 'queue idle', 60000, 200);
    await tid(page, 'history-clear').click();
    await tid(page, 'confirm-dialog-confirm').click();
    await tid(page, 'history-empty').waitFor({ timeout: 20000 });
    assert.equal(new URL(page.url()).pathname, '/sessions');
    assert.equal(await historyTotal(h), 0);
    await page.waitForFunction(() => document.querySelectorAll('[data-testid="session-row"]').length === 0);
  }, { page });
  await h.capture(page, 'pages-sessions-empty-zh-desktop');
  await context.close();
}

async function runSettings(h) {
  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'settings');
  await gotoApp(h, page, '/settings');
  await tid(page, 'settings-form').waitFor();

  await h.check('Settings: loads the provider profile; no legacy toggle', async () => {
    assert.equal(await tid(page, 'settings-provider').inputValue(), 'e2e');
    assert.equal(await tid(page, 'settings-base-url').inputValue(), h.providerURL);
    assert.equal(await tid(page, 'settings-model').inputValue(), 'e2e-model');
    assert.equal(await tid(page, 'settings-api-key-state').getAttribute('data-state'), 'configured');
    assert.equal(await tid(page, 'settings-save-bar').count(), 0);
    assert.equal(await tid(page, 'settings-child-active-runtime').getAttribute('placeholder'), '如 30m / 2h');
    assert.doesNotMatch(await tid(page, 'page-settings').innerText(), /legacy|旧版/i);
  }, { page });
  await h.capture(page, 'pages-settings-zh-desktop');

  await h.check('Settings: inline validation is linked to the field and blocks save', async () => {
    const soft = tid(page, 'settings-max-turns-soft');
    await soft.fill('0');
    await soft.press('Tab');
    await page.locator('#st-max-turns-soft-error').waitFor();
    assert.equal(await soft.getAttribute('aria-invalid'), 'true');
    assert.match(await soft.getAttribute('aria-describedby'), /st-max-turns-soft-error/);
    await tid(page, 'settings-child-budget-enabled').check();
    await tid(page, 'settings-child-active-runtime').fill('abc');
    await tid(page, 'settings-child-active-runtime').press('Tab');
    await page.locator('#st-child-active-runtime-error').waitFor();
    await tid(page, 'settings-save-bar').waitFor();
    await tid(page, 'settings-save').click();
    await tid(page, 'settings-save-error').waitFor();
    await page.waitForFunction(() => document.activeElement?.id === 'st-max-turns-soft');
    assert.equal(await tid(page, 'confirm-dialog').count(), 0);
  }, { page });
  await h.capture(page, 'pages-settings-validation-zh-desktop');

  await h.check('Settings: connection test succeeds with the current form values', async () => {
    await tid(page, 'settings-max-turns-soft').fill('30');
    await tid(page, 'settings-child-active-runtime').fill('30m');
    await tid(page, 'settings-child-elapsed').fill('2h');
    await tid(page, 'settings-child-max-turns').fill('50');
    await tid(page, 'settings-context-window').fill('150000');
    await tid(page, 'settings-reasoning-mode').selectOption('high');
    assert.equal(await page.locator('.st-field-error').count(), 0);
    const response = page.waitForResponse((res) => res.url().endsWith('/api/config/test') && res.request().method() === 'POST');
    await tid(page, 'settings-test').click();
    const res = await response;
    assert.equal(res.status(), 200);
    const body = JSON.parse(res.request().postData());
    assert.equal(body.context_window_tokens, 150000);
    assert.equal(body.reasoning_mode, 'high');
    await page.locator('[data-testid="settings-probe-result"][data-success="true"]').waitFor({ timeout: 30000 });
    assert.match(await tid(page, 'settings-probe-result').innerText(), /连接成功/);
  }, { page });

  await h.check('Settings: save confirms, writes config.yaml and round-trips after reload', async () => {
    await tid(page, 'settings-save').click();
    await tid(page, 'confirm-dialog').waitFor();
    const response = page.waitForResponse((res) => res.url().endsWith('/api/config') && res.request().method() === 'POST');
    await tid(page, 'confirm-dialog-confirm').click();
    const res = await response;
    assert.equal(res.status(), 200);
    const body = JSON.parse(res.request().postData());
    assert.deepEqual(body.child_budget, { disabled: false, max_active_runtime_sec: 1800, max_elapsed_sec: 7200, max_turns_per_attempt: 50 });
    assert.equal('legacy_ui_enabled' in body, false);
    await tid(page, 'settings-save-bar').waitFor({ state: 'detached' });
    const yaml = await readFile(h.paths.config, 'utf8');
    for (const needle of ['150000', '1800', '7200']) assert.ok(yaml.includes(needle), 'config.yaml contains ' + needle);
    assert.match(yaml, /max_turns_soft:\s*30/);
    await page.reload();
    await tid(page, 'settings-form').waitFor();
    assert.equal(await tid(page, 'settings-max-turns-soft').inputValue(), '30');
    assert.equal(await tid(page, 'settings-child-budget-enabled').isChecked(), true);
    assert.equal(await tid(page, 'settings-child-active-runtime').inputValue(), '30m');
    assert.equal(await tid(page, 'settings-child-elapsed').inputValue(), '2h');
    assert.equal(await tid(page, 'settings-context-window').inputValue(), '150000');
    assert.equal(await tid(page, 'settings-reasoning-mode').inputValue(), 'high');
  }, { page });

  await h.check('Settings: a failed connection test shows the typed error; leaving with changes asks first', async () => {
    await tid(page, 'settings-base-url').fill('http://127.0.0.1:9/v1');
    h.allowHTTP('connection test against a closed port', { url: h.baseURL + '/api/config/test', method: 'POST', limit: 2 });
    const response = page.waitForResponse((res) => res.url().endsWith('/api/config/test'));
    await tid(page, 'settings-test').click();
    await response;
    await page.locator('[data-testid="settings-probe-result"][data-success="false"]').waitFor({ timeout: 30000 });
    assert.match(await tid(page, 'settings-probe-result').innerText(), /连接测试未通过/);
    await tid(page, 'settings-probe-result').scrollIntoViewIfNeeded();
    await h.capture(page, 'pages-settings-probe-failed-zh-desktop');
    await tid(page, 'nav-workspace').click();
    const dialog = tid(page, 'confirm-dialog');
    await dialog.waitFor();
    await assertDialogContract(page, dialog, {});
    assert.equal(new URL(page.url()).pathname, '/settings');
    await tid(page, 'settings-discard').click();
    await tid(page, 'settings-save-bar').waitFor({ state: 'detached' });
    assert.equal(await tid(page, 'settings-base-url').inputValue(), h.providerURL);
  }, { page });

  await setLocale(page, 'en');
  await page.evaluate(() => document.querySelector('.page')?.scrollTo(0, 0));
  await h.check('Settings in English', async () => {
    assert.match(await tid(page, 'settings-section-provider').innerText(), /Model and provider/);
    assert.equal(await tid(page, 'settings-child-active-runtime').getAttribute('placeholder'), 'e.g. 30m / 2h');
  }, { page });
  await h.capture(page, 'pages-settings-en-desktop');
  await context.close();
  const mobile = await h.newContext({ locale: 'zh-CN', mobile: true });
  const m = await h.newPage(mobile, 'settings-mobile');
  await gotoApp(h, m, '/settings');
  await tid(m, 'settings-form').waitFor();
  await h.capture(m, 'pages-settings-zh-mobile');
  await mobile.close();
}
