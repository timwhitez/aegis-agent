// Sessions page derivations (pure, DOM-free; imports lib/ only so node:test
// can load it). HistoryView pages the server list (GET /api/history) while no
// filter is active; with a search or status filter it scans the most recent
// HISTORY_SCAN_PAGE_SIZE summaries once and filters/pages them locally.
import { sessionSummaryDisplayStatus } from '../../lib/status.js';
import { normalizeText } from '../../lib/commands.js';
import { summaryTitle } from '../../lib/session-list.js';

export const HISTORY_PAGE_SIZE = 20;
/** Server maximum for page_size (handleHistory clamps to 1..200). */
export const HISTORY_SCAN_PAGE_SIZE = 200;
/** Pages scanned at most when locating a focused session (2000 sessions). */
export const HISTORY_LOCATE_MAX_PAGES = 10;

/** Status filters; labelKey values are history.* i18n keys. */
export const STATUS_FILTERS = Object.freeze([
  Object.freeze({ id: 'all', labelKey: 'history.filter.all', statuses: null }),
  Object.freeze({ id: 'active', labelKey: 'history.filter.active', statuses: Object.freeze(['running', 'queued', 'pending', 'awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input']) }),
  Object.freeze({ id: 'completed', labelKey: 'history.filter.completed', statuses: Object.freeze(['completed']) }),
  Object.freeze({ id: 'failed', labelKey: 'history.filter.failed', statuses: Object.freeze(['failed', 'error']) }),
  Object.freeze({ id: 'stopped', labelKey: 'history.filter.stopped', statuses: Object.freeze(['paused', 'cancelled']) }),
]);

const ACTIVE_STATUSES = STATUS_FILTERS[1].statuses;

export function statusFilter(id) {
  return STATUS_FILTERS.find((filter) => filter.id === id) || STATUS_FILTERS[0];
}

export function isFiltering({ query = '', status = 'all' } = {}) {
  return Boolean(normalizeText(query)) || statusFilter(status).statuses !== null;
}

export function matchesStatus(item, status = 'all') {
  const { statuses } = statusFilter(status);
  return statuses === null || statuses.includes(String(sessionSummaryDisplayStatus(item)).toLowerCase());
}

/** Case/diacritic-insensitive match on title, id, workdir, agent and provider/model. */
export function matchesQuery(item, query, knownTitles = {}) {
  const needle = normalizeText(query);
  if (!needle) return true;
  return [summaryTitle(item, knownTitles), item?.title, item?.id, item?.workdir, item?.agent_name, item?.agent_role, item?.provider, item?.model]
    .some((value) => normalizeText(value).includes(needle));
}

export function filterHistory(items, { query = '', status = 'all', knownTitles = {} } = {}) {
  return (Array.isArray(items) ? items : []).filter((item) => item?.id && matchesStatus(item, status) && matchesQuery(item, query, knownTitles));
}

export function isChildSummary(item) {
  return Boolean(item?.parent_session_id) || (item?.depth > 0) || Boolean(item?.root_session_id && item.root_session_id !== item.id);
}

export function isActiveSummary(item) {
  return ACTIVE_STATUSES.includes(String(sessionSummaryDisplayStatus(item)).toLowerCase());
}

/**
 * Nests summaries under their parent when the parent is in the same list.
 * Keeps the server order; returns [{ item, children: [node] }].
 */
export function buildSessionTree(items) {
  const list = (Array.isArray(items) ? items : []).filter((item) => item?.id);
  const nodes = new Map(list.map((item) => [item.id, { item, children: [] }]));
  const roots = [];
  for (const item of list) {
    const node = nodes.get(item.id);
    const parent = item.parent_session_id && item.parent_session_id !== item.id ? nodes.get(item.parent_session_id) : null;
    if (parent && !isAncestor(nodes, item.id, parent.item)) parent.children.push(node);
    else roots.push(node);
  }
  return roots;
}

function isAncestor(nodes, id, candidate) {
  // Guards against parent cycles in malformed data.
  let current = candidate;
  const seen = new Set();
  while (current && !seen.has(current.id)) {
    if (current.id === id) return true;
    seen.add(current.id);
    current = current.parent_session_id ? nodes.get(current.parent_session_id)?.item : null;
  }
  return false;
}

/** Ids of ancestors of id that are present in items (nearest first). */
export function ancestorIds(items, id) {
  const byId = new Map((items || []).filter((item) => item?.id).map((item) => [item.id, item]));
  const out = [];
  const seen = new Set([id]);
  let parentId = byId.get(id)?.parent_session_id;
  while (parentId && byId.has(parentId) && !seen.has(parentId)) {
    out.push(parentId);
    seen.add(parentId);
    parentId = byId.get(parentId).parent_session_id;
  }
  return out;
}

export function clampPage(page, totalPages) {
  const value = Math.floor(Number(page) || 1);
  const max = Math.max(1, Math.floor(Number(totalPages) || 1));
  return Math.min(Math.max(1, value), max);
}

/** Local paging for filtered lists: { items, page, totalPages, total, start, end }. */
export function paginate(items, page = 1, pageSize = HISTORY_PAGE_SIZE) {
  const list = Array.isArray(items) ? items : [];
  const size = Math.max(1, Math.floor(Number(pageSize) || HISTORY_PAGE_SIZE));
  const totalPages = Math.max(1, Math.ceil(list.length / size));
  const current = clampPage(page, totalPages);
  const offset = (current - 1) * size;
  const slice = list.slice(offset, offset + size);
  return { items: slice, page: current, totalPages, total: list.length, ...pageRange({ page: current, pageSize: size, total: list.length, count: slice.length }) };
}

/** 1-based display range of a page: { start, end } (0/0 when empty). */
export function pageRange({ page = 1, pageSize = HISTORY_PAGE_SIZE, total = 0, count = 0 } = {}) {
  if (!total || !count) return { start: 0, end: 0 };
  const start = (Math.max(1, page) - 1) * pageSize + 1;
  return { start, end: Math.min(total, start + count - 1) };
}

/** Page (1-based) holding the item at a zero-based index of the server order. */
export function pageForIndex(index, pageSize = HISTORY_PAGE_SIZE) {
  return index >= 0 ? Math.floor(index / Math.max(1, pageSize)) + 1 : 1;
}

/** "provider · model" for a summary ('' when neither is known). */
export function providerModelLabel(item) {
  return [item?.provider, item?.model].map((value) => String(value || '').trim()).filter(Boolean).join(' · ');
}

/** "agent name · role" for a summary ('' when neither is known). */
export function agentLabel(item) {
  const name = String(item?.agent_name || '').trim();
  const role = String(item?.agent_role || '').trim();
  if (name && role && name !== role) return `${name} · ${role}`;
  return name || role;
}

/**
 * Classifies a failed history mutation: 'conflict' (409, work still running),
 * 'missing' (404) or 'error'. Keeps the backend message/detail/action.
 */
export function mutationError(error) {
  const status = Number(error?.status) || 0;
  return {
    kind: status === 409 ? 'conflict' : status === 404 ? 'missing' : 'error',
    message: String(error?.message || ''),
    detail: String(error?.detail || ''),
    action: String(error?.action || ''),
    status,
  };
}
