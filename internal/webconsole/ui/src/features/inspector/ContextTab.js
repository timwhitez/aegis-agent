// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function ContextTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "context".
// Data: GET /api/sessions/{id}/context via lib/api.js getSessionContext (load on tab open). Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function ContextTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.context')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-context" />`;
}
