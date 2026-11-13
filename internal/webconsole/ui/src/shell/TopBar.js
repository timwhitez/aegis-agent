// Top bar: sidebar toggle, session title + status + provider/model + workdir,
// session actions menu, inspector toggle and the language switch.
import { html } from '../lib/html.js';
import { t, getLocale } from '../lib/i18n.js';
import { compactPath } from '../lib/format.js';
import { useStore } from '../state/hooks.js';
import { currentDisplayStatus, currentSessionTitle, canInterruptSession, canStopSession } from '../state/selectors.js';
import { toggleSidebar, toggleInspector, toggleLocale, viewSessionInHistory } from '../state/actions.js';
import { pushToast } from '../state/toasts.js';
import { interruptRun, stopRun } from '../state/session-actions.js';
import { IconButton } from '../ui/IconButton.js';
import { StatusBadge } from '../ui/Badge.js';
import { Menu } from '../ui/Menu.js';
import { Icon } from '../ui/Icon.js';
import { copyText } from '../ui/CopyButton.js';

export function TopBar() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    routeName: state.route.name,
    narrow: state.ui.viewport.narrow,
    sidebarCollapsed: state.ui.sidebarCollapsed,
    inspectorOpen: state.ui.inspectorOpen,
    sessionId: state.session.id,
    title: currentSessionTitle(state),
    status: currentDisplayStatus(state),
    provider: state.session.detail?.metadata?.provider || '',
    model: state.session.detail?.metadata?.model || '',
    workdir: state.session.detail?.metadata?.workdir || '',
    canStop: canStopSession(state),
    canInterrupt: canInterruptSession(state),
  }), (a, b) => JSON.stringify(a) === JSON.stringify(b));
  const isSessionRoute = view.routeName === 'new' || view.routeName === 'session';
  const showSidebarToggle = view.narrow || view.sidebarCollapsed;

  const copyId = async () => {
    const ok = await copyText(view.sessionId);
    pushToast({ tone: ok ? 'success' : 'danger', message: ok ? t('shell.topbar.idCopied') : t('common.copyFailed') });
  };

  return html`<header class="topbar" data-testid="topbar">
    <div class="topbar-start">
      ${showSidebarToggle ? html`<${IconButton}
        icon="panel-left"
        label=${view.narrow ? t('shell.sidebar.open') : t('shell.sidebar.expand')}
        onClick=${toggleSidebar}
        data-testid="sidebar-toggle"
      />` : null}
      ${isSessionRoute ? html`<div class="topbar-title-group">
        <h1 class="topbar-title" data-testid="session-title">
          ${view.title.raw ? html`<span translate="no">${view.title.text}</span>` : view.title.text}
        </h1>
        ${view.sessionId && view.status ? html`<${StatusBadge} status=${view.status} />` : null}
        ${view.sessionId && (view.provider || view.model) ? html`<span class="chip" title=${t('shell.topbar.provider')}>
          <${Icon} name="sparkles" size=${13} /><span translate="no">${[view.provider, view.model].filter(Boolean).join(' · ')}</span>
        </span>` : null}
        ${view.sessionId && view.workdir ? html`<span class="chip chip-path" title=${`${t('shell.topbar.workdir')}: ${view.workdir}`}>
          <${Icon} name="folder" size=${13} /><span translate="no">${compactPath(view.workdir, 32)}</span>
        </span>` : null}
      </div>` : null}
    </div>
    <div class="topbar-end">
      ${view.sessionId ? html`<${Menu}
        label=${t('shell.topbar.actions')}
        testId="session-actions"
        items=${[
          { id: 'interrupt', label: t('shell.topbar.interrupt'), icon: 'hand', disabled: !view.canInterrupt, onSelect: () => interruptRun(view.sessionId), testId: 'menu-interrupt' },
          { id: 'stop', label: t('shell.topbar.stop'), icon: 'square', danger: true, disabled: !view.canStop, onSelect: () => stopRun(view.sessionId), testId: 'menu-stop' },
          { separator: true },
          { id: 'copy', label: t('shell.topbar.copyId'), icon: 'copy', onSelect: copyId, testId: 'menu-copy-id' },
          { id: 'history', label: t('shell.topbar.viewInHistory'), icon: 'history', onSelect: () => viewSessionInHistory(view.sessionId), testId: 'menu-view-history' },
        ]}
      />` : null}
      ${isSessionRoute ? html`<${IconButton}
        icon="panel-right"
        label=${view.inspectorOpen ? t('shell.inspector.close') : t('shell.inspector.open')}
        pressed=${view.inspectorOpen}
        onClick=${() => toggleInspector()}
        data-testid="inspector-toggle"
      />` : null}
      <button type="button" class="lang-toggle" aria-label=${t('shell.language.toggle')} title=${t('shell.language.toggle')} onClick=${toggleLocale} data-testid="language-toggle">
        <span class=${getLocale() === 'zh-CN' ? 'is-active' : ''} lang="zh-CN">中文</span>
        <span class="lang-sep" aria-hidden="true">/</span>
        <span class=${getLocale() === 'en' ? 'is-active' : ''} lang="en">EN</span>
      </button>
    </div>
  </header>`;
}
