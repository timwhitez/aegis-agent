// Pure helpers for the Settings page (no DOM, no fetch): per-field validation
// built on lib/settings-model.js parsers, dirty tracking, API payload mapping
// and probe fact rows. Tested by validation/scripts/ui_settings_view_helpers_test.mjs.
import {
  ROLE_PROVIDER_ROLES,
  SettingsValidationError,
  buildChildBudget,
  parseContextWindowTokens,
  parseOptionalNonNegativeInteger,
  parseSettingsDuration,
  submittedAPIKey,
  validateSettingsForm,
} from '../../lib/settings-model.js';

function str(value) {
  return value === undefined || value === null ? '' : String(value);
}

/** Field ids in visual order (first invalid one receives focus). */
export const FIELD_ORDER = Object.freeze([
  'provider',
  'contextWindowTokens',
  'maxTurnsSoft',
  'maxTurnsHard',
  'childActiveRuntime',
  'childElapsed',
  'childMaxTurns',
  'childBudget',
  ...ROLE_PROVIDER_ROLES.map((role) => `role.${role}.max_output_tokens`),
  'form',
]);

/** DOM id of a field ("role.planner.max_output_tokens" -> "st-role-planner-max-output-tokens"). */
export function fieldDomId(field) {
  return 'st-' + String(field || '').replace(/[^A-Za-z0-9]+/g, '-').replace(/([a-z])([A-Z])/g, '$1-$2').toLowerCase();
}

function capture(errors, field, check) {
  try {
    check();
  } catch (error) {
    if (error instanceof SettingsValidationError || error?.name === 'SettingsValidationError') {
      errors[field] = { key: error.key, params: { ...(error.params || {}) } };
      return;
    }
    throw error;
  }
}

/**
 * Every validation problem keyed by field id, using the lib parsers so the
 * rules stay identical to buildConfigSavePayload(). Empty object => valid.
 * @returns {Record<string, {key: string, params: object}>}
 */
export function collectFieldErrors(form = {}) {
  const errors = {};
  capture(errors, 'provider', () => validateSettingsForm({ maxTurnsSoft: '1', disableHardTurnLimit: true, provider: form.provider, providers: form.providers }));
  capture(errors, 'contextWindowTokens', () => parseContextWindowTokens(form.contextWindowTokens));
  capture(errors, 'maxTurnsSoft', () => validateSettingsForm({ maxTurnsSoft: form.maxTurnsSoft, disableHardTurnLimit: true }));
  if (!form.disableHardTurnLimit) {
    capture(errors, 'maxTurnsHard', () => validateSettingsForm({ maxTurnsSoft: '1', disableHardTurnLimit: false, maxTurnsHard: form.maxTurnsHard }));
  }
  const budget = form.childBudget || {};
  if (budget.enabled) {
    capture(errors, 'childActiveRuntime', () => parseSettingsDuration(budget.activeRuntime, 'settings.field.childActiveRuntime'));
    capture(errors, 'childElapsed', () => parseSettingsDuration(budget.elapsed, 'settings.field.childElapsed'));
    capture(errors, 'childMaxTurns', () => parseOptionalNonNegativeInteger(budget.maxTurns, 'settings.field.childMaxTurns'));
    if (!errors.childActiveRuntime && !errors.childElapsed && !errors.childMaxTurns) {
      capture(errors, 'childBudget', () => buildChildBudget(budget));
    }
  }
  (Array.isArray(form.roleProviders) ? form.roleProviders : []).forEach((row) => {
    capture(errors, `role.${row.role}.max_output_tokens`, () => parseOptionalNonNegativeInteger(row.max_output_tokens, 'settings.field.roleMaxOutputTokens'));
  });
  if (!Object.keys(errors).length) capture(errors, 'form', () => validateSettingsForm(form));
  return errors;
}

/** First invalid field in visual order ('' when valid). */
export function firstErrorField(errors = {}) {
  return FIELD_ORDER.find((field) => errors[field]) || Object.keys(errors)[0] || '';
}

/** Canonical comparable snapshot of the editable form (dirty tracking). */
export function formSnapshot(form) {
  if (!form) return '';
  const budget = form.childBudget || {};
  return JSON.stringify({
    provider: str(form.provider),
    apiProvider: str(form.apiProvider),
    baseURL: str(form.baseURL).trim(),
    model: str(form.model).trim(),
    contextWindowTokens: str(form.contextWindowTokens).trim(),
    reasoningMode: str(form.reasoningMode || 'default'),
    reasoningSummary: str(form.reasoningSummary || 'default'),
    apiKey: submittedAPIKey(form.apiKey, form.originalHasKey),
    guardrailsMode: str(form.guardrailsMode),
    maxTurnsSoft: str(form.maxTurnsSoft).trim(),
    maxTurnsHard: form.disableHardTurnLimit ? '' : str(form.maxTurnsHard).trim(),
    disableHardTurnLimit: Boolean(form.disableHardTurnLimit),
    childBudget: budget.enabled
      ? { enabled: true, activeRuntime: str(budget.activeRuntime).trim(), elapsed: str(budget.elapsed).trim(), maxTurns: str(budget.maxTurns).trim() }
      : { enabled: false },
    roles: (Array.isArray(form.roleProviders) ? form.roleProviders : []).map((row) => [
      str(row.role),
      str(row.provider).trim(),
      str(row.api_provider).trim(),
      str(row.base_url).trim(),
      str(row.model).trim(),
      str(row.reasoning_effort).trim().toLowerCase(),
      str(row.max_output_tokens).trim(),
    ]),
  });
}

