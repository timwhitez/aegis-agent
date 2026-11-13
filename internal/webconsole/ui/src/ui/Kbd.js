import { html } from '../lib/html.js';
import { modKeyLabel } from '../lib/shortcuts.js';

/** <Kbd keys=${['mod', 'K']} /> — 'mod' renders ⌘ on macOS and Ctrl elsewhere. */
export function Kbd({ keys = [], class: className = '' }) {
  return html`<span class=${`kbd-group ${className}`.trim()}>
    ${keys.map((key) => html`<kbd class="kbd">${key === 'mod' ? modKeyLabel() : key === 'shift' ? '⇧' : key}</kbd>`)}
  </span>`;
}
