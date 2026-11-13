// Shared presentational helpers for the inspector tabs: descriptor text,
// section / fact / metric layout, status pills, times and session links.
// Durable raw values always render inside translate="no" nodes; operator
// copy goes through t()/tx().
import { html } from '../../lib/html.js';
import { t, tx, hasKey, formatDateTime, formatNumber, formatRelativeTime } from '../../lib/i18n.js';
import { statusLabel } from '../../lib/session-model.js';
import { formatDuration } from '../../lib/format.js';
import { openSession, sessionPath, toggleInspector } from '../../state/actions.js';
import { Badge } from '../../ui/Badge.js';
import { CopyButton } from '../../ui/CopyButton.js';
import { badgeTone } from './inspector-model.js';

const isDescriptor = (value) => Boolean(value) && typeof value === 'object' && !Array.isArray(value) && ('key' in value || 'raw' in value);

function isoLike(value) {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(value);
}

/** Resolves nested descriptor params and formats ISO "at" params for the locale. */
export function resolveParams(params) {
  const out = {};
  for (const [name, value] of Object.entries(params || {})) {
    if (isDescriptor(value)) out[name] = value.key ? text(value) : String(value.raw ?? '');
    else if (name === 'at' && isoLike(value)) out[name] = formatDateTime(value, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) || value;
    else out[name] = value;
  }
  return out;
}

/**
 * Localized text of a descriptor's key part ('' when it only carries raw).
 * status.<token> prefers inspector.status.<token> for tokens outside the
 * shared common.status family (in_progress, ready, blocked, ...).
 */
export function text(descriptor) {
  if (descriptor === null || descriptor === undefined) return '';
  if (typeof descriptor === 'string') return tx(descriptor);
  const key = String(descriptor.key || '');
  if (!key) return '';
  const params = resolveParams(descriptor.params);
  const own = 'inspector.' + key;
  if (key.startsWith('status.') && hasKey(own)) return t(own, params);
  return tx({ key, params });
}

/** Localized status label for a raw status token. */
export function statusText(status) {
  return text(statusLabel(status));
}

/** Renders a descriptor: localized label, raw value (translate=no) or both. */
export function Txt({ d, class: className = '' }) {
  if (!d) return null;
  if (typeof d === 'string') return html`<span class=${className || undefined}>${text(d)}</span>`;
  const label = d.key ? text(d) : '';
  const raw = d.raw !== undefined && d.raw !== null ? String(d.raw) : '';
  if (label && raw) {
    return html`<span class=${className || undefined}><span class="insp-txt-label">${label}</span> <span class="insp-raw" translate="no">${raw}</span></span>`;
  }
  if (raw) return html`<span class=${`insp-raw ${className}`.trim()} translate="no">${raw}</span>`;
  return html`<span class=${className || undefined}>${label}</span>`;
}

/** Durable value (path, id, model text…) that must never be translated. */
export function Raw({ children, class: className = '', title }) {
  return html`<span class=${`insp-raw ${className}`.trim()} translate="no" title=${title}>${children}</span>`;
}

/** Status badge with localized label; tone from badgeTone(). */
export function StatusPill({ status, label, class: className = '', testId }) {
  const value = String(status || 'unknown');
  return html`<${Badge} tone=${badgeTone(value)} dot class=${`insp-status ${className}`.trim()} data-status=${value} data-testid=${testId}>${label || statusText(value)}<//>`;
}

