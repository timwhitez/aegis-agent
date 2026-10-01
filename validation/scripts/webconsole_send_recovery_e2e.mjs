// Run against a local console: node validation/scripts/webconsole_send_recovery_e2e.mjs http://127.0.0.1:8080
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { chromium } from 'playwright';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export async function runSendRecoveryE2E(baseURL, options = {}) {
  assert.ok(baseURL, 'Pass the URL of a running local aegis-agent web console');
  const outputDir = options.outputDir || process.env.AEGIS_RECOVERY_SCREENSHOTS || '/tmp/aegis-send-recovery';
  await mkdir(outputDir, { recursive: true });
  const browser = options.browser || await chromium.launch({
    executablePath: process.env.CHROME_BIN || undefined,
    args: process.getuid?.() === 0 ? ['--no-sandbox'] : []
  });
  let checks = 0;
  const consoleErrors = [];
  try {
    for (const locale of ['zh-CN', 'en']) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
      await context.addInitScript((locale) => localStorage.setItem('aegis-agent.locale.v1', locale), locale);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      page.on('console', (message) => { if (message.type() === 'error' && !/Failed to load resource|net::ERR_FAILED/.test(message.text())) consoleErrors.push(message.text()); });
      // HTTP/transport failures below are intentional test inputs, not app runtime errors.
      let response = 'failed';
      let resolvePending;
      let calls = 0;
      let lastPayload;
      const receipt = [];
      const session = () => ({
        metadata: { id: 'recovery_a', workdir: '.' }, state: { status: 'paused' },
        messages: receipt, steer_requests: [], timeline: [], tasks: [], children: []
      });
      await page.route('**/api/sessions/recovery_a/file-changes', (route) => route.fulfill({ json: { file_changes: [] } }));
      await page.route(/\/api\/sessions\/recovery_a(?:\?.*)?$/, (route) => route.fulfill({ json: session() }));
      await page.route(/\/api\/sessions\/(?:start|[^/]+\/(?:continue|steer|planmode\/revise))$/, async (route) => {
        calls++;
        lastPayload = route.request().postDataJSON();
        let outcome = response;
        if (outcome === 'pending') outcome = await new Promise((resolve) => { resolvePending = resolve; });
        if (outcome === 'unconfirmed') {
          receipt.push({ id: `durable_${calls}`, role: 'user', text: lastPayload.message || lastPayload.prompt });
          await route.abort('failed');
        } else {
          if (outcome === 'success') receipt.push({ id: `durable_${calls}`, role: 'user', text: lastPayload.message || lastPayload.prompt });
          await route.fulfill(outcome === 'success'
            ? { json: { session_id: 'recovery_a', status: 'accepted' } }
            : { status: 400, json: { error: 'provider is not configured' } });
        }
      });
      await page.goto(baseURL);
      await page.waitForFunction(() => window.AegisI18n?.locale?.() === document.documentElement.lang);
      assert.match(await page.title(), /Agent Console/);
      assert.ok(await page.locator('#chat-input').isVisible());
      assert.equal(await page.locator('html').getAttribute('lang'), locale);
      const rawText = '\n  中文原文 <script>不执行</script>\n  第二行  \n';
      for (const kind of ['start', 'continue', 'steer', 'plan-revision']) {
        for (const outcome of ['failed', 'unconfirmed']) {
          await page.evaluate((kind) => {
            resetChatSession();
            if (kind !== 'start') {
              state.sessionId = 'recovery_a'; state.sessionBacked = true;
              state.sessionDetail = { metadata: { id: 'recovery_a' }, state: { status: kind === 'steer' ? 'running' : 'paused' }, messages: [],
                plan_mode: kind === 'plan-revision' ? { status: 'awaiting_approval' } : null };
              setGeneratingViewState(kind === 'steer');
            }
            updateUI(); renderCurrentSession();
          }, kind);
          response = outcome;
          const count = calls;
          await page.locator('#chat-input').fill(rawText);
          await page.locator('#send-btn').click();
          await page.locator('[data-restore-send-draft]').waitFor();
          const card = page.locator('.failed-send-draft').last();
          assert.equal(await card.locator('.message-bubble').textContent(), rawText);
          assert.equal(lastPayload.message || lastPayload.prompt, rawText.trim());
          assert.equal(await page.locator('#chat-input').inputValue(), '');
          assert.match(await card.innerText(), locale === 'zh-CN'
            ? (outcome === 'failed' ? /输入未被接受/ : /未确认是否送达/)
            : (outcome === 'failed' ? /Prompt was not accepted/ : /Delivery unconfirmed/));
          if (outcome === 'unconfirmed' && kind !== 'start') {
            await page.waitForFunction((id) => state.sessionDetail?.messages?.some((message) => message.id === id), receipt.at(-1).id);
            assert.ok(await card.isVisible(), 'equal history text does not discard a failed request');
          }
          await card.locator('[data-restore-send-draft]').click();
          assert.equal(await page.locator('#chat-input').inputValue(), rawText);
          assert.equal(await page.locator('#send-btn').isDisabled(), false);
          assert.equal(await page.locator('#chat-input').evaluate((el) => el === document.activeElement && el.clientHeight > 24), true);
          assert.equal(calls, count + 1, 'manual restoration does not resend');
          checks++;
        }
      }
      // Local validation happens before the real composer is cleared.
      for (const validator of ['collectGoalDraft', 'collectPlanModeDraft']) {
        await page.evaluate((validator) => {
          resetChatSession();
          window.recoveryValidator = window[validator];
          window[validator] = () => ({ error: 'invalid local draft' });
        }, validator);
        await page.locator('#chat-input').fill(rawText);
        const count = calls;
        await page.locator('#send-btn').click();
        assert.equal(await page.locator('#chat-input').inputValue(), rawText);
        assert.equal(calls, count);
        await page.evaluate((validator) => { window[validator] = window.recoveryValidator; }, validator);
        checks++;
      }
      // One successful user submission creates one accepted receipt, without a failure card.
      await page.evaluate(() => { resetChatSession(); });
      response = 'success';
      const count = calls;
      const receiptCount = receipt.length;
      await page.locator('#chat-input').fill(rawText);
      await page.locator('#send-btn').click();
      await page.waitForFunction(() => state.sessionId === 'recovery_a' && state.sessionBacked);
      assert.equal(calls, count + 1);
      assert.equal(receipt.length, receiptCount + 1);
      assert.equal(await page.locator('[data-restore-send-draft]').count(), 0);
      checks++;
      // A late launch rejection remains recoverable after new-session navigation.
      await page.evaluate(() => { resetChatSession(); nodes.chatInput.value = ''; syncComposerInputEmpty(); updateUI(); });
      response = 'pending';
      await page.locator('#chat-input').fill(rawText);
      await page.locator('#send-btn').click();
      await page.waitForFunction(() => isLaunchInFlight());
      await page.locator('#new-session-btn').click();
      await page.locator('#chat-input').fill('new draft');
      resolvePending('failed');
      await page.locator('[data-restore-send-draft]').waitFor();
      await page.locator('[data-restore-send-draft]').last().click();
      assert.equal(await page.locator('#chat-input').inputValue(), 'new draft');
      await page.locator('#chat-input').fill('');
      await page.locator('[data-restore-send-draft]').last().click();
      assert.equal(await page.locator('#chat-input').inputValue(), rawText);
      checks++;
      response = 'failed';
      await page.locator('#send-btn').click();
      await page.locator('[data-restore-send-draft]').waitFor();
      await page.waitForFunction(() => document.querySelectorAll('.toast').length === 0);
      await page.screenshot({ path: `${outputDir}/${locale}-desktop.png` });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.screenshot({ path: `${outputDir}/${locale}-mobile.png` });
      assert.ok(await page.locator('[data-restore-send-draft]').last().isVisible());
      assert.deepEqual(errors, []);
      assert.deepEqual(consoleErrors, []);
      assert.equal(await page.locator('[data-nextjs-dialog], vite-error-overlay').count(), 0);
      await context.close();
    }
    console.log(`PASS: ${checks} composer recovery scenarios, zh-CN/en, desktop/mobile. Screenshots: ${outputDir}`);
  } finally {
    if (!options.browser) await browser.close();
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  await runSendRecoveryE2E(process.argv[2]);
}
