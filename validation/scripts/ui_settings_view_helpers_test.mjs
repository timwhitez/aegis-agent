import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import * as S from '../../internal/webconsole/ui/src/lib/settings-model.js';
import * as H from '../../internal/webconsole/ui/src/features/settings/settings-helpers.js';
import * as api from '../../internal/webconsole/ui/src/lib/api.js';

function config(extra = {}) {
  return {
    default_provider: 'openai',
    guardrails_mode: 'yolo',
    max_turns_soft: 24,
    max_turns_hard: -1,
    disable_hard_turn_limit: true,
    child_budget: { disabled: true, max_active_runtime_sec: 0, max_elapsed_sec: 0, max_turns_per_attempt: 0 },
    role_providers: { planner: { model: 'plan-m', max_output_tokens: 1024 } },
    providers: {
      openai: { has_key: true, api_provider: '', effective_api_provider: 'openai-compatible', base_url: 'https://example.invalid/v1', model: 'gpt-x', reasoning_mode: 'default', reasoning_summary: 'default' },
      claude: { has_key: false, api_provider: 'anthropic-compatible', effective_api_provider: 'anthropic-compatible', base_url: '', model: 'claude-x', reasoning_mode: 'max', reasoning_summary: 'default' },
    },
    ...extra,
  };
}

test('a freshly loaded form has no field errors', () => {
  assert.deepEqual(H.collectFieldErrors(S.settingsFormFromConfig(config())), {});
});

test('field errors are collected per field with lib keys and params', () => {
  const form = S.settingsFormFromConfig(config());
  form.maxTurnsSoft = '0';
  form.disableHardTurnLimit = false;
  form.maxTurnsHard = '';
  form.contextWindowTokens = '1.5';
  form.provider = 'missing';
  form.childBudget = { enabled: true, activeRuntime: '1h30', elapsed: '2x', maxTurns: '-1' };
  form.roleProviders = form.roleProviders.map((row) => (row.role === 'explorer' ? { ...row, max_output_tokens: 'abc' } : row));
  const errors = H.collectFieldErrors(form);
  assert.deepEqual(Object.keys(errors).sort(), ['childActiveRuntime', 'childElapsed', 'childMaxTurns', 'contextWindowTokens', 'maxTurnsHard', 'maxTurnsSoft', 'provider', 'role.explorer.max_output_tokens'].sort());
  assert.deepEqual(errors.maxTurnsSoft, { key: 'settings.error.softTurns', params: {} });
  assert.deepEqual(errors.maxTurnsHard, { key: 'settings.error.hardTurns', params: {} });
  assert.deepEqual(errors.childActiveRuntime, { key: 'settings.error.duration', params: { field: 'settings.field.childActiveRuntime' } });
  assert.deepEqual(errors.childMaxTurns, { key: 'settings.error.nonNegativeInt', params: { field: 'settings.field.childMaxTurns' } });
  assert.deepEqual(errors.provider, { key: 'settings.error.unknownProvider', params: { provider: 'missing' } });
  assert.deepEqual(errors['role.explorer.max_output_tokens'], { key: 'settings.error.nonNegativeInt', params: { field: 'settings.field.roleMaxOutputTokens' } });
  assert.equal(H.firstErrorField(errors), 'provider');
});

test('hard-limit and budget errors only apply when those controls are enabled', () => {
  const form = S.settingsFormFromConfig(config());
  form.maxTurnsHard = 'nope';
  form.childBudget = { enabled: false, activeRuntime: 'garbage', elapsed: '', maxTurns: '' };
  assert.deepEqual(H.collectFieldErrors(form), {});
  form.childBudget = { enabled: true, activeRuntime: '', elapsed: '0', maxTurns: '' };
  assert.deepEqual(H.collectFieldErrors(form), { childBudget: { key: 'settings.error.childBudgetEmpty', params: {} } });
  assert.equal(H.firstErrorField({ childBudget: {}, maxTurnsSoft: {} }), 'maxTurnsSoft');
  assert.equal(H.firstErrorField({}), '');
});

test('collectFieldErrors agrees with buildConfigSavePayload', () => {
  const valid = S.settingsFormFromConfig(config());
  valid.childBudget = { enabled: true, activeRuntime: '30m', elapsed: '2h', maxTurns: '50' };
  assert.deepEqual(H.collectFieldErrors(valid), {});
  assert.doesNotThrow(() => S.buildConfigSavePayload(valid));
  const invalid = { ...valid, maxTurnsSoft: '-2' };
  assert.notDeepEqual(H.collectFieldErrors(invalid), {});
  assert.throws(() => S.buildConfigSavePayload(invalid));
});

test('field DOM ids are stable kebab-case', () => {
  assert.equal(H.fieldDomId('maxTurnsSoft'), 'st-max-turns-soft');
  assert.equal(H.fieldDomId('childActiveRuntime'), 'st-child-active-runtime');
  assert.equal(H.fieldDomId('role.planner.max_output_tokens'), 'st-role-planner-max-output-tokens');
  assert.equal(H.fieldDomId('contextWindowTokens'), 'st-context-window-tokens');
});

