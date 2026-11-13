// Files tab: file changes from durable detail.file_changes, else inferred
// from the loaded messages (collectFileChanges). Shell redirects are
// heuristic, so a non-empty list always carries the "partial" disclosure.
import { html, useMemo } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { collectFileChanges } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { Badge } from '../../ui/Badge.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { withMessages } from './inspector-model.js';
import { Metrics, Notice, num, text } from './parts.js';

const OP_LABELS = {
  new: () => t('inspector.files.opNew'),
  edit: () => t('inspector.files.opEdit'),
};

function splitPath(path) {
  const value = String(path || '');
  const index = value.lastIndexOf('/');
  return index >= 0 ? { dir: value.slice(0, index + 1), name: value.slice(index + 1) } : { dir: '', name: value };
}

export function FilesTab() {
  const view = useStore((state) => ({ locale: state.ui.locale, detail: state.session.detail, messages: state.session.messages }));
  const model = useMemo(() => collectFileChanges(withMessages(view.detail, view.messages)), [view.detail, view.messages]);
  if (!model.files.length) {
    return html`<${EmptyState} icon="file-text" title=${t('inspector.files.emptyTitle')} body=${t('inspector.files.emptyBody')} data-testid="inspector-files-empty" />`;
  }
  return html`<div class="insp-tab insp-files" data-testid="inspector-files" data-source=${model.source}>
    <${Metrics} items=${[
      { label: t('inspector.files.count'), value: num(model.totals.files), testId: 'inspector-files-count' },
      { label: t('inspector.files.added'), value: '+' + num(model.totals.added) },
      { label: t('inspector.files.removed'), value: '−' + num(model.totals.removed) },
    ]} />
    ${model.disclosure ? html`<${Notice} tone="neutral" testId="inspector-files-disclosure"
      title=${html`<span class="insp-inline"><${Badge} tone="warning">${text(model.disclosure.badge)}<//> ${model.source === 'durable' ? t('inspector.files.sourceDurable') : t('inspector.files.sourceInferred')}</span>`}
    >${text(model.disclosure.note)}<//>` : null}
    <ul class="insp-list insp-file-list" role="list">
      ${model.files.map((file) => {
        const parts = splitPath(file.path);
        return html`<li class="insp-list-item insp-file" data-testid="inspector-file" data-path=${file.path} data-op=${file.op || undefined}>
          <div class="insp-list-head">
            <span class="insp-file-path" translate="no" title=${file.path}><span class="insp-file-dir">${parts.dir}</span><span class="insp-file-name">${parts.name}</span></span>
            ${file.op && OP_LABELS[file.op] ? html`<${Badge} tone=${file.op === 'new' ? 'success' : 'info'}>${OP_LABELS[file.op]()}<//>` : null}
          </div>
          <p class="insp-card-meta">
            ${file.linesAdded || file.linesRemoved ? html`<span class="insp-diffstat"><span class="is-added">+${num(file.linesAdded)}</span> <span class="is-removed">−${num(file.linesRemoved)}</span></span>` : null}
            ${file.writes ? html`<span>${t('inspector.files.writes', { count: file.writes })}</span>` : null}
            ${file.edits ? html`<span>${t('inspector.files.edits', { count: file.edits })}</span>` : null}
          </p>
        </li>`;
      })}
    </ul>
  </div>`;
}
