// README showcase: a completed MAIN session at 1440×900 with the step group
// collapsed, the todo strip, the file-change card and the inspector open on
// Tasks (side panel, not overlay), in zh-CN and en. Runs against a fresh web
// service (own config, session directory and workspace) that holds only the
// four showcase sessions, so the sidebar is not cluttered by the suite's other
// fixtures. The scripted provider selects a scenario by the marker anywhere in
// the prompt, so the prompts read naturally and keep the marker at the end.
import assert from 'node:assert/strict';
import { gotoApp, openInspectorTab, setLocale, tid, waitSessionLoaded } from './ui.mjs';

export const SHOWCASE_PROMPTS = Object.freeze({
  main: 'Inspect the bilingual web console, group the open tasks and record the changed files (E2E_UI_MAIN)',
  goal: 'Keep the release checklist green until every check passes (E2E_UI_GOAL)',
  plan: 'Plan the settings page cleanup before changing anything (E2E_UI_PLAN)',
  await: 'Draft the release notes, then wait for my review (E2E_UI_AWAIT)',
});

const idle = (detail) => !detail.active_handle && !['running', 'queued', 'pending'].includes(detail.state?.status);

export async function runShowcase(h) {
  const rt = await h.startRuntime('showcase');
  try {
    const ids = {};
    await h.check('showcase runtime: four sessions (completed MAIN, GOAL, PLAN awaiting approval, AWAIT)', async () => {
      ids.main = await rt.startSession(SHOWCASE_PROMPTS.main);
      await rt.waitDetail(ids.main, (d) => d.state?.status === 'completed' && idle(d), 'MAIN completed', 240000);
      ids.goal = await rt.startSession({ prompt: SHOWCASE_PROMPTS.goal, goal: { enabled: true } });
      ids.plan = await rt.startSession({ prompt: SHOWCASE_PROMPTS.plan, plan_mode: { enabled: true } });
      ids.await = await rt.startSession(SHOWCASE_PROMPTS.await);
      await rt.waitDetail(ids.goal, (d) => d.state?.status === 'completed' && d.goal?.status === 'complete' && idle(d), 'GOAL completed');
      await rt.waitDetail(ids.plan, (d) => d.plan_mode?.status === 'awaiting_approval' && d.state?.status === 'awaiting_input' && idle(d), 'PLAN awaiting approval');
      await rt.waitDetail(ids.await, (d) => d.state?.status === 'awaiting_input' && idle(d), 'AWAIT awaiting input');
      const listed = await rt.api('GET', '/api/sessions');
      assert.equal(listed.status, 200, listed.text);
      const sessions = Array.isArray(listed.json) ? listed.json : (listed.json?.sessions || []);
      assert.deepEqual(sessions.map((item) => item.id).sort(), Object.values(ids).sort(), 'the showcase runtime holds only its own sessions');
    });
    if (!ids.await) throw new Error('showcase sessions did not settle');

    const context = await h.newContext({ locale: 'zh-CN' });
    const page = await h.newPage(context, 'showcase');
    await gotoApp(rt, page, '/s/' + encodeURIComponent(ids.main));
    await waitSessionLoaded(page, ids.main);
    // README image: wait until the sidebar lists exactly the four sessions.
    const rows = page.locator('[data-testid="sidebar"] [data-testid="session-row"]');
    await h.until(async () => (await rows.count()) === 4 && (await page.locator('[data-testid="session-list"] .session-nav-loading').count()) === 0,
      'sidebar lists the four showcase sessions', 60000, 100);
    await openInspectorTab(page, 'tasks');
    for (const locale of ['zh-CN', 'en']) {
      await setLocale(page, locale);
      const tag = locale === 'en' ? 'en' : 'zh';
      await h.check('showcase ' + tag + ': collapsed steps, todo strip, file changes, Tasks panel, four-session sidebar', async () => {
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
        const listed = await rows.evaluateAll((nodes) => nodes.map((node) => node.dataset.sessionId));
        assert.deepEqual(listed.slice().sort(), Object.values(ids).sort(), 'sidebar shows only the showcase sessions');
        assert.equal(await rows.filter({ hasText: 'E2E_UI_MAIN' }).count(), 1);
        assert.equal((await page.locator('html').getAttribute('lang')), locale);
      }, { page });
      await h.capture(page, 'showcase-' + tag + '-desktop');
    }
    await context.close();
  } finally {
    await rt.stop();
  }
}
