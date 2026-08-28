import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { access, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { constants as fsConstants } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import process from 'node:process';
import { spawn } from 'node:child_process';
import net from 'node:net';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '..', '..');
const runStamp = new Date().toISOString().replace(/[:.]/g, '-');
const outputArg = process.argv.find((arg) => arg.startsWith('--output='));
const outputDir = path.resolve(repoRoot, outputArg ? outputArg.slice('--output='.length) : `validation/runs/webconsole-e2e-${runStamp}`);
const keepRuntime = process.argv.includes('--keep-runtime');
const runtimeRoot = await mkdtemp(path.join(tmpdir(), 'aegis-web-e2e.'));
const workspaceRoot = path.join(runtimeRoot, 'workspace');
const sessionRoot = path.join(runtimeRoot, 'sessions');
const skillsRoot = path.join(workspaceRoot, 'skills');
const binaryPath = path.join(runtimeRoot, 'aegis-agent');
const providerBinaryPath = path.join(runtimeRoot, 'budgetsmokeprovider');
const configPath = path.join(runtimeRoot, 'config.yaml');
const uploadPath = path.join(runtimeRoot, 'upload-note.txt');
const skillZipPath = path.join(runtimeRoot, 'e2e-skill.zip');
const processLogs = [];
const children = [];
const screenshots = [];
const checks = [];
const untranslatedByScreenshot = [];
const unexpectedChineseByScreenshot = [];
const browserErrors = { console: [], page: [], request: [], response: [] };

await mkdir(outputDir, { recursive: true });
await mkdir(workspaceRoot, { recursive: true });
await mkdir(skillsRoot, { recursive: true });
await writeFile(uploadPath, 'workspace upload from browser e2e\n', { mode: 0o600 });
await writeStoredZip(skillZipPath, {
  'e2e-skill/SKILL.md': '---\nname: e2e-skill\ndescription: Browser E2E managed skill\n---\n\nUse this fixture only for browser validation.\n'
});

