// Session inspector shell: side panel at ≥1280px (mode="panel"), modal sheet
// below (mode="overlay": focus trap, Esc, inert background and a fresh scroll
// position on every reopen come from ui/Dialog.js, which is mounted only
// while open). Shows only the tabs relevant for the open session
// (visibleInspectorTabs); a persisted tab that is hidden falls back to
// Overview without overwriting the preference. While a session's detail is
// loading (session switch) the persisted tab stays selected with a skeleton
// panel (inspectorTabState), so tabs do not flicker to Overview and back.
// Tab ids come from state/prefs.js INSPECTOR_TABS; each tab receives
// { sessionId, mode }.
import { html, useLayoutEffect, useMemo, useRef, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { setInspectorTab, toggleInspector } from '../../state/actions.js';
import { currentDisplayStatus } from '../../state/selectors.js';
import { Dialog } from '../../ui/Dialog.js';
import { IconButton } from '../../ui/IconButton.js';
import { Tabs, panelId, tabId } from '../../ui/Tabs.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { TAB_LABEL_KEYS, fileChangeCount, inspectorTabState, withMessages } from './inspector-model.js';
import { StatusPill } from './parts.js';
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

const TAB_LABELS = {
  overview: () => t('inspector.tab.overview'),
  plan: () => t('inspector.tab.plan'),
  goal: () => t('inspector.tab.goal'),
  tasks: () => t('inspector.tab.tasks'),
  agents: () => t('inspector.tab.agents'),
  context: () => t('inspector.tab.context'),
  timeline: () => t('inspector.tab.timeline'),
  files: () => t('inspector.tab.files'),
};

/** Tablist wrapper: single scrollable row with edge fades; keeps the active tab in view. */
function TabBar({ tabs, value }) {
  const wrapRef = useRef(null);
  const [fade, setFade] = useState({ start: false, end: false });
  const list = () => wrapRef.current?.querySelector('[role="tablist"]') || null;
  const measure = () => {
    const element = list();
    if (!element) return;
    const start = element.scrollLeft > 1;
    const end = element.scrollLeft + element.clientWidth < element.scrollWidth - 1;
    setFade((previous) => (previous.start === start && previous.end === end ? previous : { start, end }));
  };
  useLayoutEffect(() => {
    const element = list();
    if (!element) return undefined;
    element.addEventListener('scroll', measure, { passive: true });
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(measure) : null;
    observer?.observe(element);
    measure();
    return () => {
      element.removeEventListener('scroll', measure);
      observer?.disconnect();
    };
  }, []);
  useLayoutEffect(() => {
    const element = list();
    const selected = element?.querySelector('[aria-selected="true"]');
    if (element && selected) {
      const left = selected.offsetLeft - element.offsetLeft;
      const right = left + selected.offsetWidth;
      if (left < element.scrollLeft) element.scrollLeft = Math.max(0, left - 24);
      else if (right > element.scrollLeft + element.clientWidth) element.scrollLeft = right - element.clientWidth + 24;
    }
    measure();
  }, [value, tabs.map((tab) => tab.id).join(',')]);
  const classes = ['insp-tabbar', fade.start ? 'fade-start' : '', fade.end ? 'fade-end' : ''].filter(Boolean).join(' ');
  return html`<div class=${classes} ref=${wrapRef} data-testid="inspector-tabbar">
    <${Tabs} idPrefix="inspector" label=${t('inspector.tabsLabel')} tabs=${tabs} value=${value} onChange=${setInspectorTab} class="inspector-tabs" />
  </div>`;
}

/** mode: 'panel' (side-by-side, ≥1280px) | 'overlay' (modal sheet) */
export function Inspector({ mode = 'panel' }) {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    tab: state.ui.inspectorTab,
    sessionId: state.session.id,
    detail: state.session.detail,
    loading: state.session.loading,
    messages: state.session.messages,
    status: currentDisplayStatus(state),
  }));
  const bodyRef = useRef(null);
  const detail = useMemo(() => withMessages(view.detail, view.messages), [view.detail, view.messages]);
  const fileCount = useMemo(() => fileChangeCount(detail), [detail]);
  const tabState = inspectorTabState({ tab: view.tab, detail, sessionId: view.sessionId, loading: view.loading, fileCount });
  const active = tabState.active;
  const tabs = tabState.tabs.map((id) => ({ id, label: TAB_LABELS[id]() }));
  const Active = TAB_COMPONENTS[active] || OverviewTab;
  const close = () => toggleInspector(false);

  // Switching tab or session starts the panel at the top; ordinary polling
  // re-renders keep the reader's position.
  useLayoutEffect(() => {
    if (bodyRef.current) bodyRef.current.scrollTop = 0;
  }, [active, view.sessionId]);

  const body = html`
    <div class="inspector-head insp-head">
      <div class="insp-head-title">
        <h2 class="inspector-title">${t('inspector.title')}</h2>
        ${view.sessionId && view.status ? html`<${StatusPill} status=${view.status} testId="inspector-status" />` : null}
      </div>
      <${IconButton} icon="x" size="sm" label=${t('inspector.close')} onClick=${close} data-testid="inspector-close" />
    </div>
    ${view.sessionId ? html`<${TabBar} tabs=${tabs} value=${active} />` : null}
    <div
      ref=${bodyRef}
      role=${view.sessionId ? 'tabpanel' : undefined}
      id=${view.sessionId ? panelId('inspector', active) : undefined}
      aria-labelledby=${view.sessionId ? tabId('inspector', active) : undefined}
      tabindex="0"
      class="tab-panel inspector-panel-body insp-body"
      data-testid="inspector-panel"
      data-tab=${active}
      aria-busy=${tabState.pending ? 'true' : undefined}
    >
      ${!view.sessionId ? html`<${EmptyState} icon="info" body=${t('inspector.noSession')} />`
        : tabState.pending ? html`<div class="insp-tab-loading" data-testid="inspector-tab-loading"><span class="sr-only">${t('inspector.loading')}</span><${Skeleton} lines=${6} /></div>`
          : html`<${Active} key=${view.sessionId + ':' + active} sessionId=${view.sessionId} mode=${mode} />`}
    </div>`;
  if (mode === 'overlay') {
    return html`<${Dialog} title=${t('inspector.title')} hideTitle showClose=${false} placement="right" class="inspector inspector-overlay" testId="inspector" onClose=${close}>
      ${body}
    <//>`;
  }
  return html`<aside class="inspector inspector-panel" data-testid="inspector" aria-label=${t('inspector.title')}>${body}</aside>`;
}

export { TAB_LABEL_KEYS };
