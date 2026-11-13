/**
 * Settings page model: GET/POST /api/config and POST /api/config/test
 * (internal/webconsole/dto.go UpdateConfigRequest / TestConfigRequest /
 * TestConfigResponse). Pure: no DOM, no fetch.
 *
 * The save payload matches the current DTO WITHOUT legacy_ui_enabled (the
 * legacy route is removed; the field is ignored if a server still returns it).
 * Sub-agent budget is never part of the provider test payload.
 *
 * Validation errors are SettingsValidationError with .key/.params (i18n).
 *
 * i18n keys emitted:
 *   settings.error.duration          {field} value is not a duration (30m, 2h, 1h30m, seconds)
 *   settings.error.nonNegativeInt    {field} value must be a non-negative integer
 *   settings.error.softTurns         soft checkpoint turns must be positive
 *   settings.error.hardTurns         hard max turns must be positive or disabled
 *   settings.error.childBudgetEmpty  enabled sub-agent budget needs a positive limit
 *   settings.error.contextWindow     context window must be a non-negative integer
 *   settings.error.unknownProvider   {provider} provider profile is not configured
 *   settings.field.childActiveRuntime | settings.field.childElapsed |
 *   settings.field.childMaxTurns | settings.field.roleMaxOutputTokens   field names in params
 *   settings.reasoning.<mode>        reasoning mode / summary option labels
 *                                    (default, low, medium, high, xhigh, max, standard,
 *                                     off, auto, concise, detailed, inherit)
 *   settings.reasoningHelp.openai | .thinking | .default
 *   settings.summaryHelp.openai | .unavailable
 *   settings.apiProvider.inherit | .openai-compatible | .anthropic-compatible | .google
 *   settings.apiProvider.effective   {adapter} effective adapter help text
 *   settings.role.<role> | settings.roleDescription.<role>   role panel labels
 *   settings.roleState.custom | settings.roleState.default
 *   settings.state.enabled | settings.state.off
 *   settings.save.title | settings.save.confirm | settings.save.message |
 *   settings.save.messageWithKey | settings.save.cancelled | settings.save.saved
 *   settings.apiProvider.effective also carries params.adapterLabel (key slot)
 *   settings.probe.passed            {provider, model, mode} probe succeeded
 *   settings.probe.failed            probe failed (raw diagnostics separate)
 *   settings.probe.thinkingVisible   readable thinking/summary observed
 *   settings.probe.thinkingHidden    reasoning observed but not readable
 *   settings.probe.thinkingNotObserved  reasoning requested but nothing observed
 *   settings.probe.thinkingNotRequested reasoning off/default and nothing observed
 */

/** Error with an i18n key. */
export class SettingsValidationError extends Error {
  constructor(key, params = {}) {
    super(key);
    this.name = 'SettingsValidationError';
    this.key = key;
    this.params = params;
  }
}

/** Masked placeholder shown for persisted API keys. */
export const MASKED_API_KEY = '\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022';

/** API provider adapter families selectable in Settings. */
export const API_PROVIDERS = Object.freeze(['openai-compatible', 'anthropic-compatible', 'google']);

/** Role hints that accept provider overrides (explorer is supported by the backend). */
export const ROLE_PROVIDER_ROLES = Object.freeze(['planner', 'generator', 'evaluator', 'explorer']);

/** Role override reasoning effort options ('' inherits). */
export const ROLE_REASONING_EFFORTS = Object.freeze(['', 'low', 'medium', 'high', 'xhigh', 'max']);

/** Guardrails modes. */
export const GUARDRAILS_MODES = Object.freeze(['yolo', 'standard']);

const OPENAI_MODES = Object.freeze(['default', 'low', 'medium', 'high', 'xhigh', 'max']);
const OPENAI_SUMMARY_MODES = Object.freeze(['default', 'auto', 'concise', 'detailed', 'off']);
const THINKING_MODES = Object.freeze(['default', 'standard', 'max', 'off']);

function str(value) {
  return value === undefined || value === null ? '' : String(value);
}

/** Seconds to "30m", "2h", "1d" or "45s" ('' for 0/invalid). */
export function formatSettingsDuration(seconds) {
  const value = Number(seconds || 0);
  if (!Number.isFinite(value) || value <= 0) return '';
  if (value % 86400 === 0) return value / 86400 + 'd';
  if (value % 3600 === 0) return value / 3600 + 'h';
  if (value % 60 === 0) return value / 60 + 'm';
  return value + 's';
}

