// Composer send recovery (spec/17-web-console.md §8.1.1) against the real
// service. Only the send POST is intercepted to produce an explicit
// rejection (HTTP 400), an unknown result (HTTP 500 or a network abort) or a
// delivered request whose response is lost; the running-steer view uses a
// real snapshot captured while an E2E_UI_SLOW run was live. Covers start /
// continue / steer / plan revision in zh-CN and en: drafts keep the exact raw
// text and the original session, restore only into an empty composer, survive
// session switches, are never resent automatically and never persisted.
// Runs as the "recovery" area of webconsole_e2e.mjs.
import assert from 'node:assert/strict';
import { gotoApp, newSessionUI, openSession, spaGo, storeState, tid, waitSessionLoaded } from './e2e/ui.mjs';

const RAW = '\n  中文原文 <script>不执行</script>\n  第二行  \n';
const COPY = {
  'zh-CN': { failed: '发送被拒绝，消息未送达', unconfirmed: '未确认是否送达，请先核对会话记录', notEmpty: '输入框里还有内容，清空后才能恢复草稿。' },
  en: { failed: 'Rejected — the message was not delivered', unconfirmed: 'Delivery unconfirmed — check the session before resending', notEmpty: 'Clear the composer before restoring a draft.' },
};
const OUTCOMES = [
  { name: 'rejected-400', status: 'failed', http: 400 },
  { name: 'unconfirmed-500', status: 'unconfirmed', http: 500 },
  { name: 'unconfirmed-abort', status: 'unconfirmed', abort: true },
];

async function awaitingSession(h, label) {
  const id = await h.startSession('E2E_UI_AWAIT recovery ' + label);
  await h.waitDetail(id, (d) => d.state?.status === 'awaiting_input' && !d.active_handle, 'awaiting');
  return id;
}

