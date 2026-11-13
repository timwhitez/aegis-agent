import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import * as S from '../../internal/webconsole/ui/src/lib/settings-model.js';

function settingsConfig({ model = 'gpt-test', hasKey = false, reasoningMode = 'default', extra = {} } = {}) {
  return {
    default_provider: 'openai',
    guardrails_mode: 'standard',
    max_turns_soft: 24,
    max_turns_hard: -1,
    disable_hard_turn_limit: true,
    legacy_ui_enabled: true,
    child_budget: { disabled: true, max_active_runtime_sec: 0, max_elapsed_sec: 0, max_turns_per_attempt: 0 },
    role_providers: {},
    providers: {
      openai: { has_key: hasKey, api_provider: '', effective_api_provider: 'openai-compatible', base_url: 'https://example.invalid/v1', model, reasoning_mode: reasoningMode, reasoning_summary: 'default' },
      claude: { has_key: true, api_provider: 'anthropic-compatible', effective_api_provider: 'anthropic-compatible', base_url: '', model: 'claude-x', reasoning_mode: 'max', reasoning_summary: 'auto' }
    },
    ...extra
  };
}

test('duration parser accepts canonical human-readable values', () => {
  assert.equal(S.parseSettingsDuration('30m'), 1800);
  assert.equal(S.parseSettingsDuration('2h'), 7200);
  assert.equal(S.parseSettingsDuration('1h30m'), 5400);
  assert.equal(S.parseSettingsDuration(' 1h 30m '), 5400);
  assert.equal(S.parseSettingsDuration('1d'), 86400);
  assert.equal(S.parseSettingsDuration('90'), 90);
  assert.equal(S.parseSettingsDuration(''), 0);
  assert.equal(S.parseSettingsDuration('0'), 0);
  assert.equal(S.formatSettingsDuration(1800), '30m');
  assert.equal(S.formatSettingsDuration(7200), '2h');
  assert.equal(S.formatSettingsDuration(86400), '1d');
  assert.equal(S.formatSettingsDuration(45), '45s');
  assert.equal(S.formatSettingsDuration(0), '');
});

test('duration parser rejects malformed values with a keyed error', () => {
  for (const bad of ['1h30', '-1h', '2x', 'h', '0m']) {
    assert.throws(() => S.parseSettingsDuration(bad, 'settings.field.childElapsed'), (err) => err instanceof S.SettingsValidationError &&
      err.key === 'settings.error.duration' && err.params.field === 'settings.field.childElapsed', bad);
  }
});

test('default save payload keeps empty API keys, a disabled budget and no legacy field', () => {
  const form = S.settingsFormFromConfig(settingsConfig({ hasKey: false }));
  assert.equal(form.apiKey, '');
  assert.equal(form.originalHasKey, false);
  assert.equal(form.disableHardTurnLimit, true);
  assert.equal(form.maxTurnsHard, '40');
  assert.deepEqual(S.toggleStateLabel(form.childBudget.enabled), { key: 'settings.state.off' });
  const payload = S.buildConfigSavePayload(form);
  assert.equal(payload.api_key, '');
  assert.deepEqual(payload.child_budget, { disabled: true, max_active_runtime_sec: 0, max_elapsed_sec: 0, max_turns_per_attempt: 0 });
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'legacy_ui_enabled'), false, 'legacy_ui_enabled is removed from the DTO');
  assert.deepEqual(Object.keys(payload).sort(), ['api_key', 'api_provider', 'base_url', 'child_budget', 'context_window_tokens', 'disable_hard_turn_limit',
    'guardrails_mode', 'max_turns_hard', 'max_turns_soft', 'model', 'provider', 'reasoning_mode', 'reasoning_summary', 'role_providers'].sort());
  assert.equal(payload.provider, 'openai');
  assert.equal(payload.guardrails_mode, 'standard');
  assert.equal(payload.max_turns_soft, 24);
  assert.equal(payload.disable_hard_turn_limit, true);
  assert.deepEqual(Object.keys(payload.role_providers), ['planner', 'generator', 'evaluator', 'explorer']);
});

