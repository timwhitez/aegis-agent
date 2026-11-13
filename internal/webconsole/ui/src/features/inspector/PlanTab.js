// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function PlanTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "plan".
// Data: session.detail.plan_mode and plan history (details only; primary actions live in ApprovalCard). Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function PlanTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.plan')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-plan" />`;
}
