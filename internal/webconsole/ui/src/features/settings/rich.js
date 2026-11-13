// Translated copy with raw slots for the Settings page (see workspace/rich.js;
// features stay self-contained). Raw durable values (provider names, URLs,
// model ids, server diagnostics) render in translate="no" nodes.
import { html } from '../../lib/html.js';
import { t, tx } from '../../lib/i18n.js';
import { splitTemplate } from './settings-helpers.js';

/** Localized template of key with {slots} replaced by nodes or strings. */
export function richText(key, slots = {}) {
  return splitTemplate(t(key)).map((part) => {
    if ('text' in part) return part.text;
    const value = slots[part.slot];
    return value === undefined || value === null ? '' : value;
  });
}

/** Raw value; translate=${false} serializes as translate="no" (Preact sets the boolean property). */
export function Raw({ value, class: className = '', title }) {
  return html`<span class=${`raw-value ${className}`.trim()} translate=${false} title=${title}>${String(value ?? '')}</span>`;
}

/** Localized validation message for a SettingsValidationError-like {key, params}. */
export function validationText(error) {
  if (!error) return '';
  const params = error.params || {};
  if (params.provider !== undefined) {
    return html`<span>${richText(error.key, { provider: html`<${Raw} value=${params.provider} />` })}</span>`;
  }
  return tx({ key: error.key, params: { ...params, field: params.field ? tx(params.field) : undefined } });
}

/** { message, detail, action, status, code } of an APIError. */
export function errorInfo(error, fallback = '') {
  return {
    message: error?.message || fallback,
    detail: error?.detail || '',
    action: error?.action || '',
    status: Number(error?.status || 0),
    code: error?.code || '',
  };
}

/** Backend error lines: message plus detail/action. */
export function ErrorLines({ error, class: className = '' }) {
  if (!error) return null;
  return html`<div class=${`st-error-lines ${className}`.trim()}>
    ${error.message ? html`<p><${Raw} value=${error.message} /></p>` : null}
    ${error.detail ? html`<p class="st-error-sub">${richText('common.error.detail', { detail: html`<${Raw} value=${error.detail} />` })}</p>` : null}
    ${error.action ? html`<p class="st-error-sub">${richText('common.error.action', { action: html`<${Raw} value=${error.action} />` })}</p>` : null}
  </div>`;
}
