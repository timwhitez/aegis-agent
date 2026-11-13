// Renders a lib text slot: { key, params } → operator text, { raw } → durable
// text inside translate="no", { key, raw } → label node followed by a separate
// raw node. Fallback labels and raw values never share a DOM text node.
import { html } from '../../../lib/html.js';
import { slotText } from './model.js';

export function Slot({ slot, class: className = '', rawClass = '' }) {
  if (!slot || typeof slot !== 'object') return null;
  const label = slotText(slot);
  const raw = slot.raw !== undefined && slot.raw !== null && slot.raw !== '' ? String(slot.raw) : '';
  const secondary = slot.secondaryRaw ? String(slot.secondaryRaw) : '';
  if (!label && !raw && !secondary) return null;
  if (label && !raw && !secondary) return html`<span class=${`slot ${className}`.trim()}>${label}</span>`;
  return html`<span class=${`slot ${className}`.trim()}>
    ${label ? html`<span class="slot-label">${label}</span>` : null}
    ${raw ? html`<span class=${`slot-raw ${rawClass}`.trim()} translate="no">${raw}</span>` : null}
    ${secondary ? html`<span class="slot-secondary" translate="no">${secondary}</span>` : null}
  </span>`;
}
