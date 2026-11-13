// README showcase: a completed MAIN session at 1440×900 with the step group
// collapsed, the todo strip, the file-change card and the inspector open on
// Tasks (side panel, not overlay), in zh-CN and en.
import assert from 'node:assert/strict';
import { gotoApp, openInspectorTab, setLocale, tid, waitSessionLoaded } from './ui.mjs';
import { MAIN_PROMPT } from './session.mjs';

export async function runShowcase(h) {
  const mainId = await h.fixture('main', async () => {
    const id = await h.startSession(MAIN_PROMPT);
    await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed', 240000);
    return id;
  });
  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'showcase');
  await gotoApp(h, page, '/s/' + encodeURIComponent(mainId));
  await waitSessionLoaded(page, mainId);
  // README image: the sidebar list must be loaded (the overview lists recent sessions only).
  await page.locator('[data-testid="sidebar"] [data-testid="session-row"]').first().waitFor({ timeout: 60000 });
  await openInspectorTab(page, 'tasks');
  for (const locale of ['zh-CN', 'en']) {
    await setLocale(page, locale);
    const tag = locale === 'en' ? 'en' : 'zh';
    await h.check('showcase ' + tag + ': collapsed steps, todo strip, file changes, Tasks panel', async () => {
      assert.equal(await page.locator('#app-layout').getAttribute('data-inspector'), 'inline', 'side panel, not an overlay');
      await page.locator('[data-task-group="in_progress"]').waitFor();
      const groups = await page.locator('[data-testid="step-group"] .step-group-toggle').evaluateAll((nodes) => nodes.map((node) => node.getAttribute('aria-expanded')));
      assert.ok(groups.length >= 1 && groups.every((value) => value === 'false'), 'step groups collapsed: ' + groups);
      await page.locator('[data-testid="stream-state"][data-state="completed"]').waitFor();
      for (const id of ['todo-strip', 'turn-file-changes', 'step-group']) {
        const box = await tid(page, id).first().boundingBox();
        assert.ok(box && box.y < 900 && box.y + box.height > 0, id + ' visible in the viewport: ' + JSON.stringify(box));
      }
      assert.equal(await tid(page, 'todo-strip-toggle').getAttribute('aria-expanded'), 'false');
      assert.equal(await page.locator('[data-testid="session-list"] .session-nav-loading').count(), 0, 'sidebar list loaded');
      assert.equal((await page.locator('html').getAttribute('lang')), locale);
    }, { page });
    await h.capture(page, 'showcase-' + tag + '-desktop');
  }
  await context.close();
}