test('an explicitly enabled sub-agent budget is saved in seconds', () => {
  const form = S.settingsFormFromConfig(settingsConfig());
  form.childBudget = { enabled: true, activeRuntime: '1h30m', elapsed: '2h', maxTurns: '320' };
  assert.deepEqual(S.buildConfigSavePayload(form).child_budget, { disabled: false, max_active_runtime_sec: 5400, max_elapsed_sec: 7200, max_turns_per_attempt: 320 });
  assert.deepEqual(S.toggleStateLabel(true), { key: 'settings.state.enabled' });
});

test('an enabled sub-agent budget without a positive limit is rejected before saving', () => {
  const form = S.settingsFormFromConfig(settingsConfig());
  form.childBudget = { enabled: true, activeRuntime: '', elapsed: '0', maxTurns: '' };
  assert.throws(() => S.buildConfigSavePayload(form), (err) => err.key === 'settings.error.childBudgetEmpty');
  form.childBudget = { enabled: true, activeRuntime: '', elapsed: '', maxTurns: '-3' };
  assert.throws(() => S.buildConfigSavePayload(form), (err) => err.key === 'settings.error.nonNegativeInt' && err.params.field === 'settings.field.childMaxTurns');
});

test('legacy child budget field names are read and canonical fields are written', () => {
  const form = S.childBudgetFormFromConfig({ max_wall_clock_sec: 1800, max_turns: 50 });
  assert.deepEqual(form, { enabled: true, activeRuntime: '30m', elapsed: '', maxTurns: '50' });
  assert.deepEqual(S.buildChildBudget(form), { disabled: false, max_active_runtime_sec: 1800, max_elapsed_sec: 0, max_turns_per_attempt: 50 });
  assert.equal(S.childBudgetFormFromConfig(null).enabled, false);
});

test('the provider test payload omits child budget and turn guard controls', () => {
  const form = S.settingsFormFromConfig(settingsConfig());
  form.childBudget = { enabled: true, maxTurns: '100' };
  const payload = S.buildConfigTestPayload(form);
  for (const key of ['child_budget', 'max_turns_hard', 'max_turns_soft', 'role_providers', 'legacy_ui_enabled', 'guardrails_mode']) {
    assert.equal(Object.prototype.hasOwnProperty.call(payload, key), false, key);
  }
  assert.deepEqual(payload, { provider: 'openai', api_provider: '', base_url: 'https://example.invalid/v1', model: 'gpt-test', context_window_tokens: 0, reasoning_mode: 'default', reasoning_summary: 'default', api_key: '' });
});

test('a persisted API key stays masked and is never resubmitted', () => {
  const form = S.settingsFormFromConfig(settingsConfig({ hasKey: true }));
  assert.equal(form.apiKey, S.MASKED_API_KEY);
  assert.equal(form.originalHasKey, true);
  assert.equal(S.buildConfigSavePayload(form).api_key, '');
  assert.equal(S.submittedAPIKey('', true), '');
  assert.deepEqual(S.apiKeyAfterSave('', '', true), { value: S.MASKED_API_KEY, originalHasKey: true });
  assert.deepEqual(S.apiKeyAfterSave('sk-new', 'sk-new', false), { value: S.MASKED_API_KEY, originalHasKey: true });
  assert.deepEqual(S.apiKeyAfterSave('', '', false), { value: '', originalHasKey: false });
});

test('save confirmation is a danger action only when writing an API key', () => {
  const withKey = S.settingsSaveConfirmation({ apiKey: 'sk-local-test' });
  assert.deepEqual(withKey, { title: { key: 'settings.save.title' }, message: { key: 'settings.save.messageWithKey' }, confirm: { key: 'settings.save.confirm' }, tone: 'danger', writesAPIKey: true });
  assert.equal(S.settingsSaveConfirmation({ apiKey: S.MASKED_API_KEY }).tone, 'default');
  assert.equal(S.settingsSaveConfirmation({ apiKey: '' }).message.key, 'settings.save.message');
});

