// Session: start from the composer, the message stream (step groups, tool
// steps, final answer, file changes, todo strip), the inspector tabs, live
// runs (activity, steer, interrupt, stop), continue with a model override,
// Goal, child agents, and long-session scrolling.
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import {
  clickSafe, closeInspector, ensureInspector, gotoApp, openInspectorTab, openSession, sendText, setLocale, setPressed,
  startFromComposer, storeState, tid, waitSessionLoaded, waitToast,
} from './ui.mjs';

export const MAIN_PROMPT = 'E2E_UI_MAIN raw user text: Settings must remain unchanged.';
const LONG_PROMPT = 'E2E_UI_AWAIT long session for load earlier.';

/**
 * Opens the model picker and asserts the popover lies fully inside the
 * viewport and every clipping ancestor (.app-main hides horizontal overflow),
 * that Done is the element under its own centre with a mobile-size target,
 * captures the screen (overflow audit included) and closes it with Done.
 */
async function assertModelPopoverFits(h, page, shot) {
  const picker = tid(page, 'model-picker');
  await picker.waitFor();
  await picker.click();
  const popover = tid(page, 'model-popover');
  await popover.waitFor();
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const box = await popover.evaluate((node) => {
    const round = (value) => Math.round(value * 10) / 10;
    const rect = node.getBoundingClientRect();
    const clip = { left: 0, top: 0, right: innerWidth, bottom: innerHeight };
    for (let parent = node.parentElement; parent && parent !== document.documentElement; parent = parent.parentElement) {
      const style = getComputedStyle(parent);
      const bounds = parent.getBoundingClientRect();
      if (style.overflowX !== 'visible') { clip.left = Math.max(clip.left, bounds.left); clip.right = Math.min(clip.right, bounds.right); }
      if (style.overflowY !== 'visible') { clip.top = Math.max(clip.top, bounds.top); clip.bottom = Math.min(clip.bottom, bounds.bottom); }
    }
    const done = node.querySelector('[data-testid="model-done"]').getBoundingClientRect();
    const hit = document.elementFromPoint(done.left + done.width / 2, done.top + done.height / 2);
    return {
      rect: { left: round(rect.left), right: round(rect.right), top: round(rect.top), bottom: round(rect.bottom) },
      clip: { left: round(clip.left), right: round(clip.right), top: round(clip.top), bottom: round(clip.bottom) },
      done: { width: round(done.width), height: round(done.height), hit: Boolean(hit?.closest('[data-testid="model-done"]')) },
    };
  });
  const detail = JSON.stringify(box);
  assert.ok(box.rect.left >= box.clip.left - 0.5 && box.rect.right <= box.clip.right + 0.5, 'popover inside horizontally ' + detail);
  assert.ok(box.rect.top >= box.clip.top - 0.5 && box.rect.bottom <= box.clip.bottom + 0.5, 'popover inside vertically ' + detail);
  assert.ok(box.done.hit, 'Done is reachable ' + detail);
  assert.ok(box.done.width >= 44 && box.done.height >= 44, 'Done is a 44px target ' + detail);
  await h.capture(page, shot);
  await tid(page, 'model-done').click();
  await popover.waitFor({ state: 'detached' });
  assert.equal(await page.evaluate(() => document.activeElement?.dataset?.testid), 'model-picker', 'Done returns focus to the picker');
  return box;
}

