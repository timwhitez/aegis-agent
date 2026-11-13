// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function TasksTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "tasks".
// Data: session.detail.task_board (todo, tasks, counters, groups). Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function TasksTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.tasks')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-tasks" />`;
}
