// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function GoalTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "goal".
// Data: session.detail.goal and session.detail.goal_facts. Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function GoalTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.goal')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-goal" />`;
}
