import { html } from '../lib/html.js';
import { Icon } from './Icon.js';
import { Spinner } from './Spinner.js';

/**
 * <Button variant="primary|secondary|ghost|danger" size="sm|md|lg" pending icon iconRight block>
 * pending disables the button, sets aria-busy and swaps the icon for a spinner.
 * buttonRef receives the DOM <button> (plain `ref` on a function component does not).
 */
export function Button({
  variant = 'secondary',
  size = 'md',
  pending = false,
  disabled = false,
  icon = '',
  iconRight = '',
  block = false,
  type = 'button',
  class: className = '',
  buttonRef,
  children,
  ...rest
}) {
  const classes = ['btn', `btn-${variant}`, `btn-${size}`, block ? 'btn-block' : '', pending ? 'is-pending' : '', className].filter(Boolean).join(' ');
  const iconSize = size === 'sm' ? 14 : 16;
  return html`<button ref=${buttonRef} type=${type} class=${classes} disabled=${disabled || pending} aria-busy=${pending ? 'true' : undefined} ...${rest}>
    ${pending ? html`<${Spinner} size=${iconSize} />` : icon ? html`<${Icon} name=${icon} size=${iconSize} />` : null}
    ${children !== undefined && children !== null && children !== false ? html`<span class="btn-label">${children}</span>` : null}
    ${iconRight ? html`<${Icon} name=${iconRight} size=${iconSize} />` : null}
  </button>`;
}
