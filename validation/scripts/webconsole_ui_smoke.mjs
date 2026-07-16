#!/usr/bin/env node

import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

function loadWebSocket() {
  if (typeof globalThis.WebSocket === 'function') {
    return globalThis.WebSocket;
  }
  try {
    const { WebSocket } = require('undici');
    if (typeof WebSocket === 'function') {
      return WebSocket;
    }
  } catch {
    // fall through to the explicit error below
  }
  throw new Error('WebSocket is not available; use Node 20+ or install/provide undici');
}

const WebSocketCtor = loadWebSocket();

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

async function fetchJSON(url, options = {}) {
  const headers = {
    'X-Go-Cli-Agent-Web': '1',
    ...(options.headers || {})
  };
  const response = await fetch(url, { ...options, headers });
  const text = await response.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = null;
  }
  if (!response.ok) {
    throw new Error(`fetch ${url} failed: ${response.status} ${response.statusText}: ${body?.error || text}`);
  }
  return body;
}

class CDPClient {
  constructor(wsUrl) {
    this.ws = new WebSocketCtor(wsUrl);
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
      const message = JSON.parse(decodeWebSocketMessage(event.data));
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
    if (this.ws.readyState === WebSocketCtor.OPEN || this.ws.readyState === WebSocketCtor.CONNECTING) {
      this.ws.close();
    }
  }

