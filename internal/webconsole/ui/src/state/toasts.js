// Toast queue. Toasts are one-shot feedback; object-level errors render inline.
import { getState, update } from './store.js';
import { t } from '../lib/i18n.js';

let nextToastId = 1;
const timers = new Map();

/** pushToast({ tone: 'info'|'success'|'warning'|'danger', message, detail?, action?, timeout? }) -> id */
export function pushToast({ tone = 'info', message, detail = '', action = '', timeout } = {}) {
  const id = nextToastId++;
  const duration = timeout ?? (tone === 'danger' ? 8000 : 4500);
  update('toasts', (toasts) => [...toasts.slice(-4), { id, tone, message: String(message || ''), detail, action, timeout: duration }]);
  if (duration > 0 && typeof setTimeout === 'function') {
    timers.set(id, setTimeout(() => dismissToast(id), duration));
  }
  return id;
}

export function dismissToast(id) {
  const timer = timers.get(id);
  if (timer) clearTimeout(timer);
  timers.delete(id);
  if (!getState().toasts.some((toast) => toast.id === id)) return;
  update('toasts', (toasts) => toasts.filter((toast) => toast.id !== id));
}

/** Error toast keeping the backend detail/action of an APIError. */
export function toastError(error, fallbackKey = 'common.error.title') {
  const message = error?.message || t(fallbackKey);
  return pushToast({
    tone: 'danger',
    message: error?.status === 0 && !error?.message ? t('common.error.network') : message,
    detail: error?.detail || '',
    action: error?.action || '',
  });
}
