#!/usr/bin/env node

import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith('--')) {
      continue;
    }
    const key = arg.slice(2);
    const value = argv[i + 1];
    if (value && !value.startsWith('--')) {
      out[key] = value;
      i += 1;
    } else {
      out[key] = 'true';
    }
  }
  return out;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function fetchJSON(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`fetch ${url} failed: ${response.status} ${response.statusText}`);
  }
  return response.json();
}

class CDPClient {
  constructor(wsUrl) {
    this.ws = new WebSocket(wsUrl);
    this.nextID = 1;
    this.pending = new Map();
    this.eventWaiters = new Map();
    this.exceptions = [];
    this.consoleErrors = [];
    this.openPromise = new Promise((resolve, reject) => {
      this.ws.addEventListener('open', resolve, { once: true });
      this.ws.addEventListener('error', reject, { once: true });
    });
    this.ws.addEventListener('message', (event) => {
      const message = JSON.parse(event.data.toString());
      if (message.id) {
        const pending = this.pending.get(message.id);
        if (!pending) {
          return;
        }
        this.pending.delete(message.id);
        if (message.error) {
          pending.reject(new Error(message.error.message || JSON.stringify(message.error)));
          return;
        }
        pending.resolve(message.result || {});
        return;
      }
      this.handleEvent(message);
    });
    this.ws.addEventListener('close', () => {
      for (const pending of this.pending.values()) {
        pending.reject(new Error('cdp socket closed'));
      }
      this.pending.clear();
    });
  }

  handleEvent(message) {
    if (message.method === 'Runtime.exceptionThrown') {
      const text = message.params?.exceptionDetails?.text || 'runtime exception';
      this.exceptions.push(text);
    }
    if (message.method === 'Runtime.consoleAPICalled') {
      const type = message.params?.type || 'log';
      const values = (message.params?.args || []).map((item) => item.value ?? item.description ?? item.type);
      const line = `${type}: ${values.join(' ')}`;
      if (type === 'error') {
        this.consoleErrors.push(line);
      }
    }
    const waiters = this.eventWaiters.get(message.method);
    if (!waiters || waiters.length === 0) {
      return;
    }
    const waiter = waiters.shift();
    waiter.resolve(message.params || {});
  }

  async open() {
    await this.openPromise;
  }

  async close() {
    if (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING) {
      this.ws.close();
    }
  }

  async send(method, params = {}) {
    const id = this.nextID++;
    const payload = JSON.stringify({ id, method, params });
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(payload, (error) => {
        if (!error) {
          return;
        }
        this.pending.delete(id);
        reject(error);
      });
    });
  }

  waitForEvent(method, timeoutMs) {
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        const waiters = this.eventWaiters.get(method) || [];
        this.eventWaiters.set(method, waiters.filter((item) => item.resolve !== resolve));
        reject(new Error(`timed out waiting for ${method}`));
      }, timeoutMs);
      const wrappedResolve = (payload) => {
        clearTimeout(timeout);
        resolve(payload);
      };
      const waiters = this.eventWaiters.get(method) || [];
      waiters.push({ resolve: wrappedResolve });
      this.eventWaiters.set(method, waiters);
    });
  }

  async evaluate(expression, { awaitPromise = true, returnByValue = true } = {}) {
    const result = await this.send('Runtime.evaluate', {
      expression,
      awaitPromise,
      returnByValue,
    });
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.text || 'runtime evaluate failed');
    }
    return result.result?.value;
  }
}