export async function runSendRecoveryE2E(h) {
  const fixtures = {
    completed: h.fixture('recovery-completed', async () => {
      const id = await awaitingSession(h, 'continue target');
      const result = await h.api('POST', '/api/sessions/' + id + '/continue', { message: 'finish the recovery fixture' });
      assert.equal(result.status, 202, result.text);
      await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed');
      return id;
    }),
    plan: h.fixture('recovery-plan', async () => {
      const id = await h.startSession({ prompt: 'E2E_UI_PLAN_REVISE recovery revision target', plan_mode: { enabled: true } });
      await h.waitDetail(id, (d) => d.plan_mode?.status === 'awaiting_approval' && !d.active_handle, 'awaiting approval');
      return id;
    }),
    running: h.fixture('recovery-running-snapshot', async () => {
      const id = await h.startSession('E2E_UI_SLOW recovery steer target');
      const detail = await h.waitDetail(id, (d) => d.state?.status === 'running' && d.active_handle === true, 'running', 60000);
      return { id, detail };
    }),
  };
  for (const promise of Object.values(fixtures)) promise.catch(() => {});
  const evidence = [];

  for (const locale of ['zh-CN', 'en']) {
    const copy = COPY[locale];
    const tag = locale === 'en' ? 'en' : 'zh';
    const context = await h.newContext({ locale });
    const page = await h.newPage(context, 'recovery-' + tag);
    await gotoApp(h, page, '/');
    const posts = [];
    page.on('request', (request) => { if (request.method() === 'POST' && /\/api\/sessions\/(start|[^/]+\/(continue|steer|planmode\/revise))$/.test(new URL(request.url()).pathname)) posts.push(new URL(request.url()).pathname); });

    // One failed send: route, send, verify the card; returns the draft locator.
    const failSend = async (kind, endpoint, outcome, sessionId) => {
      const url = h.baseURL + endpoint;
      if (outcome.abort) h.allowAbort('send ' + kind + ' aborted', { url, method: 'POST', limit: 2 });
      else h.allowHTTP('send ' + kind + ' ' + outcome.http, { url, status: outcome.http, method: 'POST', limit: 2 });
      let payload = null;
      await page.route(url, async (route) => {
        payload = route.request().postDataJSON();
        if (outcome.abort) return route.abort('failed');
        return route.fulfill({ status: outcome.http, contentType: 'application/json', body: JSON.stringify(outcome.http === 400
          ? { error: 'fixture rejected the message', detail: 'fixture detail', action: 'fixture action' }
          : { error: 'fixture gateway failure' }) });
      });
      try {
        const before = await page.locator('[data-testid="failed-draft"]').count();
        const refresh = sessionId && outcome.status === 'unconfirmed'
          ? page.waitForRequest((request) => request.method() === 'GET' && new URL(request.url()).pathname === '/api/sessions/' + sessionId, { timeout: 15000 })
          : null;
        refresh?.catch(() => {});
        const sent = posts.length;
        await tid(page, 'composer-input').fill(RAW);
        await h.until(async () => (await tid(page, 'send-button').count()) === 1 && !(await tid(page, 'send-button').isDisabled()), 'send enabled', 30000, 40);
        await tid(page, 'composer-input').press('Enter');
        await h.until(async () => (await page.locator('[data-testid="failed-draft"]').count()) > before, 'a new failed draft', 15000, 40);
        assert.equal(posts.length, sent + 1, 'exactly one request per send');
        assert.equal(posts.at(-1), endpoint, kind + ' goes to ' + endpoint);
        const card = page.locator('[data-testid="failed-draft"]').last();
        assert.equal(await card.getAttribute('data-status'), outcome.status);
        assert.equal(await card.getAttribute('data-kind'), kind);
        assert.equal(payload.message ?? payload.prompt, RAW.trim(), 'the payload is trimmed');
        assert.equal(await card.locator('.failed-draft-text').evaluate((node) => node.textContent), RAW, 'the card keeps the exact raw text');
        assert.equal(await card.locator('.failed-draft-text').getAttribute('translate'), 'no');
        assert.equal(await tid(page, 'composer-input').inputValue(), '', 'the composer is cleared');
        assert.match(await card.locator('.failed-draft-head').innerText(), new RegExp(outcome.status === 'failed' ? copy.failed : copy.unconfirmed));
        if (sessionId) assert.equal(await card.getAttribute('data-session-id'), sessionId, 'bound to the original session');
        if (outcome.http === 400) {
          const text = await card.innerText();
          assert.match(text, /fixture rejected the message/);
          assert.match(text, /fixture detail/);
          assert.match(text, /fixture action/);
          assert.equal(await page.locator('[data-testid="toast"]').filter({ hasText: 'fixture rejected the message' }).count(), 0, 'no duplicate toast');
        }
        if (refresh) await refresh;
        return card;
      } finally {
        await page.unroute(url);
      }
    };

    const restoreLast = async () => {
      const card = page.locator('[data-testid="failed-draft"]').last();
      const count = await page.locator('[data-testid="failed-draft"]').count();
      const sent = posts.length;
      await tid(page, 'composer-input').fill('新的草稿 / new draft');
      assert.equal(await card.locator('[data-testid="failed-draft-restore"]').isDisabled(), true, 'restore needs an empty composer');
      assert.match(await card.innerText(), new RegExp(copy.notEmpty));
      await tid(page, 'composer-input').fill('');
      await card.locator('[data-testid="failed-draft-restore"]').click();
      assert.equal(await tid(page, 'composer-input').inputValue(), RAW, 'restored verbatim');
      await page.waitForFunction(() => document.activeElement?.id === 'composer-input');
      assert.equal(await page.locator('[data-testid="failed-draft"]').count(), count - 1);
      assert.equal(posts.length, sent, 'restoring never resends');
      await tid(page, 'composer-input').fill('');
    };

    await h.check('start: 400 rejected, 500 / abort unconfirmed; restore only into an empty composer (' + tag + ')', async () => {
      await newSessionUI(page);
      for (const outcome of OUTCOMES) {
        const card = await failSend('start', '/api/sessions/start', outcome, '');
        assert.match(await card.locator('.failed-draft-meta').innerText(), locale === 'en' ? /New session/ : /新会话/);
        assert.equal(new URL(page.url()).pathname, '/', 'a failed start stays on the new-session composer');
        evidence.push({ locale, kind: 'start', outcome: outcome.name });
      }
      await h.capture(page, 'recovery-start-drafts-' + tag + '-desktop');
      await restoreLast();
    }, { page });

    await h.check('continue: rejected and unconfirmed drafts point to the original session (' + tag + ')', async () => {
      const id = await fixtures.completed;
      await openSession(page, id);
      for (const outcome of OUTCOMES) {
        await failSend('continue', '/api/sessions/' + id + '/continue', outcome, id);
        evidence.push({ locale, kind: 'continue', outcome: outcome.name, session_id: id });
      }
      await restoreLast();
    }, { page });

    await h.check('steer on a running session: rejected and unconfirmed drafts (' + tag + ')', async () => {
      const { id, detail } = await fixtures.running;
      const pathname = '/api/sessions/' + id;
      const frozen = (url) => url.pathname === pathname;
      await page.route(frozen, (route) => route.fulfill({ status: 200, json: detail }));
      try {
        await openSession(page, id);
        await tid(page, 'stop-button').waitFor();
        for (const outcome of OUTCOMES) {
          await tid(page, 'composer-input').fill('x');
          assert.equal(await tid(page, 'send-button').getAttribute('data-action'), 'steer');
          await failSend('steer', '/api/sessions/' + id + '/steer', outcome, id);
          evidence.push({ locale, kind: 'steer', outcome: outcome.name, session_id: id });
        }
        await h.capture(page, 'recovery-steer-drafts-' + tag + '-desktop');
        await restoreLast();
      } finally {
        await page.unroute(frozen);
      }
    }, { page });

    await h.check('plan revision: rejected and unconfirmed drafts (' + tag + ')', async () => {
      const id = await fixtures.plan;
      await openSession(page, id);
      await tid(page, 'approval-card').waitFor();
      for (const outcome of OUTCOMES) {
        if (!(await tid(page, 'composer-revising').count())) await tid(page, 'revise-plan').click();
        await tid(page, 'composer-revising').waitFor();
        await failSend('revise', '/api/sessions/' + id + '/planmode/revise', outcome, id);
        evidence.push({ locale, kind: 'revise', outcome: outcome.name, session_id: id });
      }
      await restoreLast();
    }, { page });

    await h.check('drafts survive session switches and open their original session (' + tag + ')', async () => {
      const completedId = await fixtures.completed;
      const planId = await fixtures.plan;
      await openSession(page, planId);
      const foreign = page.locator('[data-testid="failed-draft"][data-session-id="' + completedId + '"]');
      assert.equal(await foreign.count(), 2, 'continue drafts stay visible in another session');
      await h.capture(page, 'recovery-drafts-other-session-' + tag + '-desktop');
      await foreign.first().locator('[data-testid="failed-draft-open"]').click();
      await waitSessionLoaded(page, completedId);
      assert.equal(await page.locator('[data-testid="failed-draft"][data-session-id="' + completedId + '"] [data-testid="failed-draft-open"]').count(), 0, 'no open link in its own session');
      await spaGo(page, '/');
      await page.locator('[data-testid="failed-draft"]').first().waitFor();
      const stored = await page.evaluate(() => Object.values({ ...localStorage }).join('\n'));
      assert.doesNotMatch(stored, /中文原文/, 'drafts are memory-only (never written to localStorage)');
      assert.equal(posts.filter((pathname) => pathname.endsWith('/continue')).length, 3, 'nothing was resent');
    }, { page });

    await h.check('late launch rejection after navigating to a new session stays recoverable (' + tag + ')', async () => {
      await newSessionUI(page);
      const url = h.baseURL + '/api/sessions/start';
      h.allowHTTP('late start rejection 400', { url, status: 400, method: 'POST', limit: 2 });
      let release;
      const held = new Promise((resolve) => { release = resolve; });
      let seen = false;
      await page.route(url, async (route) => { seen = true; await held; await route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'late fixture rejection' }) }); });
      try {
        const before = await page.locator('[data-testid="failed-draft"]').count();
        await tid(page, 'composer-input').fill(RAW);
        await tid(page, 'composer-input').press('Enter');
        await h.until(() => seen, 'start request in flight', 10000, 20);
        await page.locator('[data-testid="sidebar"] [data-testid="new-session"]').click();
        await tid(page, 'composer-input').fill('new draft');
        release();
        await h.until(async () => (await page.locator('[data-testid="failed-draft"]').count()) > before, 'late draft card', 15000, 40);
        const card = page.locator('[data-testid="failed-draft"]').last();
        assert.equal(await card.getAttribute('data-status'), 'failed');
        assert.equal(await tid(page, 'composer-input').inputValue(), 'new draft', 'the new draft is untouched');
        assert.equal(await card.locator('[data-testid="failed-draft-restore"]').isDisabled(), true);
        await tid(page, 'composer-input').fill('');
        await card.locator('[data-testid="failed-draft-restore"]').click();
        assert.equal(await tid(page, 'composer-input').inputValue(), RAW);
        await tid(page, 'composer-input').fill('');
      } finally {
        await page.unroute(url);
      }
    }, { page });

    await h.check('a delivered request with a lost response stays "unconfirmed" even when history shows the same text; a success creates no draft (' + tag + ')', async () => {
      const [deliveredId, successId] = await Promise.all([awaitingSession(h, 'delivered ' + tag), awaitingSession(h, 'success ' + tag)]);
      await openSession(page, deliveredId);
      const url = h.baseURL + '/api/sessions/' + deliveredId + '/continue';
      h.allowAbort('continue response lost after delivery', { url, method: 'POST', limit: 2 });
      let delivered = null;
      await page.route(url, async (route) => {
        const response = await route.fetch();
        delivered = response.status();
        await route.abort('failed');
      });
      const text = 'Delivered once: ' + tag + ' recovery check';
      try {
        const before = await page.locator('[data-testid="failed-draft"]').count();
        await tid(page, 'composer-input').fill(text);
        await h.until(async () => !(await tid(page, 'send-button').isDisabled()), 'send enabled', 30000, 40);
        await tid(page, 'composer-input').press('Enter');
        await h.until(async () => (await page.locator('[data-testid="failed-draft"]').count()) > before, 'unconfirmed draft', 15000, 40);
      } finally {
        await page.unroute(url);
      }
      assert.equal(delivered, 202, 'the server accepted the request');
      const card = page.locator('[data-testid="failed-draft"][data-session-id="' + deliveredId + '"]').last();
      assert.equal(await card.getAttribute('data-status'), 'unconfirmed');
      await h.waitDetail(deliveredId, (d) => h.settled(d) && (d.messages || []).some((m) => m.role === 'user' && m.text === text), 'delivered message');
      await page.locator('[data-testid="message"][data-role="user"]').filter({ hasText: text }).waitFor({ timeout: 30000 });
      assert.equal(await card.isVisible(), true, 'equal history text never discards the unconfirmed draft');
      const detail = await h.detail(deliveredId);
      assert.equal(detail.messages.filter((m) => m.role === 'user' && m.text === text).length, 1, 'delivered exactly once, never resent');
      await h.capture(page, 'recovery-delivered-unconfirmed-' + tag + '-desktop');
      await openSession(page, successId);
      const drafts = await page.locator('[data-testid="failed-draft"]').count();
      const ok = page.waitForResponse((response) => response.url().endsWith('/api/sessions/' + successId + '/continue'));
      await tid(page, 'composer-input').fill('A successful continue');
      await h.until(async () => !(await tid(page, 'send-button').isDisabled()), 'send enabled', 30000, 40);
      await tid(page, 'composer-input').press('Enter');
      assert.equal((await ok).status(), 202);
      await h.waitDetail(successId, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed');
      assert.equal(await page.locator('[data-testid="failed-draft"]').count(), drafts, 'a successful send creates no draft');
      while (await tid(page, 'failed-draft-dismiss').count()) await tid(page, 'failed-draft-dismiss').first().click();
      assert.equal(await tid(page, 'failed-drafts').count(), 0);
    }, { page });
    await context.close();
  }

  const mobile = await h.newContext({ locale: 'zh-CN', mobile: true });
  const m = await h.newPage(mobile, 'recovery-mobile');
  await h.check('mobile: a rejected draft fits 390px', async () => {
    const id = await fixtures.completed;
    await gotoApp(h, m, '/s/' + encodeURIComponent(id));
    await waitSessionLoaded(m, id);
    const url = h.baseURL + '/api/sessions/' + id + '/continue';
    h.allowHTTP('mobile continue 400', { url, status: 400, method: 'POST', limit: 2 });
    await m.route(url, (route) => route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'fixture mobile rejection' }) }));
    await tid(m, 'composer-input').fill(RAW);
    await h.until(async () => !(await tid(m, 'send-button').isDisabled()), 'send enabled', 30000, 40);
    await tid(m, 'send-button').click();
    await tid(m, 'failed-draft').waitFor();
    await m.unroute(url);
    assert.equal(await storeState(m, 'composer.text'), '');
    await h.capture(m, 'recovery-draft-zh-mobile');
  }, { page: m });
  await mobile.close();
  h.evidence.send_recovery = { scenarios: evidence.length, evidence };
}
