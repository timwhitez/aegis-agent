// Translated copy with raw slots: richText(key, { path: rawNode }) renders the
// localized template from t(key) and substitutes {slot} placeholders with
// nodes, so durable values (paths, names) stay in translate="no" elements and
// never pass through t() interpolation.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { splitTemplate } from './workspace-helpers.js';

/** Localized template of key with {slots} replaced by nodes or strings. */
export function richText(key, slots = {}) {
  return splitTemplate(t(key)).map((part) => {
    if ('text' in part) return part.text;
    const value = slots[part.slot];
    return value === undefined || value === null ? '' : value;
  });
}

/**
 * Raw durable value. Preact assigns the boolean translate property, so the
 * attribute must be written as translate=${false} to serialize as "no".
 */
export function Raw({ value, class: className = '', title }) {
  return html`<span class=${`raw-value ${className}`.trim()} translate=${false} title=${title}>${String(value ?? '')}</span>`;
}

/** Descriptor slot from lib/ ({key, params} or {raw}) with raw params. */
export function SlotText({ slot }) {
  if (!slot) return null;
  if (slot.raw !== undefined) return html`<${Raw} value=${slot.raw} />`;
  const params = slot.params || {};
  const nodes = {};
  for (const [name, value] of Object.entries(params)) nodes[name] = html`<${Raw} value=${value} />`;
  return html`<span>${richText(slot.key, nodes)}</span>`;
}

/** Backend error lines: message plus detail/action (server text stays raw). */
export function ErrorLines({ error, class: className = '' }) {
  if (!error) return null;
  return html`<div class=${`error-lines ${className}`.trim()}>
    ${error.message ? html`<p class="error-lines-message"><${Raw} value=${error.message} /></p>` : null}
    ${error.detail ? html`<p class="error-lines-detail">${richText('common.error.detail', { detail: html`<${Raw} value=${error.detail} />` })}</p>` : null}
    ${error.action ? html`<p class="error-lines-detail">${richText('common.error.action', { action: html`<${Raw} value=${error.action} />` })}</p>` : null}
  </div>`;
}

/** { message, detail, action, status } of an APIError (keeps backend detail/action). */
export function errorInfo(error, fallback = '') {
  return {
    message: error?.message || fallback,
    detail: error?.detail || '',
    action: error?.action || '',
    status: Number(error?.status || 0),
    code: error?.code || '',
  };
}