async function waitFor(check, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await check()) {
      return;
    }
    await sleep(200);
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const baseURL = args['base-url'];
  const workdir = args.workdir;
  const queueWorkdir = args['queue-workdir'] || workdir;
  const outputPath = args.output;
  const domPath = args['dom-output'];
  const chromePath = args.chrome || process.env.CHROME_BIN || 'google-chrome';
  const timeoutMs = Number(args['timeout-ms'] || '180000');
  const debugPort = Number(args['debug-port'] || `${36000 + Math.floor(Math.random() * 1000)}`);
  if (!baseURL || !workdir || !outputPath || !domPath) {
    throw new Error('--base-url, --workdir, --output, and --dom-output are required');
  }

  const tempRoot = await mkdtemp(path.join(tmpdir(), 'go-cli-agent-ui-smoke-'));
  const userDataDir = path.join(tempRoot, 'chrome-profile');
  await mkdir(userDataDir, { recursive: true });

  const chromeArgs = [
    '--headless=new',
    '--no-sandbox',
    '--disable-gpu',
    '--disable-dev-shm-usage',
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-background-networking',
    '--disable-extensions',
    `--remote-debugging-port=${debugPort}`,
    `--user-data-dir=${userDataDir}`,
    'about:blank',
  ];
  const chrome = spawn(chromePath, chromeArgs, {
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const chromeLogs = [];
  chrome.stdout.on('data', (chunk) => chromeLogs.push(chunk.toString()));
  chrome.stderr.on('data', (chunk) => chromeLogs.push(chunk.toString()));

  let browserClient;
  try {
    await waitFor(async () => {
      try {
        await fetchJSON(`http://127.0.0.1:${debugPort}/json/version`);
        return true;
      } catch {
        return false;
      }
    }, 15000, `chrome remote debugging\n${chromeLogs.join('')}`);

    const targets = await fetchJSON(`http://127.0.0.1:${debugPort}/json/list`);
    const pageTarget = (targets || []).find((item) => item.type === 'page' && item.webSocketDebuggerUrl);
    if (!pageTarget) {
      throw new Error('no chrome page target found');
    }

    browserClient = new CDPClient(pageTarget.webSocketDebuggerUrl);
    await browserClient.open();
    await browserClient.send('Page.enable');
    await browserClient.send('Runtime.enable');

    const loadPromise = browserClient.waitForEvent('Page.loadEventFired', 30000);
    await browserClient.send('Page.navigate', { url: baseURL });
    await loadPromise;

    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('#start-form')) && Boolean(document.querySelector('#open-start-button')) && document.title === 'Go CLI Agent Console'`),
      30000,
      'webconsole shell'
    );

    const results = {
      base_url: baseURL,
      shell_title: await browserClient.evaluate('document.title'),
      assets: {
        stylesheet_loaded: await browserClient.evaluate(`Array.from(document.styleSheets).some((sheet) => String(sheet.href || '').includes('/assets/styles.css'))`),
        module_tag_present: await browserClient.evaluate(`Boolean(document.querySelector('script[type="module"][src="/assets/app.js"]'))`),
      },
      interactions: {},
      runtime_exceptions: browserClient.exceptions,
      console_errors: browserClient.consoleErrors,
    };

    await browserClient.evaluate(`document.getElementById('open-start-button').click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.activeElement && document.activeElement.id === 'start-prompt'`),
      5000,
      'start prompt focus'
    );
    results.interactions.open_start_focus = true;

    const setField = async (selector, value) => {
      const ok = await browserClient.evaluate(`(() => {
        const el = document.querySelector(${JSON.stringify(selector)});
        if (!el) return false;
        el.focus();
        el.value = ${JSON.stringify(value)};
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        return true;
      })()`);
      if (!ok) {
        throw new Error(`failed to set ${selector}`);
      }
    };

    const openQueueJobFromSelector = async (selector, label) => {
      const target = await browserClient.evaluate(`(() => {
        const source = document.querySelector(${JSON.stringify(selector)});
        const clickable = source?.closest('[data-open-queue-job-id]') || source;
        const jobId = clickable?.dataset?.openQueueJobId || clickable?.getAttribute?.('data-open-queue-job-id') || '';
        if (!jobId) return null;
        clickable.click();
        return { jobId };
      })()`);
      if (!target?.jobId) {
        throw new Error(`missing ${label}`);
      }
      await waitFor(
        () => browserClient.evaluate(`document.getElementById('queue-job-detail')?.dataset.queueJobId === ${JSON.stringify(target.jobId)}`),
        timeoutMs,
        label
      );
      return target.jobId;
    };

    const openQueueJobMatchingText = async (scopeSelector, textNeedle, label) => {
      const target = await browserClient.evaluate(`(() => {
        const items = Array.from(document.querySelectorAll(${JSON.stringify(scopeSelector)}));
        const clickable = items.find((item) => (item.textContent || '').includes(${JSON.stringify(textNeedle)}));
        const jobId = clickable?.dataset?.openQueueJobId || clickable?.getAttribute?.('data-open-queue-job-id') || '';
        if (!jobId) return null;
        clickable.click();
        return { jobId };
      })()`);
      if (!target?.jobId) {
        throw new Error(`missing ${label}`);
      }
      await waitFor(
        () => browserClient.evaluate(`document.getElementById('queue-job-detail')?.dataset.queueJobId === ${JSON.stringify(target.jobId)}`),
        timeoutMs,
        label
      );
      return target.jobId;
    };

    await setField('#start-prompt', 'Reply with exactly the plain text WAITING. Do not call any tool.');
    await setField('#start-agent-role', 'generator');
    await setField('#start-agent-name', 'ui-smoke-driver');
    await setField('#start-workdir', workdir);
    await browserClient.evaluate(`document.getElementById('start-mode').value = 'run'; document.getElementById('start-mode').dispatchEvent(new Event('change', { bubbles: true })); true`);
    await browserClient.evaluate(`document.getElementById('start-form').requestSubmit(); true`);

    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('#continue-form')) && Boolean(document.querySelector('.session-list-item.is-active'))`),
      timeoutMs,
      'awaiting_input continue form'
    );
    results.interactions.started_session = true;
    results.session_id = await browserClient.evaluate(`document.querySelector('.session-list-item.is-active')?.dataset.sessionId || ''`);

    await browserClient.evaluate(`document.querySelector('[data-view-button="overview"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('overview-view')?.classList.contains('is-hidden')`),
      5000,
      'overview view visible'
    );
    await browserClient.evaluate(`document.querySelector('[data-drilldown-target="session-status"][data-drilldown-value="awaiting_input"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('session-filter-status')?.value === 'awaiting_input' && !document.getElementById('session-view')?.classList.contains('is-hidden') && Boolean(document.querySelector('#continue-form'))`),
      5000,
      'overview session drilldown'
    );
    results.interactions.overview_session_drilldown = true;

    await setField('#continue-message', 'Now call finish with message: ui smoke continue ok');
    await browserClient.evaluate(`document.getElementById('continue-form').requestSubmit(); true`);
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('.session-header')) && document.querySelector('.session-header')?.textContent?.includes('completed') && !document.querySelector('#continue-form')`),
      timeoutMs,
      'completed continued session'
    );
    results.interactions.continued_session = true;
    await waitFor(
      () => browserClient.evaluate(`document.querySelector('.session-list-item.is-active')?.textContent?.includes('generator') && document.getElementById('session-view')?.textContent?.includes('generator')`),
      10000,
      'role-aware session chrome'
    );
    results.interactions.start_role_visible = true;

    await setField('#session-filter-query', 'ui-smoke-reviewer');
    await waitFor(
      () => browserClient.evaluate(`document.querySelectorAll('.session-list-item').length >= 1 && Boolean(document.querySelector('[data-reveal-selected-session]')) && Boolean(document.querySelector('.session-list-item.is-active')) && document.getElementById('session-filter-query')?.value === 'ui-smoke-reviewer'`),
      5000,
      'session sidebar filter state'
    );
    results.interactions.session_filter_query = true;

    await browserClient.evaluate(`document.querySelector('[data-reveal-selected-session]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.querySelectorAll('.session-list-item').length >= 2 && !document.querySelector('[data-reveal-selected-session]') && document.getElementById('session-filter-query')?.value === ''`),
      5000,
      'session reveal selected reset'
    );
    results.interactions.session_filter_reveal = true;

    await browserClient.evaluate(`document.querySelector('[data-session-tab="tasks"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('session-view')?.textContent?.includes('No todo items for this session.')`),
      5000,
      'tasks tab visible'
    );
    results.interactions.tasks_tab_visible = true;

    await setField('#worker-count', '1');
    await browserClient.evaluate(`document.getElementById('worker-form').requestSubmit(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.body.textContent.includes('Worker pool updated.')`),
      10000,
      'worker update toast'
    );
    results.interactions.updated_worker_count = true;

    await setField('#queue-prompt', 'Return exactly one finish tool call with message: ui smoke queue ok');
    await setField('#queue-agent-name', 'ui-smoke-reviewer');
    await setField('#queue-role', 'evaluator');
    await setField('#queue-workdir', queueWorkdir);
    await browserClient.evaluate(`document.getElementById('queue-form').requestSubmit(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.body.textContent.includes('Queue job submitted.')`),
      10000,
      'queue submit toast'
    );
    results.interactions.submitted_queue_job = true;

    await browserClient.evaluate(`document.querySelector('[data-view-button="queue"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('queue-view')?.classList.contains('is-hidden')`),
      5000,
      'queue view visible'
    );
    results.interactions.opened_queue_view = true;

    await waitFor(
      () => browserClient.evaluate(`document.getElementById('queue-view')?.textContent?.includes('ui smoke queue ok') || document.getElementById('queue-view')?.textContent?.includes('ui-smoke-reviewer')`),
      timeoutMs,
      'queue job visible in queue view'
    );
    results.interactions.queue_job_visible = true;

    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('[data-filter-chip-target="queue-status"][data-filter-chip-value="completed"]'))`),
      timeoutMs,
      'queue completed chip available'
    );
    await browserClient.evaluate(`document.querySelector('[data-filter-chip-target="queue-status"][data-filter-chip-value="completed"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.querySelector('[data-filter-chip-target="queue-status"][data-filter-chip-value="completed"]')?.classList.contains('is-active') && document.getElementById('queue-filter-status')?.value === 'completed'`),
      5000,
      'queue completed chip active'
    );
    results.interactions.queue_filter_chip = true;

    await openQueueJobMatchingText('#queue-view .table-list [data-open-queue-job-id]', 'ui-smoke-reviewer', 'queue job detail open');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('queue-job-detail')?.dataset.queueJobId) && document.getElementById('queue-job-detail')?.textContent?.includes('Selected Queue Job') && document.getElementById('queue-job-detail')?.textContent?.includes('ui smoke queue ok')`),
      timeoutMs,
      'queue job detail payload'
    );
    results.interactions.queue_job_detail = true;

    await setField('#queue-filter-query', 'not-present-anywhere');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('[data-reveal-selected-queue-job]')) && Boolean(document.querySelector('#queue-view [data-open-queue-job-id]')) && document.getElementById('queue-filter-query')?.value === 'not-present-anywhere'`),
      5000,
      'queue pinned selected filter state'
    );
    results.interactions.queue_filter_pinned_selected = true;

    await browserClient.evaluate(`document.querySelector('[data-reveal-selected-queue-job]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.querySelector('[data-reveal-selected-queue-job]') && document.getElementById('queue-filter-query')?.value === ''`),
      5000,
      'queue reveal selected reset'
    );
    results.interactions.queue_filter_reveal = true;

    await browserClient.evaluate(`document.querySelector('[data-view-button="overview"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('overview-view')?.classList.contains('is-hidden')`),
      5000,
      'overview visible for queue drilldowns'
    );
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('[data-queue-drill-source="overview-recent-job"]'))`),
      timeoutMs,
      'overview recent queue job source'
    );
    const overviewRecentJobId = await openQueueJobFromSelector('[data-queue-drill-source="overview-recent-job"]', 'overview recent queue job drilldown');
    results.interactions.queue_recent_jobs_drilldown = true;

    await browserClient.evaluate(`document.querySelector('[data-view-button="overview"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('overview-view')?.classList.contains('is-hidden')`),
      5000,
      'overview visible for feed drilldown'
    );
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('[data-action-source="overview-feed-queue-job"]'))`),
      timeoutMs,
      'overview feed queue source'
    );
    const overviewFeedJobId = await openQueueJobFromSelector('[data-action-source="overview-feed-queue-job"]', 'overview feed queue drilldown');
    results.interactions.queue_feed_drilldown = true;

    await browserClient.evaluate(`document.querySelector('[data-view-button="overview"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('overview-view')?.classList.contains('is-hidden')`),
      5000,
      'overview visible for failure drilldown'
    );
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('[data-action-source="overview-failed-job"]'))`),
      timeoutMs,
      'overview failed queue source'
    );
    const overviewFailedJobId = await openQueueJobFromSelector('[data-action-source="overview-failed-job"]', 'overview failed queue drilldown');
    results.interactions.queue_failure_drilldown = true;

    await browserClient.evaluate(`document.querySelector('[data-view-button="queue"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('queue-view')?.classList.contains('is-hidden')`),
      5000,
      'queue visible for worker last-job drilldown'
    );
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.querySelector('[data-action-source="worker-last-job"]'))`),
      timeoutMs,
      'worker last-job queue source'
    );
    const workerLastJobId = await openQueueJobFromSelector('[data-action-source="worker-last-job"]', 'worker last-job queue drilldown');
    results.interactions.queue_worker_last_job_drilldown = true;
    results.queue_drilldown_job_ids = {
      overview_recent_job: overviewRecentJobId,
      overview_feed: overviewFeedJobId,
      overview_failure: overviewFailedJobId,
      worker_last_job: workerLastJobId,
    };

    await browserClient.evaluate(`document.querySelector('.session-list-item.is-active')?.click(); true`);
    await browserClient.evaluate(`document.querySelector('[data-session-tab="timeline"]')?.click(); true`);
    const timelineCounts = await browserClient.evaluate(`(() => {
      const before = document.querySelectorAll('#session-view .timeline-item').length;
      const eventChip = document.querySelector('[data-filter-chip-target="timeline-kind"][data-filter-chip-value="event"]');
      if (!eventChip) return { before, after: -1 };
      eventChip.click();
      return { before, after: document.querySelectorAll('#session-view .timeline-item').length };
    })()`);
    await waitFor(
      () => browserClient.evaluate(`document.querySelector('[data-filter-chip-target="timeline-kind"][data-filter-chip-value="event"]')?.classList.contains('is-active') && Array.from(document.querySelectorAll('#session-view .timeline-item')).every((item) => item.dataset.timelineKind === 'event')`),
      5000,
      'timeline event filter active'
    );
    results.interactions.timeline_filter_chip = true;
    results.timeline_counts = timelineCounts;
    await browserClient.evaluate(`document.querySelector('[data-clear-filter="timeline"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.querySelector('[data-filter-chip-target="timeline-kind"][data-filter-chip-value="all"]')?.classList.contains('is-active') && !document.querySelector('[data-clear-filter="timeline"]')`),
      5000,
      'timeline filters cleared'
    );
    results.interactions.timeline_filter_clear = true;

    await browserClient.evaluate(`document.querySelector('[data-session-tab="children"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('session-view')?.textContent?.includes('ui-smoke-reviewer') && document.getElementById('session-view')?.textContent?.includes('evaluator')`),
      timeoutMs,
      'children tab role visibility'
    );
    results.interactions.children_tab_visible = true;
    results.interactions.children_role_visible = true;
    await browserClient.evaluate(`document.querySelector('[data-session-tab="links"]')?.click(); true`);
    await waitFor(
      () => browserClient.evaluate(`!document.getElementById('session-view')?.classList.contains('is-hidden')`),
      5000,
      'session view visible again'
    );
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('session-view')?.textContent?.includes('ui smoke queue ok')`),
      timeoutMs,
      'background notification visible in queue links tab'
    );
    results.interactions.background_notification_visible = true;

    await browserClient.evaluate(`document.getElementById('manual-refresh-button').click(); true`);
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('manual-refresh-button')?.textContent === 'Refresh'`),
      10000,
      'refresh button reset'
    );
    results.interactions.manual_refresh = true;

    results.runtime_exceptions = browserClient.exceptions;
    results.console_errors = browserClient.consoleErrors;
    results.topbar = {
      eyebrow: await browserClient.evaluate(`document.querySelector('.eyebrow')?.textContent?.trim() || ''`),
      selected_session: await browserClient.evaluate(`document.getElementById('topbar-meta')?.textContent || ''`),
    };
    results.dom_checks = {
      has_start_form: await browserClient.evaluate(`Boolean(document.getElementById('start-form'))`),
      has_queue_form: await browserClient.evaluate(`Boolean(document.getElementById('queue-form'))`),
      has_worker_form: await browserClient.evaluate(`Boolean(document.getElementById('worker-form'))`),
      has_session_header: await browserClient.evaluate(`Boolean(document.querySelector('.session-header'))`),
      has_session_filter: await browserClient.evaluate(`Boolean(document.getElementById('session-filter-query'))`),
      has_queue_filter: await browserClient.evaluate(`Boolean(document.getElementById('queue-filter-query'))`),
      has_queue_job_detail: await browserClient.evaluate(`Boolean(document.getElementById('queue-job-detail'))`),
    };

    const dom = await browserClient.evaluate('document.documentElement.outerHTML');
    await writeFile(domPath, `${dom}\n`, 'utf8');
    await writeFile(outputPath, `${JSON.stringify(results, null, 2)}\n`, 'utf8');

    if (results.runtime_exceptions.length > 0 || results.console_errors.length > 0) {
      throw new Error(`ui smoke captured browser-side errors: exceptions=${results.runtime_exceptions.length} console_errors=${results.console_errors.length}`);
    }
  } finally {
    if (browserClient) {
      await browserClient.close().catch(() => {});
    }
    chrome.kill('SIGTERM');
    await sleep(500);
    if (!chrome.killed) {
      chrome.kill('SIGKILL');
    }
    await rm(tempRoot, { recursive: true, force: true }).catch(() => {});
  }
}

main().catch(async (error) => {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exitCode = 1;
});
