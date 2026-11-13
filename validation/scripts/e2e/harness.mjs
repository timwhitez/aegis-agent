// Shared infrastructure for the Web console browser acceptance suite
// (npm run test:e2e). Builds the binaries, renders a temporary config,
// starts the scripted provider and the real web service on random ports,
// launches Chromium, records checks / screenshots / browser errors and
// writes manifest.json. Areas live in sibling modules; nothing here knows
// about individual UI flows.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { access, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { constants as fsConstants } from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';
import { chromium } from 'playwright';
import { auditScreen } from './audits.mjs';
import { KNOWN_UI_BUGS } from './known-bugs.mjs';

export const DESKTOP = Object.freeze({ width: 1440, height: 900 });
export const MOBILE = Object.freeze({ width: 390, height: 844 });
export const LOCALE_KEY = 'aegis-agent.locale.v1';

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const slug = (text) => String(text).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 70);

export async function createHarness({ repoRoot, outputDir, only = [], keepRuntime = false }) {
  const startedAt = Date.now();
  const tmpRoot = await realpath(os.tmpdir());
  const runtimeRoot = await mkdtemp(path.join(tmpRoot, 'aegis-web-e2e.'));
  const cwd = path.join(runtimeRoot, 'workspace');
  const paths = {
    runtime: runtimeRoot,
    cwd,
    files: path.join(cwd, 'workspace'),
    sessions: path.join(runtimeRoot, 'sessions'),
    skills: path.join(cwd, 'skills'),
    config: path.join(runtimeRoot, 'config.yaml'),
    binary: path.join(runtimeRoot, 'aegis-agent'),
    providerBinary: path.join(runtimeRoot, 'budgetsmokeprovider'),
    providerLog: path.join(outputDir, 'provider-decisions.jsonl'),
  };
  const children = [];
  const processLogs = [];
  const checks = [];
  const screenshots = [];
  const allowances = [];
  const unexpected = [];
  const ignored = { aborted_gets: 0 };
  const knownSeen = new Map();
  const contexts = new Set();
  const evidence = {};
  let currentArea = 'setup';
  let browser = null;
  let executablePath = '';

  const h = {
    repoRoot, outputDir, paths, only, evidence, DESKTOP, MOBILE,
    baseURL: '', providerURL: '', delay,
    get area() { return currentArea; },
    get browser() { return browser; },
  };

  // ---- processes -------------------------------------------------------------------
  function startProcess(command, args, { label, env = {}, cwd: dir = cwd } = {}) {
    const output = [];
    const child = spawn(command, args, { cwd: dir, env: { ...process.env, TMPDIR: tmpRoot + '/', ...env }, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.on('data', (chunk) => { output.push(String(chunk)); if (output.length > 4000) output.splice(0, 2000); });
    child.stderr.on('data', (chunk) => { output.push(String(chunk)); if (output.length > 4000) output.splice(0, 2000); });
    const record = { child, label, output };
    child.once('exit', (code, signal) => processLogs.push({ label, code, signal, tail: output.join('').slice(-6000) }));
    children.push(record);
    return record;
  }

  function runCommand(command, args, label) {
    return new Promise((resolve, reject) => {
      const child = spawn(command, args, { cwd: repoRoot, env: process.env, stdio: ['ignore', 'pipe', 'pipe'] });
      const output = [];
      child.stdout.on('data', (chunk) => output.push(String(chunk)));
      child.stderr.on('data', (chunk) => output.push(String(chunk)));
      child.once('error', reject);
      child.once('exit', (code) => {
        if (code === 0) resolve();
        else reject(new Error(label + ' failed (' + code + '): ' + output.join('').slice(-4000)));
      });
    });
  }

  async function stopProcesses() {
    for (const record of children.splice(0).reverse()) {
      if (record.child.exitCode !== null || record.child.signalCode) continue;
      record.child.kill('SIGTERM');
      await Promise.race([new Promise((resolve) => record.child.once('exit', resolve)), delay(5000)]);
      if (record.child.exitCode === null && !record.child.signalCode) record.child.kill('SIGKILL');
    }
  }
  const killNow = () => { for (const record of children) { try { record.child.kill('SIGKILL'); } catch { /* gone */ } } };
  process.once('exit', killNow);
  for (const signal of ['SIGINT', 'SIGTERM']) {
    process.once(signal, () => { killNow(); process.exit(130); });
  }

  function reservePort() {
    return new Promise((resolve, reject) => {
      const server = net.createServer();
      server.once('error', reject);
      server.listen(0, '127.0.0.1', () => {
        const { port } = server.address();
        server.close((error) => (error ? reject(error) : resolve(port)));
      });
    });
  }

  async function until(fn, label, timeout = 25000, interval = 100) {
    const deadline = Date.now() + timeout;
    let last;
    while (Date.now() < deadline) {
      try {
        const value = await fn();
        if (value) return value;
      } catch (error) {
        last = error;
      }
      await delay(interval);
    }
    throw new Error('timed out waiting for ' + label + (last ? ' (' + String(last.message || last).slice(0, 300) + ')' : ''));
  }
  h.until = until;

  async function waitHTTP(url, timeout) {
    await until(async () => (await fetch(url)).ok, url, timeout, 100);
  }

  h.start = async () => {
    await mkdir(outputDir, { recursive: true });
    await mkdir(paths.files, { recursive: true });
    await mkdir(paths.skills, { recursive: true });
    await mkdir(paths.sessions, { recursive: true, mode: 0o700 });
    const go = process.env.GO || 'go';
    await Promise.all([
      runCommand(go, ['build', '-o', paths.binary, './cmd/aegis-agent'], 'build aegis-agent'),
      runCommand(go, ['build', '-o', paths.providerBinary, './validation/cmd/budgetsmokeprovider'], 'build provider fixture'),
    ]);
    const providerPort = await reservePort();
    const webPort = await reservePort();
    h.providerURL = 'http://127.0.0.1:' + providerPort;
    h.baseURL = 'http://127.0.0.1:' + webPort;
    await writeFile(paths.config, renderConfig({ providerURL: h.providerURL, sessionRoot: paths.sessions, skillsRoot: paths.skills }), { mode: 0o600 });
    startProcess(paths.providerBinary, ['--listen', '127.0.0.1:' + providerPort, '--log', paths.providerLog], { label: 'provider' });
    await waitHTTP(h.providerURL + '/healthz', 15000);
    startProcess(paths.binary, ['web', '--config', paths.config, '--listen', '127.0.0.1:' + webPort, '--workers', process.env.E2E_WEB_WORKERS || '2'], {
      label: 'web', env: { E2E_API_KEY: 'local-fixture-key' },
    });
    await waitHTTP(h.baseURL + '/api/meta', 20000);
    executablePath = await findChrome();
    browser = await chromium.launch({
      executablePath,
      headless: true,
      args: typeof process.getuid === 'function' && process.getuid() === 0 ? ['--no-sandbox'] : [],
    });
  };

  h.stop = async () => {
    for (const context of contexts) await context.close().catch(() => {});
    if (browser) await browser.close().catch(() => {});
    await stopProcesses();
    if (!keepRuntime) await rm(runtimeRoot, { recursive: true, force: true }).catch(() => {});
  };

  // ---- REST helpers ------------------------------------------------------------------
  /** api / detail / waitDetail / startSession against the web service at baseOf(). */
  function restHelpers(baseOf) {
    const rest = {};
    rest.api = async (method, url, body) => {
      const response = await fetch(baseOf() + url, {
        method,
        headers: method === 'GET' ? {} : { 'Content-Type': 'application/json', 'X-Aegis-Agent-Web': '1' },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await response.text();
      let json = null;
      try { json = text ? JSON.parse(text) : null; } catch { json = null; }
      return { status: response.status, json, text, headers: response.headers };
    };
    rest.detail = async (id) => {
      const result = await rest.api('GET', '/api/sessions/' + encodeURIComponent(id) + '?limit=40');
      if (result.status !== 200) throw new Error('GET session ' + id + ': ' + result.status + ' ' + result.text.slice(0, 300));
      return result.json;
    };
    rest.waitDetail = (id, predicate, label = 'state', timeout = 120000) => until(async () => {
      const detail = await rest.detail(id);
      return predicate(detail) ? detail : null;
    }, 'session ' + id + ' ' + label, timeout, 120);
    rest.startSession = async (payload) => {
      const result = await rest.api('POST', '/api/sessions/start', typeof payload === 'string' ? { prompt: payload } : payload);
      assert.equal(result.status, 202, result.text);
      assert.ok(result.json?.session_id, 'start returned a session id');
      return result.json.session_id;
    };
    return rest;
  }
  Object.assign(h, restHelpers(() => h.baseURL));
  h.settled = (detail) => detail && !['running', 'queued', 'pending'].includes(detail.state?.status) && !detail.active_handle;

  /**
   * Starts an extra web service with its own config, session/skills
   * directories and workspace (same binaries and scripted provider), e.g. for
   * README screenshots that must not list the suite's other sessions.
   * Returns { label, baseURL, paths, api, detail, waitDetail, startSession, stop }.
   */
  h.startRuntime = async (label) => {
    const dir = path.join(runtimeRoot, 'runtime-' + slug(label));
    const rt = { label, baseURL: '', paths: { root: dir, cwd: path.join(dir, 'workspace'), sessions: path.join(dir, 'sessions'), skills: path.join(dir, 'skills'), config: path.join(dir, 'config.yaml') } };
    await mkdir(rt.paths.cwd, { recursive: true });
    await mkdir(rt.paths.skills, { recursive: true });
    await mkdir(rt.paths.sessions, { recursive: true, mode: 0o700 });
    await writeFile(rt.paths.config, renderConfig({ providerURL: h.providerURL, sessionRoot: rt.paths.sessions, skillsRoot: rt.paths.skills }), { mode: 0o600 });
    const port = await reservePort();
    rt.baseURL = 'http://127.0.0.1:' + port;
    const record = startProcess(paths.binary, ['web', '--config', rt.paths.config, '--listen', '127.0.0.1:' + port, '--workers', process.env.E2E_WEB_WORKERS || '2'], {
      label: 'web-' + slug(label), env: { E2E_API_KEY: 'local-fixture-key' }, cwd: rt.paths.cwd,
    });
    await waitHTTP(rt.baseURL + '/api/meta', 20000);
    Object.assign(rt, restHelpers(() => rt.baseURL));
    rt.stop = async () => {
      const index = children.indexOf(record);
      if (index >= 0) children.splice(index, 1);
      if (record.child.exitCode !== null || record.child.signalCode) return;
      record.child.kill('SIGTERM');
      await Promise.race([new Promise((resolve) => record.child.once('exit', resolve)), delay(5000)]);
      if (record.child.exitCode === null && !record.child.signalCode) record.child.kill('SIGKILL');
    };
    return rt;
  };

  // Shared fixtures, created once on demand (areas may also register their own).
  const fixtureCache = new Map();
  h.fixture = (name, create) => {
    if (!fixtureCache.has(name)) {
      const promise = Promise.resolve().then(create);
      promise.catch(() => fixtureCache.delete(name));
      fixtureCache.set(name, promise);
    }
    return fixtureCache.get(name);
  };
  h.registerFixture = (name, value) => fixtureCache.set(name, Promise.resolve(value));

  // ---- browser contexts and error collection --------------------------------------------
  h.newContext = async ({ locale = 'zh-CN', mobile = false, viewport, seedLocale = true, downloads = false } = {}) => {
    const context = await browser.newContext({
      viewport: viewport || (mobile ? MOBILE : DESKTOP),
      colorScheme: 'dark',
      acceptDownloads: downloads,
      ...(mobile ? { isMobile: true, hasTouch: true } : {}),
    });
    if (seedLocale) {
      // Seed the stored preference once per context; later toggles persist normally.
      await context.addInitScript(({ key, value }) => {
        try { if (!localStorage.getItem(key)) localStorage.setItem(key, value); } catch { /* storage blocked */ }
      }, { key: LOCALE_KEY, value: locale });
    }
    contexts.add(context);
    context.once('close', () => contexts.delete(context));
    return context;
  };

  h.newPage = async (context, label = 'page') => {
    const page = await context.newPage();
    h.watch(page, label);
    return page;
  };

  h.watch = (page, label) => {
    page.on('console', (message) => {
      if (message.type() !== 'error') return;
      collect({ kind: 'console', page: label, text: message.text(), url: message.location()?.url || '' });
    });
    page.on('pageerror', (error) => collect({ kind: 'page', page: label, text: String(error?.stack || error) }));
    page.on('requestfailed', (request) => {
      const failure = request.failure()?.errorText || '';
      if (/ERR_ABORTED/.test(failure) && request.method() === 'GET') {
        // Navigations and context teardown cancel in-flight polling GETs.
        ignored.aborted_gets += 1;
        return;
      }
      collect({ kind: 'request', page: label, url: request.url(), method: request.method(), text: failure });
    });
    page.on('response', (response) => {
      if (response.status() < 400) return;
      collect({ kind: 'http', page: label, url: response.url(), method: response.request().method(), status: response.status() });
    });
  };

  function collect(entry) {
    entry.area = currentArea;
    entry.at = new Date().toISOString();
    const match = allowances.find((item) => item.active && (!item.limit || item.hits < item.limit) && item.match(entry));
    if (match) {
      match.hits += 1;
      if (match.samples.length < 3) match.samples.push(entry.kind + ' ' + (entry.method || '') + ' ' + (entry.url || entry.text).slice(0, 200) + (entry.status ? ' ' + entry.status : ''));
      return;
    }
    unexpected.push(entry);
  }

  /** Declares an error the next step provokes on purpose (active until the area ends). */
  h.allow = (label, match, { limit = 0 } = {}) => {
    const entry = { label, area: currentArea, match, limit, hits: 0, samples: [], active: true };
    allowances.push(entry);
    return entry;
  };
  const urlMatches = (pattern, url) => (pattern instanceof RegExp ? pattern.test(url) : url.includes(pattern));
  h.allowHTTP = (label, { url, status, method, limit }) => h.allow(label, (e) =>
    (e.kind === 'http' && urlMatches(url, e.url) && (!status || e.status === status) && (!method || e.method === method))
    || (e.kind === 'console' && urlMatches(url, e.url) && /Failed to load resource: the server responded with a status of/.test(e.text) && (!status || e.text.includes(String(status)))), { limit });
  h.allowAbort = (label, { url, method, limit }) => h.allow(label, (e) =>
    (e.kind === 'request' && urlMatches(url, e.url) && (!method || e.method === method))
    || (e.kind === 'console' && urlMatches(url, e.url) && /net::ERR_FAILED/.test(e.text)), { limit });
  h.unexpectedErrors = () => unexpected.slice();

  // ---- checks ------------------------------------------------------------------------
  h.runArea = async (area, fn) => {
    currentArea = area;
    const before = unexpected.length;
    const t0 = Date.now();
    process.stdout.write('\n== ' + area + '\n');
    try {
      await fn(h);
    } catch (error) {
      record({ area, name: 'area aborted', ok: false, ms: Date.now() - t0, error: String(error?.stack || error).slice(0, 3000) });
      process.stdout.write('FAIL ' + area + ': area aborted\n     ' + String(error?.message || error).split('\n').slice(0, 6).join('\n     ') + '\n');
    }
    // One round trip per open page flushes console/requestfailed events of the last step.
    for (const context of contexts) for (const open of context.pages()) await open.evaluate(() => 0).catch(() => {});
    const errors = unexpected.slice(before);
    record({ area, name: 'no unexpected console/page/request/HTTP errors', ok: errors.length === 0, ms: 0,
      error: errors.length ? JSON.stringify(errors.slice(0, 12), null, 1).slice(0, 4000) : undefined });
    if (errors.length) process.stdout.write('FAIL ' + area + ': ' + errors.length + ' unexpected browser errors\n     ' + errors.slice(0, 5).map((e) => e.kind + ' ' + (e.method || '') + ' ' + (e.url || '') + ' ' + (e.status || '') + ' ' + (e.text || '').slice(0, 160)).join('\n     ') + '\n');
    for (const item of allowances) if (item.area === area) item.active = false;
    return Date.now() - t0;
  };

  function record(entry) {
    checks.push(entry);
  }

  h.check = async (name, fn, { page } = {}) => {
    const t0 = Date.now();
    try {
      await fn();
      record({ area: currentArea, name, ok: true, ms: Date.now() - t0 });
      process.stdout.write('ok   ' + name + ' (' + (Date.now() - t0) + 'ms)\n');
      return true;
    } catch (error) {
      const entry = { area: currentArea, name, ok: false, ms: Date.now() - t0, error: String(error?.stack || error).slice(0, 3000) };
      if (page && !page.isClosed()) {
        const file = 'fail-' + slug(currentArea) + '-' + slug(name) + '.png';
        await page.screenshot({ path: path.join(outputDir, file) }).then(() => { entry.screenshot = file; }).catch(() => {});
      }
      record(entry);
      process.stdout.write('FAIL ' + name + '\n     ' + String(error?.message || error).split('\n').slice(0, 8).join('\n     ') + '\n');
      return false;
    }
  };

  /** Records a reproduced KNOWN_UI_BUGS entry for a behavioural check (counted, not failing the run). */
  h.reportKnownBug = (id, name, detail, screenshot) => {
    const bug = KNOWN_UI_BUGS.find((item) => item.id === id);
    if (!bug) throw new Error('unknown bug id ' + id);
    if (!knownSeen.has(id)) knownSeen.set(id, { ...bug, test: undefined, screenshots: [] });
    if (screenshot) knownSeen.get(id).screenshots.push(screenshot);
    record({ area: currentArea, name, ok: true, known_failure: [id + ': ' + detail], screenshot, ms: 0 });
    process.stdout.write('KNOWN ' + name + ': ' + id + '\n');
  };

  // ---- screenshots + audits ------------------------------------------------------------
  /**
   * capture(page, name, { audit = true, retainToasts = false, element })
   * Waits for finite animations, runs the cross-cutting audits for the
   * current locale/viewport, then writes the PNG and its sha256.
   */
  h.capture = async (page, name, { audit = true, retainToasts = false, element, allowLatin = [] } = {}) => {
    if (!retainToasts) {
      // A toast may auto-dismiss between all() and the click: never wait on a vanished node.
      for (const button of await page.locator('[data-testid="toast"] .toast-close').all()) await button.dispatchEvent('click', undefined, { timeout: 500 }).catch(() => {});
      await page.locator('[data-testid="toast"]').first().waitFor({ state: 'detached', timeout: 3000 }).catch(() => {});
    }
    await settleVisuals(page);
    const file = name.endsWith('.png') ? name : name + '.png';
    let audits = null;
    if (audit) {
      audits = await auditScreen(page, { allowLatin });
      const known = [];
      for (const [kind, list] of Object.entries(audits)) {
        if (!Array.isArray(list)) continue;
        audits[kind] = list.filter((violation) => {
          const bug = KNOWN_UI_BUGS.find((item) => item.audit === kind && item.test(violation));
          if (!bug) return true;
          known.push(bug.id + ': ' + violation);
          if (!knownSeen.has(bug.id)) knownSeen.set(bug.id, { ...bug, test: undefined, screenshots: [] });
          const seen = knownSeen.get(bug.id);
          if (!seen.screenshots.includes(file)) seen.screenshots.push(file);
          return false;
        });
      }
      const violations = Object.entries(audits).filter(([, value]) => Array.isArray(value) && value.length);
      record({ area: currentArea, name: 'audit ' + file, ok: violations.length === 0, ms: 0, screenshot: file,
        ...(known.length ? { known_failure: known.slice(0, 10) } : {}),
        error: violations.length ? JSON.stringify(Object.fromEntries(violations), null, 1).slice(0, 4000) : undefined });
      if (violations.length) process.stdout.write('FAIL audit ' + file + '\n     ' + JSON.stringify(Object.fromEntries(violations)).slice(0, 900) + '\n');
      else if (known.length) process.stdout.write('KNOWN audit ' + file + ': ' + known.map((item) => item.split(':')[0]).filter((v, i, a) => a.indexOf(v) === i).join(', ') + '\n');
    }
    const target = path.join(outputDir, file);
    if (element) await element.screenshot({ path: target });
    else await page.screenshot({ path: target });
    const data = await readFile(target);
    screenshots.push({
      name: file,
      area: currentArea,
      viewport: page.viewportSize(),
      locale: await page.evaluate(() => document.documentElement.lang),
      url: new URL(page.url()).pathname,
      sha256: createHash('sha256').update(data).digest('hex'),
      bytes: data.length,
      audits: audits ? Object.fromEntries(Object.entries(audits).map(([key, value]) => [key, Array.isArray(value) ? value.length : value])) : null,
    });
    return file;
  };

  h.writeManifest = async ({ commit, durations }) => {
    const failed = checks.filter((item) => !item.ok);
    const manifest = {
      schema_version: 2,
      suite: 'webconsole-e2e',
      commit,
      created_at: new Date().toISOString(),
      duration_ms: Date.now() - startedAt,
      area_durations_ms: durations,
      only: only.length ? only : 'all',
      base_url: h.baseURL,
      browser: browser ? { name: 'chromium', version: browser.version(), executable_path: executablePath } : null,
      node: process.version,
      platform: process.platform + '-' + process.arch,
      viewports: [{ name: 'desktop', ...DESKTOP }, { name: 'mobile', ...MOBILE }],
      locales: ['zh-CN', 'en'],
      color_scheme: 'dark (forced; the app must stay light)',
      result: failed.length ? 'failed' : 'passed',
      totals: { checks: checks.length, failed: failed.length, known_failures: checks.filter((item) => item.known_failure).length, screenshots: screenshots.length },
      checks,
      screenshots,
      known_ui_bugs: [...knownSeen.values()],
      known_ui_bugs_not_observed: KNOWN_UI_BUGS.filter((bug) => !knownSeen.has(bug.id)).map(({ id, title }) => ({ id, title })),
      expected_errors: allowances.map(({ label, area, hits, samples }) => ({ label, area, hits, samples })),
      unexpected_errors: unexpected,
      ignored,
      evidence,
      process_logs: failed.length ? processLogs.concat(children.map((c) => ({ label: c.label, running: true, tail: c.output.join('').slice(-6000) }))) : processLogs.map(({ label, code, signal }) => ({ label, code, signal })),
    };
    await writeFile(path.join(outputDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', { mode: 0o644 });
    return manifest;
  };

  h.checks = checks;
  h.knownBugs = () => [...knownSeen.values()];
  h.screenshots = screenshots;
  return h;
}

async function settleVisuals(page) {
  await page.evaluate(() => document.fonts?.ready).catch(() => {});
  await page.waitForFunction(() => document.getAnimations().every((animation) => {
    if (animation.playState !== 'running') return true;
    const timing = animation.effect?.getComputedTiming?.();
    return !timing || timing.iterations === Infinity || timing.endTime === Infinity;
  }), null, { timeout: 4000 }).catch(() => {});
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}

export function renderConfig({ providerURL, sessionRoot, skillsRoot }) {
  return [
    'schema_version: 1',
    'default_provider: e2e',
    'providers:',
    '  e2e:',
    '    api_provider: openai-compatible',
    '    api_key_env: E2E_API_KEY',
    '    base_url: ' + JSON.stringify(providerURL),
    '    model: e2e-model',
    '    request_timeout_sec: 30',
    '    stream_idle_timeout_ms: 30000',
    '    retry:',
    '      max_attempts: 1',
    '    max_output_tokens: 2048',
    '    context_window_tokens: 200000',
    '    store: false',
    'web:',
    '  legacy_ui_enabled: false',
    'session:',
    '  dir: ' + JSON.stringify(sessionRoot),
    '  dir_mode: "0700"',
    'skills:',
    '  dirs:',
    '    - ' + JSON.stringify(skillsRoot),
    'runtime:',
    '  guardrails_mode: yolo',
    '  max_turns_soft: 24',
    '  max_turns_hard: -1',
    '  command_timeout_sec: 30',
    '  exec_policy:',
    '    mode: warn',
    '  shell_env_allowlist: [PATH, HOME, LANG, TERM]',
    '  compact:',
    '    input_char_threshold: 0',
    '    keep_recent_tool_results: 3',
    '    keep_recent_tool_result_bytes: 65536',
    '    hysteresis_delta_chars: 0',
    '    keep_recent_messages: 0',
    '    utilization_factor: 0.85',
    '    semantic_summary:',
    '      enabled: false',
    '  queue:',
    '    auto_worker: false',
    '    reaper_interval_ms: -1',
    '',
  ].join('\n');
}

async function findChrome() {
  const candidates = [process.env.GOOGLE_CHROME_BIN, process.env.CHROME_BIN, chromium.executablePath(),
    '/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/usr/bin/chromium', '/usr/bin/chromium-browser'].filter(Boolean);
  for (const candidate of candidates) {
    try {
      await access(candidate, fsConstants.X_OK);
      return candidate;
    } catch {
      // try the next candidate
    }
  }
  throw new Error('Chrome/Chromium executable not found; run npx playwright install chromium or set GOOGLE_CHROME_BIN');
}

/** Minimal stored (uncompressed) zip writer for skill fixtures. */
export function storedZip(entries) {
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
  return Buffer.concat([...localParts, centralData, end]);
}

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}
