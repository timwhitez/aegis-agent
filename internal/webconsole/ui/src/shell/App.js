// Application layout: sidebar · main (top bar + routed view) · inspector.
// Modal layers (drawer, overlay inspector, palette, dialogs) render outside
// #app-layout so the layout can be made inert while they are open.
import { html } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { useStore } from '../state/hooks.js';
import { setSidebarOpen } from '../state/actions.js';
import { Sidebar } from './Sidebar.js';
import { TopBar } from './TopBar.js';
import { CommandPalette } from './CommandPalette.js';
import { ShortcutHelp } from './ShortcutHelp.js';
import { Dialog } from '../ui/Dialog.js';
import { DialogHost } from '../ui/DialogHost.js';
import { Toasts } from '../ui/Toasts.js';
import { SessionView } from '../features/session/SessionView.js';
import { Inspector } from '../features/inspector/Inspector.js';
import { HistoryView } from '../features/history/HistoryView.js';
import { SkillsView } from '../features/skills/SkillsView.js';
import { WorkspaceView } from '../features/workspace/WorkspaceView.js';
import { SettingsView } from '../features/settings/SettingsView.js';

const PAGE_VIEWS = { sessions: HistoryView, skills: SkillsView, workspace: WorkspaceView, settings: SettingsView };

export function App() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    routeName: state.route.name,
    narrow: state.ui.viewport.narrow,
    wide: state.ui.viewport.wide,
    sidebarCollapsed: state.ui.sidebarCollapsed,
    sidebarOpen: state.ui.sidebarOpen,
    inspectorOpen: state.ui.inspectorOpen,
    paletteOpen: state.ui.paletteOpen,
    shortcutsOpen: state.ui.shortcutsOpen,
  }));
  const isSessionRoute = view.routeName === 'new' || view.routeName === 'session';
  const sidebarInline = !view.narrow && !view.sidebarCollapsed;
  const inspectorInline = isSessionRoute && view.inspectorOpen && view.wide;
  const inspectorOverlay = isSessionRoute && view.inspectorOpen && !view.wide;
  const Page = PAGE_VIEWS[view.routeName];

  return html`
    <a class="skip-link" href="#main">${t('shell.skipToContent')}</a>
    <div
      id="app-layout"
      class="app-layout"
      data-sidebar=${sidebarInline ? 'inline' : 'hidden'}
      data-inspector=${inspectorInline ? 'inline' : 'none'}
      data-route=${view.routeName}
    >
      ${sidebarInline ? html`<${Sidebar} mode="inline" />` : null}
      <main id="main" class="app-main" tabindex="-1">
        <${TopBar} />
        <div class="app-view">
          ${isSessionRoute ? html`<${SessionView} />` : Page ? html`<${Page} />` : null}
        </div>
      </main>
      ${inspectorInline ? html`<${Inspector} mode="panel" />` : null}
    </div>
    ${view.narrow && view.sidebarOpen ? html`<${Dialog}
      title=${t('shell.sidebar.dialogLabel')}
      hideTitle
      showClose=${false}
      placement="left"
      class="sidebar-drawer"
      testId="sidebar-drawer"
      onClose=${() => setSidebarOpen(false)}
    ><${Sidebar} mode="drawer" /><//>` : null}
    ${inspectorOverlay ? html`<${Inspector} mode="overlay" />` : null}
    ${view.paletteOpen ? html`<${CommandPalette} />` : null}
    ${view.shortcutsOpen ? html`<${ShortcutHelp} />` : null}
    <${DialogHost} />
    <${Toasts} />
  `;
}
