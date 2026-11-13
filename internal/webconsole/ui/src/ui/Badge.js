import { html } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { statusTone, statusLabelKey } from '../lib/status.js';

/** <Badge tone="neutral|info|success|warning|danger|brand" dot>text</Badge> */
export function Badge({ tone = 'neutral', dot = false, class: className = '', children, ...rest }) {
  return html`<span class=${`badge tone-${tone} ${className}`.trim()} ...${rest}>
    ${dot ? html`<span class="badge-dot" aria-hidden="true"></span>` : null}${children}
  </span>`;
}

/** Localized status label; unknown raw statuses render untranslated. */
export function StatusLabel({ status }) {
  const key = statusLabelKey(status);
  return key ? t(key) : html`<span translate="no">${String(status || '')}</span>`;
}

/**
 * <StatusBadge status="running" /> — tone from lib/status.js; running pulses.
 * The label sits in .status-badge-label so compact layouts (mobile top bar)
 * can show the dot only while the label stays available to assistive tech.
 */
export function StatusBadge({ status, class: className = '' }) {
  const tone = statusTone(status);
  const key = statusLabelKey(status);
  return html`<${Badge} tone=${tone} dot class=${`status-badge ${String(status) === 'running' ? 'is-live' : ''} ${className}`} data-status=${status} data-testid="status-badge" title=${key ? t(key) : String(status || '')}>
    <span class="status-badge-label"><${StatusLabel} status=${status} /></span>
  <//>`;
}

/** Bare status dot with an accessible label (sidebar rows). */
export function StatusDot({ status }) {
  const key = statusLabelKey(status);
  const label = key ? t(key) : String(status || '');
  return html`<span class=${`status-dot tone-${statusTone(status)} ${String(status) === 'running' ? 'is-live' : ''}`} data-status=${status} role="img" aria-label=${label} title=${label}></span>`;
}
