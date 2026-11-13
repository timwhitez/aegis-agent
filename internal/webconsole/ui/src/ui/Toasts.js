import { html } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { useStore } from '../state/hooks.js';
import { dismissToast, MAX_VISIBLE_TOASTS } from '../state/toasts.js';
import { Icon } from './Icon.js';
import { IconButton } from './IconButton.js';

const TONE_ICON = { info: 'info', success: 'check', warning: 'alert-triangle', danger: 'alert-circle' };

/**
 * Where the rack sits (styles/components.css): top-right of the main column
 * below the top bar on session views (left of an inline inspector), inside
 * the quiet top-bar band on page views with older toasts collapsed behind
 * the newest (expanded on hover / keyboard focus) so page-header actions and
 * filters stay clear, and top-centre on mobile. Never at the bottom, where
 * the composer, stop/send and failed-draft controls live.
 */
export function toastPlacement(state) {
  const sessionRoute = state.route.name === 'new' || state.route.name === 'session';
  if (!sessionRoute) return 'page';
  return state.ui.inspectorOpen && state.ui.viewport.wide ? 'beside-inspector' : 'session';
}

/** Toast rack: polite live region; danger toasts use role=alert. */
export function Toasts() {
  const view = useStore((state) => ({ toasts: state.toasts, placement: toastPlacement(state) }));
  const toasts = view.toasts.slice(-MAX_VISIBLE_TOASTS);
  // Depth 0 is the newest toast; page views collapse older ones behind it.
  const depth = (index) => toasts.length - 1 - index;
  return html`<section class="toast-rack" data-placement=${view.placement} aria-live="polite" aria-label=${t('common.toast.region')} data-testid="toasts">
    ${toasts.map((toast, index) => html`<div key=${toast.id} class=${`toast tone-${toast.tone}`} style=${`--toast-depth:${depth(index)}`} role=${toast.tone === 'danger' ? 'alert' : 'status'} data-testid="toast">
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