/**
 * Parse "30m", "2h", "1h30m", "1d", "45s" or plain seconds; '' and '0' => 0.
 * @param {string} rawValue
 * @param {string} [fieldKey] i18n key of the field (error params)
 * @throws {SettingsValidationError} settings.error.duration
 */
export function parseSettingsDuration(rawValue, fieldKey = '') {
  const raw = str(rawValue).trim().toLowerCase();
  if (raw === '' || raw === '0') return 0;
  if (/^\d+$/.test(raw)) return Number.parseInt(raw, 10);
  const unitSeconds = { d: 86400, h: 3600, m: 60, s: 1 };
  let total = 0;
  let consumed = '';
  for (const match of raw.matchAll(/(\d+)\s*([dhms])/g)) {
    total += Number.parseInt(match[1], 10) * unitSeconds[match[2]];
    consumed += match[0].replace(/\s+/g, '');
  }
  if (total <= 0 || consumed !== raw.replace(/\s+/g, '')) {
    throw new SettingsValidationError('settings.error.duration', { field: fieldKey });
  }
  return total;
}

/**
 * Optional non-negative integer ('' => 0).
 * @throws {SettingsValidationError} settings.error.nonNegativeInt
 */
export function parseOptionalNonNegativeInteger(rawValue, fieldKey = '') {
  const raw = str(rawValue).trim();
  if (raw === '') return 0;
  if (!/^\d+$/.test(raw)) throw new SettingsValidationError('settings.error.nonNegativeInt', { field: fieldKey });
  return Number.parseInt(raw, 10);
}

/** 'openai' | 'thinking' | '' for an adapter family. */
export function reasoningFamily(apiProvider) {
  switch (str(apiProvider)) {
    case 'openai-compatible':
      return 'openai';
    case 'anthropic-compatible':
    case 'google':
      return 'thinking';
    default:
      return '';
  }
}

/** Effective adapter: explicit selection, else provider effective/configured value. */
export function effectiveAPIProvider(selected, provider) {
  return str(selected) || str(provider?.effective_api_provider) || str(provider?.api_provider);
}

/** Reasoning modes for an adapter family. */
export function reasoningModesFor(apiProvider) {
  const family = reasoningFamily(apiProvider);
  if (family === 'openai') return OPENAI_MODES.slice();
  if (family === 'thinking') return THINKING_MODES.slice();
  return ['default'];
}

/** Reasoning summary modes for an adapter family. */
export function reasoningSummaryModesFor(apiProvider) {
  return reasoningFamily(apiProvider) === 'openai' ? OPENAI_SUMMARY_MODES.slice() : ['default'];
}

/** Keep a preferred mode/summary when the adapter supports it, else 'default'. */
export function coerceReasoningSelection(apiProvider, mode, summary) {
  const modes = reasoningModesFor(apiProvider);
  const summaries = reasoningSummaryModesFor(apiProvider);
  return {
    mode: modes.includes(str(mode)) ? str(mode) : 'default',
    summary: summaries.includes(str(summary)) ? str(summary) : 'default'
  };
}

/** Option list [{value, label}] for selects. */
export function reasoningOptions(values) {
  return values.map((value) => ({ value, label: { key: 'settings.reasoning.' + (value || 'inherit') } }));
}

/** Reasoning/summary select model for an adapter. */
export function reasoningControls(apiProvider, preferredMode, preferredSummary) {
  const family = reasoningFamily(apiProvider);
  const selection = coerceReasoningSelection(apiProvider, preferredMode, preferredSummary);
  return {
    family,
    adapter: { key: 'settings.apiProvider.effective', params: { adapter: str(apiProvider) }, adapterLabel: apiProviderLabel(apiProvider) },
    modes: reasoningOptions(reasoningModesFor(apiProvider)),
    summaries: reasoningOptions(reasoningSummaryModesFor(apiProvider)),
    mode: selection.mode,
    summary: selection.summary,
    modeHelp: { key: family === 'openai' ? 'settings.reasoningHelp.openai' : family === 'thinking' ? 'settings.reasoningHelp.thinking' : 'settings.reasoningHelp.default' },
    summaryHelp: { key: family === 'openai' ? 'settings.summaryHelp.openai' : 'settings.summaryHelp.unavailable' }
  };
}

/** Label slot for an API provider value ('' => inherit). */
export function apiProviderLabel(value) {
  const v = str(value);
  return { key: 'settings.apiProvider.' + (API_PROVIDERS.includes(v) ? v : 'inherit') };
}

/** API key field value: mask when a key is persisted. */
export function apiKeyFieldState(provider) {
  const hasKey = Boolean(provider?.has_key);
  return { value: hasKey ? MASKED_API_KEY : '', originalHasKey: hasKey };
}

