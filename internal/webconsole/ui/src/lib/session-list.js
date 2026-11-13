// Sidebar / palette session list derivation (pure).
import { sessionSummaryDisplayStatus, isAttentionStatus } from './status.js';
import { firstLine, toTimestamp } from './format.js';
import { normalizeText } from './commands.js';

/** Root sessions only (children are reachable from the inspector). */
export function rootSessions(items) {
  return (Array.isArray(items) ? items : []).filter((item) => item && item.id && !item.parent_session_id
    && (!item.root_session_id || item.root_session_id === item.id) && !(item.depth > 0));
}

/**
 * Durable title text for a summary, or '' when only a UI fallback is possible.
 * knownTitles maps session id -> first prompt learned from opened sessions.
 */
export function summaryTitle(item, knownTitles = {}) {
  const learned = item?.id ? knownTitles[item.id] : '';
  return firstLine(learned || item?.goal_objective || item?.plan_mode_summary || item?.agent_name || '', 120);
}

export function filterSessions(items, query, knownTitles = {}) {
  const needle = normalizeText(query);
  if (!needle) return items;
  return items.filter((item) => [summaryTitle(item, knownTitles), item.id, item.workdir, item.agent_name, item.model]
    .some((value) => normalizeText(value).includes(needle)));
}

function startOfDay(ms) {
  const date = new Date(ms);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
}

export const SESSION_GROUPS = Object.freeze(['attention', 'today', 'yesterday', 'week', 'older']);

/**
 * Groups summaries for the sidebar: attention (running / awaiting_*) first,
 * then today / yesterday / previous 7 days / older by updated_at (newest first).
 * Returns [{ key, items }] without empty groups.
 */
export function groupSessions(items, now = Date.now()) {
  const groups = Object.fromEntries(SESSION_GROUPS.map((key) => [key, []]));
  const today = startOfDay(now);
  const yesterday = today - 86400000;
  const weekAgo = today - 6 * 86400000;
  const sorted = [...(items || [])].sort((a, b) => (toTimestamp(b.updated_at || b.created_at) || 0) - (toTimestamp(a.updated_at || a.created_at) || 0));
  for (const item of sorted) {
    if (isAttentionStatus(sessionSummaryDisplayStatus(item))) {
      groups.attention.push(item);
      continue;
    }
    const time = toTimestamp(item.updated_at || item.created_at);
    if (!Number.isFinite(time) || time < weekAgo) groups.older.push(item);
    else if (time >= today) groups.today.push(item);
    else if (time >= yesterday) groups.yesterday.push(item);
    else groups.week.push(item);
  }
  return SESSION_GROUPS.map((key) => ({ key, items: groups[key] })).filter((group) => group.items.length > 0);
}

/** First user prompt of a session when the loaded window starts at the beginning. */
export function firstPromptFromMessages(messages, hasMoreEarlier) {
  if (hasMoreEarlier) return '';
  const first = (messages || []).find((message) => message?.role === 'user' && String(message.text || '').trim());
  return first ? firstLine(first.text, 120) : '';
}

/**
 * Merges two message windows by id. `later` wins for duplicate ids and its new
 * ids are appended after `earlier`. Used for tail polling (merge(current,
 * tail)) and for earlier pages (merge(page, current)). Local helper; the
 * canonical message-flow derivation lives in session-model.js.
 */
export function mergeMessageWindows(earlier, later) {
  const keyOf = (message, index, side) => message?.id || `${side}:${message?.role || ''}:${message?.created_at || ''}:${index}`;
  const laterByKey = new Map();
  (later || []).forEach((message, index) => laterByKey.set(keyOf(message, index, 'b'), message));
  const seen = new Set();
  const result = [];
  (earlier || []).forEach((message, index) => {
    const key = keyOf(message, index, 'a');
    if (seen.has(key)) return;
    seen.add(key);
    result.push(laterByKey.has(key) ? laterByKey.get(key) : message);
  });
  (later || []).forEach((message, index) => {
    const key = keyOf(message, index, 'b');
    if (seen.has(key)) return;
    seen.add(key);
    result.push(message);
  });
  return result;
}
