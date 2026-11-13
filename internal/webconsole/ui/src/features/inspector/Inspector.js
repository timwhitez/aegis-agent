// Inspector shell (shell-owned, final): side panel at ≥1280px, modal overlay
// below. Header, tablist and the active tab panel; tab content components are
// owned by the inspector feature worker.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { setInspectorTab, toggleInspector } from '../../state/actions.js';
import { INSPECTOR_TABS } from '../../state/prefs.js';
import { Dialog } from '../../ui/Dialog.js';
import { IconButton } from '../../ui/IconButton.js';
import { Tabs, TabPanel } from '../../ui/Tabs.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { OverviewTab } from './OverviewTab.js';
import { PlanTab } from './PlanTab.js';
import { GoalTab } from './GoalTab.js';
import { TasksTab } from './TasksTab.js';
import { AgentsTab } from './AgentsTab.js';
import { ContextTab } from './ContextTab.js';
import { TimelineTab } from './TimelineTab.js';
import { FilesTab } from './FilesTab.js';

const TAB_COMPONENTS = {
  overview: OverviewTab, plan: PlanTab, goal: GoalTab, tasks: TasksTab,
  agents: AgentsTab, context: ContextTab, timeline: TimelineTab, files: FilesTab,
};

/** mode: 'panel' (side-by-side, ≥1280px) | 'overlay' (modal sheet) */
export function Inspector({ mode = 'panel' }) {
  const view = useStore((state) => ({ locale: state.ui.locale, tab: state.ui.inspectorTab, sessionId: state.session.id }));
  const close = () => toggleInspector(false);
  const tabs = INSPECTOR_TABS.map((id) => ({ id, label: t(`shell.inspector.tab.${id}`) }));
  const Active = TAB_COMPONENTS[view.tab] || OverviewTab;
  const body = html`
    <div class="inspector-head">
      <h2 class="inspector-title">${t('shell.inspector.title')}</h2>
      <${IconButton} icon="x" size="sm" label=${t('shell.inspector.close')} onClick=${close} data-testid="inspector-close" />
    </div>
    <${Tabs} idPrefix="inspector" label=${t('shell.inspector.tabs')} tabs=${tabs} value=${view.tab} onChange=${setInspectorTab} class="inspector-tabs" />
    <${TabPanel} idPrefix="inspector" id=${view.tab} class="inspector-panel-body">
      ${view.sessionId
        ? html`<${Active} sessionId=${view.sessionId} />`
        : html`<${EmptyState} icon="info" body=${t('shell.inspector.noSession')} />`}
    <//>`;
  if (mode === 'overlay') {
    return html`<${Dialog} title=${t('shell.inspector.title')} hideTitle showClose=${false} placement="right" class="inspector inspector-overlay" testId="inspector" onClose=${close}>
      ${body}
    <//>`;
  }
  return html`<aside class="inspector inspector-panel" data-testid="inspector" aria-label=${t('shell.inspector.title')}>${body}</aside>`;
}