/** <time> with compact local format and the full timestamp as title. */
export function TimeText({ at, relative = false, class: className = '' }) {
  if (!at) return null;
  const full = formatDateTime(at, { dateStyle: 'medium', timeStyle: 'medium' });
  if (!full) return null;
  const shown = relative ? formatRelativeTime(at) : formatDateTime(at, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  return html`<time class=${`insp-time ${className}`.trim()} datetime=${at} title=${full}>${shown}</time>`;
}

export function num(value) {
  return formatNumber(Number(value || 0), { maximumFractionDigits: 0 });
}

export function duration(ms) {
  return formatDuration(Number(ms || 0));
}

/** Titled block inside a tab. */
export function Section({ title, aside = null, children, testId, class: className = '' }) {
  return html`<section class=${`insp-section ${className}`.trim()} data-testid=${testId}>
    ${title || aside ? html`<div class="insp-section-head">
      ${title ? html`<h3 class="insp-section-title">${title}</h3>` : html`<span></span>`}
      ${aside}
    </div>` : null}
    ${children}
  </section>`;
}

/** Definition list of label/value facts. */
export function Facts({ children, class: className = '' }) {
  return html`<dl class=${`insp-facts ${className}`.trim()}>${children}</dl>`;
}

export function Fact({ label, children, testId }) {
  if (children === null || children === undefined || children === false || children === '') return null;
  return html`<div class="insp-fact" data-testid=${testId}><dt>${label}</dt><dd>${children}</dd></div>`;
}

/** Small stat tiles: [{ label, value, sub?, testId? }] */
export function Metrics({ items, class: className = '' }) {
  return html`<div class=${`insp-metrics ${className}`.trim()}>
    ${items.filter(Boolean).map((item) => html`<div class="insp-metric" data-testid=${item.testId}>
      <span class="insp-metric-value">${item.value}</span>
      <span class="insp-metric-label">${item.label}</span>
      ${item.sub ? html`<span class="insp-metric-sub">${item.sub}</span>` : null}
    </div>`)}
  </div>`;
}

/** Raw chips (loaded skills, ids, paths). */
export function RawChips({ values, max = 0 }) {
  const list = (values || []).filter(Boolean);
  if (!list.length) return null;
  const shown = max > 0 ? list.slice(0, max) : list;
  return html`<span class="insp-chips">
    ${shown.map((value) => html`<span class="chip" translate="no" title=${value}><span>${value}</span></span>`)}
    ${max > 0 && list.length > max ? html`<span class="chip insp-chip-more">+${list.length - max}</span>` : null}
  </span>`;
}

/** Raw value followed by a copy button. */
export function CopyValue({ value, display, label }) {
  if (!value) return null;
  return html`<span class="insp-copy-value">
    <span class="insp-raw insp-mono" translate="no" title=${value}>${display || value}</span>
    <${CopyButton} text=${value} label=${label || t('common.copy')} class="insp-copy-btn" />
  </span>`;
}

/**
 * Link that opens a durable session (/s/<id>) through the router. In the
 * overlay inspector it closes the sheet first so the opened session is visible.
 */
export function SessionLink({ id, children, mode = 'panel', testId, class: className = '' }) {
  if (!id) return null;
  const onClick = (event) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (mode === 'overlay') toggleInspector(false);
    openSession(id);
  };
  return html`<a class=${`insp-link ${className}`.trim()} href=${sessionPath(id)} onClick=${onClick} data-testid=${testId} data-session-id=${id}>${children}</a>`;
}

/** Inline notice block (tone: info | warning | danger | neutral). */
export function Notice({ tone = 'neutral', title, children, role = 'note', testId, actions = null }) {
  return html`<div class=${`insp-notice tone-${tone}`} role=${role} data-testid=${testId}>
    ${title ? html`<p class="insp-notice-title">${title}</p>` : null}
    ${children ? html`<div class="insp-notice-body">${children}</div>` : null}
    ${actions ? html`<div class="insp-notice-actions">${actions}</div>` : null}
  </div>`;
}

/** Quiet empty message inside a section. */
export function Muted({ children, testId }) {
  return html`<p class="insp-muted" data-testid=${testId}>${children}</p>`;
}

function prefersReducedMotion() {
  try {
    return globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
  } catch {
    return false;
  }
}

/**
 * Scrolls the conversation to an inline card ([data-testid=<testId>]) and
 * moves focus to it. From the overlay inspector the sheet closes first so the
 * background is no longer inert (spec 17 §7.3).
 */
export function revealInlineCard(testId, mode = 'panel') {
  if (mode === 'overlay') toggleInspector(false);
  const raf = globalThis.requestAnimationFrame || ((fn) => setTimeout(fn, 16));
  const attempt = (tries) => {
    const card = globalThis.document?.querySelector('[data-testid="' + testId + '"]');
    if (!card || card.closest('[inert]')) {
      if (tries < 20) raf(() => attempt(tries + 1));
      return;
    }
    card.scrollIntoView({ block: 'center', behavior: prefersReducedMotion() ? 'auto' : 'smooth' });
    if (!card.hasAttribute('tabindex')) card.setAttribute('tabindex', '-1');
    card.focus({ preventScroll: true });
  };
  raf(() => raf(() => attempt(0)));
}