let browser;
try {
  await runCommand(process.env.GO || 'go', ['build', '-o', binaryPath, './cmd/aegis-agent'], { cwd: repoRoot, label: 'build agent' });
  await runCommand(process.env.GO || 'go', ['build', '-o', providerBinaryPath, './validation/cmd/budgetsmokeprovider'], { cwd: repoRoot, label: 'build provider fixture' });

  const providerPort = await reservePort();
  const webPort = await reservePort();
  const providerURL = `http://127.0.0.1:${providerPort}`;
  const baseURL = `http://127.0.0.1:${webPort}`;
  await writeFile(configPath, renderConfig({ providerURL, sessionRoot, skillsRoot }), { mode: 0o600 });

  children.push(startProcess(providerBinaryPath, ['--listen', `127.0.0.1:${providerPort}`], { cwd: workspaceRoot, label: 'provider' }));
  await waitHTTP(`${providerURL}/healthz`, 15_000);
  children.push(startProcess(binaryPath, ['web', '--config', configPath, '--listen', `127.0.0.1:${webPort}`, '--workers', '0'], {
    cwd: workspaceRoot,
    label: 'web',
    env: { ...process.env, E2E_API_KEY: 'local-fixture-key' }
  }));
  await waitHTTP(baseURL, 20_000);

  const executablePath = await findChrome();
  browser = await chromium.launch({
    executablePath,
    headless: true,
    args: typeof process.getuid === 'function' && process.getuid() === 0 ? ['--no-sandbox'] : []
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    colorScheme: 'dark',
    acceptDownloads: true
  });
  const page = await context.newPage();
  installBrowserErrorCollection(page);

  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');
  await check('default locale is zh-CN', async () => {
    assert.equal(await page.locator('html').getAttribute('lang'), 'zh-CN');
    await assertText(page.locator('[data-view="settings"] span'), '设置');
    await assertText(page.locator('[data-view="workspace"] span'), '工作区');
  });
  await check('light theme ignores requested dark OS theme', async () => {
    const theme = await page.evaluate(() => ({
      colorScheme: getComputedStyle(document.documentElement).colorScheme,
      body: getComputedStyle(document.body).backgroundColor,
      surface: getComputedStyle(document.querySelector('.main-content')).backgroundColor
    }));
    assert.equal(theme.colorScheme, 'light');
    assert.notEqual(theme.body, 'rgb(0, 0, 0)');
    assert.match(theme.surface, /rgba?\(/);
  });
  await check('all visible v2 button targets are at least 44px', () => assertMinimumTargets(page));
  await capture(page, '01-session-default-zh-desktop.png');

  await page.locator('#language-toggle-btn').click();
  await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
  await page.reload({ waitUntil: 'networkidle' });
  await check('English locale persists across reload', async () => {
    assert.equal(await page.locator('html').getAttribute('lang'), 'en');
    await assertText(page.locator('[data-view="settings"] span'), 'Settings');
  });
  await capture(page, '02-session-default-en-desktop.png');
  await page.locator('#language-toggle-btn').click();
  await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');

  const mainID = await startSession(page, 'E2E_UI_MAIN raw user text: Settings must remain unchanged.');
  await waitForSession(baseURL, mainID, (detail) => detail?.state?.status === 'completed', 30_000);
  await writeEmptyBackgroundNotification(mainID);
  await check('empty background notification is loaded from the durable session store', async () => {
    const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(mainID)}`);
    assert.equal(detail.background_notifications?.some((item) => item.id === 'notification_e2e_empty'), true);
  });
  await openSession(page, mainID);
  await check('raw user content is not translated', async () => {
    await page.locator('.message.user .message-bubble').filter({ hasText: 'Settings must remain unchanged' }).waitFor();
  });
  await openInspectorTab(page, 'tasks');
  await check('Todo and derived task groups render from durable facts', async () => {
	for (const group of ['in_progress', 'ready', 'blocked', 'completed', 'cancelled']) {
	  await page.locator(`[data-task-group="${group}"]`).waitFor();
	}
	for (const taskID of ['task_0001', 'task_0002', 'task_0003', 'task_0004', 'task_0005']) {
	  await page.locator(`[data-task-id="${taskID}"]`).waitFor();
	}
    assert.match(await page.locator('.metric-card').filter({ hasText: '待办' }).innerText(), /2 个进行中/);
    assert.equal(await page.locator('[data-inspector-tab="agents"]').count(), 0);
  });
  await check('inspector tabs support Arrow, Home, and End keyboard navigation', async () => {
    const tasksTab = page.locator('[data-inspector-tab="tasks"]');
	await tasksTab.press('ArrowRight');
    const contextTab = page.locator('[data-inspector-tab="context"][aria-selected="true"]');
    await contextTab.waitFor();
	await page.waitForFunction(() => {
	  const selected = document.querySelector('[data-inspector-tab="context"][aria-selected="true"]');
	  const panel = document.querySelector('.inspector-content');
	  return selected && !String(panel?.textContent || '').includes('Loading context');
  });
	await page.locator('[data-inspector-tab="context"][aria-selected="true"]').press('End');
    const timelineTab = page.locator('[data-inspector-tab="timeline"][aria-selected="true"]');
    await timelineTab.waitFor();
	await timelineTab.press('Home');
    await page.locator('[data-inspector-tab="summary"][aria-selected="true"]').waitFor();
    await tasksTab.click();
  });
	await openInspectorTab(page, 'context');
	await check('Chinese Context inspector localizes operator copy and preserves durable IDs', async () => {
	  const panel = page.locator('.context-panel');
	  await panel.getByText('根会话峰值', { exact: true }).waitFor();
	  await panel.getByText('提供商用量', { exact: true }).waitFor();
	  await panel.getByText('会话链路', { exact: true }).waitFor();
	  assert.equal(await panel.getByText('Root peak', { exact: true }).count(), 0);
	  assert.equal(await panel.locator('h4').getAttribute('translate'), 'no');
	});
	await capture(page, '03c-session-context-zh-desktop.png');
	await page.evaluate(() => closeInspectorSlideOut({ restoreFocus: false }));
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
	await openInspectorTab(page, 'context');
	await check('English Context inspector restores every operator label', async () => {
	  const panel = page.locator('.context-panel');
	  await panel.getByText('Root peak', { exact: true }).waitFor();
	  await panel.getByText('Provider usage', { exact: true }).waitFor();
	  await panel.getByText('Lineage', { exact: true }).waitFor();
	  assert.equal(await panel.getByText('根会话峰值', { exact: true }).count(), 0);
	});
	await capture(page, '03d-session-context-en-desktop.png');
	await openInspectorTab(page, 'tasks');
	await check('English translates dynamic Todo and all task groups without changing durable data', async () => {
	  await assertText(page.locator('[data-view="settings"] span'), 'Settings');
	  await assertText(page.locator('[data-view="workspace"] span'), 'Workspace');
	  await assertText(page.locator('[data-task-group="in_progress"] .task-group-heading span').first(), 'In progress');
	  await assertText(page.locator('[data-task-group="ready"] .task-group-heading span').first(), 'Ready');
	  await assertText(page.locator('[data-task-group="blocked"] .task-group-heading span').first(), 'Blocked');
	  await assertText(page.locator('[data-task-group="completed"] .task-group-heading span').first(), 'Completed');
	  await assertText(page.locator('[data-task-group="cancelled"] .task-group-heading span').first(), 'Cancelled');
	  await page.locator('.todo-card-title').filter({ hasText: 'Inspect bilingual Web Console' }).waitFor();
	  const collisionTask = page.locator('[data-task-id="task_0001"]');
	  await assertText(collisionTask.locator('.task-card-copy'), 'No description.');
	  const owner = collisionTask.locator('.task-chip').filter({ hasText: /^Settings$/ });
	  await assertText(owner, 'Settings');
	  assert.equal(await owner.getAttribute('translate'), 'no');
	});
	await capture(page, '03b-session-task-todo-en-desktop.png');
	await page.evaluate(() => closeInspectorSlideOut({ restoreFocus: false }));
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');
	await openInspectorTab(page, 'tasks');
  await check('Chinese Task fallbacks localize without translating owner facts', async () => {
    const collisionTask = page.locator('[data-task-id="task_0001"]');
    await assertText(collisionTask.locator('.task-card-copy'), '无描述。');
    const owner = collisionTask.locator('.task-chip').filter({ hasText: /^Settings$/ });
    await assertText(owner, 'Settings');
    assert.equal(await owner.getAttribute('translate'), 'no');
  });
  await capture(page, '03-session-task-todo-zh-desktop.png');
  await openInspectorTab(page, 'timeline');
  await check('timeline and tool lanes are visible', async () => {
    assert.ok(await page.locator('.timeline-card, .timeline-item').count() > 0);
    assert.ok(await page.locator('.tool-lane, .tool-card').count() > 0);
  });

  const goalID = await startSession(page, 'E2E_UI_GOAL complete the browser goal.', { goal: true });
  await waitForSession(baseURL, goalID, (detail) => detail?.state?.status === 'completed' && detail?.goal?.status === 'complete', 25_000);
  await openSession(page, goalID);
  await openInspectorTab(page, 'goal');
  await check('Goal creation and completion are visible', async () => {
    await page.locator('.goal-panel').waitFor();
    assert.match(await page.locator('.goal-panel').innerText(), /E2E_UI_GOAL/);
  });
  await capture(page, '04-goal-complete-zh-desktop.png');

  const planID = await startSession(page, 'E2E_UI_PLAN exercise real approval.', { plan: true });
  await waitForSession(baseURL, planID, (detail) => detail?.plan_mode?.status === 'awaiting_approval', 25_000);
  await openSession(page, planID);
  await openInspectorTab(page, 'plan');
  const approvePlanButton = page.locator('#inspector-slide-out [data-plan-action="approve"]');
  await approvePlanButton.waitFor();
  const composerApprovePlanButton = page.locator('.plan-action-btn.primary[data-plan-action="approve"]');
  await composerApprovePlanButton.waitFor();
  await check('Plan primary action uses the v2 lime palette', () => assertV2PrimaryAction(composerApprovePlanButton));
  await capture(page, '05-plan-awaiting-approval-zh-desktop.png');
  await approvePlanButton.click();
  await waitForSession(baseURL, planID, (detail) => detail?.state?.status === 'completed', 25_000);
  await check('Plan approval resumes and completes the session', async () => {
    const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(planID)}`);
    assert.equal(detail.plan_mode.status, 'executing');
  });

	const revisedPlanID = await startSession(page, 'E2E_UI_PLAN_REVISE exercise a real browser revision.', { plan: true });
	await waitForSession(baseURL, revisedPlanID, (detail) => detail?.plan_mode?.status === 'awaiting_approval' && detail?.plan_mode?.plan_version === 1, 25_000);
	await openSession(page, revisedPlanID);
	await openInspectorTab(page, 'plan');
	await page.locator('#inspector-slide-out [data-plan-action="revise"]').click();
	await page.locator('#chat-input').fill('Use the narrower verified scope.');
	const reviseResponse = page.waitForResponse((response) => response.request().method() === 'POST' && response.url().includes(`/api/sessions/${revisedPlanID}/`));
	await page.locator('#send-btn').click();
	const revised = await reviseResponse;
	assert.ok(revised.url().endsWith(`/api/sessions/${revisedPlanID}/planmode/revise`), `unexpected Plan revision route: ${revised.url()}`);
	assert.equal(revised.status(), 202);
	await waitForSession(baseURL, revisedPlanID, (detail) => detail?.plan_mode?.status === 'awaiting_approval' && detail?.plan_mode?.plan_version === 2, 25_000);
	await openSession(page, revisedPlanID);
	await openInspectorTab(page, 'plan');
	await check('Plan revision creates a second durable version before approval', async () => {
	  const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(revisedPlanID)}`);
	  assert.equal(detail.plan_mode.plan_version, 2);
	  assert.match(detail.plan_mode.summary, /Revised plan accepted/);
	  await page.locator('.plan-panel').filter({ hasText: 'Revised plan' }).waitFor();
	});
	await capture(page, '05b-plan-revised-zh-desktop.png');
	await page.locator('#inspector-slide-out [data-plan-action="approve"]').click();
	await waitForSession(baseURL, revisedPlanID, (detail) => detail?.state?.status === 'completed', 25_000);

	const inputPlanID = await startSession(page, 'E2E_UI_PLAN_INPUT exercise request_user_input.', { plan: true });
	await waitForSession(baseURL, inputPlanID, (detail) => detail?.plan_mode?.status === 'awaiting_user_input' && detail?.plan_mode?.pending_request, 25_000);
	await openSession(page, inputPlanID);
	await openInspectorTab(page, 'plan');
	await page.locator('[data-plan-input-action="select"][data-value="Narrow (Recommended)"]').click();
	const planInputSubmit = page.locator('[data-plan-input-action="submit"]:not([disabled])');
	await planInputSubmit.waitFor();
	await capture(page, '05c-plan-input-zh-desktop.png');
	const inputResponse = page.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/sessions/${inputPlanID}/planmode/input`));
	await planInputSubmit.click();
	assert.equal((await inputResponse).status(), 202);
	await waitForSession(baseURL, inputPlanID, (detail) => detail?.plan_mode?.status === 'awaiting_approval' && detail?.plan_mode?.plan_version === 1, 25_000);
	await openSession(page, inputPlanID);
	await openInspectorTab(page, 'plan');
	await check('request_user_input answer resumes planning and produces an approvable plan', async () => {
	  const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(inputPlanID)}`);
	  assert.equal(detail.plan_mode.pending_request, undefined);
	  assert.match(detail.plan_mode.summary, /durable browser input/);
	});
	await page.locator('#inspector-slide-out [data-plan-action="approve"]').click();
	await waitForSession(baseURL, inputPlanID, (detail) => detail?.state?.status === 'completed', 25_000);

	const childrenID = await startSession(page, 'E2E_UI_CHILDREN spawn and expose a real child.');
	const childrenDetail = await waitForSession(baseURL, childrenID, (detail) => detail?.state?.status === 'completed' && detail?.children?.sessions?.length === 1, 30_000);
	const childID = childrenDetail.children.sessions[0].id;
	await openSession(page, childrenID);
	await check('child delegation is visible and its durable child session opens from v2', async () => {
	  const openChild = page.locator(`[data-sub-agent-open="${childID}"]`).first();
	  await openChild.waitFor();
	  await openChild.click();
	  await confirmDialog(page);
	  await page.waitForFunction((id) => typeof state !== 'undefined' && state.sessionId === id, childID);
	  const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(childID)}`);
	  assert.equal(detail.metadata.parent_session_id, childrenID);
	});
	await openSession(page, childrenID);
	await openInspectorTab(page, 'timeline');
	await capture(page, '05d-child-delegation-zh-desktop.png');

  const awaitID = await startSession(page, 'E2E_UI_AWAIT park until browser continues.');
  await waitForSession(baseURL, awaitID, (detail) => detail?.state?.status === 'awaiting_input', 25_000);
  await openSession(page, awaitID);
  await page.locator('#chat-input').fill('Continue from the browser.');
  const continueResponse = page.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/sessions/${awaitID}/continue`));
  await page.locator('#send-btn').click();
  assert.equal((await continueResponse).status(), 202);
  await waitForSession(baseURL, awaitID, (detail) => detail?.state?.status === 'completed', 25_000);
  await check('continue action completes an awaiting-input session', async () => {
    const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(awaitID)}`);
    assert.equal(detail.state.status, 'completed');
  });

  const steerID = await startSession(page, 'E2E_UI_SLOW accept a live steer.');
  await page.locator('#chat-input').fill('Live steer from browser.');
  const steerResponse = page.waitForResponse((response) => response.request().method() === 'POST' && response.url().includes(`/api/sessions/${steerID}/steer`));
  await page.locator('#send-btn').click();
  assert.equal((await steerResponse).status(), 202);
  await waitForSession(baseURL, steerID, (detail) => detail?.state?.status === 'completed', 25_000);
  await check('running steer is accepted and consumed', async () => {
    const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(steerID)}`);
    assert.ok((detail.timeline || []).some((item) => String(item.event_type || '').includes('steer')));
  });

  const interruptID = await startSession(page, 'E2E_UI_SLOW interrupt this run.');
  await page.locator('#interrupt-session-btn:not([disabled])').waitFor();
  await page.locator('#interrupt-session-btn').click();
  await waitForSession(baseURL, interruptID, isSettledOrPaused, 20_000);
  await check('interrupt settles or pauses the owned session', async () => {
    const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(interruptID)}`);
    assert.notEqual(detail.state.status, 'running');
  });

  const stopID = await startSession(page, 'E2E_UI_SLOW stop this run.');
  await page.locator('#stop-session-btn:not([disabled])').waitFor();
  await page.locator('#stop-session-btn').click();
  await waitForSession(baseURL, stopID, isSettledOrPaused, 20_000);
  await check('stop settles or pauses the owned session', async () => {
    const detail = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(stopID)}`);
    assert.notEqual(detail.state.status, 'running');
  });

  await page.locator('[data-view="workspace"]').click();
  await page.locator('#workspace-view:not(.is-hidden) .file-tree').waitFor();
  await page.locator('.tree-node[data-path="e2e-created.txt"]').click();
  await check('workspace preview preserves exact file content', async () => {
    await page.locator('.workspace-preview-content').waitFor();
    assert.equal((await page.locator('.workspace-preview-content').innerText()).trim(), 'e2e workspace artifact');
  });
  await capture(page, '06-workspace-preview-zh-desktop.png');
  const download = page.waitForEvent('download');
  await page.locator('#workspace-download-btn').click();
  const downloaded = await download;
  const downloadPath = path.join(runtimeRoot, 'downloaded-e2e-created.txt');
  await downloaded.saveAs(downloadPath);
  assert.equal((await readFile(downloadPath, 'utf8')).trim(), 'e2e workspace artifact');

	await page.locator('#workspace-new-folder-btn').focus();
	await page.locator('#workspace-new-folder-btn').click();
	await completePromptDialog(page, 'e2e-folder');
  await page.locator('.tree-node[data-path="e2e-folder"]').waitFor();
	await page.waitForFunction(() => document.activeElement?.id === 'workspace-new-folder-btn');
  await page.locator('#workspace-upload-input').setInputFiles(uploadPath);
  await page.locator('.tree-node[data-path="upload-note.txt"]').waitFor();
  await page.locator('.tree-node[data-path="upload-note.txt"]').click();
	await page.locator('#workspace-rename-btn').focus();
	await page.locator('#workspace-rename-btn').click();
	await completePromptDialog(page, 'e2e-upload-renamed.txt');
  await page.locator('.tree-node[data-path="e2e-upload-renamed.txt"]').waitFor();
	await page.waitForFunction(() => document.activeElement?.id === 'workspace-rename-btn');
	await page.locator('#workspace-delete-file-btn').focus();
	await page.locator('#workspace-delete-file-btn').click();
	await confirmDialog(page);
  await page.locator('.tree-node[data-path="e2e-upload-renamed.txt"]').waitFor({ state: 'detached' });
	await page.waitForFunction(() => document.activeElement?.id === 'workspace-refresh-btn');
  await page.locator('.tree-node[data-path="e2e-folder"]').click();
	await page.locator('#workspace-delete-dir-btn').focus();
	await page.locator('#workspace-delete-dir-btn').click();
	await confirmDialog(page);
	await page.locator('.tree-node[data-path="e2e-folder"]').waitFor({ state: 'detached' });
	await page.waitForFunction(() => document.activeElement?.id === 'workspace-refresh-btn');
  await check('workspace create/upload/rename/delete interactions complete', async () => {
    const listing = await getJSON(`${baseURL}/api/files?path=.`);
    const names = listing.map((item) => item.name);
    assert.ok(!names.includes('e2e-folder'));
    assert.ok(!names.includes('e2e-upload-renamed.txt'));
  });
  await capture(page, '07-workspace-zh-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
	await check('Workspace switches existing controls to English', async () => {
	  await assertText(page.locator('#workspace-view .view-title'), 'Workspace');
	  assert.equal(await page.locator('#workspace-new-folder-btn').getAttribute('aria-label'), 'New folder');
	  assert.equal(await page.locator('#workspace-refresh-btn').getAttribute('title'), 'Refresh');
	});
	await capture(page, '07b-workspace-en-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');

  await page.locator('[data-view="skills"]').click();
  await page.locator('#skills-view:not(.is-hidden)').waitFor();
  await check('Skill upload primary action uses the v2 lime palette', () => assertV2PrimaryAction(page.locator('#skill-upload-btn')));
  await page.locator('#skill-upload').setInputFiles(skillZipPath);
  await page.locator('.skill-card').filter({ hasText: 'e2e-skill' }).waitFor();
  const skillCard = page.locator('.skill-card').filter({ hasText: 'e2e-skill' });
	await check('Chinese Skill author prefix localizes while the author fact stays verbatim', async () => {
	  await assertText(skillCard.locator('.skill-author').first(), '作者 Local');
	  const author = skillCard.locator('.skill-author [data-i18n-skip]').first();
	  await assertText(author, 'Local');
	  assert.equal(await author.getAttribute('translate'), 'no');
	});
  await capture(page, '08-skills-installed-zh-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
	await check('Skills switches existing controls to English', async () => {
	  await assertText(page.locator('#skills-view .view-title'), 'Skills');
	  await assertText(page.locator('#skill-upload-btn'), 'Upload .zip');
	  await assertText(skillCard.locator('[data-skill-action]'), 'Uninstall');
	  await assertText(skillCard.locator('.skill-author').first(), 'by Local');
	});
	await capture(page, '08b-skills-installed-en-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');
  await skillCard.locator('[data-skill-action]').click();
  await confirmDialog(page);
  await skillCard.waitFor({ state: 'detached' });
  await check('skill zip upload and uninstall use the real service', async () => {
    const skills = await getJSON(`${baseURL}/api/skills`);
    assert.ok(!skills.some((item) => item.id === 'e2e-skill'));
  });

  await page.locator('[data-view="settings"]').click();
  await page.locator('#settings-save-btn').waitFor();
  await check('Settings save primary action uses the v2 lime palette', () => assertV2PrimaryAction(page.locator('#settings-save-btn')));
  const probeResponse = page.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith('/api/config/test'));
  await page.locator('#settings-test-btn').click();
  assert.equal((await probeResponse).status(), 200);
  await page.locator('#settings-test-btn:not([disabled])').waitFor({ timeout: 20_000 });
  await check('Settings provider probe succeeds against local fixture', async () => {
    assert.match(await page.locator('#toast-rack').innerText(), /测试通过|passed/);
  });
  await page.locator('#settings-enable-legacy-ui').check();
  await page.locator('#settings-save-btn').click();
  if (await page.locator('.confirm-dialog').count()) await confirmDialog(page);
  await page.waitForTimeout(250);
  assert.equal((await fetch(`${baseURL}/legacy/`)).status, 200);
  const legacyPage = await context.newPage();
  installBrowserErrorCollection(legacyPage);
  await legacyPage.goto(`${baseURL}/legacy/`, { waitUntil: 'networkidle' });
  await legacyPage.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');
  await check('legacy rollback route reuses the shared durable service and remains distinguishable from v2', async () => {
    assert.equal(await legacyPage.locator('html').getAttribute('data-ui'), null);
    assert.equal(await legacyPage.locator('#v2-new-session-btn').count(), 0);
    await legacyPage.locator('#new-session-btn').waitFor();
    await legacyPage.locator('[data-view="chat"]').click();
    await legacyPage.locator('#chat-input').waitFor();
	});
	await openSession(legacyPage, mainID);
	await openInspectorTab(legacyPage, 'summary');
	const emptyNotificationCard = legacyPage.locator('.notification-card').filter({ hasText: 'Settings · evaluator' }).first();
	await check('Chinese empty notification fallback localizes and preserves agent identity', async () => {
	  await assertText(emptyNotificationCard.locator('.notification-copy'), '未记录最终文本。');
	  await assertText(emptyNotificationCard.locator('.job-card-title'), 'Settings · evaluator');
	  await assertText(emptyNotificationCard.locator('.job-card-meta'), 'job_e2e_empty · accepted · 会话 已完成');
	  assert.equal(await emptyNotificationCard.locator('.job-card-title [data-i18n-skip]').getAttribute('translate'), 'no');
	});
	await captureElement(legacyPage, emptyNotificationCard, '11a-legacy-notification-zh-desktop.png');
	await legacyPage.evaluate(() => closeInspectorSlideOut({ restoreFocus: false }));
	await legacyPage.locator('#language-toggle-btn').click();
	await legacyPage.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
	await openInspectorTab(legacyPage, 'summary');
	await check('English empty notification fallback restores without rewriting durable facts', async () => {
	  await assertText(emptyNotificationCard.locator('.notification-copy'), 'No final text recorded.');
	  await assertText(emptyNotificationCard.locator('.job-card-title'), 'Settings · evaluator');
	  await assertText(emptyNotificationCard.locator('.job-card-meta'), 'job_e2e_empty · accepted · session Completed');
	});
	await captureElement(legacyPage, emptyNotificationCard, '11b-legacy-notification-en-desktop.png');
	await legacyPage.evaluate(() => closeInspectorSlideOut({ restoreFocus: false }));
	await legacyPage.locator('#language-toggle-btn').click();
	await legacyPage.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');
	await legacyPage.locator('#new-session-btn').click();
	await legacyPage.locator('#chat-input').waitFor();
  await capture(legacyPage, '11-legacy-rollback-zh-desktop.png');
  await legacyPage.close();
  await page.locator('#settings-enable-legacy-ui').uncheck();
  await page.locator('#settings-save-btn').click();
  if (await page.locator('.confirm-dialog').count()) await confirmDialog(page);
  await page.locator('#settings-view').evaluate((element) => { element.scrollTop = 0; });
  await capture(page, '09-settings-top-zh-desktop.png');
  await page.locator('#settings-save-btn').scrollIntoViewIfNeeded();
  await capture(page, '10-settings-bottom-zh-desktop.png');
	await page.locator('#settings-view').evaluate((element) => { element.scrollTop = 0; });
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
	await check('Settings switches existing form controls to English', async () => {
	  await assertText(page.locator('#settings-view .view-title'), 'Settings');
	  await assertText(page.locator('#settings-save-btn'), 'Save Changes');
	  await page.locator('#settings-view').getByText('Frontend Migration', { exact: true }).waitFor();
	});
	await capture(page, '10b-settings-en-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');

  await page.locator('[data-view="history"]').click();
  await page.locator('#history-view:not(.is-hidden) [data-delete-session]').first().waitFor();
  await check('Sessions translates its fallback while preserving custom agent identity facts', async () => {
    await assertText(page.locator('#history-view .history-session-fallback').first(), '主会话');
    const customAgent = page.locator('#history-view .history-session-agent-label').filter({ hasText: 'e2e-child · evaluator' }).first();
    assert.equal((await customAgent.textContent())?.trim(), 'e2e-child · evaluator');
    assert.equal(await customAgent.getAttribute('translate'), 'no');
  });
  await capture(page, '12-sessions-zh-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
	await check('Sessions switches existing rows and relative times to English', async () => {
	  await assertText(page.locator('#history-view .view-header .view-title'), 'Sessions');
	  await assertText(page.locator('#history-view .history-session-fallback').first(), 'Master session');
	  assert.match(await page.locator('#history-view .history-session-time').first().innerText(), /^(?:just now|\d+m ago)$/);
	  assert.equal(await page.locator('#history-view').getByText('刚刚', { exact: true }).count(), 0);
	});
	await capture(page, '12b-sessions-en-desktop.png');
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');

  await openSession(page, mainID);
  await page.setViewportSize({ width: 390, height: 844 });
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'en');
  await openInspectorTab(page, 'tasks');
  const slide = page.locator('#inspector-slide-out');
	await check('English mobile dynamic inspector stays translated and exposes no Background tab', async () => {
    await slide.waitFor();
    assert.equal(await slide.getAttribute('aria-hidden'), 'false');
    assert.equal(await page.locator('[data-inspector-tab="agents"]').count(), 0);
    assert.equal(await page.locator('#app').getAttribute('inert'), '');
	  await assertText(page.locator('[data-task-group="in_progress"] .task-group-heading span').first(), 'In progress');
	});
	await capture(page, '13b-inspector-open-en-mobile.png');
	await slide.evaluate((element) => { element.scrollTop = Math.min(240, element.scrollHeight - element.clientHeight); });
	assert.ok(await slide.evaluate((element) => element.scrollTop > 0));
	await page.keyboard.press('Escape');
	assert.equal(await slide.getAttribute('aria-hidden'), 'true');
	assert.equal(await page.locator('#inspector-toggle-btn').evaluate((node) => document.activeElement === node), true);
	await page.locator('#language-toggle-btn').click();
	await page.waitForFunction(() => window.AegisI18n?.locale?.() === 'zh-CN');
	await openInspectorTab(page, 'tasks');
	await check('mobile inspector traps focus, inerts the background, and restores its opener', async () => {
	  assert.equal(await slide.getAttribute('aria-hidden'), 'false');
	  assert.equal(await page.locator('#app').getAttribute('inert'), '');
	  assert.equal(await slide.evaluate((element) => element.scrollTop), 0);
	  assert.ok((await slide.locator('.inspector-header').boundingBox())?.y >= 0);
	});
  await capture(page, '13-inspector-open-zh-mobile.png');
  await page.keyboard.press('Escape');
  assert.equal(await slide.getAttribute('aria-hidden'), 'true');
	assert.equal(await page.locator('#inspector-toggle-btn').evaluate((node) => document.activeElement === node), true);
  await capture(page, '14-session-zh-mobile.png');
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.locator('[data-view="history"]').click();
  await page.locator('#history-view:not(.is-hidden) [data-delete-session]').first().waitFor();

  await page.locator(`[data-delete-session="${awaitID}"]`).click();
  await confirmDialog(page);
  await page.locator(`[data-delete-session="${awaitID}"]`).waitFor({ state: 'detached' });
  await check('single-session delete removes its durable tree', async () => {
    assert.equal((await fetch(`${baseURL}/api/sessions/${encodeURIComponent(awaitID)}`)).status, 404);
  });

  await page.locator('[data-history-clear]').click();
  await confirmDialog(page);
  await page.locator('.empty-panel').filter({ hasText: /暂无会话|No sessions/ }).first().waitFor({ timeout: 15_000 });
  await check('clear history succeeds only after all runs settle', async () => {
    const history = await getJSON(`${baseURL}/api/history?page=1&page_size=8`);
    assert.equal(history.total, 0);
  });

  const untranslated = Array.from(new Set(untranslatedByScreenshot.flatMap((entry) => entry.values))).sort();
  assert.deepEqual(untranslated, [], `untranslated operator text: ${JSON.stringify(untranslated)}`);
  checks.push({ name: 'untranslated operator text audit', ok: true, detail: untranslated });
  const unexpectedChinese = Array.from(new Set(unexpectedChineseByScreenshot.flatMap((entry) => entry.values))).sort();
  assert.deepEqual(unexpectedChinese, [], `Chinese operator text in English locale: ${JSON.stringify(unexpectedChinese)}`);
  checks.push({ name: 'English locale operator text audit', ok: true, detail: unexpectedChinese });
  assertBrowserErrors();

  const manifest = {
    schema_version: 1,
    commit: await gitHead(),
    created_at: new Date().toISOString(),
    base_url: baseURL,
    browser: await browser.version(),
    executable_path: executablePath,
    locales: ['zh-CN', 'en'],
    viewports: [{ width: 1440, height: 1000 }, { width: 390, height: 844 }],
    checks,
    screenshots,
    browser_errors: browserErrors,
    process_logs: processLogs
  };
  await writeFile(path.join(outputDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', { mode: 0o600 });
  process.stdout.write(`Web Console E2E passed: ${outputDir}\n`);
} finally {
  if (browser) await browser.close().catch(() => {});
  for (const child of children.reverse()) await stopProcess(child);
  if (!keepRuntime) await rm(runtimeRoot, { recursive: true, force: true });
}

async function check(name, fn) {
  await fn();
  checks.push({ name, ok: true });
}

async function writeEmptyBackgroundNotification(sessionID) {
  const controlDir = path.join(sessionRoot, sessionID, 'control');
  await mkdir(controlDir, { recursive: true, mode: 0o700 });
  const notification = {
    id: 'notification_e2e_empty',
    created_at: new Date().toISOString(),
    source: 'queue',
    queue_job_id: 'job_e2e_empty',
    agent_name: 'Settings',
    agent_role: 'evaluator',
    status: 'completed',
    session_status: 'completed',
    delivery_status: 'accepted'
  };
  await writeFile(
    path.join(controlDir, 'background.jsonl'),
    `${JSON.stringify(notification)}\n`,
    { mode: 0o600 }
  );
}

async function startSession(page, prompt, options = {}) {
  if ((await page.locator('#inspector-slide-out').getAttribute('aria-hidden')) === 'false') {
    await page.evaluate(() => closeInspectorSlideOut({ restoreFocus: false }));
  }
  await page.locator('#v2-new-session-btn').click();
  if (options.goal) await page.locator('#goal-toggle-btn').click();
  if (options.plan) await page.locator('#plan-toggle-btn').click();
  await page.locator('#chat-input').fill(prompt);
  const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith('/api/sessions/start'));
  await page.locator('#send-btn').click();
  const response = await responsePromise;
  assert.equal(response.status(), 202, await response.text());
  const payload = await response.json();
  assert.ok(payload.session_id);
	await page.waitForFunction((id) => typeof state !== 'undefined' && state.sessionId === id, payload.session_id);
  return payload.session_id;
}

async function openSession(page, sessionID) {
  await page.evaluate(async (id) => { await openSession(id, { switchToChat: true }); }, sessionID);
	await page.waitForFunction((id) => typeof state !== 'undefined' && state.sessionId === id, sessionID);
}

async function openInspectorTab(page, tab) {
  if ((await page.locator('#inspector-slide-out').getAttribute('aria-hidden')) !== 'false') {
    await page.locator('#inspector-toggle-btn').click();
  }
  const selectedTab = page.locator(`[data-inspector-tab="${tab}"][aria-selected="true"]`);
  if (await selectedTab.count() === 0) {
    await page.locator(`[data-inspector-tab="${tab}"]`).click();
  }
  await selectedTab.waitFor();
}

async function confirmDialog(page, restoreSelector = '') {
  const dialog = page.locator('.confirm-dialog');
  await dialog.waitFor();
  const first = dialog.locator('button').first();
  const last = dialog.locator('button').last();
  await last.focus();
  await page.keyboard.press('Tab');
  assert.equal(await first.evaluate((node) => document.activeElement === node), true);
  await dialog.locator('.confirm-dialog-confirm').click();
  await dialog.waitFor({ state: 'detached' });
	if (restoreSelector) {
	  assert.equal(await page.locator(restoreSelector).evaluate((node) => document.activeElement === node), true);
	}
}

async function completePromptDialog(page, value) {
  const dialog = page.locator('.prompt-dialog');
  await dialog.waitFor();
	const input = dialog.locator('.prompt-dialog-input');
	await dialog.locator('.confirm-dialog-confirm').focus();
	await page.keyboard.press('Tab');
	assert.equal(await input.evaluate((node) => document.activeElement === node), true);
	await input.fill(value);
  await dialog.locator('.confirm-dialog-confirm').click();
  await dialog.waitFor({ state: 'detached' });
}

async function assertMinimumTargets(page) {
  const tooSmall = await page.evaluate(() => Array.from(document.querySelectorAll('button, [role="button"]'))
    .filter((node) => {
      const style = getComputedStyle(node);
      const rect = node.getBoundingClientRect();
      return style.display !== 'none' && style.visibility !== 'hidden' && rect.width > 0 && rect.height > 0 && !node.disabled;
    })
    .map((node) => {
      const rect = node.getBoundingClientRect();
      return { id: node.id, className: node.className, label: node.getAttribute('aria-label') || node.textContent.trim(), width: rect.width, height: rect.height };
    })
    .filter((item) => item.width < 43.5 || item.height < 43.5));
  assert.deepEqual(tooSmall, []);
}

async function assertV2PrimaryAction(locator) {
  const style = await locator.evaluate((element) => ({
    backgroundColor: getComputedStyle(element).backgroundColor,
    color: getComputedStyle(element).color
  }));
  assert.equal(style.backgroundColor, 'rgb(182, 232, 88)');
  assert.equal(style.color, 'rgb(16, 19, 11)');
}

async function collectUntranslatedOperatorText(page) {
  return page.evaluate(() => {
	const skip = '[translate="no"], [data-i18n-skip], [data-i18n-control], pre, code, .message-bubble, .thinking-body, .tool-output-block, .tool-json-block, .tl-name, .tl-id-chip, .tl-body, .timeline-card-data, .agent-result-copy, .agent-card-title, .agent-card-copy, .agent-card-meta, .sa-tree-label, .sa-tree-meta, .session-rail-meta, .session-rail-id, .tf-row-label, .tf-file-path, .goal-objective, .goal-raw, .workspace-preview-content, .path-pill, .tiny-code-chip';
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const values = new Set();
    const allowedTechnicalText = (value) => ['Aegis', 'Google Gemini', 'Token'].includes(value)
      || /^ID[：:].+/.test(value)
      || /^Token \d+(?:\.\d+)?$/.test(value)
      || /^task_\d+$/.test(value)
	  || /^\d{6,}…[0-9a-f]{4}$/i.test(value);
    let node;
    while ((node = walker.nextNode())) {
      const parent = node.parentElement;
      const text = node.nodeValue.trim();
      if (!text || parent?.closest(skip) || !parent?.getClientRects?.().length || getComputedStyle(parent).display === 'none') continue;
      if (/\p{Script=Han}/u.test(text)) continue;
      if (/[A-Za-z]{2,}/.test(text) && !allowedTechnicalText(text)) values.add(text);
    }
    for (const element of document.querySelectorAll('[aria-label], [title], [placeholder], [data-tooltip]')) {
      if (element.closest(skip) || !element.getClientRects().length || getComputedStyle(element).display === 'none') continue;
      for (const name of ['aria-label', 'title', 'placeholder', 'data-tooltip']) {
        const value = String(element.getAttribute(name) || '').trim();
        if (!value || /\p{Script=Han}/u.test(value) || !/[A-Za-z]{2,}/.test(value)) continue;
        if (allowedTechnicalText(value)) continue;
        values.add(`${name}: ${value}`);
      }
    }
    return Array.from(values).sort();
  });
}

async function collectUnexpectedChineseOperatorText(page) {
  return page.evaluate(() => {
	const skip = '[translate="no"], [data-i18n-skip], [data-i18n-control], pre, code, .message-bubble, .thinking-body, .tool-output-block, .tool-json-block, .tl-name, .tl-id-chip, .tl-body, .timeline-card-data, .agent-result-copy, .agent-card-title, .agent-card-copy, .agent-card-meta, .sa-tree-label, .sa-tree-meta, .session-rail-meta, .session-rail-id, .tf-row-label, .tf-file-path, .goal-objective, .goal-raw, .workspace-preview-content, .path-pill, .tiny-code-chip';
    const values = new Set();
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      const parent = node.parentElement;
      const text = node.nodeValue.trim();
      if (!text || parent?.closest(skip) || !parent?.getClientRects?.().length || getComputedStyle(parent).display === 'none') continue;
      if (/\p{Script=Han}/u.test(text)) values.add(text);
    }
    for (const element of document.querySelectorAll('[aria-label], [title], [placeholder], [data-tooltip]')) {
      if (element.closest(skip) || !element.getClientRects().length || getComputedStyle(element).display === 'none') continue;
      for (const name of ['aria-label', 'title', 'placeholder', 'data-tooltip']) {
        const value = String(element.getAttribute(name) || '').trim();
        if (/\p{Script=Han}/u.test(value)) values.add(`${name}: ${value}`);
      }
    }
    return Array.from(values).sort();
  });
}

function isSettledOrPaused(detail) {
  return detail && ['paused', 'awaiting_input', 'completed', 'failed', 'cancelled'].includes(detail.state?.status);
}

async function waitForSession(baseURL, sessionID, predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await getJSON(`${baseURL}/api/sessions/${encodeURIComponent(sessionID)}`).catch(() => null);
    if (last && predicate(last)) return last;
    await delay(120);
  }
  throw new Error(`session ${sessionID} did not reach expected state: ${JSON.stringify(last?.state || last)}`);
}

async function assertText(locator, expected) {
  assert.equal((await locator.innerText()).trim(), expected);
}

async function capture(page, name) {
  await page.locator('#toast-rack .toast').last().waitFor({ state: 'detached', timeout: 4_000 }).catch(() => {});
  await page.waitForTimeout(350);
  if ((await page.locator('html').getAttribute('lang')) === 'zh-CN') {
    untranslatedByScreenshot.push({ name, values: await collectUntranslatedOperatorText(page) });
  } else {
    unexpectedChineseByScreenshot.push({ name, values: await collectUnexpectedChineseOperatorText(page) });
  }
  if ((await page.locator('html').getAttribute('data-ui')) === 'aegis-v2') {
    await assertMinimumTargets(page);
    await assertNoHorizontalOverflow(page);
  }
  const target = path.join(outputDir, name);
  await page.screenshot({ path: target, fullPage: true });
  const data = await readFile(target);
  screenshots.push({ name, viewport: page.viewportSize(), locale: await page.evaluate(() => document.documentElement.lang), sha256: createHash('sha256').update(data).digest('hex'), bytes: data.length });
}

async function captureElement(page, locator, name) {
  await locator.waitFor();
  const target = path.join(outputDir, name);
  await locator.screenshot({ path: target });
  const data = await readFile(target);
  screenshots.push({
    name,
    viewport: page.viewportSize(),
    locale: await page.evaluate(() => document.documentElement.lang),
    sha256: createHash('sha256').update(data).digest('hex'),
    bytes: data.length
  });
}

async function assertNoHorizontalOverflow(page) {
  const overflow = await page.evaluate(() => ({
    document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    body: document.body.scrollWidth - document.body.clientWidth
  }));
  assert.ok(overflow.document <= 1 && overflow.body <= 1, `horizontal overflow: ${JSON.stringify(overflow)}`);
}

function installBrowserErrorCollection(page) {
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.console.push(message.text());
  });
  page.on('pageerror', (error) => browserErrors.page.push(String(error?.stack || error)));
  page.on('requestfailed', (request) => browserErrors.request.push(`${request.method()} ${request.url()} ${request.failure()?.errorText || ''}`));
  page.on('response', (response) => {
    if (response.status() >= 400) browserErrors.response.push(`${response.status()} ${response.request().method()} ${response.url()}`);
  });
}

function assertBrowserErrors() {
  assert.deepEqual(browserErrors.console, [], 'browser console errors');
  assert.deepEqual(browserErrors.page, [], 'browser page errors');
  assert.deepEqual(browserErrors.request, [], 'browser request failures');
  assert.deepEqual(browserErrors.response, [], 'browser HTTP failures');
}

function renderConfig({ providerURL, sessionRoot, skillsRoot }) {
  return `schema_version: 1
default_provider: e2e
providers:
  e2e:
    api_provider: openai-compatible
    api_key_env: E2E_API_KEY
    base_url: ${JSON.stringify(providerURL)}
    model: e2e-model
    request_timeout_sec: 30
    stream_idle_timeout_ms: 30000
    retry:
      max_attempts: 1
    max_output_tokens: 2048
    context_window_tokens: 200000
    store: false
web:
  legacy_ui_enabled: false
session:
  dir: ${JSON.stringify(sessionRoot)}
  dir_mode: "0700"
skills:
  dirs:
    - ${JSON.stringify(skillsRoot)}
runtime:
  guardrails_mode: yolo
  max_turns_soft: 24
  max_turns_hard: -1
  command_timeout_sec: 30
  exec_policy:
    mode: warn
  shell_env_allowlist: [PATH, HOME, LANG, TERM]
  compact:
    input_char_threshold: 0
    keep_recent_tool_results: 3
    keep_recent_tool_result_bytes: 65536
    hysteresis_delta_chars: 0
    keep_recent_messages: 0
    utilization_factor: 0.85
    semantic_summary:
      enabled: false
  queue:
    auto_worker: false
    reaper_interval_ms: -1
`;
}

async function findChrome() {
  const candidates = [process.env.GOOGLE_CHROME_BIN, process.env.CHROME_BIN, chromium.executablePath(), '/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/usr/bin/chromium', '/usr/bin/chromium-browser'].filter(Boolean);
  for (const candidate of candidates) {
    try {
      await access(candidate, fsConstants.X_OK);
      return candidate;
    } catch {
      // Try the next system browser.
    }
  }
  throw new Error('Chrome/Chromium executable not found; set GOOGLE_CHROME_BIN');
}

function startProcess(command, args, options) {
  const stdout = [];
  const stderr = [];
  const child = spawn(command, args, { cwd: options.cwd, env: options.env || process.env, stdio: ['ignore', 'pipe', 'pipe'] });
  child.stdout.on('data', (chunk) => stdout.push(String(chunk)));
  child.stderr.on('data', (chunk) => stderr.push(String(chunk)));
  const record = { child, label: options.label, stdout, stderr };
  child.once('exit', (code, signal) => {
    processLogs.push({ label: options.label, code, signal, stdout: stdout.join('').slice(-8000), stderr: stderr.join('').slice(-8000) });
  });
  return record;
}

async function stopProcess(record) {
  if (!record || record.child.exitCode !== null) return;
  record.child.kill('SIGTERM');
  await Promise.race([new Promise((resolve) => record.child.once('exit', resolve)), delay(5000)]);
  if (record.child.exitCode === null) record.child.kill('SIGKILL');
}

async function runCommand(command, args, options) {
  await new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd: options.cwd, env: process.env, stdio: ['ignore', 'pipe', 'pipe'] });
    const stdout = [];
    const stderr = [];
    child.stdout.on('data', (chunk) => stdout.push(String(chunk)));
    child.stderr.on('data', (chunk) => stderr.push(String(chunk)));
    child.once('error', reject);
    child.once('exit', (code) => {
      processLogs.push({ label: options.label, code, stdout: stdout.join('').slice(-8000), stderr: stderr.join('').slice(-8000) });
      if (code === 0) resolve();
      else reject(new Error(`${options.label} failed (${code}): ${stderr.join('')}`));
    });
  });
}

async function reservePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

async function waitHTTP(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = new Error(`${response.status} ${url}`);
    } catch (error) {
      lastError = error;
    }
    await delay(100);
  }
  throw lastError || new Error(`timed out waiting for ${url}`);
}

async function getJSON(url) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${response.status} ${url}: ${await response.text()}`);
  return response.json();
}

