// Toggle chip used in the composer toolbar (Goal / Plan / interrupt).
import { html } from '../../../lib/html.js';
import { Icon } from '../../../ui/Icon.js';

/** <ModeChip icon label hint pressed onToggle testId tone="brand|warning" /> — hint is the short explanation (tooltip). */
export function ModeChip({ icon, label, hint = '', pressed = false, onToggle, testId, tone = 'brand', disabled = false }) {
  return html`<button
    type="button"
    class=${`mode-chip tone-${tone}`}
    aria-pressed=${String(Boolean(pressed))}
    title=${hint || undefined}
    disabled=${disabled}
    data-testid=${testId}
    onClick=${() => onToggle?.(!pressed)}
  >
    <${Icon} name=${icon} size=${14} />
    <span class="mode-chip-label">${label}</span>
  </button>`;
}
