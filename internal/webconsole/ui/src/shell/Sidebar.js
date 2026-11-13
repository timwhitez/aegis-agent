// Always-visible sidebar: brand, new session, search, grouped live session
// list (root sessions from /api/overview), page navigation, connection state.
import { html, useMemo } from '../lib/html.js';
import { t, formatRelativeTime, formatDateTime } from '../lib/i18n.js';
import { shortId } from '../lib/format.js';
import { sessionSummaryDisplayStatus } from '../lib/status.js';
import { filterSessions, groupSessions, summaryTitle } from '../lib/session-list.js';
import { useStore } from '../state/hooks.js';
import { onLinkClick, sessionPath } from '../state/router.js';
import { newSession, setSidebarOpen, setSidebarQuery, toggleSidebar, refreshOverview } from '../state/actions.js';
import { Button } from '../ui/Button.js';
import { IconButton } from '../ui/IconButton.js';
import { Icon } from '../ui/Icon.js';
import { Kbd } from '../ui/Kbd.js';
import { StatusDot } from '../ui/Badge.js';
import { Skeleton } from '../ui/Skeleton.js';
import { useNow } from './useNow.js';

const NAV_ITEMS = [
  { route: 'sessions', path: '/sessions', icon: 'history', key: 'shell.nav.sessions' },
  { route: 'skills', path: '/skills', icon: 'box', key: 'shell.nav.skills' },
  { route: 'workspace', path: '/workspace', icon: 'folder', key: 'shell.nav.workspace' },
  { route: 'settings', path: '/settings', icon: 'settings', key: 'shell.nav.settings' },
];

export function BrandMark({ size = 28 }) {
  return html`<span class="brand-mark" style=${`width:${size}px;height:${size}px`} aria-hidden="true">
    <svg viewBox="0 0 24 24" width=${Math.round(size * 0.62)} height=${Math.round(size * 0.62)} fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" stroke-linecap="round">
      <path d="M12 3.5l7 3v5.2c0 4.2-2.9 7.7-7 8.8-4.1-1.1-7-4.6-7-8.8V6.5z" />
      <path d="M9 12.2l2.2 2.2L15.5 10" />
    </svg>
  </span>`;
}

