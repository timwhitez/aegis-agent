// Toast queue. Toasts are one-shot feedback; object-level errors render inline.
import { getState, update } from './store.js';
import { t } from '../lib/i18n.js';

let nextToastId = 1;
const timers = new Map();
/** At most this many toasts are kept (and shown); older ones drop off. */
export const MAX_VISIBLE_TOASTS = 3;

const TOAST_PARTS = Object.freeze(['message', 'detail', 'action']);

/**
 * Normalizes the raw marker of a toast (spec/17-web-console-frontend.md §9):
 * true marks every part, an object marks parts by name. Raw parts are durable
 * backend text (error message/detail/action) and render inside translate="no";
 * operator copy from t() stays translatable.
 * @returns {{message: boolean, detail: boolean, action: boolean}}
 */
export function toastRawParts(raw) {
  return Object.fromEntries(TOAST_PARTS.map((part) => [part, raw === true || Boolean(raw && typeof raw === 'object' && raw[part])]));
}

/**
 * pushToast({ tone: 'info'|'success'|'warning'|'danger', message, detail?, action?, raw?, timeout? }) -> id
 * raw: true | { message?, detail?, action? } marks backend-provided parts (see toastRawParts).
 */
export function pushToast({ tone = 'info', message, detail = '', action = '', raw = false, timeout } = {}) {
  const id = nextToastId++;
  const duration = timeout ?? (tone === 'danger' ? 8000 : 4500);
  const entry = { id, tone, message: String(message || ''), detail: String(detail || ''), action: String(action || ''), raw: toastRawParts(raw), timeout: duration };
  update('toasts', (toasts) => [...toasts.slice(-(MAX_VISIBLE_TOASTS - 1)), entry]);
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

/**
 * Error toast keeping the backend message/detail/action of an APIError as raw
 * text; only the localized fallback (fallbackKey, network error) is copy.
 */
export function toastError(error, fallbackKey = 'common.error.title') {
  const backendMessage = String(error?.message || '');
  const fallback = error?.status === 0 && !backendMessage ? t('common.error.network') : t(fallbackKey);
  return pushToast({
    tone: 'danger',
    message: backendMessage || fallback,
    detail: error?.detail || '',
    action: error?.action || '',
    raw: { message: Boolean(backendMessage), detail: true, action: true },
  });
}