test('configured max reasoning is preserved for OpenAI-compatible providers', () => {
  const form = S.settingsFormFromConfig(settingsConfig({ reasoningMode: 'max' }));
  assert.equal(form.reasoningMode, 'max');
  const controls = S.reasoningControls(S.effectiveAPIProvider(form.apiProvider, settingsConfig().providers.openai), form.reasoningMode, form.reasoningSummary);
  assert.equal(controls.mode, 'max');
  assert.ok(controls.modes.some((option) => option.value === 'max'));
  assert.ok(S.ROLE_REASONING_EFFORTS.includes('max'));
});

test('provider-family reasoning options', () => {
  assert.deepEqual(S.reasoningModesFor('openai-compatible'), ['default', 'low', 'medium', 'high', 'xhigh', 'max']);
  assert.deepEqual(S.reasoningSummaryModesFor('openai-compatible'), ['default', 'auto', 'concise', 'detailed', 'off']);
  for (const provider of ['anthropic-compatible', 'google']) {
    assert.deepEqual(S.reasoningModesFor(provider), ['default', 'standard', 'max', 'off']);
    assert.deepEqual(S.reasoningSummaryModesFor(provider), ['default']);
  }
  assert.deepEqual(S.reasoningModesFor(''), ['default']);
  assert.deepEqual(S.coerceReasoningSelection('google', 'xhigh', 'auto'), { mode: 'default', summary: 'default' });
  assert.deepEqual(S.coerceReasoningSelection('google', 'max', 'default'), { mode: 'max', summary: 'default' });
  const thinking = S.reasoningControls('anthropic-compatible', 'standard', 'detailed');
  assert.equal(thinking.family, 'thinking');
  assert.deepEqual(thinking.modeHelp, { key: 'settings.reasoningHelp.thinking' });
  assert.deepEqual(thinking.summaryHelp, { key: 'settings.summaryHelp.unavailable' });
  assert.deepEqual(thinking.modes[0], { value: 'default', label: { key: 'settings.reasoning.default' } });
  const claude = S.settingsFormFromConfig(settingsConfig(), 'claude');
  assert.equal(claude.reasoningMode, 'max');
  assert.equal(claude.reasoningSummary, 'default', 'unsupported summary falls back to default');
  assert.deepEqual(S.apiProviderLabel('google'), { key: 'settings.apiProvider.google' });
  assert.deepEqual(S.apiProviderLabel(''), { key: 'settings.apiProvider.inherit' });
});

test('role provider overrides inherit when empty and round-trip trimmed values', () => {
  const rows = S.roleProvidersFormFromConfig({ planner: { provider: 'claude', model: ' m ', reasoning_effort: 'HIGH', max_output_tokens: 4096 }, evaluator: {} });
  assert.deepEqual(rows.map((row) => row.role), ['planner', 'generator', 'evaluator', 'explorer']);
  assert.equal(rows[0].custom, true);
  assert.deepEqual(rows[0].state, { key: 'settings.roleState.custom' });
  assert.equal(rows[0].max_output_tokens, '4096');
  assert.equal(rows[0].reasoning_effort, 'high');
  assert.equal(rows[2].custom, false);
  assert.deepEqual(rows[2].label, { key: 'settings.role.evaluator' });
  const payload = S.buildRoleProviders(rows);
  assert.deepEqual(payload.planner, { provider: 'claude', api_provider: '', base_url: '', model: 'm', reasoning_effort: 'high', max_output_tokens: 4096 });
  assert.deepEqual(payload.generator, { provider: '', api_provider: '', base_url: '', model: '', reasoning_effort: '', max_output_tokens: 0 });
  assert.throws(() => S.buildRoleProviders([{ role: 'planner', max_output_tokens: '-1' }]), (err) => err.params.field === 'settings.field.roleMaxOutputTokens');
  assert.deepEqual(S.buildRoleProviders([{ role: 'orchestrator', model: 'x' }]), {}, 'unknown roles are not sent');
});

