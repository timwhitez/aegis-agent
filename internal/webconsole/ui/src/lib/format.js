// Display formatting helpers that do not depend on locale dictionaries.
// Locale-aware date/number helpers live in i18n.js.

/** Short display form of an id: the last "-"/"_" segment when it is 4..length chars. */
export function shortId(id, length = 8) {
  const text = String(id || '');
  if (text.length <= length) return text;
  const tail = text.split(/[_-]/).pop() || '';
  if (tail.length >= 4 && tail.length <= length) return tail;
  return text.slice(-length);
}

export function truncate(text, max = 80) {
  const value = String(text ?? '');
  if (value.length <= max) return value;
  return `${value.slice(0, Math.max(0, max - 1)).trimEnd()}…`;
}

export function firstLine(text, max = 120) {
  const line = String(text ?? '').split(/\r?\n/).map((part) => part.trim()).find(Boolean) || '';
  return truncate(line, max);
}

export function formatBytes(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return '';
  if (value < 1024) return `${value} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let size = value / 1024;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

/** Compact duration such as "4s", "2m 05s", "1h 02m". */
export function formatDuration(ms) {
  const total = Math.max(0, Math.floor(Number(ms) / 1000));
  if (!Number.isFinite(total)) return '';
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  if (hours > 0) return `${hours}h ${String(minutes).padStart(2, '0')}m`;
  if (minutes > 0) return `${minutes}m ${String(seconds).padStart(2, '0')}s`;
  return `${seconds}s`;
}

export function prettyJSON(value) {
  if (typeof value === 'string') {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  try {
    return JSON.stringify(value, null, 2) ?? '';
  } catch {
    return String(value);
  }
}

export function basename(path) {
  const text = String(path || '').replace(/[\\/]+$/, '');
  const index = Math.max(text.lastIndexOf('/'), text.lastIndexOf('\\'));
  return index >= 0 ? text.slice(index + 1) : text;
}

/** Keeps the last path segments of a long path: "…/project/src". */
export function compactPath(path, max = 36) {
  const text = String(path || '');
  if (text.length <= max) return text;
  const parts = text.split('/').filter(Boolean);
  let result = '';
  for (let i = parts.length - 1; i >= 0; i -= 1) {
    const next = `/${parts[i]}${result}`;
    if (next.length + 1 > max && result) break;
    result = next;
  }
  return `…${result}`;
}

export function toTimestamp(value) {
  if (value instanceof Date) return value.getTime();
  if (typeof value === 'number') return value;
  const parsed = Date.parse(String(value || ''));
  return Number.isFinite(parsed) ? parsed : NaN;
}
