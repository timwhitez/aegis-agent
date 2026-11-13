// Agents tab: child sessions merged with queue jobs (subAgentRows), parent
// coordination and every background notification. Each row shows status,
// role, final text / last error excerpt, budget summary and links to open
// the child (or parent) session.
import { html, useMemo } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { maybeArray, subAgentRows } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { parentCoordinationView } from './inspector-model.js';
import { Metrics, Section, num } from './parts.js';
import { CoordinationCard, NotificationCard, SubAgentCard } from './cards.js';

export function AgentsTab({ sessionId, mode = 'panel' }) {
  const view = useStore((state) => ({ locale: state.ui.locale, detail: state.session.detail }));
  const detail = view.detail;
  const rows = useMemo(() => subAgentRows(detail), [detail]);
  const jobs = maybeArray(detail?.children?.jobs);
  const notifications = maybeArray(detail?.background_notifications).slice().reverse();
  const coordination = parentCoordinationView(detail);
  if (!rows.length && !notifications.length) {
    return html`<${EmptyState} icon="users" title=${t('inspector.agents.emptyTitle')} body=${t('inspector.agents.emptyBody')} data-testid="inspector-agents-empty" />`;
  }
  return html`<div class="insp-tab insp-agents" data-testid="inspector-agents">
    <${Metrics} items=${[
      { label: t('inspector.agents.subAgents'), value: num(rows.length), testId: 'inspector-agents-count' },
      { label: t('inspector.agents.jobs'), value: num(jobs.length) },
      { label: t('inspector.agents.notifications'), value: num(notifications.length) },
    ]} />
    ${coordination ? html`<${CoordinationCard} view=${coordination} mode=${mode} />` : null}
    <${Section} title=${t('inspector.agents.title')} testId="inspector-agents-list">
      ${rows.length
        ? html`<div class="insp-stack">${rows.map((row) => html`<${SubAgentCard} key=${row.id} row=${row} mode=${mode} currentSessionId=${sessionId} />`)}</div>`
        : html`<p class="insp-muted">${t('inspector.agents.none')}</p>`}
    <//>
    ${notifications.length ? html`<${Section} title=${t('inspector.notifications.title')} testId="inspector-notifications">
      <div class="insp-stack">${notifications.map((item) => html`<${NotificationCard} key=${item.id} item=${item} mode=${mode} />`)}</div>
    <//>` : null}
  </div>`;
}
