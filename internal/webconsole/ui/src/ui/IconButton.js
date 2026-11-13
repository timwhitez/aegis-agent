import { html } from '../lib/html.js';
import { Icon } from './Icon.js';
import { Spinner } from './Spinner.js';

/**
 * <IconButton icon="x" label="Close" variant="ghost|secondary|primary|danger" size="sm|md" pressed? pending? />
 * label is required: it becomes aria-label and the native tooltip.
 */
export function IconButton({
  icon,
  label,
  variant = 'ghost',
  size = 'md',
  pressed,
  pending = false,
  disabled = false,
  tooltip = true,
  type = 'button',
  class: className = '',
  buttonRef,
  ...rest
}) {
  if (!label) throw new Error(`IconButton "${icon}" requires a label`);
  const classes = ['icon-btn', `btn-${variant}`, `icon-btn-${size}`, pending ? 'is-pending' : '', className].filter(Boolean).join(' ');
  return html`<button
    ref=${buttonRef}
    type=${type}
    class=${classes}
    aria-label=${label}
    title=${tooltip ? label : undefined}
    aria-pressed=${pressed === undefined ? undefined : String(Boolean(pressed))}
    aria-busy=${pending ? 'true' : undefined}
    disabled=${disabled || pending}
    ...${rest}
  >${pending ? html`<${Spinner} size=${size === 'sm' ? 14 : 16} />` : html`<${Icon} name=${icon} size=${size === 'sm' ? 15 : 17} />`}</button>`;
}
