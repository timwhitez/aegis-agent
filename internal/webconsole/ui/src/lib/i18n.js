// Locale state, t() lookup and Intl formatting. Pure: dictionaries are
// registered by src/i18n/index.js; browser globals are touched only when
// present, so node:test can import this module directly.

export const LOCALES = Object.freeze(['zh-CN', 'en']);
export const DEFAULT_LOCALE = 'zh-CN';
export const LOCALE_STORAGE_KEY = 'aegis-agent.locale.v1';

let dictionaries = { 'zh-CN': {}, en: {} };
let current = DEFAULT_LOCALE;
const listeners = new Set();

export function normalizeLocale(value) {
  const text = String(value || '').trim().toLowerCase();
  if (text.startsWith('en')) return 'en';
  if (text.startsWith('zh')) return 'zh-CN';
  return null;
}

/** Replaces the active dictionaries: { 'zh-CN': {key: text}, en: {...} }. */
export function registerDictionaries(next) {
  dictionaries = { 'zh-CN': { ...(next?.['zh-CN'] || {}) }, en: { ...(next?.en || {}) } };
}

export function getDictionaries() {
  return dictionaries;
}

export function getLocale() {
  return current;
}

function storage() {
  try {
    return globalThis.localStorage || null;
  } catch {
    return null;
  }
}

function applyDocumentLocale(locale) {
  const root = globalThis.document?.documentElement;
  if (root) root.lang = locale;
}

/** Reads the persisted preference (default zh-CN) and syncs <html lang>. */
export function initLocale() {
  let stored = null;
  try {
    stored = storage()?.getItem(LOCALE_STORAGE_KEY);
  } catch {
    stored = null;
  }
  current = normalizeLocale(stored) || DEFAULT_LOCALE;
  applyDocumentLocale(current);
  return current;
}

export function setLocale(locale, { persist = true } = {}) {
  const next = normalizeLocale(locale) || DEFAULT_LOCALE;
  if (persist) {
    try {
      storage()?.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      // Storage may be unavailable (private mode); the in-memory locale still applies.
    }
  }
  applyDocumentLocale(next);
  if (next === current) return current;
  current = next;
  for (const listener of Array.from(listeners)) listener(current);
  return current;
}

export function toggleLocale() {
  return setLocale(current === 'en' ? 'zh-CN' : 'en');
}

export function subscribeLocale(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function hasKey(key, locale = current) {
  return Object.prototype.hasOwnProperty.call(dictionaries[locale] || {}, key);
}

export function interpolate(template, params) {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(params, name) && params[name] !== undefined && params[name] !== null
      ? String(params[name])
      : match
  ));
}

/** t(key, params) -> localized text with {name} interpolation; falls back to zh-CN, then the key. */
export function t(key, params) {
  const table = dictionaries[current] || {};
  let template = table[key];
  if (typeof template !== 'string') template = dictionaries[DEFAULT_LOCALE]?.[key];
  if (typeof template !== 'string') return key;
  return interpolate(template, params);
}

// ---- Intl helpers --------------------------------------------------------------

function toDate(value) {
  if (value instanceof Date) return value;
  if (typeof value === 'number') return new Date(value);
  const parsed = Date.parse(String(value || ''));
  return Number.isFinite(parsed) ? new Date(parsed) : null;
}

export function formatNumber(value, options) {
  const number = Number(value);
  if (!Number.isFinite(number)) return '';
  return new Intl.NumberFormat(current, options).format(number);
}

export function formatDateTime(value, options = { dateStyle: 'medium', timeStyle: 'short' }) {
  const date = toDate(value);
  if (!date) return '';
  return new Intl.DateTimeFormat(current, options).format(date);
}

export function formatTime(value) {
  return formatDateTime(value, { hour: '2-digit', minute: '2-digit' });
}

/** "刚刚" / "5 分钟前" / "yesterday"; dates older than a week are absolute. */
export function formatRelativeTime(value, now = Date.now()) {
  const date = toDate(value);
  if (!date) return '';
  const diffSeconds = Math.round((date.getTime() - now) / 1000);
  const abs = Math.abs(diffSeconds);
  if (abs < 45) return t('common.time.justNow');
  const rtf = new Intl.RelativeTimeFormat(current, { numeric: 'auto', style: 'short' });
  if (abs < 45 * 60) return rtf.format(Math.round(diffSeconds / 60), 'minute');
  if (abs < 22 * 3600) return rtf.format(Math.round(diffSeconds / 3600), 'hour');
  if (abs < 7 * 86400) return rtf.format(Math.round(diffSeconds / 86400), 'day');
  return formatDateTime(date, { month: 'short', day: 'numeric' });
}