export async function runSession(h) {
  // A long MAIN session (for "load earlier") and a background notification
  // fixture are prepared through REST while the browser drives the rest.
  const longMain = h.fixture('long-session', async () => {
    // AWAIT parks after one provider call; every continue is a single finish
    // call, so the session grows past one 40-message page cheaply.
    const id = await h.startSession(LONG_PROMPT);
    let detail = await h.waitDetail(id, (d) => d.state?.status === 'awaiting_input' && !d.active_handle, 'awaiting');
    for (let index = 1; index <= 30 && !(detail.has_more_messages && index > 3); index += 1) {
      const result = await h.api('POST', '/api/sessions/' + encodeURIComponent(id) + '/continue', { message: 'Long session follow-up ' + index });
      assert.equal(result.status, 202, result.text);
      detail = await h.waitDetail(id, (d) => h.settled(d) && (d.messages || []).some((m) => m.text === 'Long session follow-up ' + index), 'follow-up ' + index);
    }
    assert.equal(detail.has_more_messages, true, 'the long session spans more than one page');
    const control = path.join(h.paths.sessions, id, 'control');
    await mkdir(control, { recursive: true, mode: 0o700 });
    await writeFile(path.join(control, 'background.jsonl'), JSON.stringify({
      id: 'notification_e2e_empty', created_at: new Date().toISOString(), source: 'queue', queue_job_id: 'job_e2e_empty',
      agent_name: 'Settings', agent_role: 'evaluator', status: 'completed', session_status: 'completed',
      delivery_status: 'accepted', available_actions: ['inspect'],
    }) + '\n', { mode: 0o600 });
    return id;
  });
  longMain.catch(() => {});

  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'session-desktop');
  const contextRequests = [];
  page.on('request', (request) => { if (/\/api\/sessions\/[^/]+\/context(\?|$)/.test(new URL(request.url()).pathname + new URL(request.url()).search)) contextRequests.push(request.url()); });
  await gotoApp(h, page, '/');
  let mainId = '';

  await h.check('MAIN from the composer: payload, navigation, completion', async () => {
    const { id, payload } = await startFromComposer(page, MAIN_PROMPT);
    mainId = id;
    assert.equal(payload.prompt, MAIN_PROMPT);
    assert.equal(payload.goal, undefined);
    assert.equal(payload.plan_mode, undefined);
    assert.equal(await tid(page, 'composer-input').inputValue(), '', 'composer cleared after an accepted start');
    await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed', 240000);
    await page.locator('[data-testid="stream-state"][data-state="completed"]').waitFor({ timeout: 15000 });
    h.registerFixture('main', id);
  }, { page });
  if (!mainId) throw new Error('MAIN did not start; remaining session checks depend on it');

  await h.check('MAIN stream: raw user bubble, collapsed step group, final answer exactly once', async () => {
    const user = page.locator('[data-testid="message"][data-role="user"]').filter({ hasText: 'Settings must remain unchanged' });
    await user.waitFor();
    assert.equal(await user.count(), 1);
    assert.equal(await user.locator('.msg-bubble').getAttribute('translate'), 'no');
    const group = page.locator('[data-testid="step-group"]').first();
    await group.waitFor();
    assert.equal(await group.locator('.step-group-toggle').getAttribute('aria-expanded'), 'false', 'step groups start collapsed');
    assert.match(await group.innerText(), /已执行 \d+ 个步骤/);
    assert.equal(await page.locator('[data-testid="message"][data-final="true"]').count(), 1);
    assert.equal(await page.locator('[data-testid="message-list"]').getByText('E2E main completed', { exact: true }).count(), 1, 'finish answer rendered once');
    assert.equal(await page.locator('[data-testid="message"][data-role="harness"]').count(), 0);
  }, { page });

  await h.check('MAIN tool step expansion shows raw arguments; file-change card and todo strip', async () => {
    const group = page.locator('[data-testid="step-group"]').filter({ hasText: 'todo_write' }).first();
    await group.locator('.step-group-toggle').click();
    assert.equal(await group.locator('.step-group-toggle').getAttribute('aria-expanded'), 'true');
    assert.ok(await group.locator('[data-testid="tool-step"]').count() >= 10);
    const shell = group.locator('[data-testid="tool-step"][data-tool="shell"]');
    await shell.locator('.step-row').click();
    const detail = shell.locator('[data-testid="step-detail"]');
    await detail.waitFor();
    assert.match(await detail.locator('[data-testid="step-args"] pre').innerText(), /e2e-created\.txt/);
    assert.equal(await detail.locator('pre').first().getAttribute('translate'), 'no');
    await shell.scrollIntoViewIfNeeded();
    await h.capture(page, 'session-main-steps-zh-desktop');
    await shell.locator('.step-row').click();
    await group.locator('.step-group-toggle').click();
    assert.equal(await group.locator('.step-group-toggle').getAttribute('aria-expanded'), 'false');
    const files = tid(page, 'turn-file-changes');
    await files.waitFor();
    assert.match(await files.innerText(), /e2e-created\.txt/);
    assert.equal(await files.locator('.files-row-path').first().getAttribute('translate'), 'no');
    const todo = tid(page, 'todo-strip');
    await todo.waitFor();
    assert.match(await todo.innerText(), /\d+\/\d+/);
    await tid(page, 'todo-strip-toggle').click();
    assert.equal(await tid(page, 'todo-strip-toggle').getAttribute('aria-expanded'), 'true');
    assert.ok(await page.getByTestId('todo-item').count() >= 3);
    await h.capture(page, 'session-main-todo-open-zh-desktop');
    await tid(page, 'todo-strip-toggle').click();
    assert.equal(await tid(page, 'composer-input').getAttribute('placeholder'), '追加后续要求，在原有上下文中继续…');
  }, { page });

  await h.check('inspector panel (≥1280px): relevant tabs in one row, keyboard Arrow/Home/End', async () => {
    await ensureInspector(page);
    assert.equal(await tid(page, 'inspector').evaluate((node) => node.tagName), 'ASIDE');
    assert.equal(await page.locator('#app-layout').getAttribute('data-inspector'), 'inline');
    const ids = await page.locator('[data-testid="inspector-tabbar"] [role="tab"]').evaluateAll((nodes) => nodes.map((node) => node.dataset.testid.replace('inspector-tab-', '')));
    for (const tab of ['overview', 'tasks', 'context', 'timeline', 'files']) assert.ok(ids.includes(tab), tab + ' in ' + ids);
    assert.ok(!ids.includes('plan') && !ids.includes('goal'), 'irrelevant tabs hidden: ' + ids);
    const tops = await page.locator('[data-testid="inspector-tabbar"] [role="tab"]').evaluateAll((nodes) => nodes.map((node) => Math.round(node.getBoundingClientRect().top)));
    assert.equal(new Set(tops).size, 1, 'tabs stay in one row');
    await openInspectorTab(page, 'overview');
    await tid(page, 'inspector-tab-overview').focus();
    await page.keyboard.press('ArrowRight');
    await page.locator('[data-testid="inspector-tab-' + ids[1] + '"][aria-selected="true"]').waitFor();
    await page.keyboard.press('End');
    await page.locator('[data-testid="inspector-tab-' + ids.at(-1) + '"][aria-selected="true"]').waitFor();
    await page.keyboard.press('Home');
    await page.locator('[data-testid="inspector-tab-overview"][aria-selected="true"]').waitFor();
    assert.equal(await tid(page, 'inspector-tab-overview').evaluate((node) => document.activeElement === node), true);
  }, { page });

  await h.check('inspector Overview: status and raw facts; Context not requested yet', async () => {
    await openInspectorTab(page, 'overview');
    assert.equal(await tid(page, 'inspector-status').getAttribute('data-status'), 'completed');
    await page.locator('[data-testid="inspector-fact-session-id"]').getByText(mainId).waitFor();
    assert.equal(await page.locator('[data-testid="inspector-fact-session-id"] .insp-raw').getAttribute('translate'), 'no');
    assert.equal(contextRequests.length, 0, 'context loads lazily');
  }, { page });
  await h.capture(page, 'session-inspector-overview-zh-desktop');

  await h.check('inspector Tasks: todo metric, five derived groups, localized fallback, raw owner', async () => {
    await openInspectorTab(page, 'tasks');
    for (const group of ['in_progress', 'ready', 'blocked', 'completed', 'cancelled']) await page.locator('[data-task-group="' + group + '"]').waitFor();
    for (const id of ['task_0001', 'task_0002', 'task_0003', 'task_0004', 'task_0005']) await page.locator('[data-task-id="' + id + '"]').waitFor();
    assert.match(await page.locator('[data-testid="inspector-tasks-todo-metric"]').innerText(), /2 个进行中/);
    const task = page.locator('[data-task-id="task_0001"]');
    assert.equal((await task.locator('.task-card-copy').innerText()).trim(), '无描述。');
    assert.equal(await task.locator('.task-chip').filter({ hasText: /^Settings$/ }).getAttribute('translate'), 'no');
  }, { page });
  await h.capture(page, 'session-inspector-tasks-zh-desktop');

  await h.check('inspector Files and Timeline', async () => {
    await openInspectorTab(page, 'files');
    await page.locator('[data-testid="inspector-file"][data-path="e2e-created.txt"]').waitFor();
    await openInspectorTab(page, 'timeline');
    const items = page.locator('[data-testid="inspector-timeline-item"]');
    await items.first().waitFor();
    const total = await items.count();
    assert.ok(total > 3, 'timeline items ' + total);
    await page.locator('[data-testid="inspector-timeline-search"]').fill('Settings must remain');
    await h.until(async () => (await items.count()) >= 1 && (await items.count()) < total, 'timeline search narrows', 5000, 50);
    await page.locator('[data-testid="inspector-timeline-search"]').fill('');
  }, { page });
  await h.capture(page, 'session-inspector-timeline-zh-desktop');

  await h.check('inspector Context: lazy-loads once, zh-CN labels, Refresh issues one request', async () => {
    await openInspectorTab(page, 'context');
    const panel = tid(page, 'inspector-context');
    await panel.waitFor();
    await panel.getByText('根会话峰值', { exact: true }).waitFor();
    await panel.getByText('提供商用量', { exact: true }).waitFor();
    await panel.getByText('会话链路', { exact: true }).waitFor();
    assert.equal(contextRequests.length, 1, 'opening Context requests it once');
    assert.equal(await panel.locator('h4').first().getAttribute('translate'), 'no');
    await tid(page, 'inspector-context-refresh').click();
    await page.waitForFunction(() => !document.querySelector('[data-testid="inspector-context-refresh"]')?.disabled);
    assert.equal(contextRequests.length, 2, 'refresh issues exactly one more request');
    await openInspectorTab(page, 'tasks');
    await openInspectorTab(page, 'context');
    await panel.getByText('根会话峰值', { exact: true }).waitFor();
    // Reuse is per session visit (spec/17-web-console.md §7.4.1), whatever detail refreshes happened meanwhile.
    assert.equal(contextRequests.length, 2, 'switching back to Context reuses the loaded report');
  }, { page });
  await h.capture(page, 'session-context-zh-desktop');
  await h.check('inspector Context in English restores every operator label', async () => {
    await setLocale(page, 'en');
    const panel = tid(page, 'inspector-context');
    await panel.getByText('Root peak', { exact: true }).waitFor();
    await panel.getByText('Provider usage', { exact: true }).waitFor();
    await panel.getByText('Lineage', { exact: true }).waitFor();
    assert.equal(await panel.getByText('根会话峰值', { exact: true }).count(), 0);
    assert.equal((await tid(page, 'inspector-tab-context').innerText()).trim(), 'Context');
  }, { page });
  await h.capture(page, 'session-context-en-desktop');
  await openInspectorTab(page, 'tasks');
  await h.check('inspector Tasks in English translates groups and fallbacks only', async () => {
    assert.equal((await page.locator('[data-task-group="in_progress"] .task-group-heading span').first().innerText()).trim(), 'In progress');
    assert.equal((await page.locator('[data-task-id="task_0001"] .task-card-copy').innerText()).trim(), 'No description.');
    assert.match(await page.locator('[data-testid="inspector-tasks-todo-metric"]').innerText(), /2 in progress/);
    assert.match(await page.locator('[data-testid="step-group"]').first().innerText(), /Ran \d+ steps?/);
  }, { page });
  await h.capture(page, 'session-main-en-desktop');
  await setLocale(page, 'zh-CN');
  await closeInspector(page);

  await h.check('SLOW: live activity, stop morphs to steer, steer and interrupt-armed steer', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_SLOW 运行中追加指令');
    await tid(page, 'live-activity').waitFor({ timeout: 6000 });
    await tid(page, 'stop-button').waitFor({ timeout: 6000 });
    // Steering needs the server-side run to be live (the server answers 404 "not running" before that).
    await h.waitDetail(id, (d) => d.state?.status === 'running' && d.active_handle === true, 'running', 20000);
    assert.equal(await tid(page, 'goal-toggle').count(), 0, 'Goal only on new sessions');
    const steers = [];
    const onSteer = (request) => { if (request.url().endsWith('/steer') && request.method() === 'POST') steers.push(request.postDataJSON()); };
    page.on('request', onSteer);
    try {
      await tid(page, 'composer-input').fill('请在总结中提到追加指令');
      assert.equal(await tid(page, 'stop-button').count(), 0, 'typing morphs stop into steer');
      assert.equal(await tid(page, 'send-button').getAttribute('data-action'), 'steer');
      const first = page.waitForResponse((res) => res.url().endsWith('/api/sessions/' + id + '/steer'));
      await tid(page, 'composer-input').press('Enter');
      assert.equal((await first).status(), 202);
      await setPressed(tid(page, 'interrupt-toggle'), true);
      assert.match(await tid(page, 'composer-input').getAttribute('placeholder'), /打断当前步骤/);
      await tid(page, 'composer-input').fill('打断并改为简短总结');
      await h.capture(page, 'session-interrupt-armed-zh-desktop');
      await tid(page, 'composer-input').press('Enter');
      await h.until(() => steers.length >= 2, 'two steer requests', 6000, 30);
    } finally {
      page.off('request', onSteer);
    }
    assert.deepEqual(steers, [{ message: '请在总结中提到追加指令', interrupt: false }, { message: '打断并改为简短总结', interrupt: true }]);
    assert.match(await tid(page, 'live-activity').locator('.activity-elapsed').innerText(), /秒/);
    await h.capture(page, 'session-running-zh-desktop');
    const detail = await h.waitDetail(id, (d) => (d.steer_requests || []).length >= 2 && h.settled(d), 'steers settled', 30000);
    assert.ok(detail.steer_requests.some((item) => item.interrupt === true), 'interrupt steer recorded');
    await tid(page, 'live-activity').waitFor({ state: 'detached', timeout: 10000 });
  }, { page });

  await h.check('stop a running session: the primary button morphs into stop and settles the run', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_SLOW 停止测试');
    const stop = tid(page, 'stop-button');
    await stop.waitFor({ timeout: 6000 });
    await h.until(async () => !(await stop.isDisabled()), 'stop enabled', 6000, 50);
    assert.ok(/停止/.test(await stop.getAttribute('aria-label')), 'stop is labelled');
    const response = page.waitForResponse((res) => res.url().endsWith('/api/sessions/' + id + '/stop') && res.request().method() === 'POST');
    await stop.click();
    assert.ok([200, 202].includes((await response).status()));
    await h.waitDetail(id, (d) => ['paused', 'cancelled', 'awaiting_input'].includes(d.state?.status) && !d.active_handle, 'stopped', 15000);
    await tid(page, 'send-button').waitFor();
    await h.until(async () => /继续/.test(await tid(page, 'composer-input').getAttribute('placeholder')), 'continue placeholder', 8000);
  }, { page });
  await h.capture(page, 'session-stopped-zh-desktop');

  await h.check('AWAIT → continue with a provider/model override from the model picker', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_AWAIT 等待输入后继续');
    await h.waitDetail(id, (d) => d.state?.status === 'awaiting_input' && !d.active_handle, 'awaiting', 20000);
    await page.locator('[data-testid="stream-state"][data-state="awaiting"]').waitFor({ timeout: 10000 });
    assert.match(await page.locator('[data-testid="stream-state"]').innerText(), /Browser E2E requires a continue message/);
    await h.until(async () => (await tid(page, 'composer-input').getAttribute('placeholder')) === '回复以继续这个会话…', 'continue placeholder', 8000);
    await tid(page, 'model-picker').click();
    await tid(page, 'model-popover').waitFor();
    // Desktop placement: the popover starts under the chip.
    const chipLeft = await tid(page, 'model-picker').evaluate((node) => node.getBoundingClientRect().left);
    const popoverLeft = await tid(page, 'model-popover').evaluate((node) => node.getBoundingClientRect().left);
    assert.ok(Math.abs(popoverLeft - chipLeft) <= 1, 'desktop popover aligned with the chip: ' + popoverLeft + ' vs ' + chipLeft);
    await h.capture(page, 'session-model-picker-zh-desktop');
    await page.locator('[data-testid="model-option"][data-provider="e2e"]').check();
    await tid(page, 'model-input').fill('e2e-model');
    await page.keyboard.press('Escape');
    await tid(page, 'model-popover').waitFor({ state: 'detached' });
    assert.match(await tid(page, 'model-picker').innerText(), /e2e · e2e-model/);
    assert.equal(await page.evaluate(() => document.activeElement?.dataset?.testid), 'model-picker', 'Esc returns focus to the picker');
    const request = page.waitForRequest((req) => req.url().endsWith('/api/sessions/' + id + '/continue') && req.method() === 'POST');
    await sendText(h, page, '继续完成任务');
    assert.deepEqual((await request).postDataJSON(), { message: '继续完成任务', provider: 'e2e', model: 'e2e-model' });
    await waitToast(h, page, /会话已继续/);
    await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'continued', 20000);
    await h.until(async () => /默认模型/.test(await tid(page, 'model-picker').innerText()), 'override resets', 8000);
  }, { page });

  await h.check('Goal: start with the Goal toggle, live banner, inspector Goal tab', async () => {
    await page.getByTestId('new-session').first().click();
    await tid(page, 'composer-input').waitFor();
    await setPressed(tid(page, 'plan-toggle'), true);
    await setPressed(tid(page, 'goal-toggle'), true);
    assert.equal(await tid(page, 'plan-toggle').getAttribute('aria-pressed'), 'false', 'Goal and Plan are exclusive');
    assert.match(await page.locator('.composer-note').innerText(), /Goal 模式/);
    await tid(page, 'composer-input').fill('E2E_UI_GOAL 完成一个目标');
    const request = page.waitForRequest((req) => req.url().endsWith('/api/sessions/start'));
    await tid(page, 'composer-input').press('Enter');
    assert.deepEqual((await request).postDataJSON().goal, { enabled: true });
    await page.waitForURL(/\/s\//);
    const id = decodeURIComponent(new URL(page.url()).pathname.split('/')[2]);
    await tid(page, 'goal-banner').waitFor({ timeout: 15000 });
    await h.waitDetail(id, (d) => d.goal?.status === 'complete' && d.state?.status === 'completed' && !d.active_handle, 'goal complete', 20000);
    await h.until(async () => (await tid(page, 'goal-banner').getAttribute('data-status')) === 'complete', 'banner status', 8000);
    assert.match(await tid(page, 'goal-banner').innerText(), /E2E_UI_GOAL/);
    await h.capture(page, 'session-goal-banner-zh-desktop');
    await tid(page, 'goal-details').click();
    await page.locator('[data-testid="inspector-tab-goal"][aria-selected="true"]').waitFor();
    const panel = tid(page, 'inspector-goal');
    await panel.waitFor();
    assert.equal(await panel.getAttribute('data-goal-status'), 'complete');
    assert.match(await tid(page, 'inspector-goal-objective').innerText(), /E2E_UI_GOAL/);
    await tid(page, 'inspector-goal-audit').waitFor();
    await h.capture(page, 'session-goal-inspector-zh-desktop');
    await closeInspector(page);
  }, { page });

  await h.check('CHILDREN (workers ≥2): Agents tab lists the child; it opens and links back', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_CHILDREN 拆分并验证子任务');
    const detail = await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle && (d.children?.sessions || []).length === 1, 'children', 60000);
    const childId = detail.children.sessions[0].id;
    await openInspectorTab(page, 'agents');
    const row = page.locator('[data-testid="inspector-agent-row"][data-session-id="' + childId + '"]');
    await row.waitFor();
    assert.match(await row.innerText(), /e2e-child/);
    assert.equal(await row.getAttribute('data-status'), 'completed');
    assert.equal(await row.locator('.insp-card-title').getAttribute('translate'), 'no');
    await h.capture(page, 'session-agents-zh-desktop');
    await row.locator('[data-testid="inspector-agent-open"]').click();
    await waitSessionLoaded(page, childId);
    await openInspectorTab(page, 'overview');
    await page.locator('[data-testid="inspector-link-parent"][data-session-id="' + id + '"]').waitFor();
    await closeInspector(page);
  }, { page });

  const longId = await longMain;
  await h.check('long session: background notification card (raw identity, localized fallback)', async () => {
    await openSession(page, longId);
    await openInspectorTab(page, 'agents');
    const card = page.locator('[data-testid="inspector-notification"][data-notification-id="notification_e2e_empty"]');
    await card.waitFor();
    assert.equal(await card.locator('.insp-card-title').getAttribute('translate'), 'no');
    assert.equal((await card.locator('.insp-card-title').innerText()).trim(), 'Settings · evaluator');
    assert.equal((await card.locator('.insp-card-copy').innerText()).trim(), '未记录最终文本。');
    await h.capture(page, 'session-notification-zh-desktop');
    await setLocale(page, 'en');
    assert.equal((await card.locator('.insp-card-copy').innerText()).trim(), 'No final text recorded.');
    assert.equal((await card.locator('.insp-card-title').innerText()).trim(), 'Settings · evaluator', 'durable identity unchanged');
    await setLocale(page, 'zh-CN');
    await closeInspector(page);
  }, { page });

  await h.check('long session: load earlier keeps the reading position; back-to-latest returns to the end', async () => {
    const scroller = page.locator('.session-scroll');
    await scroller.evaluate((node) => { node.scrollTop = 0; node.dispatchEvent(new Event('scroll')); });
    const button = tid(page, 'load-earlier');
    await button.waitFor();
    const anchorId = await page.locator('[data-testid="message"]').first().getAttribute('data-message-id');
    const anchor = page.locator('[data-message-id="' + anchorId + '"]');
    const before = await anchor.evaluate((node) => node.getBoundingClientRect().top);
    const count = await page.locator('[data-testid="message"]').count();
    await button.click();
    await page.waitForFunction((n) => document.querySelectorAll('[data-testid="message"]').length > n, count);
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const after = await anchor.evaluate((node) => node.getBoundingClientRect().top);
    assert.ok(Math.abs(after - before) < 6, 'anchor moved ' + before + ' -> ' + after);
    await page.locator('[data-testid="message"][data-role="user"]').filter({ hasText: LONG_PROMPT }).waitFor();
    await h.capture(page, 'session-load-earlier-zh-desktop');
    await tid(page, 'back-to-latest').waitFor();
    await tid(page, 'back-to-latest').click();
    await page.waitForFunction(() => { const el = document.querySelector('.session-scroll'); return el.scrollHeight - el.scrollTop - el.clientHeight < 130; });
    await tid(page, 'back-to-latest').waitFor({ state: 'detached' });
    const messages = await storeState(page, 'session.messages');
    assert.ok(messages.some((message) => message.text === LONG_PROMPT), 'polling keeps the loaded earlier page');
  }, { page });
  await context.close();

  // Mobile stream layout for the MAIN session.
  for (const locale of ['zh-CN', 'en']) {
    const tag = locale === 'en' ? 'en' : 'zh';
    const mobile = await h.newContext({ locale, mobile: true });
    const m = await h.newPage(mobile, 'session-mobile-' + tag);
    await gotoApp(h, m, '/s/' + encodeURIComponent(mainId));
    await waitSessionLoaded(m, mainId);
    await h.check('mobile ' + tag + ': stream, expanded steps and overlay inspector', async () => {
      const group = m.locator('[data-testid="step-group"]').first();
      await group.locator('.step-group-toggle').click();
      await group.locator('[data-testid="tool-step"][data-tool="shell"] .step-row').click();
      await group.locator('[data-testid="tool-step"][data-tool="shell"]').scrollIntoViewIfNeeded();
      await h.capture(m, 'session-main-' + tag + '-mobile');
      await tid(m, 'inspector-toggle').click();
      await tid(m, 'inspector').waitFor();
      assert.equal(await tid(m, 'inspector').getAttribute('role'), 'dialog');
      assert.equal(await m.locator('#app-layout').evaluate((node) => node.inert), true);
      await openInspectorTab(m, 'tasks');
      await h.capture(m, 'session-inspector-tasks-' + tag + '-mobile');
      await m.keyboard.press('Escape');
      await tid(m, 'inspector').waitFor({ state: 'detached' });
      assert.equal(await tid(m, 'inspector-toggle').evaluate((node) => document.activeElement === node), true, 'focus returns to the toggle');
      await tid(m, 'inspector-toggle').click();
      await tid(m, 'inspector').waitFor();
      assert.equal(await tid(m, 'inspector-panel').evaluate((node) => node.scrollTop), 0, 'reopened overlay starts at the top');
      await m.keyboard.press('Escape');
      await tid(m, 'inspector').waitFor({ state: 'detached' });
    }, { page: m });
    await h.check('mobile ' + tag + ': model picker stays inside 390px in continue mode and on /, Done reachable', async () => {
      // MAIN is completed: continue mode with the Plan chip before the model chip.
      assert.equal(await tid(m, 'composer').getAttribute('data-kind'), 'continue');
      const cont = await assertModelPopoverFits(h, m, 'session-model-picker-continue-' + tag + '-mobile');
      // New session: Goal and Plan chips push the model chip furthest right.
      await gotoApp(h, m, '/');
      await tid(m, 'goal-toggle').waitFor();
      const fresh = await assertModelPopoverFits(h, m, 'session-model-picker-new-' + tag + '-mobile');
      h.evidence['model-popover-390-' + tag] = { continue: cont, new: fresh };
    }, { page: m });
    await mobile.close();
  }
}
