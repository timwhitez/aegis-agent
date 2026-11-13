// Sessions page (/sessions, data-testid="page-sessions"): search, status
// filter, paging over GET /api/history, open / stop / delete per row, clear
// all, and reveal of ui.historyFocus ("在会话管理中查看"). List data, filters,
// paging and pending flags are page-local; durable facts come from the server.
import { html, useEffect, useMemo, useRef, useState } from '../../lib/html.js';
import { t, formatDateTime, formatNumber, formatRelativeTime } from '../../lib/i18n.js';
import * as api from '../../lib/api.js';
import { compactPath, shortId, truncate } from '../../lib/format.js';
import { sessionSummaryDisplayStatus } from '../../lib/status.js';
import { summaryTitle } from '../../lib/session-list.js';
import { useStore } from '../../state/hooks.js';
import { navigate, newSession, refreshOverview, viewSessionInHistory } from '../../state/actions.js';
import { onLinkClick, sessionPath } from '../../state/router.js';
import { confirmDialog } from '../../state/dialogs.js';
import { pushToast } from '../../state/toasts.js';
import { stopRun } from '../../state/session-actions.js';
import { useNow } from '../../shell/useNow.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Button } from '../../ui/Button.js';
import { IconButton } from '../../ui/IconButton.js';
import { Icon } from '../../ui/Icon.js';
import { Menu } from '../../ui/Menu.js';
import { Badge, StatusBadge } from '../../ui/Badge.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { copyText } from '../../ui/CopyButton.js';
import {
  HISTORY_LOCATE_MAX_PAGES,
  HISTORY_PAGE_SIZE,
  HISTORY_SCAN_PAGE_SIZE,
  STATUS_FILTERS,
  agentLabel,
  ancestorIds,
  buildSessionTree,
  filterHistory,
  isActiveSummary,
  isChildSummary,
  isFiltering,
  mutationError,
  pageForIndex,
  pageRange,
  paginate,
  providerModelLabel,
} from './history-model.js';

const POLL_MS = 4000;
const FOCUS_HIGHLIGHT_MS = 2600;

function errorInfo(error, fallbackKey) {
  return { message: error?.message || t(fallbackKey), detail: error?.detail || '', action: error?.action || '', status: error?.status || 0 };
}

function prefersReducedMotion() {
  try {
    return globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
  } catch {
    return false;
  }
}

