// File list pane of the Workspace page: parent row, folders/files with
// optional size/modified columns, roving keyboard focus (↑/↓/Home/End,
// Enter opens, Backspace goes up, Space toggles in selection mode),
// multi-select checkboxes and drag-and-drop upload target.
import { html, useId } from '../../lib/html.js';
import { t, formatDateTime, formatRelativeTime } from '../../lib/i18n.js';
import { bytesLabel } from '../../lib/workspace-model.js';
import { Icon } from '../../ui/Icon.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { Button } from '../../ui/Button.js';
import { entryMeta, isMutableEntry, listHasMeta, nextFocusIndex } from './workspace-helpers.js';
import { ErrorLines } from './rich.js';

function isEditable(target) {
  return Boolean(target?.closest?.('input[type="text"], input:not([type]), textarea, select, [contenteditable="true"]'));
}

function RawName({ value }) {
  return html`<span class="raw-value ws-entry-name" translate=${false}>${String(value ?? '')}</span>`;
}

function rowIcon(row) {
  if (row.kind === 'parent') return 'arrow-up';
  return row.type === 'directory' ? 'folder' : 'file-text';
}

function SizeCell({ row }) {
  const meta = entryMeta(row);
  if (meta.size === null) return html`<span class="ws-cell ws-cell-size" aria-hidden="true"></span>`;
  const label = bytesLabel(meta.size);
  return html`<span class="ws-cell ws-cell-size">${label.raw ?? t('workspace.sizeUnknown')}</span>`;
}

function ModifiedCell({ row }) {
  const meta = entryMeta(row);
  if (!meta.modified) return html`<span class="ws-cell ws-cell-modified" aria-hidden="true"></span>`;
  return html`<span class="ws-cell ws-cell-modified" title=${formatDateTime(meta.modified)}>${formatRelativeTime(meta.modified)}</span>`;
}

/**
 * Props: rows, focusIndex, setFocusIndex, listRef, activePath, selectionMode,
 * selection (Set), onActivate(row), onToggle(path, checked), onParent(),
 * loading, error, onRetry, writable, atRoot, dragActive, dragHandlers,
 * entryCount, onUpload.
 */