  async send(method, params = {}) {
    const id = this.nextID++;
    const payload = JSON.stringify({ id, method, params });
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      try {
        this.ws.send(payload);
      } catch (error) {
        this.pending.delete(id);
        reject(error);
      }
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

function decodeWebSocketMessage(data) {
  if (typeof data === 'string') {
    return data;
  }
  if (Buffer.isBuffer(data)) {
    return data.toString();
  }
  if (data instanceof ArrayBuffer) {
    return Buffer.from(data).toString();
  }
  if (ArrayBuffer.isView(data)) {
    return Buffer.from(data.buffer, data.byteOffset, data.byteLength).toString();
  }
  return String(data);
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

function normalizeWhitespace(value) {
  return String(value || '').replace(/\s+/g, ' ').trim();
}

function sessionToolNames(detail) {
  return (detail.messages || []).flatMap((msg) => (msg.tool_calls || []).map((call) => call.name));
}

function sessionHasRichActivity(detail) {
  const toolNames = sessionToolNames(detail);
  return toolNames.length > 0 || (detail.timeline || []).length > 2 || (detail.children?.sessions || []).length > 0 || (detail.children?.jobs || []).length > 0;
}

async function findFallbackSession(baseURL, excludeIDs = []) {
  const sessions = await fetchJSON(`${baseURL}/api/sessions?limit=40`);
  const candidates = [];
  let best = null;
  let bestScore = -1;
  for (const item of sessions || []) {
    if (excludeIDs.includes(item.id)) {
      continue;
    }
    const detail = await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(item.id)}?limit=80`);
    if (!sessionHasRichActivity(detail)) {
      continue;
    }
    const score =
      ((detail.children?.sessions || []).length * 4) +
      ((detail.children?.jobs || []).length * 4) +
      sessionToolNames(detail).length +
      Math.min((detail.timeline || []).length, 10);
    candidates.push({ session: item, detail, score });
    if (score > bestScore) {
      best = { session: item, detail };
      bestScore = score;
    }
  }
  const agentHeavy = candidates
    .filter((item) => ((item.detail.children?.sessions || []).length + (item.detail.children?.jobs || []).length) > 0)
    .sort((a, b) => b.score - a.score);
  if (agentHeavy.length > 0) {
    return { session: agentHeavy[0].session, detail: agentHeavy[0].detail };
  }
  return best;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const baseURL = args['base-url'];
  const workdir = args.workdir;
  const queueWorkdir = args['queue-workdir'] || workdir;
  const outputPath = args.output;
  const domPath = args['dom-output'];
  const chromePath = args.chrome || process.env.CHROME_BIN || 'google-chrome';
  const timeoutMs = Number(args['timeout-ms'] || '240000');
  const debugPort = Number(args['debug-port'] || `${36000 + Math.floor(Math.random() * 1000)}`);
  const budgetLifecycle = args['budget-lifecycle'] === 'true';
  const keepHistory = args['keep-history'] === 'true';

  if (!baseURL || !workdir || !outputPath || !domPath) {
    throw new Error('--base-url, --workdir, --output, and --dom-output are required');
  }

  const tempRoot = await mkdtemp(path.join(tmpdir(), 'go-cli-agent-ui-smoke-'));
  const userDataDir = path.join(tempRoot, 'chrome-profile');

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
  const chrome = spawn(chromePath, chromeArgs, { stdio: ['ignore', 'pipe', 'pipe'] });
  const chromeLogs = [];
  chrome.stdout.on('data', (chunk) => chromeLogs.push(chunk.toString()));
  chrome.stderr.on('data', (chunk) => chromeLogs.push(chunk.toString()));

  let browserClient;
  let results = null;
  const workspaceCleanupPaths = [];
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
      () => browserClient.evaluate(`document.title.endsWith('Agent Console') && Boolean(document.getElementById('chat-input')) && Boolean(document.getElementById('send-btn')) && Boolean(document.getElementById('chat-messages')) && Boolean(document.getElementById('interrupt-session-btn')) && Boolean(document.getElementById('stop-session-btn'))`),
      30000,
      'webconsole shell'
    );

    results = {
      base_url: baseURL,
      shell_title: await browserClient.evaluate('document.title'),
      assets: {
        stylesheet_loaded: await browserClient.evaluate(`Array.from(document.styleSheets).some((sheet) => String(sheet.href || '').includes('/styles.css') || String(sheet.href || '').endsWith('styles.css'))`),
        script_tag_present: await browserClient.evaluate(`Boolean(document.querySelector('script[src="app.js"]'))`),
        split_modules_loaded: await browserClient.evaluate(`['api.js', 'events.js', 'settings-view.js', 'workspace-view.js', 'session-view.js', 'app.js'].every((src) => Boolean(document.querySelector('script[src="' + src + '"]')))`)
      },
      interactions: {},
      runtime_exceptions: browserClient.exceptions,
      console_errors: browserClient.consoleErrors,
    };

    const click = async (selector, label) => {
      const ok = await browserClient.evaluate(`(() => {
        const el = document.querySelector(${JSON.stringify(selector)});
        if (!el) return false;
        el.click();
        return true;
      })()`);
      if (!ok) {
        throw new Error(`missing element for ${label}: ${selector}`);
      }
    };

    const setValue = async (selector, value) => {
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
        throw new Error(`missing input: ${selector}`);
      }
    };

    await click('[data-view="settings"]', 'settings nav');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('settings-provider')) && Boolean(document.getElementById('settings-baseurl')) && Boolean(document.getElementById('settings-model')) && Boolean(document.getElementById('settings-enable-child-budget'))`),
      15000,
      'settings form'
    );
    results.interactions.settings_loaded = true;
    const childBudgetDefaults = await browserClient.evaluate(`(() => {
      const enabled = document.getElementById('settings-enable-child-budget');
      const activeRuntime = document.getElementById('settings-child-budget-active-runtime');
      const elapsed = document.getElementById('settings-child-budget-elapsed');
      const maxTurns = document.getElementById('settings-child-budget-max-turns');
      const state = document.getElementById('settings-child-budget-state');
      const hardDisabled = document.getElementById('settings-disable-hard-turn-limit');
      const hardTurns = document.getElementById('settings-max-turns-hard');
      const hardState = document.getElementById('settings-global-turn-limit-state');
      const settingsText = document.querySelector('[data-view-panel="settings"], #settings-view')?.textContent || document.body.textContent || '';
      return {
        enabled: Boolean(enabled?.checked),
        active_runtime_disabled: Boolean(activeRuntime?.disabled),
        elapsed_disabled: Boolean(elapsed?.disabled),
        max_turns_disabled: Boolean(maxTurns?.disabled),
        state: state?.textContent || '',
        hard_disabled: Boolean(hardDisabled?.checked),
        hard_input_disabled: Boolean(hardTurns?.disabled),
        hard_state: hardState?.textContent || '',
        global_scope_copy: settingsText.includes('master, foreground child, and background/queue child sessions'),
        soft_copy: settingsText.includes('Soft is a one-time checkpoint reminder and never stops execution'),
        snapshot_copy: settingsText.includes('Changes affect newly created child/job work only'),
        parent_actions_copy: settingsText.includes('A parent can extend/resume or cancel/settle a budget-paused child')
      };
    })()`);
    if (childBudgetDefaults.enabled || !childBudgetDefaults.active_runtime_disabled || !childBudgetDefaults.elapsed_disabled || !childBudgetDefaults.max_turns_disabled || childBudgetDefaults.state !== 'Off') {
      throw new Error(`expected default child budget controls to be disabled: ${JSON.stringify(childBudgetDefaults)}`);
    }
    if (budgetLifecycle && (!childBudgetDefaults.hard_disabled || !childBudgetDefaults.hard_input_disabled || childBudgetDefaults.hard_state !== 'Off' || !childBudgetDefaults.global_scope_copy || !childBudgetDefaults.soft_copy || !childBudgetDefaults.snapshot_copy || !childBudgetDefaults.parent_actions_copy)) {
      throw new Error(`expected global/default budget semantics in Settings: ${JSON.stringify(childBudgetDefaults)}`);
    }
    await click('#settings-enable-child-budget', 'sub-agent budget toggle');
    const childBudgetEnabled = await browserClient.evaluate(`(() => ({
      active_runtime_enabled: !document.getElementById('settings-child-budget-active-runtime')?.disabled,
      elapsed_enabled: !document.getElementById('settings-child-budget-elapsed')?.disabled,
      max_turns_enabled: !document.getElementById('settings-child-budget-max-turns')?.disabled,
      state: document.getElementById('settings-child-budget-state')?.textContent || ''
    }))()`);
    if (!childBudgetEnabled.active_runtime_enabled || !childBudgetEnabled.elapsed_enabled || !childBudgetEnabled.max_turns_enabled || childBudgetEnabled.state !== 'Enabled') {
      throw new Error(`expected child budget controls to enable together: ${JSON.stringify(childBudgetEnabled)}`);
    }
    results.interactions.child_budget_defaults_off = true;
    results.interactions.child_budget_toggle_works = true;
    results.interactions.global_turn_guard_default_off = childBudgetDefaults.hard_disabled && childBudgetDefaults.hard_input_disabled && childBudgetDefaults.hard_state === 'Off';
    results.interactions.global_turn_guard_scope_copy = childBudgetDefaults.global_scope_copy;
    results.interactions.soft_checkpoint_copy = childBudgetDefaults.soft_copy;
    results.interactions.child_budget_snapshot_copy = childBudgetDefaults.snapshot_copy;
    results.interactions.child_budget_parent_actions_copy = childBudgetDefaults.parent_actions_copy;

    if (budgetLifecycle) {
      await setValue('#settings-child-budget-active-runtime', '30m');
      await setValue('#settings-child-budget-elapsed', '2h');
      await setValue('#settings-child-budget-max-turns', '1');
      await click('#settings-save-btn', 'save budget settings');
      await waitFor(
        () => browserClient.evaluate(`Boolean(document.querySelector('.confirm-dialog-confirm'))`),
        5000,
        'settings save confirmation'
      );
      await click('.confirm-dialog-confirm', 'confirm budget settings save');
      await waitFor(
        () => browserClient.evaluate(`Array.from(document.querySelectorAll('.toast')).some((item) => item.textContent.includes('Settings saved.'))`),
        15000,
        'settings saved toast'
      );
      const savedConfig = await fetchJSON(`${baseURL}/api/config`);
      const savedBudget = savedConfig?.child_budget || {};
      if (savedConfig?.max_turns_hard !== -1 || savedConfig?.disable_hard_turn_limit !== true || savedBudget.disabled !== false || savedBudget.max_active_runtime_sec !== 1800 || savedBudget.max_elapsed_sec !== 7200 || savedBudget.max_turns_per_attempt !== 1 || Object.prototype.hasOwnProperty.call(savedBudget, 'max_wall_clock_sec') || Object.prototype.hasOwnProperty.call(savedBudget, 'max_turns')) {
        throw new Error(`unexpected canonical Settings round-trip: ${JSON.stringify(savedConfig)}`);
      }
      results.interactions.settings_budget_saved = true;
      results.interactions.settings_canonical_round_trip = true;
      results.saved_budget = savedBudget;
    } else {
      await click('#settings-enable-child-budget', 'sub-agent budget reset');
    }

    await click('[data-view="workspace"]', 'workspace nav');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('file-tree')) && Boolean(document.getElementById('editor-content'))`),
      15000,
      'workspace pane'
    );
    await waitFor(
      () => browserClient.evaluate(`(() => {
        const tree = document.getElementById('file-tree');
        return Boolean(tree) && !tree.querySelector('.view-loading') && (tree.querySelectorAll('.tree-node').length > 0 || tree.textContent.includes('empty'));
      })()`),
      15000,
      'workspace directory listing'
    );
    results.interactions.workspace_loaded = true;

    const browsedWorkspacePath = await browserClient.evaluate(`(() => {
      const button = Array.from(document.querySelectorAll('#file-tree .tree-node')).find((item) =>
        item.dataset.type === 'directory' && item.dataset.navigation !== 'parent' && Boolean(item.dataset.path)
      );
      if (!button) return '';
      const path = button.dataset.path;
      button.click();
      return path;
    })()`);
    if (!browsedWorkspacePath) {
      throw new Error('workspace smoke requires at least one browsable child directory');
    }
    await waitFor(
      () => browserClient.evaluate(`typeof currentWorkspacePath === 'function' && currentWorkspacePath() === ${JSON.stringify(browsedWorkspacePath)}`),
      15000,
      'workspace child directory navigation'
    );
    await click('[data-view="history"]', 'history nav for workspace retention');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('history-view')) && document.getElementById('history-view').textContent.includes('Sessions')`),
      15000,
      'history view for workspace retention'
    );
    await click('[data-view="workspace"]', 'workspace nav after history');
    await waitFor(
      () => browserClient.evaluate(`typeof currentWorkspacePath === 'function' && currentWorkspacePath() === ${JSON.stringify(browsedWorkspacePath)}`),
      15000,
      'workspace path retained after view switch'
    );
    results.interactions.workspace_path_retained = true;
    results.interactions.workspace_browsed_path = browsedWorkspacePath;

    const uploadName = `webconsole-upload-smoke-${Date.now()}.txt`;
    const renamedName = uploadName.replace('upload', 'renamed');
    const uploadPath = `${browsedWorkspacePath}/${uploadName}`;
    const renamedPath = `${browsedWorkspacePath}/${renamedName}`;
    const uploadStarted = await browserClient.evaluate(`(() => {
      const input = document.getElementById('workspace-upload-input');
      if (!input || typeof DataTransfer !== 'function' || typeof File !== 'function') return false;
      const transfer = new DataTransfer();
      transfer.items.add(new File(['workspace upload smoke'], ${JSON.stringify(uploadName)}, { type: 'text/plain' }));
      input.files = transfer.files;
      input.dispatchEvent(new Event('change', { bubbles: true }));
      return true;
    })()`);
    if (!uploadStarted) {
      throw new Error('workspace upload input could not be activated');
    }
    workspaceCleanupPaths.push(uploadPath);
    await waitFor(
      () => browserClient.evaluate(`Array.from(document.querySelectorAll('#file-tree .tree-node')).some((item) => item.dataset.path === ${JSON.stringify(uploadPath)})`),
      15000,
      'workspace uploaded file'
    );
    await click(`#file-tree .tree-node[data-path=${JSON.stringify(uploadPath)}]`, 'uploaded workspace file');
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('editor-filename')?.textContent === ${JSON.stringify(uploadPath)}`),
      15000,
      'workspace uploaded file preview'
    );
    await browserClient.evaluate(`(() => {
      window.__workspaceOriginalPrompt = window.prompt;
      window.prompt = () => ${JSON.stringify(renamedName)};
    })()`);
    await click('#workspace-rename-btn', 'workspace rename action');
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('editor-filename')?.textContent === ${JSON.stringify(renamedPath)} && Array.from(document.querySelectorAll('#file-tree .tree-node')).some((item) => item.dataset.path === ${JSON.stringify(renamedPath)})`),
      15000,
      'workspace renamed file'
    );
    workspaceCleanupPaths.push(renamedPath);
    await browserClient.evaluate(`(() => {
      if (window.__workspaceOriginalPrompt) {
        window.prompt = window.__workspaceOriginalPrompt;
        delete window.__workspaceOriginalPrompt;
      }
    })()`);
    results.interactions.workspace_upload = true;
    results.interactions.workspace_rename = true;

    await click('[data-view="skills"]', 'skills nav');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('skills-grid')) && document.getElementById('skills-grid').textContent.length >= 0`),
      15000,
      'skills view'
    );
    results.interactions.skills_loaded = true;

    await click('[data-view="history"]', 'history nav');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('history-view')) && document.getElementById('history-view').textContent.includes('Clear sessions') && document.getElementById('history-view').textContent.includes('Sessions')`),
      15000,
      'history view'
    );
    results.interactions.history_loaded = true;

    await click('[data-view="chat"]', 'chat nav');
    await waitFor(
      () => browserClient.evaluate(`Boolean(document.getElementById('chat-input')) && Boolean(document.getElementById('chat-messages'))`),
      15000,
      'chat view'
    );
    results.interactions.chat_scrollbar_visible = await browserClient.evaluate(`(() => {
      const el = document.getElementById('chat-container');
      if (!el) return false;
      const styles = window.getComputedStyle(el);
      return !el.classList.contains('scrollbar-hide') && styles.overflowY === 'auto';
    })()`);
    results.interactions.chat_scrollbar_clear_of_input = await browserClient.evaluate(`(() => {
      const scroller = document.getElementById('chat-container');
      const inputArea = document.querySelector('#chat-view .input-area');
      if (!scroller || !inputArea) return false;
      const scrollerRect = scroller.getBoundingClientRect();
      const inputRect = inputArea.getBoundingClientRect();
      const horizontallyAligned = scrollerRect.right > inputRect.left && scrollerRect.left < inputRect.right;
      return !horizontallyAligned || scrollerRect.bottom <= inputRect.top + 1;
    })()`);
    if (!results.interactions.chat_scrollbar_clear_of_input) {
      throw new Error('chat scrollbar bottom is covered by the input area');
    }

    const initialSessionChip = await browserClient.evaluate(`document.getElementById('session-id-display')?.textContent || ''`);
    const initialEphemeralPrefix = normalizeWhitespace(initialSessionChip);
    const prompt = budgetLifecycle
      ? [
          'BUDGET_BROWSER_SMOKE.',
          'Exercise one foreground child budget pause, explicit parent extension/resume, and completion.',
          'Then exercise one background child budget pause followed by explicit parent cancel/settle.',
          'Keep durable evidence and finish with exact message: budget browser parent complete.'
        ].join(' ')
      : [
          'This is a frontend smoke validation.',
          'Do not inspect the workspace.',
          'Call todo_write with exactly one completed todo item content "frontend smoke".',
          'Then call agent_spawn with background=false, agent_name="ui-smoke-reviewer", agent_role="evaluator", prompt="Immediately call finish with exact message: ui smoke child ok".',
          'If agent_spawn returns a session_id, call agent_status for that child session.',
          'Then call agent_list.',
          'Finally call finish with exact message: ui smoke parent ok.'
        ].join(' ');
    await setValue('#chat-input', prompt);
    await waitFor(
      () => browserClient.evaluate(`(() => {
        const button = document.getElementById('send-btn');
        return Boolean(button) && !button.disabled;
      })()`),
      10000,
      'send button enabled'
    );
    await click('#send-btn', 'send button');

    await waitFor(
      () => browserClient.evaluate(`(() => {
        const initial = ${JSON.stringify(initialEphemeralPrefix)};
        const chip = String(document.getElementById('session-id-display')?.textContent || '').replace(/\\s+/g, ' ').trim();
        const pending = Boolean(document.querySelector('.pending-stage-card'));
        const generating = typeof state !== 'undefined' && state.isGenerating === true;
        const durable = chip !== initial && (
          (typeof state !== 'undefined' && state.sessionBacked === true) ||
          /\\b\\d{8}-\\d{6}-[0-9a-f]{6}\\b/i.test(chip)
        );
        const text = [
          document.getElementById('chat-messages')?.textContent || '',
          document.getElementById('input-status-text')?.textContent || '',
          document.getElementById('connection-status')?.textContent || ''
        ].join(' ');
        const launchText = /Launching session|Continuing session|Bootstrapping|Live session|Running/i.test(text);
        return pending || generating || durable || launchText;
      })()`),
      20000,
      'launch feedback or durable session'
    );
    const launchFeedback = await browserClient.evaluate(`(() => {
      const initial = ${JSON.stringify(initialEphemeralPrefix)};
      const chip = String(document.getElementById('session-id-display')?.textContent || '').replace(/\\s+/g, ' ').trim();
      const text = [
        document.getElementById('chat-messages')?.textContent || '',
        document.getElementById('input-status-text')?.textContent || '',
        document.getElementById('connection-status')?.textContent || ''
      ].join(' ');
      const pending = Boolean(document.querySelector('.pending-stage-card'));
      const generating = typeof state !== 'undefined' && state.isGenerating === true;
      const durable = chip !== initial && (
        (typeof state !== 'undefined' && state.sessionBacked === true) ||
        /\\b\\d{8}-\\d{6}-[0-9a-f]{6}\\b/i.test(chip)
      );
      const launchText = /Launching session|Continuing session|Bootstrapping|Live session|Running/i.test(text);
      return { pending, generating, durable, launchText };
    })()`);
    results.interactions.pending_stage_visible = Boolean(launchFeedback.pending);
    results.interactions.launch_feedback_visible = Boolean(
      launchFeedback.pending ||
      launchFeedback.generating ||
      launchFeedback.durable ||
      launchFeedback.launchText
    );
    results.interactions.durable_session_adopted_before_wait = Boolean(launchFeedback.durable);
    results.interactions.stop_button_visible = await browserClient.evaluate(`(() => {
      const el = document.getElementById('stop-session-btn');
      if (!el) return false;
      return el.classList.contains('is-visible');
    })()`);

    await waitFor(
      () => browserClient.evaluate(`(() => {
        const value = String(document.getElementById('session-id-display')?.textContent || '').replace(/\\s+/g, ' ').trim();
        return value !== ${JSON.stringify(initialEphemeralPrefix)};
      })()`),
      40000,
      'durable session id'
    );

    const sessionId = await browserClient.evaluate(`(() => {
      if (typeof state !== 'undefined' && state.sessionId && !/^0x/i.test(String(state.sessionId))) {
        return String(state.sessionId);
      }
      const text = String(document.getElementById('session-id-display')?.textContent || '');
      const match = text.match(/\\b\\d{8}-\\d{6}-[0-9a-f]{6}\\b/i);
      return match ? match[0] : text.replace(/^ID:\\s*/, '');
    })()`);
    results.session_id = sessionId;

    let liveSessionDetail = null;
    await waitFor(
      async () => {
        liveSessionDetail = await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(sessionId)}?limit=80`);
        const status = liveSessionDetail.state?.status || '';
        const toolNames = sessionToolNames(liveSessionDetail);
        if (budgetLifecycle) {
          const children = liveSessionDetail.children?.sessions || [];
          const jobs = liveSessionDetail.children?.jobs || [];
          return ['completed', 'awaiting_input', 'paused'].includes(status) &&
            ['todo_write', 'agent_spawn', 'agent_prompt', 'agent_stop', 'agent_list', 'finish'].every((name) => toolNames.includes(name)) &&
            children.some((item) => item.agent_name === 'budget-resume-child' && item.status === 'completed') &&
            jobs.some((item) => item.agent_name === 'budget-cancel-child' && item.status === 'cancelled');
        }
        if (toolNames.includes('todo_write') && toolNames.includes('agent_spawn') && toolNames.includes('agent_list') && ['completed', 'awaiting_input', 'paused'].includes(status)) {
          return true;
        }
        return ['completed', 'awaiting_input', 'paused', 'failed'].includes(status);
      },
      timeoutMs,
      'parent session settle'
    );

    results.live_session_status = liveSessionDetail.state?.status || '';
    results.live_session_error = liveSessionDetail.state?.last_error || '';

    let activeSessionId = sessionId;
    let activeSessionDetail = liveSessionDetail;
    let childSessionId = '';
    let queueDetail = null;
    let queueJob = null;
    let usedFallback = false;

    const liveToolNames = sessionToolNames(liveSessionDetail);
    const liveRich = liveToolNames.includes('todo_write') && liveToolNames.includes('agent_spawn') && liveToolNames.includes('agent_list');
    results.interactions.parent_session_completed = ['completed', 'awaiting_input', 'paused'].includes(liveSessionDetail.state?.status || '');
    results.interactions.live_tool_flow = liveRich;

    if (!liveRich) {
      results.provider_status = /connection refused|i\/o timeout|context deadline exceeded|upstream_timeout/i.test(results.live_session_error) ? 'unavailable' : 'live_failed';
      const fallback = await findFallbackSession(baseURL, [sessionId]);
      if (!fallback) {
        throw new Error(`live session failed without rich activity and no fallback session was found: ${results.live_session_error}`);
      }
      usedFallback = true;
      activeSessionId = fallback.session.id;
      activeSessionDetail = fallback.detail;
      results.fallback_session_id = activeSessionId;

      await click('[data-view="history"]', 'history nav for fallback');
      await waitFor(
        async () => {
          const found = await browserClient.evaluate(`Boolean(document.querySelector('[data-open-session="${activeSessionId}"]'))`);
          if (found) {
            return true;
          }
          await browserClient.evaluate(`(() => {
            const next = document.querySelector('[data-history-page="next"]');
            if (!next || next.disabled) return false;
            next.click();
            return true;
          })()`);
          return false;
        },
        30000,
        'fallback session card'
      );
      await click(`[data-open-session="${activeSessionId}"]`, 'fallback session open');
      await waitFor(
        () => browserClient.evaluate(`(document.getElementById('session-id-display')?.textContent || '').includes(${JSON.stringify(activeSessionId)})`),
        20000,
        'fallback session active in chat view'
      );
    } else {
      results.provider_status = 'available';
    }

    await waitFor(
      () => browserClient.evaluate(`(() => {
        const root = document.getElementById('chat-messages');
        return Boolean(root) && root.querySelectorAll('.tool-lane, .tl-row-call, .tl-row-result, .tool-card').length >= 1;
      })()`),
      30000,
      'tool activity visible'
    );
    results.interactions.tool_cards_visible = true;
    results.interactions.tool_activity_visible = true;

    await waitFor(
      () => browserClient.evaluate(`(() => {
        const root = document.getElementById('chat-messages');
        return Boolean(root) && root.textContent.includes('Flow') && root.querySelectorAll('.timeline-card').length >= 1;
      })()`),
      15000,
      'inline flow visible'
    );
    results.interactions.timeline_visible = true;

    childSessionId = await browserClient.evaluate(`(() => {
      const root = document.getElementById('chat-messages');
      const buttons = Array.from(root ? root.querySelectorAll('[data-open-session]') : []);
      const current = (document.getElementById('session-id-display')?.textContent || '').replace(/^ID:\\s*/, '');
      const other = buttons.find((button) => button.dataset.openSession && button.dataset.openSession !== current);
      return other?.dataset?.openSession || '';
    })()`);

    if (budgetLifecycle) {
      queueDetail = (liveSessionDetail.children?.jobs || []).find((item) => item.agent_name === 'budget-cancel-child') || null;
      results.queue_job_id = queueDetail?.id || '';
      results.interactions.queue_job_submitted = false;
      results.interactions.queue_job_completed = false;
      results.interactions.queue_job_cancelled = Boolean(queueDetail && queueDetail.status === 'cancelled');
    } else if (!usedFallback) {
      queueJob = await fetchJSON(`${baseURL}/api/queue/jobs`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          parent_session_id: sessionId,
          prompt: 'Immediately call finish with exact message: ui smoke queue ok',
          agent_name: 'ui-smoke-queue',
          agent_role: 'generator',
          mode: 'exec',
          workdir: queueWorkdir
        })
      });
      results.queue_job_id = queueJob.id || '';
      results.interactions.queue_job_submitted = true;

      await waitFor(
        async () => {
          try {
            queueDetail = await fetchJSON(`${baseURL}/api/queue/jobs/${encodeURIComponent(queueJob.id)}`);
          } catch (err) {
            if (String(err?.message || '').includes('404 Not Found')) {
              return false;
            }
            throw err;
          }
          return queueDetail.status === 'completed' && String(queueDetail.final_text || '').includes('ui smoke queue ok');
        },
        timeoutMs,
        'queue job completion'
      );
      results.interactions.queue_job_completed = true;
    } else {
      const fallbackJob = (activeSessionDetail.children?.jobs || [])[0] || null;
      queueDetail = fallbackJob;
      results.queue_job_id = fallbackJob?.id || '';
      results.interactions.queue_job_submitted = false;
      results.interactions.queue_job_completed = Boolean(fallbackJob && fallbackJob.status === 'completed');
    }

    activeSessionDetail = await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(activeSessionId)}?limit=80`);
    if (!childSessionId) {
      childSessionId = activeSessionDetail.children?.sessions?.[0]?.id || '';
    }
    results.child_session_id = childSessionId;
    results.interactions.child_session_visible = Boolean(childSessionId) || (activeSessionDetail.children?.sessions || []).length > 0;
    results.interactions.tasks_tab_visible = (activeSessionDetail.task_board?.tasks || []).length > 0 || (activeSessionDetail.task_board?.todo || []).length > 0;

    if (budgetLifecycle) {
      const childSessions = activeSessionDetail.children?.sessions || [];
      const childJobs = activeSessionDetail.children?.jobs || [];
      const resumeChild = childSessions.find((item) => item.agent_name === 'budget-resume-child');
      const cancelledChild = childSessions.find((item) => item.agent_name === 'budget-cancel-child');
      const cancelledJob = childJobs.find((item) => item.agent_name === 'budget-cancel-child');
      const cancelledNotification = (activeSessionDetail.background_notifications || []).find((item) => item.agent_name === 'budget-cancel-child');
      if (!resumeChild?.id || !cancelledChild?.id || !cancelledJob?.id || !cancelledNotification) {
        throw new Error(`budget lifecycle children are incomplete: ${JSON.stringify({ childSessions, childJobs, notifications: activeSessionDetail.background_notifications || [] })}`);
      }
      const resumeDetail = await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(resumeChild.id)}?limit=80`);
      const cancelledDetail = await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(cancelledChild.id)}?limit=80`);
      const resumeBudget = resumeDetail.metadata?.effective_budget || {};
      const cancelledBudget = cancelledJob.effective_budget || {};
      const resumeDetailText = JSON.stringify(resumeDetail);
      if (resumeDetail.state?.status !== 'completed' || Number(resumeBudget.attempt || 0) < 2 || !resumeDetailText.includes('session.child_budget.exceeded') || !resumeDetailText.includes('session.child_budget.extended')) {
        throw new Error(`foreground child did not preserve pause/extend/resume completion evidence: ${resumeDetailText}`);
      }
      if (cancelledJob.status !== 'cancelled' || cancelledJob.session_status !== 'paused' || cancelledJob.stop_reason !== 'agent_stop' || cancelledDetail.state?.status !== 'paused' || cancelledDetail.state?.pause_reason !== 'child_budget_turns_exceeded' || cancelledBudget.status !== 'exhausted') {
        throw new Error(`background child did not preserve cancel/settle semantics: ${JSON.stringify({ cancelledJob, cancelledDetail })}`);
      }
      if (cancelledNotification.status !== 'cancelled' || (cancelledNotification.available_actions || []).length !== 0 || cancelledNotification.effective_budget?.status !== 'exhausted') {
        throw new Error(`terminal budget notification retained stale actions or facts: ${JSON.stringify(cancelledNotification)}`);
      }
      const overview = await fetchJSON(`${baseURL}/api/overview`);
      if (Number(overview.queue_counters?.cancelled || 0) < 1 || (overview.recent_failures || []).some((item) => item.status === 'cancelled' || item.id === cancelledJob.id)) {
        throw new Error(`cancelled work polluted failure telemetry: ${JSON.stringify(overview)}`);
      }

      await waitFor(
        () => browserClient.evaluate(`(() => {
          const sessions = state?.sessionDetail?.children?.sessions || [];
          const jobs = state?.sessionDetail?.children?.jobs || [];
          return sessions.some((item) => item.agent_name === 'budget-resume-child') && jobs.some((item) => item.agent_name === 'budget-cancel-child' && item.status === 'cancelled');
        })()`),
        20000,
        'budget lifecycle child facts in browser state'
      );
      const inspectorRendered = await browserClient.evaluate(`Boolean(document.querySelector('[data-inspector-tab="agents"]'))`);
      if (!inspectorRendered) {
        await click('#inspector-toggle-btn', 'open inspector');
        await waitFor(
          () => browserClient.evaluate(`Boolean(document.querySelector('[data-inspector-tab="agents"]'))`),
          5000,
          'inspector tabs'
        );
      }
      await click('[data-inspector-tab="agents"]', 'agents inspector tab');
      await waitFor(
        () => browserClient.evaluate(`(() => {
          const text = [document.getElementById('inspector-panel')?.textContent || '', document.getElementById('inspector-slide-out')?.textContent || ''].join(' ');
          return text.includes('budget-resume-child') && text.includes('budget-cancel-child') && text.includes('attempt 2') && text.includes('turns') && text.includes('left') && text.includes('deadline') && text.includes('runtime.child_budget') && text.includes('Cancelled');
        })()`),
        15000,
        'budget lifecycle inspector telemetry'
      );
      const inspectorFacts = await browserClient.evaluate(`(() => {
        const panel = [document.getElementById('inspector-slide-out'), document.getElementById('inspector-panel')]
          .find((item) => (item?.textContent || '').includes('budget-resume-child'));
        const text = panel?.textContent || '';
        const cancelledCard = Array.from(panel?.querySelectorAll('.agent-card, .notification-card') || []).find((item) => item.textContent.includes('budget-cancel-child') && item.textContent.includes('Cancelled'));
        return {
          has_attempt: text.includes('attempt 2'),
          has_usage: text.includes('turns') && text.includes('left'),
          has_deadline: text.includes('deadline'),
          has_reason: text.includes('child_budget_turns_exceeded'),
          has_source: text.includes('runtime.child_budget'),
          cancelled_visible: Boolean(cancelledCard),
          terminal_actions_cleared: Boolean(cancelledCard) && !cancelledCard.textContent.includes('Parent actions:')
        };
      })()`);
      if (!Object.values(inspectorFacts).every(Boolean)) {
        throw new Error(`budget inspector facts are incomplete: ${JSON.stringify(inspectorFacts)}`);
      }
      results.interactions.foreground_budget_pause_extend_resume_complete = true;
      results.interactions.background_budget_pause_cancel_settle = true;
      results.interactions.cancelled_excluded_from_failures = true;
      results.interactions.budget_inspector_telemetry_visible = true;
      results.interactions.terminal_budget_actions_cleared = true;
      results.budget_lifecycle = {
        resume_child_id: resumeChild.id,
        resume_attempt: resumeBudget.attempt,
        cancelled_child_id: cancelledChild.id,
        cancelled_job_id: cancelledJob.id,
        cancelled_job_status: cancelledJob.status,
        cancelled_child_status: cancelledDetail.state?.status || '',
        cancelled_child_pause_reason: cancelledDetail.state?.pause_reason || ''
      };
    }

    await click('[data-view="history"]', 'history nav after session');
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('history-view')?.textContent?.includes('Clear sessions') && document.getElementById('history-view')?.textContent?.includes('Page')`),
      15000,
      'history view visible after activity'
    );
    results.interactions.history_data_visible = true;
    results.interactions.queue_job_visible = Boolean(queueDetail && ['completed', 'cancelled', 'failed'].includes(queueDetail.status));
    results.interactions.queue_job_detail_api_verified = budgetLifecycle
      ? Boolean(queueDetail && queueDetail.status === 'cancelled' && queueDetail.stop_reason === 'agent_stop')
      : Boolean(
          queueDetail &&
          queueDetail.status === 'completed' &&
          String(queueDetail.final_text || '').includes('ui smoke queue ok')
        );
    results.interactions.frontend_queue_surface_skipped = true;

    await click('[data-view="chat"]', 'return to chat');
    await waitFor(
      () => browserClient.evaluate(`document.getElementById('chat-messages')?.textContent?.length > 0`),
      15000,
      'chat return after history'
    );
    const settledSessionResourceCountBefore = await browserClient.evaluate(`(() => {
      const sessionId = ${JSON.stringify(activeSessionId)};
      return performance
        .getEntriesByType('resource')
        .filter((entry) => String(entry.name || '').includes('/api/sessions/') && String(entry.name || '').includes(sessionId))
        .length;
    })()`);
    await sleep(4200);
    const settledSessionResourceCountAfter = await browserClient.evaluate(`(() => {
      const sessionId = ${JSON.stringify(activeSessionId)};
      return performance
        .getEntriesByType('resource')
        .filter((entry) => String(entry.name || '').includes('/api/sessions/') && String(entry.name || '').includes(sessionId))
        .length;
    })()`);
    results.interactions.settled_session_stops_polling = settledSessionResourceCountAfter === settledSessionResourceCountBefore;

    const sessionDetail = await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(activeSessionId)}?limit=80`);
    const childDetail = childSessionId ? await fetchJSON(`${baseURL}/api/sessions/${encodeURIComponent(childSessionId)}?limit=40`) : { state: {} };

    results.summary = {
      active_session_id: activeSessionId,
      used_fallback: usedFallback,
      parent_status: sessionDetail.state?.status || '',
      parent_turns: sessionDetail.state?.turn || 0,
      parent_tool_calls: (sessionDetail.messages || []).reduce((count, msg) => count + (msg.tool_calls || []).length, 0),
      child_status: childDetail.state?.status || '',
      queue_status: queueDetail?.status || '',
      background_notifications: (sessionDetail.background_notifications || []).length,
      timeline_entries: (sessionDetail.timeline || []).length,
      task_items: (sessionDetail.task_board?.tasks || []).length,
      todo_items: (sessionDetail.task_board?.todo || []).length,
    };

    if (!keepHistory) {
      await click('[data-view="history"]', 'history nav before clear');
      await waitFor(
        () => browserClient.evaluate(`document.getElementById('history-view')?.textContent?.includes('Clear sessions')`),
        15000,
        'history view before clear'
      );
      await browserClient.evaluate(`(() => {
        window.__codexOriginalConfirm = window.confirm;
        window.confirm = () => true;
      })()`);
      let historyCleared = false;
      let usedLocalClearConfirm = false;
      const historyEmpty = () => browserClient.evaluate(`(() => {
        const active = document.querySelector('.nav-item.active[data-view="history"]');
        const text = document.getElementById('history-view')?.textContent || '';
        return Boolean(active) && (text.includes('No history yet.') || text.includes('No saved sessions yet.'));
      })()`);
      for (let attempt = 0; attempt < 3 && !historyCleared; attempt += 1) {
        await click('[data-history-clear]', `clear history attempt ${attempt + 1}`);
        await waitFor(
          () => browserClient.evaluate(`(() => {
            const active = document.querySelector('.nav-item.active[data-view="history"]');
            const text = document.getElementById('history-view')?.textContent || '';
            const empty = Boolean(active) && (text.includes('No history yet.') || text.includes('No saved sessions yet.'));
            return empty || Boolean(document.querySelector('.confirm-dialog-confirm'));
          })()`),
          5000,
          'history clear confirmation'
        );
        usedLocalClearConfirm = await browserClient.evaluate(`(() => {
          const button = document.querySelector('.confirm-dialog-confirm');
          if (!button) return false;
          button.click();
          return true;
        })()`) || usedLocalClearConfirm;
        try {
          await waitFor(historyEmpty, 5000, 'history stays active after clear');
          historyCleared = true;
        } catch (err) {
          if (attempt === 2) {
            throw err;
          }
          await sleep(500);
        }
      }
      results.interactions.history_clear_local_confirm = usedLocalClearConfirm;
      await browserClient.evaluate(`(() => {
        if (window.__codexOriginalConfirm) {
          window.confirm = window.__codexOriginalConfirm;
          delete window.__codexOriginalConfirm;
        }
      })()`);
      results.interactions.history_clear_keeps_view = true;
    } else {
      results.interactions.history_preserved_for_durable_audit = true;
    }

    if ((results.runtime_exceptions || []).length > 0) {
      throw new Error(`runtime exceptions detected: ${results.runtime_exceptions.join(' | ')}`);
    }
    if ((results.console_errors || []).length > 0) {
      throw new Error(`console errors detected: ${results.console_errors.join(' | ')}`);
    }

    const domHTML = await browserClient.evaluate('document.documentElement.outerHTML');
    await writeFile(outputPath, JSON.stringify(results, null, 2));
    await writeFile(domPath, domHTML);
  } catch (err) {
    if (browserClient) {
      try {
        const diagnostics = await browserClient.evaluate(`(() => {
          const root = document.getElementById('chat-messages');
          const view = document.querySelector('.nav-item.active[data-view]')?.getAttribute('data-view') || '';
          return {
            current_view: view,
            session_chip: document.getElementById('session-id-display')?.textContent || '',
            is_generating: typeof state !== 'undefined' ? Boolean(state.isGenerating) : false,
            session_backed: typeof state !== 'undefined' ? Boolean(state.sessionBacked) : false,
            pending_stage_visible: Boolean(document.querySelector('.pending-stage-card')),
            chat_text_excerpt: (root?.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 1000),
            tool_activity_count: root ? root.querySelectorAll('.tool-lane, .tl-row-call, .tl-row-result, .tool-card').length : 0,
            timeline_count: root ? root.querySelectorAll('.timeline-card').length : 0,
            history_text_excerpt: (document.getElementById('history-view')?.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 500)
          };
        })()`);
        const failedResults = results || {
          base_url: baseURL,
          interactions: {},
          runtime_exceptions: browserClient.exceptions,
          console_errors: browserClient.consoleErrors,
        };
        failedResults.failure = err?.message || String(err);
        failedResults.failure_diagnostics = diagnostics;
        await writeFile(outputPath, JSON.stringify(failedResults, null, 2));
        const domHTML = await browserClient.evaluate('document.documentElement.outerHTML');
        await writeFile(domPath, domHTML);
      } catch (writeErr) {
        console.error(`failed to write ui smoke diagnostics: ${writeErr?.message || String(writeErr)}`);
      }
    }
    throw err;
  } finally {
    for (const workspacePath of workspaceCleanupPaths.reverse()) {
      await fetchJSON(`${baseURL}/api/files?path=${encodeURIComponent(workspacePath)}`, { method: 'DELETE' }).catch(() => {});
    }
    if (browserClient) {
      await browserClient.close().catch(() => {});
    }
    if (chrome.exitCode === null) {
      chrome.kill('SIGTERM');
    }
    await rm(tempRoot, { recursive: true, force: true }).catch(() => {});
  }
}

main().catch(async (error) => {
  console.error(error?.stack || String(error));
  process.exitCode = 1;
});
