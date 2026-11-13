// Plan Mode inline: approve & run, request changes (desktop and from the
// mobile overlay inspector), cancel with confirmation, plan input answers
// (option and free-form Other). Approval CAS / receipts / coverage live in
// webconsole_approval_cas_e2e.mjs and webconsole_approval_receipts_e2e.mjs.
import assert from 'node:assert/strict';
import { assertDialogContract } from './audits.mjs';
import { gotoApp, openInspectorTab, setLocale, startFromComposer, tid, waitSessionLoaded, waitToast } from './ui.mjs';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

export async function approveWhenReady(h, page) {
  await h.until(async () => (await tid(page, 'approve-plan').count()) === 1 && !(await tid(page, 'approve-plan').isDisabled()), 'approve enabled (planning run settled)', 60000, 50);
  await tid(page, 'approve-plan').click();
}

async function planFixture(h, prompt, status = 'awaiting_approval') {
  const id = await h.startSession({ prompt, plan_mode: { enabled: true, objective: prompt } });
  // A runner waiting on request_user_input keeps its handle; approval waits need it released.
  await h.waitDetail(id, (d) => d.plan_mode?.status === status && (status === 'awaiting_user_input' || !d.active_handle), status);
  return id;
}

export async function runPlan(h) {
  const fixtures = {
    enPlan: planFixture(h, 'E2E_UI_PLAN english approval card'),
    enInput: planFixture(h, 'E2E_UI_PLAN_INPUT english option answer', 'awaiting_user_input'),
    mobileRevise: planFixture(h, 'E2E_UI_PLAN_REVISE mobile overlay revision'),
  };
  for (const promise of Object.values(fixtures)) promise.catch(() => {});

  const context = await h.newContext({ locale: 'zh-CN' });
  const page = await h.newPage(context, 'plan-desktop');
  await gotoApp(h, page, '/');

  await h.check('approve & run (E2E_UI_PLAN): inline card, reviewed target, receipt, harness reminder', async () => {
    const { id, payload } = await startFromComposer(page, 'E2E_UI_PLAN 先出计划再执行', { plan: true });
    assert.deepEqual(payload.plan_mode, { enabled: true });
    const card = tid(page, 'approval-card');
    await card.waitFor({ timeout: 90000 });
    await tid(page, 'composer-plan-hint').waitFor();
    assert.match(await card.innerText(), /批准并运行/);
    assert.match(await card.innerText(), /Browser observes awaiting approval/);
    assert.equal(await card.getAttribute('data-plan-version'), '1');
    assert.equal(await card.locator('.approval-plan-body').getAttribute('translate'), 'no');
    await h.waitDetail(id, (d) => d.plan_mode?.status === 'awaiting_approval' && !d.active_handle, 'awaiting approval');
    await h.capture(page, 'plan-approval-card-zh-desktop');
    const detail = await h.detail(id);
    const request = page.waitForRequest((req) => req.url().endsWith('/api/sessions/' + id + '/planmode/approve'));
    const response = page.waitForResponse((res) => res.url().endsWith('/api/sessions/' + id + '/planmode/approve'));
    await approveWhenReady(h, page);
    const body = (await request).postDataJSON();
    assert.match(body.approval_request_id, UUID);
    assert.deepEqual({ plan_mode_id: body.plan_mode_id, plan_version: body.plan_version, expected_revision: body.expected_revision, override_coverage: body.override_coverage },
      { plan_mode_id: detail.plan_mode.plan_mode_id, plan_version: detail.plan_mode.plan_version, expected_revision: detail.plan_mode.approval_revision, override_coverage: false });
    assert.equal((await response).status(), 202);
    await waitToast(h, page, /计划已批准，开始执行/);
    await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed');
    const receipt = page.locator('[data-testid="approval-receipt"][data-approval-operation-phase="admitted"]');
    await receipt.waitFor({ timeout: 15000 });
    assert.equal(await receipt.getAttribute('data-approval-request-id'), body.approval_request_id);
    assert.equal(await receipt.locator('.approval-receipt-id').getAttribute('translate'), 'no');
    assert.equal(await tid(page, 'approve-plan').count(), 0, 'no approval buttons after admission');
    const reminder = page.locator('[data-testid="message"][data-role="harness"]').first();
    await reminder.waitFor({ timeout: 15000 });
    assert.equal(await reminder.locator('.notice-body').getAttribute('translate'), 'no');
    assert.match(await reminder.locator('.notice-label').innerText(), /运行框架/);
    assert.equal(await page.locator('[data-testid="message"][data-role="user"]').filter({ hasText: 'E2E_UI_PLAN 先出计划再执行' }).count(), 1, 'the reminder is not rendered as the user');
    await h.capture(page, 'plan-approved-zh-desktop');
  }, { page });

  await h.check('request changes (E2E_UI_PLAN_REVISE): revising composer, revision v2, approve', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_PLAN_REVISE 修改计划', { plan: true });
    await page.locator('[data-testid="approval-card"][data-plan-version="1"]').waitFor({ timeout: 90000 });
    await tid(page, 'revise-plan').click();
    await tid(page, 'composer-revising').waitFor();
    assert.match(await tid(page, 'composer-revising').innerText(), /正在修改计划 v1/);
    await page.waitForFunction(() => document.activeElement?.id === 'composer-input');
    await h.until(async () => (await tid(page, 'composer-input').getAttribute('placeholder')) === '说明计划需要怎么改…', 'revise placeholder', 15000);
    await tid(page, 'composer-input').fill('请缩小范围');
    assert.equal(await tid(page, 'send-button').getAttribute('aria-label'), '发送修改意见');
    await h.capture(page, 'plan-revising-zh-desktop');
    // Launch window: hold the revise POST ("sending"), then the detail refreshes
    // after its 202 ("starting": plan_mode is still awaiting_approval while the
    // revision run launches). The card must stay disabled with a status and
    // never send an approve (the server answers 409 SESSION_NOT_RESUMABLE).
    const approveRequests = [];
    const onApprove = (req) => { if (req.url().includes('/planmode/approve')) approveRequests.push(req.url()); };
    page.on('request', onApprove);
    let releaseRevise;
    const reviseGate = new Promise((resolve) => { releaseRevise = resolve; });
    let holdDetail = false;
    let releaseDetail;
    const detailGate = new Promise((resolve) => { releaseDetail = resolve; });
    const sessionPath = '/api/sessions/' + encodeURIComponent(id);
    const isRevise = (url) => url.pathname === sessionPath + '/planmode/revise';
    const isDetail = (url) => url.pathname === sessionPath;
    // A held request the page aborts meanwhile (newer refresh) cannot be continued: ignore that.
    const holdRevise = async (route) => { await reviseGate; await route.continue().catch(() => {}); };
    const holdDetails = async (route) => { if (holdDetail && route.request().method() === 'GET') await detailGate; await route.continue().catch(() => {}); };
    await page.route(isRevise, holdRevise);
    await page.route(isDetail, holdDetails);
    const card = page.locator('[data-testid="approval-card"][data-plan-version="1"]');
    const assertLocked = async (reason, shot) => {
      const status = card.locator('[data-testid="approval-settling"][data-reason="' + reason + '"]');
      await status.waitFor({ timeout: 15000 });
      assert.match(await status.innerText(), /不能批准或取消/);
      assert.equal(await card.locator('[data-testid="approve-plan"]').isDisabled(), true, reason + ': approve disabled');
      assert.equal(await card.locator('[data-testid="cancel-plan"]').isDisabled(), true, reason + ': cancel disabled');
      assert.equal(await card.locator('[data-testid="approve-plan"]').getAttribute('aria-describedby'), await status.getAttribute('id'));
      await h.capture(page, shot);
      // A disabled button ignores click(): no approve request may leave the page.
      await card.locator('[data-testid="approve-plan"]').evaluate((node) => node.click());
    };
    try {
      const revise = page.waitForRequest((req) => req.url().endsWith(sessionPath + '/planmode/revise'));
      await tid(page, 'composer-input').press('Enter');
      assert.deepEqual((await revise).postDataJSON(), { message: '请缩小范围' });
      await assertLocked('sending', 'plan-revise-sending-zh-desktop');
      holdDetail = true;
      const accepted = page.waitForResponse((res) => res.url().endsWith(sessionPath + '/planmode/revise'));
      releaseRevise();
      assert.equal((await accepted).status(), 202);
      await page.locator('[data-testid="composer"][data-kind="starting"]').waitFor({ timeout: 15000 });
      await assertLocked('starting', 'plan-revise-starting-zh-desktop');
    } finally {
      releaseRevise();
      holdDetail = false;
      releaseDetail();
      await page.unroute(isRevise, holdRevise);
      await page.unroute(isDetail, holdDetails);
    }
    await page.locator('[data-testid="approval-card"][data-plan-version="2"]').waitFor({ timeout: 90000 });
    page.off('request', onApprove);
    assert.deepEqual(approveRequests, [], 'no approve request during the revise launch window');
    assert.match(await tid(page, 'approval-card').innerText(), /Revised plan/);
    assert.equal(await tid(page, 'composer-revising').count(), 0);
    const detail = await h.waitDetail(id, (d) => d.plan_mode?.plan_version === 2 && !d.active_handle, 'v2');
    assert.match(detail.plan_mode.summary, /Revised plan accepted/);
    await approveWhenReady(h, page);
    await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'revised completed');
  }, { page });

  await h.check('cancel asks for confirmation (dialog contract, Esc keeps the plan) then cancels', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_PLAN 取消计划', { plan: true });
    await tid(page, 'approval-card').waitFor({ timeout: 90000 });
    await h.waitDetail(id, (d) => d.plan_mode?.status === 'awaiting_approval' && !d.active_handle, 'awaiting');
    const cancel = tid(page, 'cancel-plan');
    await h.until(async () => !(await cancel.isDisabled()), 'cancel enabled', 30000, 50);
    let posts = 0;
    const onRequest = (req) => { if (req.url().endsWith('/planmode/cancel')) posts += 1; };
    page.on('request', onRequest);
    try {
      await cancel.click();
      const dialog = tid(page, 'confirm-dialog');
      await dialog.waitFor();
      assert.match(await dialog.innerText(), /取消这个计划？/);
      assert.match(await tid(page, 'confirm-dialog-confirm').getAttribute('class'), /btn-danger/);
      await h.capture(page, 'plan-cancel-confirm-zh-desktop');
      await assertDialogContract(page, dialog, { returnTo: cancel });
      assert.equal(posts, 0, 'Esc cancels the dialog without a request');
      await cancel.click();
      await tid(page, 'confirm-dialog-confirm').click();
      await h.until(() => posts === 1, 'cancel posted', 10000, 30);
    } finally {
      page.off('request', onRequest);
    }
    await waitToast(h, page, /计划已取消/);
    await h.waitDetail(id, (d) => d.plan_mode?.status !== 'awaiting_approval', 'cancelled');
    await h.until(async () => (await tid(page, 'approve-plan').count()) === 0, 'approval card gone', 15000);
  }, { page });

  await h.check('plan input (E2E_UI_PLAN_INPUT): free-form Other answer, then approve', async () => {
    const { id } = await startFromComposer(page, 'E2E_UI_PLAN_INPUT 回答问题', { plan: true });
    const card = tid(page, 'plan-input-card');
    await card.waitFor({ timeout: 90000 });
    assert.match(await card.innerText(), /Which scope should the browser plan use/);
    assert.equal(await tid(page, 'plan-input-submit').isDisabled(), true, 'nothing selected yet');
    await tid(page, 'composer-plan-hint').waitFor();
    await h.capture(page, 'plan-input-zh-desktop');
    await tid(page, 'plan-input-other').check();
    await tid(page, 'plan-input-other-text').fill('只覆盖控制台');
    assert.equal(await tid(page, 'plan-input-submit').isDisabled(), false);
    const answer = page.waitForRequest((req) => req.url().endsWith('/api/sessions/' + id + '/planmode/input'));
    await h.waitDetail(id, (d) => d.plan_mode?.status === 'awaiting_user_input', 'input pending');
    await tid(page, 'plan-input-submit').click();
    const body = (await answer).postDataJSON();
    assert.equal(body.answers.length, 1);
    assert.deepEqual(body.answers[0], { question_id: 'scope_choice', label: 'Other', value: '只覆盖控制台', is_other: true });
    await waitToast(h, page, /回答已提交，继续规划/);
    await tid(page, 'approval-card').waitFor({ timeout: 90000 });
    const detail = await h.waitDetail(id, (d) => d.plan_mode?.status === 'awaiting_approval' && !d.active_handle, 'answered plan');
    assert.equal(detail.plan_mode.pending_request, undefined);
    assert.match(detail.plan_mode.summary, /durable browser input/);
    await approveWhenReady(h, page);
    await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'input plan completed');
  }, { page });

  await setLocale(page, 'en');
  await h.check('English approval card and option answer for plan input', async () => {
    const planId = await fixtures.enPlan;
    await page.goto(h.baseURL + '/s/' + encodeURIComponent(planId));
    await waitSessionLoaded(page, planId);
    await tid(page, 'approval-card').waitFor();
    assert.match(await tid(page, 'approval-card').innerText(), /Approve and run/);
    assert.match(await tid(page, 'composer-plan-hint').innerText(), /Plan needs approval/);
    await h.capture(page, 'plan-approval-card-en-desktop');
    const inputId = await fixtures.enInput;
    await page.goto(h.baseURL + '/s/' + encodeURIComponent(inputId));
    await waitSessionLoaded(page, inputId);
    await tid(page, 'plan-input-card').waitFor();
    await page.locator('[data-testid="plan-input-option"][data-label="Narrow (Recommended)"]').check();
    await h.capture(page, 'plan-input-en-desktop');
    const answer = page.waitForRequest((req) => req.url().endsWith('/api/sessions/' + inputId + '/planmode/input'));
    await tid(page, 'plan-input-submit').click();
    const body = (await answer).postDataJSON();
    assert.equal(body.answers[0].question_id, 'scope_choice');
    assert.equal(body.answers[0].label, 'Narrow (Recommended)');
    assert.notEqual(body.answers[0].is_other, true);
    await waitToast(h, page, /Answers submitted; planning continues/);
    await h.waitDetail(inputId, (d) => d.plan_mode?.status === 'awaiting_approval' && !d.active_handle, 'answered');
  }, { page });
  await context.close();

  const mobile = await h.newContext({ locale: 'zh-CN', mobile: true });
  const m = await h.newPage(mobile, 'plan-mobile');
  await h.check('mobile: request changes after the overlay inspector — focus in the composer, background not inert', async () => {
    const id = await fixtures.mobileRevise;
    await gotoApp(h, m, '/s/' + encodeURIComponent(id));
    await waitSessionLoaded(m, id);
    await tid(m, 'approval-card').waitFor();
    await h.capture(m, 'plan-approval-card-zh-mobile');
    await openInspectorTab(m, 'plan');
    assert.equal(await tid(m, 'inspector').getAttribute('role'), 'dialog');
    assert.equal(await m.locator('#app-layout').evaluate((node) => node.inert), true);
    const panel = tid(m, 'inspector-plan');
    await panel.waitFor();
    assert.equal(await panel.getAttribute('data-plan-status'), 'awaiting_approval');
    assert.equal(await panel.locator('[data-testid="approve-plan"], [data-testid="revise-plan"]').count(), 0, 'no duplicate primary actions in the inspector');
    await h.capture(m, 'plan-inspector-zh-mobile');
    await tid(m, 'inspector-plan-goto-approval').click();
    await tid(m, 'inspector').waitFor({ state: 'detached' });
    assert.equal(await m.locator('#app-layout').evaluate((node) => node.inert), false, 'background interactive again');
    await m.waitForFunction(() => document.activeElement?.closest?.('[data-testid="approval-card"]'));
    await tid(m, 'revise-plan').click();
    await tid(m, 'composer-revising').waitFor();
    await m.waitForFunction(() => document.activeElement?.id === 'composer-input');
    assert.equal(await m.locator('#app-layout').evaluate((node) => node.inert), false);
    await tid(m, 'composer-input').fill('移动端：请缩小范围');
    await h.capture(m, 'plan-revising-zh-mobile');
    const revise = m.waitForRequest((req) => req.url().endsWith('/api/sessions/' + id + '/planmode/revise'));
    await tid(m, 'composer-input').press('Enter');
    assert.deepEqual((await revise).postDataJSON(), { message: '移动端：请缩小范围' });
    await m.locator('[data-testid="approval-card"][data-plan-version="2"]').waitFor({ timeout: 90000 });
  }, { page: m });
  await h.check('mobile en: plan input card fits 390px', async () => {
    await setLocale(m, 'en');
    const id = await planFixture(h, 'E2E_UI_PLAN_INPUT mobile english', 'awaiting_user_input');
    await m.goto(h.baseURL + '/s/' + encodeURIComponent(id));
    await waitSessionLoaded(m, id);
    await tid(m, 'plan-input-card').waitFor();
    await tid(m, 'plan-input-card').scrollIntoViewIfNeeded();
    await h.capture(m, 'plan-input-en-mobile');
    // Release the runner that waits on request_user_input (it keeps a live handle).
    const cancelled = await h.api('POST', '/api/sessions/' + encodeURIComponent(id) + '/planmode/cancel', {});
    assert.ok(cancelled.status < 300, cancelled.text);
    await h.waitDetail(id, (d) => h.settled(d), 'cancelled input settled');
  }, { page: m });
  await mobile.close();
}
