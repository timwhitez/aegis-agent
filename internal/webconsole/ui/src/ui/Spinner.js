import { html } from '../lib/html.js';
import { Icon } from './Icon.js';

/** <Spinner size label? /> — label makes it a polite status for assistive tech. */
export function Spinner({ size = 16, label = '', class: className = '' }) {
  return html`<span class=${`spinner ${className}`.trim()} role=${label ? 'status' : undefined} aria-label=${label || undefined}>
    <${Icon} name="loader" size=${size} />
  </span>`;
}