/** Key to submit: '' keeps the persisted key (masked or cleared field). */
export function submittedAPIKey(value, originalHasKey) {
  const v = str(value);
  if (v === MASKED_API_KEY && originalHasKey) return '';
  return v;
}

/** API key field after a successful save. */
export function apiKeyAfterSave(fieldValue, submittedKey, originalHasKey) {
  if (str(submittedKey) !== '') return { value: MASKED_API_KEY, originalHasKey: true };
  if (originalHasKey) return { value: MASKED_API_KEY, originalHasKey: true };
  if (str(fieldValue) !== MASKED_API_KEY) return { value: '', originalHasKey: false };
  return { value: str(fieldValue), originalHasKey: Boolean(originalHasKey) };
}

/** Provider-scoped form fields for a provider profile. */
export function providerFormFields(config, providerName) {
  const provider = config?.providers?.[providerName] || null;
  const key = apiKeyFieldState(provider);
  const apiProvider = str(provider?.api_provider);
  const selection = coerceReasoningSelection(effectiveAPIProvider(apiProvider, provider), provider?.reasoning_mode || 'default', provider?.reasoning_summary || 'default');
  return {
    provider: str(providerName),
    apiProvider,
    baseURL: str(provider?.base_url),
    model: str(provider?.model),
    contextWindowTokens: provider?.context_window_tokens ? String(provider.context_window_tokens) : '',
    reasoningMode: selection.mode,
    reasoningSummary: selection.summary,
    apiKey: key.value,
    originalHasKey: key.originalHasKey
  };
}

/** Child budget form values from GET /api/config (accepts legacy field names). */
export function childBudgetFormFromConfig(childBudget = {}) {
  const budget = childBudget || {};
  const active = Number(budget.max_active_runtime_sec || budget.max_wall_clock_sec || 0);
  const elapsed = Number(budget.max_elapsed_sec || 0);
  const turns = Number(budget.max_turns_per_attempt || budget.max_turns || 0);
  const disabled = Object.prototype.hasOwnProperty.call(budget, 'disabled')
    ? Boolean(budget.disabled)
    : active <= 0 && elapsed <= 0 && turns <= 0;
  return {
    enabled: !disabled,
    activeRuntime: formatSettingsDuration(active),
    elapsed: formatSettingsDuration(elapsed),
    maxTurns: turns > 0 ? String(turns) : ''
  };
}

/**
 * Canonical child_budget DTO. Disabled => all dimensions 0; enabled =>
 * non-negative values with at least one positive limit.
 * @throws {SettingsValidationError}
 */
export function buildChildBudget(form = {}) {
  if (!form.enabled) {
    return { disabled: true, max_active_runtime_sec: 0, max_elapsed_sec: 0, max_turns_per_attempt: 0 };
  }
  const maxActive = parseSettingsDuration(form.activeRuntime, 'settings.field.childActiveRuntime');
  const maxElapsed = parseSettingsDuration(form.elapsed, 'settings.field.childElapsed');
  const maxTurns = parseOptionalNonNegativeInteger(form.maxTurns, 'settings.field.childMaxTurns');
  if (maxActive === 0 && maxElapsed === 0 && maxTurns === 0) {
    throw new SettingsValidationError('settings.error.childBudgetEmpty');
  }
  return { disabled: false, max_active_runtime_sec: maxActive, max_elapsed_sec: maxElapsed, max_turns_per_attempt: maxTurns };
}

function roleRow(role, override = {}) {
  const o = override || {};
  return {
    role,
    provider: str(o.provider),
    api_provider: str(o.api_provider),
    base_url: str(o.base_url),
    model: str(o.model),
    reasoning_effort: str(o.reasoning_effort).toLowerCase(),
    max_output_tokens: Number(o.max_output_tokens || 0) > 0 ? String(o.max_output_tokens) : ''
  };
}

/** True when any role override field is set (Custom vs Default badge). */
export function roleOverrideIsCustom(row) {
  return ['provider', 'api_provider', 'base_url', 'model', 'reasoning_effort'].some((field) => str(row?.[field]).trim() !== '') ||
    Number(row?.max_output_tokens || 0) > 0;
}