test('save notices and role effort options are keyed', () => {
  assert.deepEqual(S.SETTINGS_SAVE_NOTICES.cancelled, { key: 'settings.save.cancelled', tone: 'info' });
  assert.deepEqual(S.SETTINGS_SAVE_NOTICES.saved, { key: 'settings.save.saved', tone: 'success' });
  assert.deepEqual(S.roleReasoningOptions().map((option) => option.label.key), ['settings.reasoning.inherit', 'settings.reasoning.low',
    'settings.reasoning.medium', 'settings.reasoning.high', 'settings.reasoning.xhigh', 'settings.reasoning.max']);
});

test('turn guard validation', () => {
  const form = S.settingsFormFromConfig(settingsConfig());
  assert.throws(() => S.validateSettingsForm({ ...form, maxTurnsSoft: '0' }), (err) => err.key === 'settings.error.softTurns');
  assert.throws(() => S.validateSettingsForm({ ...form, disableHardTurnLimit: false, maxTurnsHard: '' }), (err) => err.key === 'settings.error.hardTurns');
  assert.equal(S.validateSettingsForm({ ...form, disableHardTurnLimit: true, maxTurnsHard: '' }), true);
  assert.throws(() => S.validateSettingsForm({ ...form, contextWindowTokens: '1.5' }), (err) => err.key === 'settings.error.contextWindow');
  assert.throws(() => S.validateSettingsForm({ ...form, provider: 'missing' }), (err) => err.key === 'settings.error.unknownProvider' && err.params.provider === 'missing');
  assert.equal(S.buildConfigSavePayload({ ...form, disableHardTurnLimit: false, maxTurnsHard: '60', contextWindowTokens: '200000' }).context_window_tokens, 200000);
});

test('probe results separate request success from observed thinking', () => {
  const visible = S.interpretProbeResult({ success: true, provider: 'openai', model: 'gpt', reasoning_mode: 'high', reasoning_summary: 'auto', reasoning_summary_observed: true, thinking_detail: 'summary text returned', thinking_strategy: 'responses-summary' });
  assert.equal(visible.success, true);
  assert.equal(visible.tone, 'success');
  assert.deepEqual(visible.title.params.provider, 'openai');
  assert.equal(visible.title.key, 'settings.probe.passed');
  assert.equal(visible.thinking.key, 'settings.probe.thinkingVisible');
  assert.deepEqual(visible.detail, { raw: 'summary text returned' });
  assert.deepEqual(visible.strategy, { raw: 'responses-summary' });
  const hidden = S.interpretProbeResult({ success: true, reasoning_mode: 'high', reasoning_encrypted_observed: true, reasoning_tokens: 12 });
  assert.equal(hidden.thinking.key, 'settings.probe.thinkingHidden');
  assert.equal(hidden.thinking.reasoningTokens, 12);
  assert.equal(S.interpretProbeResult({ success: true, reasoning_mode: 'max' }).thinking.key, 'settings.probe.thinkingNotObserved');
  assert.equal(S.interpretProbeResult({ success: true, reasoning_mode: 'default' }).thinking.key, 'settings.probe.thinkingNotRequested');
  const failed = S.interpretProbeResult({ success: false, stop_reason: 'error' });
  assert.equal(failed.tone, 'error');
  assert.deepEqual(failed.title, { key: 'settings.probe.failed' });
  assert.equal(failed.facts.stopReason, 'error');
  const error = S.interpretProbeError({ message: 'provider rejected', code: 'PROVIDER_INVALID_REQUEST', detail: 'HTTP 400', action: 'check base URL' });
  assert.deepEqual(error.detail, { raw: 'HTTP 400' });
  assert.equal(error.code, 'PROVIDER_INVALID_REQUEST');
  assert.deepEqual(error.action, { raw: 'check base URL' });
});

test('module is pure', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/lib/settings-model.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage)\b/);
  assert.doesNotMatch(source, /legacy_ui_enabled/);
});