/** api.testConfig() input from a buildConfigTestPayload() DTO. */
export function toTestConfigInput(payload = {}) {
  return {
    provider: payload.provider,
    apiProvider: payload.api_provider,
    baseURL: payload.base_url,
    model: payload.model,
    contextWindowTokens: payload.context_window_tokens,
    reasoningMode: payload.reasoning_mode,
    reasoningSummary: payload.reasoning_summary,
    apiKey: payload.api_key,
  };
}

/** api.saveConfig() input from a buildConfigSavePayload() DTO (same wire body). */
export function toSaveConfigInput(dto = {}) {
  const budget = dto.child_budget;
  return {
    guardrailsMode: dto.guardrails_mode,
    maxTurnsSoft: dto.max_turns_soft,
    maxTurnsHard: dto.max_turns_hard,
    disableHardTurnLimit: dto.disable_hard_turn_limit,
    childBudget: budget ? {
      disabled: budget.disabled,
      maxActiveRuntimeSec: budget.max_active_runtime_sec,
      maxElapsedSec: budget.max_elapsed_sec,
      maxTurnsPerAttempt: budget.max_turns_per_attempt,
    } : undefined,
    ...toTestConfigInput(dto),
    roleProviders: dto.role_providers,
  };
}

/**
 * Probe fact rows [{key, value, bool?}] (non-empty only) from interpretProbeResult()
 * plus the raw response for fields the interpreter does not expose.
 */
export function probeFactRows(interpreted, raw = {}) {
  const facts = interpreted?.facts || {};
  const rows = [];
  const push = (key, value, bool = false) => {
    if (value === undefined || value === null || value === '' || value === 0) return;
    rows.push(bool ? { key, value: String(value), bool: value === true } : { key, value: String(value) });
  };
  push('settings.probe.fact.provider', facts.provider);
  push('settings.probe.fact.apiProvider', facts.effectiveAPIProvider || facts.apiProvider);
  push('settings.probe.fact.model', facts.model);
  push('settings.probe.fact.stopReason', facts.stopReason);
  push('settings.probe.fact.reasoningEffort', facts.reasoningEffort);
  push('settings.probe.fact.thinkingBudget', facts.thinkingBudget);
  if (typeof raw?.include_thoughts === 'boolean') push('settings.probe.fact.includeThoughts', raw.include_thoughts, true);
  push('settings.probe.fact.maxOutputTokens', facts.maxOutputTokens);
  push('settings.probe.fact.contextWindow', facts.effectiveContextWindowTokens);
  push('settings.probe.fact.reasoningTokens', interpreted?.thinking?.reasoningTokens);
  return rows;
}

/** Observation checklist for the probe result panel. */
export function probeObservations(interpreted) {
  const thinking = interpreted?.thinking || {};
  return [
    { key: 'settings.probe.observed.visible', observed: Boolean(thinking.visibleObserved) },
    { key: 'settings.probe.observed.summary', observed: Boolean(thinking.summaryObserved) },
    { key: 'settings.probe.observed.replay', observed: Boolean(thinking.replayObserved) },
    { key: 'settings.probe.observed.encrypted', observed: Boolean(thinking.encryptedObserved) },
  ];
}

/** Split a translated template into text and {slot} parts. */
export function splitTemplate(template) {
  const parts = [];
  const text = String(template ?? '');
  let last = 0;
  for (const match of text.matchAll(/\{(\w+)\}/g)) {
    if (match.index > last) parts.push({ text: text.slice(last, match.index) });
    parts.push({ slot: match[1] });
    last = match.index + match[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return parts;
}

/**
 * In-app link that should be confirmed before leaving a dirty form:
 * same-origin path, not an API/asset URL, not the settings page itself.
 */
export function isGuardedNavigationHref(href, currentPath = '/settings') {
  const value = str(href);
  if (!value.startsWith('/') || value.startsWith('//')) return false;
  if (value.startsWith('/api/') || value.startsWith('/ui/')) return false;
  const pathOnly = value.split(/[?#]/)[0].replace(/\/+$/, '') || '/';
  return pathOnly !== (str(currentPath).replace(/\/+$/, '') || '/');
}