export function HistoryView() {
  const store = useStore((state) => ({
    locale: state.ui.locale,
    titles: state.overview.titles,
    focus: state.ui.historyFocus,
    pageVisible: state.connection.pageVisible,
  }));
  const now = useNow(30000);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('all');
  const [page, setPage] = useState(1);
  const [serverData, setServerData] = useState(null); // GET /api/history page
  const [scanData, setScanData] = useState(null); // recent HISTORY_SCAN_PAGE_SIZE summaries for filtering
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [expanded, setExpanded] = useState(() => new Set());
  const [pending, setPending] = useState(() => new Set());
  const [clearing, setClearing] = useState(false);
  const [focusId, setFocusId] = useState('');
  const requestSeq = useRef(0);
  // The requested page lives in a ref too, so loads started from stale
  // closures (poll ticks, post-mutation reloads) always fetch the latest page.
  const pageRef = useRef(1);
  const goToPage = (value) => {
    pageRef.current = value;
    setPage(value);
  };
  const filtering = isFiltering({ query, status });
  const dataKey = filtering ? 'scan' : `page:${page}`;

  async function load({ silent = false } = {}) {
    const seq = ++requestSeq.current;
    if (!silent) setLoading(true);
    try {
      if (filtering) {
        const data = await api.getHistory({ page: 1, pageSize: HISTORY_SCAN_PAGE_SIZE });
        if (seq !== requestSeq.current) return;
        setScanData(data);
      } else {
        const requested = pageRef.current;
        const data = await api.getHistory({ page: requested, pageSize: HISTORY_PAGE_SIZE });
        if (seq !== requestSeq.current) return;
        const totalPages = Number(data?.total_pages) || 0;
        if (requested > 1 && totalPages > 0 && requested > totalPages) {
          // The page shrank (deletes elsewhere): move to the last page, which reloads.
          goToPage(totalPages);
          return;
        }
        if (requested > 1 && totalPages === 0) goToPage(1);
        setServerData(data);
      }
      setError(null);
    } catch (err) {
      if (seq !== requestSeq.current) return;
      if (!silent) setError(errorInfo(err, 'history.loadFailed'));
    } finally {
      if (seq === requestSeq.current) setLoading(false);
    }
  }
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => { loadRef.current(); }, [dataKey]);

  // Reveal ui.historyFocus: find the page that holds it, expand its parents
  // and highlight the row; then clear the store flag so it fires once.
  useEffect(() => {
    const id = store.focus;
    if (!id) return undefined;
    let cancelled = false;
    (async () => {
      const found = await locateSession(id);
      if (cancelled) return;
      viewSessionInHistory('');
      setQuery('');
      setStatus('all');
      if (!found) {
        pushToast({ tone: 'info', message: t('history.focus.notFound') });
        return;
      }
      const ancestors = ancestorIds(found.pageItems, id);
      if (ancestors.length) setExpanded((current) => new Set([...current, ...ancestors]));
      goToPage(found.page);
      setFocusId(id);
    })();
    return () => { cancelled = true; };
  }, [store.focus]);

  // Poll while a visible row is still active so statuses settle in place.
  const visibleItems = filtering ? (scanData?.items || []) : (serverData?.items || []);
  const hasActive = visibleItems.some(isActiveSummary);
  useEffect(() => {
    if (!store.pageVisible || !hasActive) return undefined;
    const timer = setInterval(() => loadRef.current({ silent: true }), POLL_MS);
    return () => clearInterval(timer);
  }, [store.pageVisible, hasActive, dataKey]);

  const listView = useMemo(() => {
    if (filtering) {
      const matches = filterHistory(scanData?.items, { query, status, knownTitles: store.titles });
      const paged = paginate(matches, page, HISTORY_PAGE_SIZE);
      return {
        ready: Boolean(scanData),
        nodes: paged.items.map((item) => ({ item, children: [] })),
        count: paged.items.length,
        total: paged.total,
        page: paged.page,
        totalPages: paged.totalPages,
        start: paged.start,
        end: paged.end,
        scanLimited: (Number(scanData?.total) || 0) > HISTORY_SCAN_PAGE_SIZE,
        grandTotal: Number(scanData?.total) || 0,
      };
    }
    const items = serverData?.items || [];
    const total = Number(serverData?.total) || 0;
    const current = Number(serverData?.page) || page;
    const range = pageRange({ page: current, pageSize: Number(serverData?.page_size) || HISTORY_PAGE_SIZE, total, count: items.length });
    return {
      ready: Boolean(serverData),
      nodes: buildSessionTree(items),
      count: items.length,
      total,
      page: current,
      totalPages: Math.max(1, Number(serverData?.total_pages) || 1),
      start: range.start,
      end: range.end,
      scanLimited: false,
      grandTotal: total,
    };
  }, [filtering, scanData, serverData, query, status, page, store.titles]);

  // Scroll the focused row into view once it renders, then fade the highlight.
  useEffect(() => {
    if (!focusId || !listView.ready) return undefined;
    const row = globalThis.document?.querySelector(`.history-row[data-session-id="${cssEscape(focusId)}"]`);
    if (!row) return undefined;
    row.scrollIntoView?.({ block: 'center', behavior: prefersReducedMotion() ? 'auto' : 'smooth' });
    row.querySelector('.history-row-link')?.focus({ preventScroll: true });
    const timer = setTimeout(() => setFocusId(''), FOCUS_HIGHLIGHT_MS);
    return () => clearTimeout(timer);
  }, [focusId, listView.ready, listView.nodes]);

  const setPendingId = (id, on) => setPending((current) => {
    const next = new Set(current);
    if (on) next.add(id);
    else next.delete(id);
    return next;
  });

  async function deleteSession(item) {
    const ok = await confirmDialog({
      title: t('history.delete.title'),
      body: html`<span class="history-confirm-target" translate="no">${rowTitleText(item, store.titles)}</span><span class="history-confirm-text">${t('history.delete.body')}</span>`,
      confirmLabel: t('history.delete.confirm'),
      danger: true,
    });
    if (!ok) return;
    setPendingId(item.id, true);
    try {
      await api.deleteSession(item.id);
      pushToast({ tone: 'success', message: t('history.delete.success') });
      setScanData(null);
      await loadRef.current({ silent: true });
      refreshOverview({ silent: true });
    } catch (err) {
      const info = mutationError(err);
      if (info.kind === 'missing') {
        await loadRef.current({ silent: true });
        refreshOverview({ silent: true });
      }
      pushToast({
        tone: 'danger',
        message: info.kind === 'conflict' ? t('history.delete.conflict') : t('history.delete.failed'),
        detail: info.detail || info.message,
        action: info.action || (info.kind === 'conflict' ? t('history.delete.conflictAction') : ''),
        raw: { detail: true, action: Boolean(info.action) },
      });
    } finally {
      setPendingId(item.id, false);
    }
  }

  async function clearHistory() {
    const total = listView.grandTotal || Number(serverData?.total) || 0;
    const ok = await confirmDialog({
      title: t('history.clear.title'),
      body: t('history.clear.body', { count: formatNumber(total) }),
      confirmLabel: t('history.clear.confirm'),
      danger: true,
    });
    if (!ok) return;
    setClearing(true);
    try {
      await api.clearSessions();
      pushToast({ tone: 'success', message: t('history.clear.success') });
      setExpanded(new Set());
      setScanData(null);
      setServerData(null);
      if (pageRef.current !== 1) goToPage(1);
      else await loadRef.current();
      refreshOverview({ silent: true });
    } catch (err) {
      const info = mutationError(err);
      pushToast({
        tone: 'danger',
        message: info.kind === 'conflict' ? t('history.clear.conflict') : t('history.clear.failed'),
        detail: info.detail || info.message,
        action: info.action || (info.kind === 'conflict' ? t('history.clear.conflictAction') : ''),
        raw: { detail: true, action: Boolean(info.action) },
      });
    } finally {
      setClearing(false);
    }
  }

  async function stopSession(item) {
    setPendingId(item.id, true);
    try {
      await stopRun(item.id);
    } finally {
      setPendingId(item.id, false);
      loadRef.current({ silent: true });
    }
  }

  async function copyId(item) {
    const ok = await copyText(item.id);
    pushToast({ tone: ok ? 'success' : 'danger', message: ok ? t('history.row.idCopied') : t('common.copyFailed') });
  }

  const toggleExpanded = (id) => setExpanded((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return next;
  });

  const clearFilters = () => {
    setQuery('');
    setStatus('all');
    goToPage(1);
  };

  const rowContext = {
    now,
    titles: store.titles,
    expanded,
    pending,
    focusId,
    onToggle: toggleExpanded,
    onDelete: deleteSession,
    onStop: stopSession,
    onCopy: copyId,
  };

  const totalSessions = filtering ? listView.grandTotal : listView.total;
  const showInitialLoading = loading && !listView.ready && !error;
  const showError = Boolean(error) && !listView.ready;

  return html`<div class="page page-history" data-testid="page-sessions">
    <div class="page-inner">
      <${PageHeader}
        title=${t('shell.page.sessions.title')}
        description=${t('shell.page.sessions.description')}
        actions=${html`
          <${IconButton}
            icon="refresh"
            label=${t('history.refresh')}
            variant="secondary"
            pending=${loading && listView.ready}
            onClick=${() => { setScanData(null); loadRef.current(); }}
            data-testid="history-refresh"
          />
          <${Button}
            variant="secondary"
            icon="trash"
            class="history-clear-btn"
            pending=${clearing}
            disabled=${!totalSessions}
            onClick=${clearHistory}
            data-testid="history-clear"
          >${t('history.clear.button')}<//>`}
      />

      <div class="page-toolbar" role="search">
        <label class="page-search">
          <${Icon} name="search" size=${15} />
          <span class="sr-only">${t('history.search.label')}</span>
          <input
            type="search"
            class="page-search-input"
            placeholder=${t('history.search.placeholder')}
            value=${query}
            data-testid="history-search"
            onInput=${(event) => { setQuery(event.currentTarget.value); goToPage(1); }}
            onKeyDown=${(event) => { if (event.key === 'Escape' && query) { event.stopPropagation(); setQuery(''); goToPage(1); } }}
          />
          ${query ? html`<${IconButton} icon="x" size="sm" label=${t('history.search.clear')} class="page-search-clear" onClick=${() => { setQuery(''); goToPage(1); }} />` : null}
        </label>
        <div class="segmented" role="group" aria-label=${t('history.filter.label')}>
          ${STATUS_FILTERS.map((filter) => html`<button
            type="button"
            key=${filter.id}
            class=${`segmented-btn ${status === filter.id ? 'is-active' : ''}`}
            aria-pressed=${String(status === filter.id)}
            data-testid=${`history-filter-${filter.id}`}
            onClick=${() => { setStatus(filter.id); goToPage(1); }}
          >${t(filter.labelKey)}</button>`)}
        </div>
      </div>

      <p class="page-count" aria-live="polite" data-testid="history-count">
        ${listView.ready ? (filtering
          ? t('history.countFiltered', { count: formatNumber(listView.total) })
          : t('history.count', { count: formatNumber(listView.total) })) : ''}
        ${listView.ready && listView.scanLimited ? html`<span class="page-count-note">${t('history.scanLimited', { limit: formatNumber(HISTORY_SCAN_PAGE_SIZE) })}</span>` : null}
      </p>

      ${showInitialLoading ? html`<div class="history-list is-loading" aria-busy="true">
        <span class="sr-only" role="status">${t('history.loading')}</span>
        ${[0, 1, 2, 3].map((index) => html`<div class="history-skeleton" key=${index}><${Skeleton} lines=${2} /></div>`)}
      </div>` : null}

      ${showError ? html`<${PageAlert} error=${error} titleKey="history.loadFailed" onRetry=${() => loadRef.current()} />` : null}

      ${listView.ready && listView.total === 0 && !filtering ? html`<${EmptyState}
        icon="history"
        title=${t('history.empty.title')}
        body=${t('history.empty.body')}
        class="page-empty"
        data-testid="history-empty"
      ><${Button} variant="primary" icon="plus" onClick=${newSession}>${t('history.empty.action')}<//><//>` : null}

      ${listView.ready && listView.total === 0 && filtering ? html`<${EmptyState}
        icon="search"
        title=${t('history.noMatch.title')}
        body=${t('history.noMatch.body')}
        class="page-empty"
        data-testid="history-no-match"
      ><${Button} variant="secondary" onClick=${clearFilters}>${t('history.noMatch.action')}<//><//>` : null}

      ${listView.ready && listView.count > 0 ? html`<ul class="history-list" role="list" aria-label=${t('history.list.label')} data-testid="history-list">
        ${listView.nodes.map((node) => html`<${HistoryNode} key=${node.item.id} node=${node} depth=${0} ctx=${rowContext} />`)}
      </ul>` : null}

      ${listView.ready && listView.total > 0 && listView.totalPages > 1 ? html`<nav class="pagination" aria-label=${t('history.pagination.label')} data-testid="history-pagination">
        <span class="pagination-range">${t('history.pagination.range', { start: formatNumber(listView.start), end: formatNumber(listView.end), total: formatNumber(listView.total) })}</span>
        <div class="pagination-controls">
          <${IconButton}
            icon="chevron-left"
            label=${t('history.pagination.prev')}
            variant="secondary"
            disabled=${listView.page <= 1}
            onClick=${() => goToPage(Math.max(1, listView.page - 1))}
            data-testid="history-prev"
          />
          <span class="pagination-page" data-testid="history-page-label">${t('history.pagination.page', { page: formatNumber(listView.page), pages: formatNumber(listView.totalPages) })}</span>
          <${IconButton}
            icon="chevron-right"
            label=${t('history.pagination.next')}
            variant="secondary"
            disabled=${listView.page >= listView.totalPages}
            onClick=${() => goToPage(Math.min(listView.totalPages, listView.page + 1))}
            data-testid="history-next"
          />
        </div>
      </nav>` : null}
    </div>
  </div>`;
}

/** Locates a session in the server order: { page, pageItems } or null. */
async function locateSession(id) {
  let offset = 0;
  for (let pageNo = 1; pageNo <= HISTORY_LOCATE_MAX_PAGES; pageNo += 1) {
    let data;
    try {
      data = await api.getHistory({ page: pageNo, pageSize: HISTORY_SCAN_PAGE_SIZE });
    } catch {
      return null;
    }
    const items = Array.isArray(data?.items) ? data.items : [];
    const index = items.findIndex((item) => item?.id === id);
    if (index >= 0) {
      const page = pageForIndex(offset + index, HISTORY_PAGE_SIZE);
      const start = (page - 1) * HISTORY_PAGE_SIZE - offset;
      return { page, pageItems: items.slice(Math.max(0, start), Math.max(0, start) + HISTORY_PAGE_SIZE) };
    }
    offset += items.length;
    if (!items.length || pageNo >= (Number(data?.total_pages) || 0)) return null;
  }
  return null;
}

function cssEscape(value) {
  const text = String(value || '');
  return globalThis.CSS?.escape ? globalThis.CSS.escape(text) : text.replace(/["\\]/g, '\\$&');
}

function rowTitleText(item, titles) {
  return summaryTitle(item, titles) || shortId(item.id);
}

function HistoryNode({ node, depth, ctx }) {
  const { item, children } = node;
  const open = children.length > 0 && ctx.expanded.has(item.id);
  return html`<li class="history-node" data-depth=${depth}>
    <${HistoryRow} item=${item} depth=${depth} childCount=${children.length} open=${open} ctx=${ctx} />
    ${open ? html`<ul class="history-children" role="list" id=${`history-children-${item.id}`}>
      ${children.map((child) => html`<${HistoryNode} key=${child.item.id} node=${child} depth=${depth + 1} ctx=${ctx} />`)}
    </ul>` : null}
  </li>`;
}

function HistoryRow({ item, depth, childCount, open, ctx }) {
  const status = sessionSummaryDisplayStatus(item);
  const title = summaryTitle(item, ctx.titles);
  const time = item.updated_at || item.created_at;
  const providerModel = providerModelLabel(item);
  const agent = agentLabel(item);
  const pending = ctx.pending.has(item.id);
  const child = isChildSummary(item);
  const running = String(status) === 'running';
  const menuItems = [
    { id: 'open', label: t('history.row.open'), icon: 'external-link', onSelect: () => navigate(sessionPath(item.id)), testId: 'history-row-open' },
    { id: 'copy', label: t('history.row.copyId'), icon: 'copy', onSelect: () => ctx.onCopy(item), testId: 'history-row-copy' },
    ...(running ? [{ id: 'stop', label: t('history.row.stop'), icon: 'square', onSelect: () => ctx.onStop(item), testId: 'history-row-stop' }] : []),
    { separator: true },
    { id: 'delete', label: t('history.row.delete'), icon: 'trash', danger: true, disabled: pending, onSelect: () => ctx.onDelete(item), testId: 'history-row-delete' },
  ];
  return html`<div
    class=${`history-row ${depth > 0 ? 'is-child' : ''} ${ctx.focusId === item.id ? 'is-focused' : ''} ${pending ? 'is-pending' : ''}`}
    data-testid="history-row"
    data-session-id=${item.id}
    data-status=${status}
    aria-busy=${pending ? 'true' : undefined}
  >
    <div class="history-row-main">
      <a class="history-row-link" href=${sessionPath(item.id)} onClick=${onLinkClick} data-testid="history-row-link">
        ${title
          ? html`<span class="history-row-title" translate="no" title=${title}>${title}</span>`
          : html`<span class="history-row-title is-fallback">${t('history.row.untitled')}</span>`}
      </a>
      <div class="history-row-meta">
        ${child ? html`<${Badge} class="history-child-badge">${t('history.row.child')}<//>` : null}
        ${providerModel ? html`<span class="history-meta-item" translate="no">${providerModel}</span>` : null}
        ${agent ? html`<span class="history-meta-item" translate="no">${agent}</span>` : null}
        ${item.workdir ? html`<span class="history-meta-item history-meta-path" title=${item.workdir}>
          <${Icon} name="folder" size=${12} /><span translate="no">${compactPath(item.workdir, 40)}</span>
        </span>` : null}
        <span class="history-meta-item history-meta-id" translate="no" title=${item.id}>${shortId(item.id)}</span>
      </div>
      ${item.last_error && (status === 'failed' || status === 'error') ? html`<p class="history-row-error">
        <span class="sr-only">${t('history.row.lastError')}</span><span translate="no">${truncate(item.last_error, 180)}</span>
      </p>` : null}
    </div>
    <div class="history-row-side">
      <${StatusBadge} status=${status} />
      <time class="history-row-time" datetime=${time} title=${formatDateTime(time, { dateStyle: 'medium', timeStyle: 'medium' })}>${formatRelativeTime(time, ctx.now)}</time>
      ${childCount > 0 ? html`<button
        type="button"
        class="history-children-toggle"
        aria-expanded=${String(open)}
        aria-controls=${open ? `history-children-${item.id}` : undefined}
        data-testid="history-children-toggle"
        onClick=${() => ctx.onToggle(item.id)}
      >
        <${Icon} name=${open ? 'chevron-up' : 'chevron-down'} size=${14} />
        <span>${childCount === 1 ? t('history.row.childrenOne') : t('history.row.childrenMany', { count: formatNumber(childCount) })}</span>
      </button>` : null}
      <${Menu} label=${t('history.row.actions')} items=${menuItems} testId="history-row-menu" disabled=${pending} />
    </div>
  </div>`;
}

/** Inline object-level error that keeps the backend detail/action. */
function PageAlert({ error, titleKey, onRetry }) {
  return html`<div class="page-alert" role="alert" data-testid="history-error">
    <span class="page-alert-icon"><${Icon} name="alert-circle" size=${18} /></span>
    <div class="page-alert-body">
      <p class="page-alert-title">${t(titleKey)}</p>
      ${error?.message ? html`<p class="page-alert-text" translate="no">${error.message}</p>` : null}
      ${error?.detail ? html`<p class="page-alert-text"><span>${t('history.error.detailLabel')}</span> <span translate="no">${error.detail}</span></p>` : null}
      ${error?.action ? html`<p class="page-alert-text"><span>${t('history.error.actionLabel')}</span> <span translate="no">${error.action}</span></p>` : null}
    </div>
    <${Button} size="sm" variant="secondary" icon="refresh" onClick=${onRetry}>${t('common.retry')}<//>
  </div>`;
}
