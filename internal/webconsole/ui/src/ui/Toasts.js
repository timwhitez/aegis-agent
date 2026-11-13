import { html } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { useStore } from '../state/hooks.js';
import { dismissToast } from '../state/toasts.js';
import { Icon } from './Icon.js';
import { IconButton } from './IconButton.js';

const TONE_ICON = { info: 'info', success: 'check', warning: 'alert-triangle', danger: 'alert-circle' };

/** Toast rack: polite live region; danger toasts use role=alert. */
export function Toasts() {
  const toasts = useStore((state) => state.toasts, Object.is);
  return html`<section class="toast-rack" aria-live="polite" aria-label=${t('common.toast.region')} data-testid="toasts">
    ${toasts.map((toast) => html`<div key=${toast.id} class=${`toast tone-${toast.tone}`} role=${toast.tone === 'danger' ? 'alert' : 'status'} data-testid="toast">
      <span class="toast-icon"><${Icon} name=${TONE_ICON[toast.tone] || 'info'} size=${16} /></span>
      <div class="toast-body">
        <p class="toast-message">${toast.message}</p>
        ${toast.detail ? html`<p class="toast-detail">${t('common.error.detail', { detail: toast.detail })}</p>` : null}
        ${toast.action ? html`<p class="toast-detail">${t('common.error.action', { action: toast.action })}</p>` : null}
      </div>
      <${IconButton} icon="x" size="sm" label=${t('common.dismiss')} class="toast-close" onClick=${() => dismissToast(toast.id)} />
    </div>`)}
  </section>`;
}