/** mode: 'inline' (desktop column) | 'drawer' (inside the narrow-viewport modal) */
export function Sidebar({ mode = 'inline' }) {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    sessions: state.overview.sessions,
    titles: state.overview.titles,
    loaded: Boolean(state.overview.data),
    overviewError: state.overview.error,
    query: state.ui.sidebarQuery,
    activeId: state.session.id,
    routeName: state.route.name,
    relay: state.connection.relay,
    online: state.connection.online,
  }));
  const now = useNow(30000);
  const groups = useMemo(
    () => groupSessions(filterSessions(view.sessions, view.query, view.titles), now),
    [view.sessions, view.query, view.titles, now],
  );
  const closeDrawer = () => setSidebarOpen(false);
  const onNav = (event) => {
    onLinkClick(event);
    if (mode === 'drawer') closeDrawer();
  };

  const connectionKey = !view.online ? 'shell.connection.offline'
    : view.relay === 'open' ? 'shell.connection.open'
      : view.relay === 'connecting' ? 'shell.connection.connecting' : 'shell.connection.closed';
  const connectionTone = !view.online || view.relay === 'closed' ? 'warning' : view.relay === 'open' ? 'success' : 'neutral';

  return html`<aside class=${`sidebar sidebar-${mode}`} data-testid="sidebar" aria-label=${t('shell.sidebar.dialogLabel')}>
    <div class="sidebar-head">
      <a class="brand" href="/" onClick=${(event) => { event.preventDefault(); newSession(); }}>
        <${BrandMark} />
        <span class="brand-text">
          <span class="brand-name">${t('shell.brand')}</span>
          <span class="brand-subtitle">${t('shell.brandSubtitle')}</span>
        </span>
      </a>
      ${mode === 'drawer'
        ? html`<${IconButton} icon="x" label=${t('shell.sidebar.close')} onClick=${closeDrawer} />`
        : html`<${IconButton} icon="panel-left" label=${t('shell.sidebar.collapse')} onClick=${toggleSidebar} data-testid="sidebar-collapse" />`}
    </div>

    <div class="sidebar-actions">
      <${Button} variant="primary" icon="plus" block onClick=${newSession} data-testid="new-session" class="new-session-btn">
        <span>${t('shell.newSession')}</span>
        <${Kbd} keys=${['mod', 'shift', 'O']} class="new-session-kbd" />
      <//>
      <label class="sidebar-search">
        <${Icon} name="search" size=${15} />
        <span class="sr-only">${t('shell.search.label')}</span>
        <input
          type="search"
          class="sidebar-search-input"
          placeholder=${t('shell.search.placeholder')}
          value=${view.query}
          data-testid="session-search"
          onInput=${(event) => setSidebarQuery(event.currentTarget.value)}
          onKeyDown=${(event) => { if (event.key === 'Escape' && view.query) { event.stopPropagation(); setSidebarQuery(''); } }}
        />
      </label>
    </div>

    <nav class="session-nav" aria-label=${t('shell.sessions.label')} data-testid="session-list">
      ${!view.loaded && !view.overviewError ? html`<div class="session-nav-loading" aria-live="polite">
        <span class="sr-only">${t('shell.sessions.loading')}</span><${Skeleton} lines=${4} />
      </div>` : null}
      ${view.overviewError && !view.loaded ? html`<div class="session-nav-note" role="alert">
        <p>${t('shell.sessions.loadFailed')}</p>
        <${Button} size="sm" variant="ghost" icon="refresh" onClick=${() => refreshOverview({ silent: false })}>${t('common.retry')}<//>
      </div>` : null}
      ${view.loaded && view.sessions.length === 0 ? html`<p class="session-nav-note">${t('shell.sessions.empty')}</p>` : null}
      ${view.loaded && view.sessions.length > 0 && groups.length === 0 ? html`<p class="session-nav-note">${t('shell.search.noResults')}</p>` : null}
      ${groups.map((group) => html`<section class="session-group" key=${group.key} data-group=${group.key}>
        <h2 class="session-group-label">${t(`shell.group.${group.key}`)}</h2>
        <ul class="session-rows" role="list">
          ${group.items.map((item) => html`<li key=${item.id}>
            <${SessionRow} item=${item} active=${item.id === view.activeId} titles=${view.titles} now=${now} onNav=${onNav} />
          </li>`)}
        </ul>
      </section>`)}
    </nav>
    ${view.loaded && view.sessions.length > 0 ? html`<a class="view-all-link" href="/sessions" onClick=${onNav}>
      ${t('shell.sessions.viewAll')}<${Icon} name="chevron-right" size=${14} />
    </a>` : null}

    <nav class="sidebar-nav" aria-label=${t('shell.nav.label')}>
      ${NAV_ITEMS.map((item) => html`<a
        class=${`sidebar-nav-link ${view.routeName === item.route ? 'is-active' : ''}`}
        href=${item.path}
        aria-current=${view.routeName === item.route ? 'page' : undefined}
        data-testid=${`nav-${item.route}`}
        onClick=${onNav}
      ><${Icon} name=${item.icon} size=${16} /><span>${t(item.key)}</span></a>`)}
    </nav>

    <footer class="sidebar-footer" data-testid="connection-status" data-state=${view.relay}>
      <span class=${`connection-dot tone-${connectionTone}`} aria-hidden="true"></span>
      <span class="connection-text">${t(connectionKey)}</span>
    </footer>
  </aside>`;
}

function SessionRow({ item, active, titles, now, onNav }) {
  const status = sessionSummaryDisplayStatus(item);
  const title = summaryTitle(item, titles);
  const time = item.updated_at || item.created_at;
  return html`<a
    class=${`session-row ${active ? 'is-active' : ''}`}
    href=${sessionPath(item.id)}
    aria-current=${active ? 'page' : undefined}
    data-testid="session-row"
    data-session-id=${item.id}
    data-status=${status}
    onClick=${onNav}
  >
    <${StatusDot} status=${status} />
    ${title
      ? html`<span class="session-row-title" translate="no">${title}</span>`
      : html`<span class="session-row-title is-fallback">${t('shell.session.untitled', { id: '' })}<span translate="no">${shortId(item.id)}</span></span>`}
    <time class="session-row-time" datetime=${time} title=${formatDateTime(time)}>${formatRelativeTime(time, now)}</time>
  </a>`;
}
