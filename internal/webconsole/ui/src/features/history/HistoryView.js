// PLACEHOLDER — owned by the history feature worker.
// Contract: export function HistoryView(); route /sessions; root element
// data-testid="page-sessions" with the shared PageHeader. Data: GET /api/history paging, open (/s/{id}), delete (confirmDialog), clear all; reveal ui.historyFocus when set.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function HistoryView() {
  useStore((state) => state.ui.locale, Object.is);
  return html`<div class="page" data-testid="page-sessions">
    <div class="page-inner">
      <${PageHeader} title=${t('shell.page.sessions.title')} description=${t('shell.page.sessions.description')} />
      <${EmptyState} icon="history" title=${t('shell.placeholder.title')} body=${t('shell.placeholder.body')} />
    </div>
  </div>`;
}
