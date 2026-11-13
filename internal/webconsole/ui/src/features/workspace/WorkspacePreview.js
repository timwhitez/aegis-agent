// Preview pane of the Workspace page: file header with download / rename /
// delete, byte-paged UTF-8 text preview ("加载更多" follows the server byte
// cursor; text is rendered as a text node so CRLF and combining marks stay
// exact), unsupported-type (415) state with a download link, and errors.
// On mobile the pane is a full-screen sheet with a back button.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { workspaceDownloadURL } from '../../lib/api.js';
import { bytesLabel, previewProgress } from '../../lib/workspace-model.js';
import { Button } from '../../ui/Button.js';
import { IconButton } from '../../ui/IconButton.js';
import { Icon } from '../../ui/Icon.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { Spinner } from '../../ui/Spinner.js';
import { entryName } from './workspace-helpers.js';
import { ErrorLines, Raw, SlotText } from './rich.js';

function DownloadLink({ path, variant = 'secondary', label }) {
  return html`<a
    class=${`btn btn-${variant} btn-sm ws-download`}
    href=${workspaceDownloadURL(path)}
    download=${entryName(path) || 'download'}
    data-testid="workspace-download"
  ><${Icon} name="download" size=${14} /><span class="btn-label">${label || t('workspace.preview.download')}</span></a>`;
}

function PreviewBody({ preview, onLoadMore, onRetry }) {
  if (preview.status === 'loading') {
    return html`<div class="ws-preview-state" aria-busy="true">
      <${Spinner} size=${16} label=${t('workspace.preview.loading')} />
      <${Skeleton} lines=${8} />
    </div>`;
  }
  if (preview.status === 'unsupported') {
    return html`<${EmptyState}
      icon="file"
      class="ws-preview-empty"
      title=${t('workspace.preview.unsupportedTitle')}
      body=${t('workspace.preview.unsupportedBody')}
      data-testid="workspace-preview-unsupported"
    ><${DownloadLink} path=${preview.path} variant="primary" /><//>`;
  }
  if (preview.status === 'error') {
    return html`<div class="ws-preview-state ws-preview-error" role="alert" data-testid="workspace-preview-error">
      <span class="ws-state-icon tone-danger"><${Icon} name="alert-circle" size=${18} /></span>
      <p class="ws-state-title">${t('workspace.preview.errorTitle')}</p>
      <p class="ws-state-body"><${SlotText} slot=${preview.error?.slot} /></p>
      <${ErrorLines} error=${{ detail: preview.error?.detail, action: preview.error?.action }} class="ws-state-body" />
      <${Button} size="sm" icon="refresh" onClick=${onRetry}>${t('workspace.retry')}<//>
    </div>`;
  }
  const data = preview.data;
  const progress = previewProgress(data);
  return html`
    <pre
      class="ws-preview-content"
      translate=${false}
      tabindex="0"
      aria-label=${t('workspace.preview.contentLabel')}
      data-testid="workspace-preview-content"
    >${data?.content ?? ''}</pre>
    ${progress || preview.moreError ? html`<div class="ws-preview-foot" data-testid="workspace-preview-foot">
      <span class=${preview.moreError ? 'ws-preview-foot-error' : 'ws-preview-foot-meta'} role=${preview.moreError ? 'alert' : undefined}>
        ${preview.moreError
          ? html`${t('workspace.preview.loadMoreFailed')} <${SlotText} slot=${preview.moreError.slot} />`
          : html`<${SlotText} slot=${progress.label} />`}
      </span>
      ${progress ? html`<${Button}
        size="sm"
        icon=${preview.moreError ? 'refresh' : 'arrow-down'}
        pending=${preview.moreLoading}
        onClick=${onLoadMore}
        data-testid="workspace-preview-more"
      >${preview.moreLoading ? t('workspace.preview.loadingMore') : t('workspace.preview.loadMore')}<//>` : null}
    </div>` : null}
  `;
}

/**
 * Props: preview, sheet (mobile full-screen), backRef, onClose, onLoadMore,
 * onRetry, onRename, onDelete, canMutate, pending, renameRef.
 */
export function WorkspacePreview({ preview, sheet, backRef, onClose, onLoadMore, onRetry, onRename, onDelete, canMutate, pending, renameRef }) {
  if (!preview) {
    return html`<section class="ws-preview-pane is-idle" aria-label=${t('workspace.preview.label')} data-testid="workspace-preview">
      <${EmptyState} icon="file-text" class="ws-preview-empty" title=${t('workspace.preview.emptyTitle')} body=${t('workspace.preview.emptyBody')} />
    </section>`;
  }
  const name = entryName(preview.path);
  const size = preview.data ? bytesLabel(preview.data.size) : null;
  return html`<section
    class=${`ws-preview-pane ${sheet ? 'is-sheet' : ''}`}
    aria-label=${t('workspace.preview.label')}
    data-testid="workspace-preview"
    data-path=${preview.path}
    data-status=${preview.status}
    onKeyDown=${(event) => { if (sheet && event.key === 'Escape' && !event.isComposing) { event.stopPropagation(); onClose(); } }}
  >
    <header class="ws-preview-head">
      ${sheet ? html`<${IconButton} icon="chevron-left" label=${t('workspace.preview.back')} buttonRef=${backRef} onClick=${onClose} data-testid="workspace-preview-back" />` : null}
      <div class="ws-preview-heading">
        <h2 class="ws-preview-title"><span class="raw-value" translate=${false}>${name}</span></h2>
        <p class="ws-preview-sub">
          ${preview.path !== name ? html`<${Raw} value=${preview.path} class="ws-preview-path" />` : null}
          ${preview.path !== name && size ? html`<span class="ws-dot" aria-hidden="true">·</span>` : null}
          ${size ? html`<span>${size.raw ?? t('workspace.sizeUnknown')}</span>` : null}
        </p>
      </div>
      <div class="ws-preview-actions">
        <${DownloadLink} path=${preview.path} />
        <${IconButton}
          icon="pencil"
          buttonRef=${renameRef}
          label=${t('workspace.preview.rename')}
          disabled=${!canMutate || Boolean(pending)}
          pending=${pending === 'rename'}
          onClick=${onRename}
          data-testid="workspace-rename"
        />
        <${IconButton}
          icon="trash"
          label=${t('workspace.preview.delete')}
          class="ws-danger-icon"
          disabled=${!canMutate || Boolean(pending)}
          pending=${pending === 'delete-file'}
          onClick=${onDelete}
          data-testid="workspace-delete-file"
        />
        ${sheet ? null : html`<${IconButton} icon="x" label=${t('workspace.preview.close')} onClick=${onClose} data-testid="workspace-preview-close" />`}
      </div>
    </header>
    <div class="ws-preview-body">
      <${PreviewBody} preview=${preview} onLoadMore=${onLoadMore} onRetry=${onRetry} />
    </div>
  </section>`;
}
