// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function TimelineTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "timeline".
// Data: session.detail.timeline / events and session.liveEvents. Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function TimelineTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.timeline')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-timeline" />`;
}