/** Role override rows (all roles, empty fields inherit). */
export function roleProvidersFormFromConfig(roleProviders = {}) {
  return ROLE_PROVIDER_ROLES.map((role) => {
    const row = roleRow(role, roleProviders?.[role]);
    return {
      ...row,
      custom: roleOverrideIsCustom(row),
      label: { key: 'settings.role.' + role },
      description: { key: 'settings.roleDescription.' + role },
      state: { key: roleOverrideIsCustom(row) ? 'settings.roleState.custom' : 'settings.roleState.default' }
    };
  });
}

/**
 * role_providers DTO: trimmed strings, max_output_tokens as a non-negative
 * integer (blank => 0 inherit). Every listed role is sent so cleared rows reset.
 * @throws {SettingsValidationError}
 */
export function buildRoleProviders(rows = []) {
  const out = {};
  (Array.isArray(rows) ? rows : []).forEach((row) => {
    const role = str(row?.role).trim().toLowerCase();
    if (!ROLE_PROVIDER_ROLES.includes(role)) return;
    out[role] = {
      provider: str(row.provider).trim(),
      api_provider: str(row.api_provider).trim(),
      base_url: str(row.base_url).trim(),
      model: str(row.model).trim(),
      reasoning_effort: str(row.reasoning_effort).trim().toLowerCase(),
      max_output_tokens: parseOptionalNonNegativeInteger(row.max_output_tokens, 'settings.field.roleMaxOutputTokens')
    };
  });
  return out;
}

/**
 * Whole Settings form from GET /api/config.
 * @param {object} config
 * @param {string} [providerName] selected profile (default: config.default_provider)
 */
export function settingsFormFromConfig(config = {}, providerName) {
  const providers = config?.providers && typeof config.providers === 'object' ? config.providers : {};
  const selected = providerName || str(config?.default_provider) || Object.keys(providers)[0] || '';
  const soft = Number(config?.max_turns_soft || 24);
  const hard = Number(config?.max_turns_hard || 0);
  return {
    providers: Object.keys(providers),
    guardrailsMode: str(config?.guardrails_mode || 'yolo'),
    maxTurnsSoft: soft > 0 ? String(soft) : '24',
    maxTurnsHard: hard > 0 ? String(hard) : '40',
    disableHardTurnLimit: Boolean(config?.disable_hard_turn_limit),
    childBudget: childBudgetFormFromConfig(config?.child_budget),
    roleProviders: roleProvidersFormFromConfig(config?.role_providers),
    browser: config?.browser || null,
    ...providerFormFields(config, selected)
  };
}

/** Parse the context window field ('' => 0 auto). */
export function parseContextWindowTokens(value) {
  const raw = str(value).trim();
  if (raw === '') return 0;
  if (!/^\d+$/.test(raw)) throw new SettingsValidationError('settings.error.contextWindow');
  return Number.parseInt(raw, 10);
}

/**
 * Validate everything a save needs (turn guard, child budget, numbers).
 * @throws {SettingsValidationError}
 */
export function validateSettingsForm(form = {}) {
  const soft = Number.parseInt(str(form.maxTurnsSoft), 10);
  if (!Number.isFinite(soft) || soft <= 0) throw new SettingsValidationError('settings.error.softTurns');
  if (!form.disableHardTurnLimit) {
    const hard = Number.parseInt(str(form.maxTurnsHard), 10);
    if (!Number.isFinite(hard) || hard <= 0) throw new SettingsValidationError('settings.error.hardTurns');
  }
  buildChildBudget(form.childBudget || {});
  parseContextWindowTokens(form.contextWindowTokens);
  buildRoleProviders(form.roleProviders);
  if (form.provider && Array.isArray(form.providers) && form.providers.length && !form.providers.includes(form.provider)) {
    throw new SettingsValidationError('settings.error.unknownProvider', { provider: form.provider });
  }
  return true;
}

/**
 * Provider subset for POST /api/config/test (no child budget, no turn guard,
 * no role overrides).
 */
export function buildConfigTestPayload(form = {}) {
  return {
    provider: str(form.provider),
    api_provider: str(form.apiProvider),
    base_url: str(form.baseURL),
    model: str(form.model),
    context_window_tokens: parseContextWindowTokens(form.contextWindowTokens),
    reasoning_mode: str(form.reasoningMode || 'default'),
    reasoning_summary: str(form.reasoningSummary || 'default'),
    api_key: submittedAPIKey(form.apiKey, form.originalHasKey)
  };
}

/**
 * POST /api/config payload (UpdateConfigRequest without legacy_ui_enabled).
 * @throws {SettingsValidationError}
 */
