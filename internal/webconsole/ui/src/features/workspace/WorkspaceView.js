// PLACEHOLDER — owned by the workspace feature worker.
// Contract: export function WorkspaceView(); route /workspace; root element
// data-testid="page-workspace" with the shared PageHeader. Data: workspace file APIs with lib/workspace-model.js.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function WorkspaceView() {
  useStore((state) => state.ui.locale, Object.is);
  return html`<div class="page" data-testid="page-workspace">
    <div class="page-inner">
      <${PageHeader} title=${t('shell.page.workspace.title')} description=${t('shell.page.workspace.description')} />
      <${EmptyState} icon="folder" title=${t('shell.placeholder.title')} body=${t('shell.placeholder.body')} />
    </div>
  </div>`;
}
