// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function OverviewTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "overview".
// Data: session.detail (metadata, state, provider_attempts, longrun_checkpoint, contract, required_artifacts). Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function OverviewTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.overview')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-overview" />`;
}
