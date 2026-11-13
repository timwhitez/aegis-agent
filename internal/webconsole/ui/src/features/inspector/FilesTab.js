// PLACEHOLDER — owned by the inspector feature worker.
// Contract: export function FilesTab({ sessionId }); rendered inside the
// inspector tab panel (role=tabpanel provided by Inspector.js) for tab "files".
// Data: session.detail.file_changes, refreshed with getSessionFileChanges. Read via useStore(selector); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function FilesTab() {
  return html`<${EmptyState} icon="info" title=${t('shell.inspector.tab.files')} body=${t('shell.placeholder.body')} data-testid="inspector-placeholder-files" />`;
}
