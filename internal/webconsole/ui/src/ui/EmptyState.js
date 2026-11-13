import { html } from '../lib/html.js';
import { Icon } from './Icon.js';

/** <EmptyState icon title body>actions</EmptyState> */
export function EmptyState({ icon = '', title, body = '', class: className = '', children, ...rest }) {
  return html`<div class=${`empty-state ${className}`.trim()} ...${rest}>
    ${icon ? html`<span class="empty-state-icon"><${Icon} name=${icon} size=${20} /></span>` : null}
    ${title ? html`<p class="empty-state-title">${title}</p>` : null}
    ${body ? html`<p class="empty-state-body">${body}</p>` : null}
    ${children ? html`<div class="empty-state-actions">${children}</div>` : null}
  </div>`;
}
