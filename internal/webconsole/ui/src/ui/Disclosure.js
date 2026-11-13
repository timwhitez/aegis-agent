import { html, useId, useState } from '../lib/html.js';
import { Icon } from './Icon.js';

/**
 * <Disclosure summary=${node} defaultOpen open? onToggle?>content</Disclosure>
 * Controlled when open is provided. Content is not rendered while closed.
 */
export function Disclosure({ summary, defaultOpen = false, open, onToggle, class: className = '', children, ...rest }) {
  const [innerOpen, setInnerOpen] = useState(defaultOpen);
  const isOpen = open === undefined ? innerOpen : open;
  const id = useId();
  const toggle = () => {
    if (open === undefined) setInnerOpen(!isOpen);
    onToggle?.(!isOpen);
  };
  return html`<div class=${`disclosure ${isOpen ? 'is-open' : ''} ${className}`.trim()} ...${rest}>
    <button type="button" class="disclosure-trigger" aria-expanded=${String(isOpen)} aria-controls=${id} onClick=${toggle}>
      <${Icon} name="chevron-right" size=${14} class="disclosure-chevron" />
      <span class="disclosure-summary">${summary}</span>
    </button>
    <div id=${id} class="disclosure-content" hidden=${!isOpen}>${isOpen ? children : null}</div>
  </div>`;
}