export function buildConfigSavePayload(form = {}) {
  validateSettingsForm(form);
  const provider = buildConfigTestPayload(form);
  return {
    guardrails_mode: str(form.guardrailsMode),
    max_turns_soft: Number.parseInt(str(form.maxTurnsSoft || '0'), 10),
    max_turns_hard: Number.parseInt(str(form.maxTurnsHard || '0'), 10),
    disable_hard_turn_limit: Boolean(form.disableHardTurnLimit),
    child_budget: buildChildBudget(form.childBudget || {}),
    ...provider,
    role_providers: buildRoleProviders(form.roleProviders)
  };
}

/** Confirmation dialog model; writing an API key is a danger-tone action. */
export function settingsSaveConfirmation(form = {}) {
  const value = str(form.apiKey).trim();
  const writesAPIKey = value !== '' && value !== MASKED_API_KEY;
  return {
    title: { key: 'settings.save.title' },
    message: { key: writesAPIKey ? 'settings.save.messageWithKey' : 'settings.save.message' },
    confirm: { key: 'settings.save.confirm' },
    tone: writesAPIKey ? 'danger' : 'default',
    writesAPIKey
  };
}

/** Toasts after the save flow (confirmation declined / saved). */
export const SETTINGS_SAVE_NOTICES = Object.freeze({
  cancelled: Object.freeze({ key: 'settings.save.cancelled', tone: 'info' }),
  saved: Object.freeze({ key: 'settings.save.saved', tone: 'success' })
});

/** Role override reasoning effort options ('' => settings.reasoning.inherit). */
export function roleReasoningOptions() {
  return reasoningOptions(ROLE_REASONING_EFFORTS.slice());
}

/** Enabled/Off state badges for toggles. */
export function toggleStateLabel(enabled) {
  return { key: enabled ? 'settings.state.enabled' : 'settings.state.off' };
}

/**
 * Interpret a POST /api/config/test response. "Request accepted" and
 * "readable thinking actually observed" are reported separately.
 */
export function interpretProbeResult(result = {}) {
  const r = result || {};
  const success = r.success === true;
  const mode = str(r.reasoning_mode || 'default');
  const summary = str(r.reasoning_summary || 'default');
  const visible = Boolean(r.thinking_visible_observed || r.reasoning_summary_observed);
  const hidden = !visible && Boolean(r.thinking_replay_observed || r.reasoning_encrypted_observed || Number(r.reasoning_tokens || 0) > 0);
  const requested = !['default', 'off', ''].includes(mode) || !['default', 'off', ''].includes(summary);
  let thinkingKey = 'settings.probe.thinkingNotObserved';
  if (visible) thinkingKey = 'settings.probe.thinkingVisible';
  else if (hidden) thinkingKey = 'settings.probe.thinkingHidden';
  else if (!requested) thinkingKey = 'settings.probe.thinkingNotRequested';
  return {
    success,
    tone: success ? 'success' : 'error',
    title: success
      ? { key: 'settings.probe.passed', params: { provider: str(r.provider), model: str(r.model), mode, modeLabel: { key: 'settings.reasoning.' + mode } } }
      : { key: 'settings.probe.failed' },
    thinking: {
      key: thinkingKey,
      visibleObserved: visible,
      hiddenObserved: hidden,
      summaryObserved: Boolean(r.reasoning_summary_observed),
      replayObserved: Boolean(r.thinking_replay_observed),
      encryptedObserved: Boolean(r.reasoning_encrypted_observed),
      reasoningTokens: Number(r.reasoning_tokens || 0)
    },
    facts: {
      provider: str(r.provider),
      apiProvider: str(r.api_provider),
      effectiveAPIProvider: str(r.effective_api_provider),
      model: str(r.model),
      stopReason: str(r.stop_reason),
      reasoningEffort: str(r.reasoning_effort),
      thinkingBudget: Number(r.thinking_budget || 0),
      maxOutputTokens: Number(r.max_output_tokens || 0),
      effectiveContextWindowTokens: Number(r.effective_context_window_tokens || 0)
    },
    detail: r.thinking_detail ? { raw: str(r.thinking_detail) } : null,
    strategy: r.thinking_strategy ? { raw: str(r.thinking_strategy) } : null,
    finishMessage: r.finish_message ? { raw: str(r.finish_message) } : null
  };
}

/** Probe failure from an API error envelope {error, code, detail, action}. */
export function interpretProbeError(err) {
  return {
    success: false,
    tone: 'error',
    title: { key: 'settings.probe.failed' },
    message: err?.message ? { raw: str(err.message) } : null,
    code: str(err?.code),
    detail: err?.detail ? { raw: str(err.detail) } : null,
    action: err?.action ? { raw: str(err.action) } : null
  };
}
