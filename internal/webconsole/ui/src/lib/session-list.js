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
 * Prefers the server-derived summary.title (first operator prompt); knownTitles
 * maps session id -> first prompt learned from opened sessions.
 */
export function summaryTitle(item, knownTitles = {}) {
  const learned = item?.id ? knownTitles[item.id] : '';
  return firstLine(item?.title || learned || item?.goal_objective || item?.plan_mode_summary || item?.agent_name || '', 120);
}

export function filterSessions(items, query, knownTitles = {}) {
  const needle = normalizeText(query);
  if (!needle) return items;
  return items.filter((item) => [summaryTitle(item, knownTitles), item.title, item.id, item.workdir, item.agent_name, item.model]
    .some((value) => normalizeText(value).includes(needle)));
}

function startOfDay(ms) {
  const date = new Date(ms);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
}

export const SESSION_GROUPS = Object.freeze(['attention', 'today', 'yesterday', 'week', 'older']);

/** Rows shown per sidebar group before "Show N more". */
export const SIDEBAR_GROUP_LIMIT = 6;

/**
 * Display status of a sidebar row. Summaries use the plan-mode aware
 * sessionSummaryDisplayStatus; the open session uses current.status (the
 * detail-derived status the top bar shows) so the two never disagree while
 * the polled overview lags behind the session detail.
 * @param {object} item session summary
 * @param {{id?: string, status?: string}} [current] open session id + display status
 */
export function sidebarRowStatus(item, current = {}) {
  if (current?.id && current.status && item?.id === current.id) return current.status;
  return sessionSummaryDisplayStatus(item);
}

/**
 * Groups summaries for the sidebar: attention (running / awaiting_*) first,
 * then today / yesterday / previous 7 days / older by updated_at (newest first).
 * Returns [{ key, items }] without empty groups. options.statusOf(item)
 * overrides the display status used for the attention group.
 */
export function groupSessions(items, now = Date.now(), { statusOf = sessionSummaryDisplayStatus } = {}) {
  const groups = Object.fromEntries(SESSION_GROUPS.map((key) => [key, []]));
  const today = startOfDay(now);
  const yesterday = today - 86400000;
  const weekAgo = today - 6 * 86400000;
  const sorted = [...(items || [])].sort((a, b) => (toTimestamp(b.updated_at || b.created_at) || 0) - (toTimestamp(a.updated_at || a.created_at) || 0));
  for (const item of sorted) {
    if (isAttentionStatus(statusOf(item))) {
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

/**
 * Caps one sidebar group: the first `limit` rows (in order) plus the active
 * row wherever it is, so the open session is always visible. Nothing is
 * hidden when expanded or when at most one row would be hidden.
 * -> { visible, hiddenCount }
 */
export function capSessionGroup(items, { limit = SIDEBAR_GROUP_LIMIT, expanded = false, activeId = '' } = {}) {
  const list = Array.isArray(items) ? items : [];
  if (expanded || list.length <= limit + 1) return { visible: list, hiddenCount: 0 };
  const visible = list.filter((item, index) => index < limit || (activeId && item?.id === activeId));
  return { visible, hiddenCount: list.length - visible.length };
}

/** First user prompt of a session when the loaded window starts at the beginning. */
export function firstPromptFromMessages(messages, hasMoreEarlier) {
  if (hasMoreEarlier) return '';
  const first = (messages || []).find((message) => message?.role === 'user' && String(message.text || '').trim());
  return first ? firstLine(first.text, 120) : '';
}

/** True when both windows hold messages and share no message id (a gap may sit between them). */
export function windowsDisjoint(current, tail) {
  const currentIds = new Set((current || []).map((message) => message?.id).filter(Boolean));
  const tailIds = (tail || []).map((message) => message?.id).filter(Boolean);
  if (!currentIds.size || !tailIds.length) return false;
  return !tailIds.some((id) => currentIds.has(id));
}

/**
 * Closes a gap between the loaded window (known) and a newer tail that shares
 * no id with it by paging backwards from the tail: fetchPage(beforeId) must
 * resolve to { messages, has_more }. Stops as soon as a page overlaps the
 * known window (bridged: messages = known + everything after it), when the
 * history start is reached (bridged: messages = the fetched history), or
 * after maxPages (not bridged: messages = what was fetched, older messages
 * remain reachable through "load earlier"). Never drops loaded messages
 * silently.
 */
export async function bridgeMessageGap({ known = [], tail = [], fetchPage, maxPages = 10 }) {
  const knownIds = new Set(known.map((message) => message?.id).filter(Boolean));
  let collected = tail.slice();
  for (let page = 0; page < maxPages; page += 1) {
    const beforeId = collected[0]?.id;
    if (!beforeId) break;
    const result = await fetchPage(beforeId);
    const messages = Array.isArray(result?.messages) ? result.messages : [];
    collected = mergeMessageWindows(messages, collected);
    if (messages.some((message) => knownIds.has(message?.id))) {
      return { bridged: true, reachedStart: false, messages: mergeMessageWindows(known, collected) };
    }
    if (!messages.length || !result?.has_more) {
      return { bridged: true, reachedStart: true, messages: collected };
    }
  }
  return { bridged: false, reachedStart: false, messages: collected };
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