async function gitHead() {
  return new Promise((resolve, reject) => {
    const child = spawn('git', ['rev-parse', 'HEAD'], { cwd: repoRoot, stdio: ['ignore', 'pipe', 'pipe'] });
    const output = [];
    child.stdout.on('data', (chunk) => output.push(String(chunk)));
    child.once('error', reject);
    child.once('exit', (code) => code === 0 ? resolve(output.join('').trim()) : reject(new Error('git rev-parse failed')));
  });
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function writeStoredZip(target, entries) {
  const localParts = [];
  const centralParts = [];
  let offset = 0;
  for (const [name, content] of Object.entries(entries)) {
    const nameBytes = Buffer.from(name);
    const data = Buffer.from(content);
    const checksum = crc32(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt32LE(checksum, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(nameBytes.length, 26);
    localParts.push(local, nameBytes, data);

    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt32LE(checksum, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(nameBytes.length, 28);
    central.writeUInt32LE((0o100600 << 16) >>> 0, 38);
    central.writeUInt32LE(offset, 42);
    centralParts.push(central, nameBytes);
    offset += local.length + nameBytes.length + data.length;
  }
  const centralData = Buffer.concat(centralParts);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(entries).length, 8);
  end.writeUInt16LE(Object.keys(entries).length, 10);
  end.writeUInt32LE(centralData.length, 12);
  end.writeUInt32LE(offset, 16);
  await writeFile(target, Buffer.concat([...localParts, centralData, end]), { mode: 0o600 });
}

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}
