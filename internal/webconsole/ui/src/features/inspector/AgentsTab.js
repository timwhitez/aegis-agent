// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function AgentsTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "agents".
// Data: session.detail.children (sessions, jobs) and background_notifications. Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function AgentsTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.agents')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-agents" />`;
}