export function WorkspaceList(props) {
  const {
    rows, focusIndex, setFocusIndex, listRef, activePath, selectionMode, selection, onActivate, onToggle, onParent,
    loading, error, onRetry, writable, atRoot, dragActive, dragHandlers, entryCount, onUpload, hasListing, inert,
  } = props;
  const hintId = useId();
  const showMeta = listHasMeta(rows);

  const focusRow = (index) => {
    setFocusIndex(index);
    const buttons = listRef.current?.querySelectorAll('.ws-entry');
    buttons?.[index]?.focus();
  };

  const onKeyDown = (event) => {
    if (event.isComposing || event.altKey || event.ctrlKey || event.metaKey) return;
    const next = nextFocusIndex(focusIndex, event.key, rows.length);
    if (next >= 0) {
      event.preventDefault();
      focusRow(next);
      return;
    }
    if (event.key === 'Backspace' && !isEditable(event.target)) {
      event.preventDefault();
      onParent();
      return;
    }
    if (event.key === ' ' && selectionMode && event.target?.classList?.contains('ws-entry')) {
      const row = rows[focusIndex];
      if (row && isMutableEntry(row)) {
        event.preventDefault();
        onToggle(row.path, !selection.has(row.path));
      }
    }
  };

  let body;
  if (error && !hasListing) {
    body = html`<div class="ws-list-state ws-list-error" role="alert" data-testid="workspace-list-error">
      <span class="ws-state-icon tone-danger"><${Icon} name="alert-circle" size=${18} /></span>
      <p class="ws-state-title">${t('workspace.list.loadFailed')}</p>
      <${ErrorLines} error=${error} class="ws-state-body" />
      <${Button} size="sm" icon="refresh" onClick=${onRetry}>${t('workspace.retry')}<//>
    </div>`;
  } else if (!hasListing) {
    body = html`<div class="ws-list-state" aria-busy="true">
      <${Skeleton} lines=${6} />
      <span class="sr-only" role="status">${t('workspace.list.loading')}</span>
    </div>`;
  } else {
    body = html`
      ${showMeta ? html`<div class="ws-list-head" aria-hidden="true">
        <span class="ws-cell ws-cell-name">${t('workspace.column.name')}</span>
        <span class="ws-cell ws-cell-size">${t('workspace.column.size')}</span>
        <span class="ws-cell ws-cell-modified">${t('workspace.column.modified')}</span>
      </div>` : null}
      <ul
        class=${`ws-list ${showMeta ? 'has-meta' : ''}`}
        role="list"
        ref=${listRef}
        aria-label=${t('workspace.list.label')}
        aria-describedby=${hintId}
        aria-busy=${loading ? 'true' : undefined}
        data-testid="workspace-list"
        onKeyDown=${onKeyDown}
      >
        ${rows.map((row, index) => {
          const selectable = selectionMode && isMutableEntry(row);
          const checked = selectable && selection.has(row.path);
          const active = row.kind !== 'parent' && row.type === 'file' && row.path === activePath;
          const nameId = `${hintId}-n${index}`;
          return html`<li
            key=${(row.kind || 'entry') + ':' + row.path}
            class=${`ws-row ${active ? 'is-active' : ''} ${checked ? 'is-selected' : ''} ${row.kind === 'parent' ? 'is-parent' : ''}`}
            data-testid=${row.kind === 'parent' ? 'workspace-parent' : 'workspace-entry'}
            data-path=${row.path}
            data-type=${row.kind === 'parent' ? 'parent' : row.type}
          >
            ${selectionMode ? html`<label class="ws-check-cell" aria-hidden="true">${selectable ? html`<input
              type="checkbox"
              class="ws-check"
              tabindex="-1"
              aria-hidden="true"
              checked=${checked}
              data-testid="workspace-entry-check"
              onChange=${(event) => onToggle(row.path, event.currentTarget.checked)}
            />` : null}</label>` : null}
            <button
              type="button"
              class="ws-entry"
              tabindex=${index === focusIndex ? '0' : '-1'}
              aria-current=${active ? 'true' : undefined}
              aria-pressed=${selectable ? String(checked) : undefined}
              onFocus=${() => setFocusIndex(index)}
              onClick=${() => onActivate(row, index)}
            >
              <span class=${`ws-entry-icon ${row.type === 'directory' || row.kind === 'parent' ? 'is-folder' : ''}`}><${Icon} name=${rowIcon(row)} size=${16} /></span>
              <span class="ws-cell ws-cell-name" id=${nameId}>
                ${row.kind === 'parent' ? t('workspace.list.parent') : html`<${RawName} value=${row.name} />`}
              </span>
              ${showMeta && row.kind !== 'parent' ? html`<${SizeCell} row=${row} /><${ModifiedCell} row=${row} />` : null}
              ${row.type === 'directory' && row.kind !== 'parent' ? html`<${Icon} name="chevron-right" size=${14} class="ws-entry-chevron" />` : null}
            </button>
          </li>`;
        })}
      </ul>
      ${entryCount === 0 ? html`<${EmptyState}
        icon="folder-open"
        class="ws-empty"
        title=${atRoot ? t('workspace.list.rootEmptyTitle') : t('workspace.list.emptyTitle')}
        body=${writable ? t('workspace.list.emptyBody') : t('workspace.list.emptyReadOnlyBody')}
      >${writable ? html`<${Button} size="sm" icon="upload" onClick=${onUpload}>${t('workspace.action.upload')}<//>` : null}<//>` : null}
    `;
  }

  return html`<section
    class=${`ws-list-pane ${dragActive ? 'is-dragging' : ''}`}
    aria-label=${t('workspace.list.label')}
    inert=${inert ? true : undefined}
    ...${dragHandlers}
  >
    <p id=${hintId} class="sr-only">${t('workspace.list.hint')}</p>
    <div class="ws-list-scroll">${body}</div>
    ${hasListing ? html`<div class="ws-list-foot">
      <span>${t('workspace.list.count', { count: entryCount })}</span>
      <span class="ws-list-foot-hint">${t('workspace.list.sensitiveHidden')}</span>
    </div>` : null}
    ${dragActive ? html`<div class=${`ws-dropzone ${writable ? '' : 'is-blocked'}`} data-testid="workspace-dropzone" aria-hidden="true">
      <${Icon} name=${writable ? 'upload' : 'alert-circle'} size=${20} />
      <span>${writable ? t('workspace.drop.title') : t('workspace.drop.readOnly')}</span>
    </div>` : null}
  </section>`;
}