test('dirty tracking ignores masked keys, disabled sections and whitespace', () => {
  const form = S.settingsFormFromConfig(config());
  const base = H.formSnapshot(form);
  assert.equal(H.formSnapshot({ ...form }), base);
  assert.equal(H.formSnapshot({ ...form, apiKey: '' }), base, 'empty replacement keeps the stored key');
  assert.notEqual(H.formSnapshot({ ...form, apiKey: 'sk-new' }), base);
  assert.equal(H.formSnapshot({ ...form, model: ' gpt-x ' }), base);
  assert.notEqual(H.formSnapshot({ ...form, model: 'gpt-y' }), base);
  assert.equal(H.formSnapshot({ ...form, maxTurnsHard: '999' }), base, 'hard value is ignored while the limit is off');
  assert.notEqual(H.formSnapshot({ ...form, disableHardTurnLimit: false }), base);
  assert.equal(H.formSnapshot({ ...form, childBudget: { ...form.childBudget, activeRuntime: '9h' } }), base, 'disabled budget values are ignored');
  const roles = form.roleProviders.map((row) => (row.role === 'planner' ? { ...row, model: 'other' } : row));
  assert.notEqual(H.formSnapshot({ ...form, roleProviders: roles }), base);
  assert.equal(H.formSnapshot({ ...form, ...S.providerFormFields(config(), 'openai') }), base, 'switching back to the original profile is clean');
  assert.notEqual(H.formSnapshot({ ...form, ...S.providerFormFields(config(), 'claude') }), base);
  assert.equal(H.formSnapshot(null), '');
});

async function captureBody(call) {
  const original = globalThis.fetch;
  let captured = null;
  globalThis.fetch = async (url, options) => {
    captured = { url, options };
    return { ok: true, status: 200, statusText: 'OK', json: async () => ({ ok: true }) };
  };
  try {
    await call();
  } finally {
    globalThis.fetch = original;
  }
  return { url: captured.url, method: captured.options.method, headers: captured.options.headers, body: JSON.parse(captured.options.body) };
}

test('save input reproduces the settings-model DTO on the wire', async () => {
  const form = S.settingsFormFromConfig(config());
  form.childBudget = { enabled: true, activeRuntime: '1h30m', elapsed: '', maxTurns: '320' };
  form.apiKey = 'sk-local';
  const dto = S.buildConfigSavePayload(form);
  const sent = await captureBody(() => api.saveConfig(H.toSaveConfigInput(dto)));
  assert.equal(sent.url, '/api/config');
  assert.equal(sent.method, 'POST');
  assert.equal(sent.headers['X-Aegis-Agent-Web'], '1');
  assert.deepEqual(sent.body, dto);
  assert.equal('legacy_ui_enabled' in sent.body, false);
});

test('test input reproduces the probe DTO and never carries budget or turn guard fields', async () => {
  const form = S.settingsFormFromConfig(config());
  form.contextWindowTokens = '128000';
  const payload = S.buildConfigTestPayload(form);
  const sent = await captureBody(() => api.testConfig(H.toTestConfigInput(payload)));
  assert.equal(sent.url, '/api/config/test');
  assert.deepEqual(sent.body, payload);
  for (const key of ['child_budget', 'max_turns_soft', 'max_turns_hard', 'role_providers', 'guardrails_mode']) assert.equal(key in sent.body, false, key);
});

test('probe fact rows and observations', () => {
  const raw = { success: true, provider: 'openai', effective_api_provider: 'openai-compatible', model: 'gpt-x', reasoning_mode: 'high', stop_reason: 'end_turn', thinking_budget: 0, max_output_tokens: 2048, include_thoughts: false, reasoning_summary_observed: true, reasoning_tokens: 7 };
  const interpreted = S.interpretProbeResult(raw);
  const rows = H.probeFactRows(interpreted, raw);
  assert.deepEqual(rows.map((row) => row.key), [
    'settings.probe.fact.provider', 'settings.probe.fact.apiProvider', 'settings.probe.fact.model', 'settings.probe.fact.stopReason',
    'settings.probe.fact.includeThoughts', 'settings.probe.fact.maxOutputTokens', 'settings.probe.fact.reasoningTokens',
  ]);
  assert.deepEqual(rows.find((row) => row.key === 'settings.probe.fact.includeThoughts'), { key: 'settings.probe.fact.includeThoughts', value: 'false', bool: false });
  assert.deepEqual(H.probeObservations(interpreted).map((item) => item.observed), [true, true, false, false]);
  assert.deepEqual(H.probeFactRows(S.interpretProbeError({ message: 'x', code: 'PROVIDER_AUTH_ERROR' }), null), []);
});

test('only in-app page links are guarded while the form is dirty', () => {
  assert.equal(H.isGuardedNavigationHref('/workspace'), true);
  assert.equal(H.isGuardedNavigationHref('/s/abc'), true);
  assert.equal(H.isGuardedNavigationHref('/'), true);
  assert.equal(H.isGuardedNavigationHref('/settings'), false);
  assert.equal(H.isGuardedNavigationHref('/settings/'), false);
  assert.equal(H.isGuardedNavigationHref('/api/file/download?path=a'), false);
  assert.equal(H.isGuardedNavigationHref('/ui/app.css'), false);
  assert.equal(H.isGuardedNavigationHref('https://example.com/'), false);
  assert.equal(H.isGuardedNavigationHref('//evil.example/'), false);
  assert.equal(H.isGuardedNavigationHref('#top'), false);
});

test('settings helpers module is pure', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/features/settings/settings-helpers.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage)\b/);
});
