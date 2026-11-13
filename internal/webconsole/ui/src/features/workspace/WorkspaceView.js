// Workspace page (route /workspace, data-testid page-workspace): a restricted
// file manager over the server cwd (spec/17-web-console.md §2.2, §7 workspace
// rules, §7.19). Browsing may reach the parent cwd; uploads, new folders,
// renames and deletes are limited to the workspace/ root (server enforced; the
// UI disables them outside it). The browsed path is a UI preference bucketed
// by (workspace root, last session) and follows the session workdir only per
// lib/workspace-model.js syncWorkspaceToSession().
import { html, useEffect, useMemo, useRef, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import * as api from '../../lib/api.js';
import * as W from '../../lib/workspace-model.js';
import { useStore } from '../../state/hooks.js';
import { refreshMeta } from '../../state/actions.js';
import { pushToast, toastError } from '../../state/toasts.js';
import { confirmDialog, promptDialog } from '../../state/dialogs.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { Button } from '../../ui/Button.js';
import { IconButton } from '../../ui/IconButton.js';
import { Icon } from '../../ui/Icon.js';
import { Menu } from '../../ui/Menu.js';
import { Spinner } from '../../ui/Spinner.js';
import { copyText } from '../../ui/CopyButton.js';
import { lastSessionContext } from './workspace-session.js';
import {
  absoluteWorkspacePath,
  breadcrumbModel,
  canWriteDirectory,
  entryName,
  isMutableEntry,
  isOutsideWorkspaceRoot,
  loadPathPreferences,
  partitionEntries,
  previewFailureKind,
  savePathPreferences,
  selectAllState,
  validateEntryName,
} from './workspace-helpers.js';
import { ErrorLines, Raw, SlotText, errorInfo, richText } from './rich.js';
import { WorkspaceList } from './WorkspaceList.js';
import { WorkspacePreview } from './WorkspacePreview.js';

// ---- browsed-path preference (module scope: survives page switches) ----------

function storage() {
  try {
    return globalThis.localStorage || null;
  } catch {
    return null;
  }
}

let syncState = null;

function getSyncState() {
  if (!syncState) syncState = W.createWorkspaceState({ pathByBucket: loadPathPreferences(storage()) });
  return syncState;
}

function commitSyncState(next) {
  syncState = next;
  savePathPreferences(storage(), W.workspacePathPreferences(next));
}

function hasFiles(event) {
  return Array.from(event.dataTransfer?.types || []).includes('Files');
}

// ---- page ------------------------------------------------------------------------

export function WorkspaceView() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    mobile: state.ui.viewport.mobile,
    root: state.meta.data?.workspace_root || '',
    metaReady: Boolean(state.meta.data) || Boolean(state.meta.error),
    metaLoading: state.meta.loading,
  }));
  const [path, setPath] = useState(() => getSyncState().path || '');
  const [listing, setListing] = useState(null);
  const [listLoading, setListLoading] = useState(true);
  const [listError, setListError] = useState(null);
  const [notice, setNotice] = useState(null);
  const [preview, setPreview] = useState(null);
  const [selectionMode, setSelectionMode] = useState(false);
  const [selection, setSelection] = useState(() => new Set());
  const [pending, setPending] = useState('');
  const [uploadFailures, setUploadFailures] = useState([]);
  const [batchError, setBatchError] = useState(null);
  const [dragActive, setDragActive] = useState(false);
  const [focusIndex, setFocusIndex] = useState(0);
  const [liveStatus, setLiveStatus] = useState('');

  const dirSeq = useRef(W.createRequestSequence());
  const previewSeq = useRef(W.createRequestSequence());
  const mounted = useRef(true);
  const listRef = useRef(null);
  const fileInputRef = useRef(null);
  const newFolderRef = useRef(null);
  const uploadRef = useRef(null);
  const refreshRef = useRef(null);
  const renameRef = useRef(null);
  const backRef = useRef(null);
  const dragDepth = useRef(0);
  const ctxRef = useRef({ workspaceRoot: '', sessionId: '' });
  const focusAfterLoad = useRef(null);
  const previewRef = useRef(null);
  const pathRef = useRef(path);
  previewRef.current = preview;
  pathRef.current = path;

  // Pending buttons are disabled and drop focus; hand it back once the action settles.
  function restoreFocus(ref) {
    requestAnimationFrame(() => {
      const active = document.activeElement;
      if (!active || active === document.body || !document.contains(active) || active.disabled) ref.current?.focus({ preventScroll: true });
    });
  }

  const writable = canWriteDirectory(path);
  const atRoot = !W.normalizeWorkspacePath(path);
  const rows = useMemo(() => {
    if (!listing) return [];
    return [...(listing.parent ? [{ ...listing.parent, kind: 'parent' }] : []), ...listing.entries];
  }, [listing]);
  const selectablePaths = useMemo(() => (listing?.entries || []).filter(isMutableEntry).map((entry) => entry.path), [listing]);
  const sheet = Boolean(view.mobile && preview);

  useEffect(() => () => { mounted.current = false; }, []);

  // ---- loading ----------------------------------------------------------------

  async function loadDirectory(target, { keepPreview = false, focus = null } = {}) {
    const seq = dirSeq.current.next();
    setListLoading(true);
    if (!keepPreview) {
      previewSeq.current.next();
      setPreview(null);
    }
    try {
      const result = await W.loadDirectoryWithFallback(target, (candidate) => api.listWorkspaceFiles(candidate));
      if (!mounted.current || !dirSeq.current.isCurrent(seq)) return false;
      const next = partitionEntries(result.tree);
      const changedDir = result.path !== pathRef.current;
      setListing(next);
      setListError(null);
      setPath(result.path);
      setNotice(result.notice);
      setBatchError(null);
      commitSyncState(W.setWorkspacePath(getSyncState(), result.path, ctxRef.current));
      setSelection((current) => (changedDir ? new Set() : W.pruneSelection(current, next.entries)));
      if (keepPreview) {
        const current = previewRef.current;
        if (current && !next.entries.some((entry) => entry.path === current.path)) {
          previewSeq.current.next();
          setPreview(null);
        }
      }
      const rowsNext = [...(next.parent ? [{ ...next.parent, kind: 'parent' }] : []), ...next.entries];
      let index = 0;
      if (focus?.path !== undefined) {
        const found = rowsNext.findIndex((row) => row.kind !== 'parent' && row.path === focus.path);
        index = found >= 0 ? found : 0;
      } else if (!changedDir) {
        index = Math.min(focusIndexRef.current, Math.max(rowsNext.length - 1, 0));
      }
      setFocusIndex(index);
      focusAfterLoad.current = focus && focus.move !== false ? index : null;
      return true;
    } catch (error) {
      if (!mounted.current || !dirSeq.current.isCurrent(seq)) return false;
      setListError({ ...errorInfo(error, t('workspace.list.loadFailed')), target: W.normalizeWorkspacePath(target) });
      return false;
    } finally {
      if (mounted.current && dirSeq.current.isCurrent(seq)) setListLoading(false);
    }
  }

  const focusIndexRef = useRef(focusIndex);
  focusIndexRef.current = focusIndex;

  // Move focus into the list after keyboard/click navigation re-rendered it.
  useEffect(() => {
    if (focusAfterLoad.current === null || listLoading) return;
    const index = focusAfterLoad.current;
    focusAfterLoad.current = null;
    listRef.current?.querySelectorAll('.ws-entry')?.[index]?.focus({ preventScroll: false });
  }, [listing, listLoading]);

  // Initial load: sync the bucket for (workspace root, last session) once meta is known.
  useEffect(() => {
    if (!view.metaReady) {
      if (!view.metaLoading) refreshMeta();
      return;
    }
    const context = lastSessionContext();
    const sessionId = W.workspaceSessionId(context.id, context.backed);
    ctxRef.current = { workspaceRoot: view.root, sessionId };
    const synced = W.syncWorkspaceToSession(getSyncState(), { workspaceRoot: view.root, sessionId, sessionWorkdir: context.workdir });
    commitSyncState(synced.state);
    pathRef.current = synced.state.path;
    setPath(synced.state.path);
    loadDirectory(synced.state.path);
  }, [view.metaReady, view.root]);

  async function openFile(filePath) {
    const seq = previewSeq.current.next();
    setPreview({ path: filePath, status: 'loading', data: null, error: null, moreLoading: false, moreError: null });
    try {
      const data = await api.readWorkspaceFile(filePath, 0, W.WORKSPACE_FILE_PREVIEW_CHUNK_SIZE);
      if (!mounted.current || !previewSeq.current.isCurrent(seq)) return false;
      const page = W.interpretPreviewPage(data, { path: filePath, offset: 0 });
      if (!page.ok) {
        setPreview({ path: filePath, status: 'error', data: null, error: { slot: page.error }, moreLoading: false, moreError: null });
        return true;
      }
      setPreview({ path: filePath, status: 'ready', data: page.preview, error: null, moreLoading: false, moreError: null });
      return true;
    } catch (error) {
      if (!mounted.current || !previewSeq.current.isCurrent(seq)) return false;
      if (previewFailureKind(error) === 'unsupported') {
        setPreview({ path: filePath, status: 'unsupported', data: null, error: null, moreLoading: false, moreError: null });
      } else {
        setPreview({
          path: filePath,
          status: 'error',
          data: null,
          error: { slot: W.previewLoadError(error, filePath), detail: error?.detail || '', action: error?.action || '' },
          moreLoading: false,
          moreError: null,
        });
      }
      return true;
    }
  }

  async function loadMore() {
    const current = previewRef.current;
    const request = W.nextPreviewRequest(current?.data);
    if (!request || current.moreLoading) return;
    const seq = previewSeq.current.next();
    setPreview({ ...current, moreLoading: true, moreError: null });
    try {
      const data = await api.readWorkspaceFile(request.path, request.offset, request.limit);
      if (!mounted.current || !previewSeq.current.isCurrent(seq)) return;
      const page = W.interpretPreviewPage(data, { path: request.path, offset: request.offset, previous: current.data, append: true });
      if (!page.ok) {
        setPreview((latest) => ({ ...latest, moreLoading: false, moreError: { slot: page.error } }));
        return;
      }
      setPreview((latest) => ({ ...latest, data: page.preview, moreLoading: false, moreError: null }));
    } catch (error) {
      if (!mounted.current || !previewSeq.current.isCurrent(seq)) return;
      setPreview((latest) => ({ ...latest, moreLoading: false, moreError: { slot: W.previewLoadError(error, request.path) } }));
    }
  }

  function closePreview() {
    const closing = previewRef.current?.path;
    previewSeq.current.next();
    setPreview(null);
    if (closing) {
      const index = rows.findIndex((row) => row.kind !== 'parent' && row.path === closing);
      if (index >= 0) {
        setFocusIndex(index);
        requestAnimationFrame(() => listRef.current?.querySelectorAll('.ws-entry')?.[index]?.focus());
      }
    }
  }

  // Focus the back button when the mobile sheet opens.
  useEffect(() => {
    if (sheet) backRef.current?.focus({ preventScroll: true });
  }, [sheet, preview?.path]);

  // ---- navigation ---------------------------------------------------------------

  function navigateTo(target, { focus = null } = {}) {
    setUploadFailures([]);
    loadDirectory(target, { focus });
  }

  function goParent() {
    if (listing?.parent) navigateTo(listing.parent.path, { focus: { path: W.normalizeWorkspacePath(path) } });
  }

  function onActivate(row, index) {
    setFocusIndex(index);
    if (selectionMode && isMutableEntry(row)) {
      toggleSelection(row.path, !selection.has(row.path));
      return;
    }
    if (row.kind === 'parent') {
      goParent();
      return;
    }
    if (row.type === 'directory') {
      navigateTo(row.path, { focus: { path: '' } });
      return;
    }
    openFile(row.path);
  }

  function toggleSelection(entryPath, checked) {
    setSelection((current) => W.toggleSelection(current, entryPath, checked));
  }

  function toggleSelectionMode() {
    setSelectionMode((on) => !on);
    setSelection(new Set());
    setBatchError(null);
  }

  // ---- mutations ----------------------------------------------------------------

  const folderLabel = (dir) => (W.normalizeWorkspacePath(dir) ? html`<${Raw} value=${dir} class="ws-inline-path" />` : t('workspace.rootLabel'));

  async function refresh() {
    if (pending) return;
    setPending('refresh');
    const ok = await loadDirectory(pathRef.current, { keepPreview: true });
    if (mounted.current) {
      setPending('');
      restoreFocus(refreshRef);
      if (ok) setLiveStatus(t('workspace.refreshed'));
    }
  }

  async function createFolder() {
    if (pending || !writable) return;
    const dir = pathRef.current;
    const name = await promptDialog({
      title: t('workspace.newFolder.title'),
      label: t('workspace.newFolder.label'),
      body: html`<span>${richText('workspace.newFolder.body', { path: folderLabel(dir) })}</span>`,
      confirmLabel: t('workspace.newFolder.confirm'),
      validate: (value) => {
        const key = validateEntryName(value);
        return key ? t(key) : '';
      },
    });
    if (name === null || !mounted.current) return;
    setPending('mkdir');
    try {
      const result = await api.createWorkspaceDirectory(dir || '.', name.trim());
      pushToast({ tone: 'success', message: t('workspace.newFolder.done') });
      await loadDirectory(dir, { keepPreview: true, focus: { path: W.normalizeWorkspacePath(result?.path || W.joinWorkspacePath(dir, name.trim())), move: false } });
    } catch (error) {
      toastError(error, 'workspace.newFolder.failed');
    } finally {
      if (mounted.current) {
        setPending('');
        restoreFocus(newFolderRef);
      }
    }
  }

  async function uploadFiles(fileList) {
    const files = Array.from(fileList || []).filter(Boolean);
    if (!files.length || pending || !writable) return;
    const dir = pathRef.current;
    setPending('upload');
    setUploadFailures([]);
    const failures = [];
    let uploaded = 0;
    let lastPath = '';
    for (let index = 0; index < files.length; index += 1) {
      setLiveStatus(t('workspace.upload.progress', { current: index + 1, total: files.length }));
      try {
        const result = await api.uploadWorkspaceFile(dir || '.', files[index]);
        uploaded += 1;
        lastPath = W.normalizeWorkspacePath(result?.path || '');
      } catch (error) {
        failures.push({ name: files[index].name, ...errorInfo(error, t('workspace.upload.failedGeneric')) });
      }
    }
    if (!mounted.current) return;
    setPending('');
    setLiveStatus('');
    restoreFocus(uploadRef);
    if (uploaded) pushToast({ tone: 'success', message: uploaded === 1 ? t('workspace.upload.doneOne') : t('workspace.upload.doneMany', { count: uploaded }) });
    if (failures.length) {
      setUploadFailures(failures);
      pushToast({ tone: 'danger', message: failures.length === 1 ? t('workspace.upload.failedOne') : t('workspace.upload.failedMany', { count: failures.length }) });
    }
    if (uploaded) await loadDirectory(dir, { keepPreview: true, focus: lastPath ? { path: lastPath, move: false } : null });
  }

  async function deleteCurrentFolder() {
    const dir = pathRef.current;
    if (pending || !dir || !writable) return;
    const ok = await confirmDialog({
      title: t('workspace.deleteFolder.title'),
      body: html`<span>${richText('workspace.deleteFolder.body', { path: html`<${Raw} value=${dir} class="ws-inline-path" />` })}</span>`,
      confirmLabel: t('workspace.deleteFolder.confirm'),
      danger: true,
    });
    if (!ok || !mounted.current) return;
    setPending('delete-folder');
    try {
      await api.deleteWorkspacePath(dir);
      pushToast({ tone: 'success', message: t('workspace.deleteFolder.done') });
      await loadDirectory(W.parentWorkspacePath(dir));
    } catch (error) {
      toastError(error, 'workspace.deleteFolder.failed');
    } finally {
      if (mounted.current) {
        setPending('');
        restoreFocus(refreshRef);
      }
    }
  }

  async function deleteSelected() {
    const paths = W.sortedSelection(selection);
    if (pending || !paths.length) return;
    const ok = await confirmDialog({
      title: t('workspace.batchDelete.title', { count: paths.length }),
      body: t('workspace.batchDelete.body'),
      confirmLabel: t('workspace.batchDelete.confirm'),
      danger: true,
    });
    if (!ok || !mounted.current) return;
    setPending('delete-selected');
    setBatchError(null);
    try {
      const result = await api.deleteWorkspacePaths(paths);
      const count = Number(result?.count || paths.length);
      pushToast({ tone: 'success', message: t('workspace.batchDelete.done', { count }) });
      setSelection(new Set());
      setSelectionMode(false);
      await loadDirectory(pathRef.current, { keepPreview: true });
    } catch (error) {
      setBatchError(errorInfo(error, t('workspace.batchDelete.failed')));
      toastError(error, 'workspace.batchDelete.failed');
    } finally {
      if (mounted.current) {
        setPending('');
        restoreFocus(refreshRef);
      }
    }
  }

  async function renameFile() {
    const current = previewRef.current;
    if (!current?.path || pending) return;
    const currentName = entryName(current.path);
    const name = await promptDialog({
      title: t('workspace.rename.title'),
      label: t('workspace.rename.label'),
      initialValue: currentName,
      body: t('workspace.rename.body'),
      confirmLabel: t('workspace.rename.confirm'),
      validate: (value) => {
        const key = validateEntryName(value, { current: currentName });
        return key ? t(key) : '';
      },
    });
    if (name === null || !mounted.current) return;
    setPending('rename');
    try {
      const trimmed = name.trim();
      const result = await api.renameWorkspaceFile(current.path, trimmed);
      const renamed = W.normalizeWorkspacePath(result?.path || W.joinWorkspacePath(W.parentWorkspacePath(current.path), trimmed));
      pushToast({ tone: 'success', message: t('workspace.rename.done') });
      await loadDirectory(pathRef.current, { keepPreview: true });
      if (mounted.current) await openFile(renamed);
    } catch (error) {
      toastError(error, 'workspace.rename.failed');
    } finally {
      if (mounted.current) {
        setPending('');
        restoreFocus(renameRef);
      }
    }
  }

  async function deleteFile() {
    const current = previewRef.current;
    if (!current?.path || pending) return;
    const ok = await confirmDialog({
      title: t('workspace.deleteFile.title'),
      body: html`<span>${richText('workspace.deleteFile.body', { name: html`<${Raw} value=${entryName(current.path)} class="ws-inline-path" />` })}</span>`,
      confirmLabel: t('workspace.deleteFile.confirm'),
      danger: true,
    });
    if (!ok || !mounted.current) return;
    setPending('delete-file');
    try {
      await api.deleteWorkspacePath(current.path);
      pushToast({ tone: 'success', message: t('workspace.deleteFile.done') });
      previewSeq.current.next();
      setPreview(null);
      await loadDirectory(pathRef.current, { keepPreview: true });
    } catch (error) {
      toastError(error, 'workspace.deleteFile.failed');
    } finally {
      if (mounted.current) {
        setPending('');
        restoreFocus(refreshRef);
      }
    }
  }

  async function copyCurrentPath() {
    const absolute = absoluteWorkspacePath(view.root, pathRef.current);
    const ok = await copyText(absolute || pathRef.current || '.');
    pushToast({ tone: ok ? 'success' : 'danger', message: ok ? t('workspace.pathCopied') : t('common.copyFailed') });
  }

  const dragHandlers = {
    onDragEnter: (event) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      dragDepth.current += 1;
      setDragActive(true);
    },
    onDragOver: (event) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      if (event.dataTransfer) event.dataTransfer.dropEffect = writable && !pending ? 'copy' : 'none';
    },
    onDragLeave: (event) => {
      if (!hasFiles(event)) return;
      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (dragDepth.current === 0) setDragActive(false);
    },
    onDrop: (event) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      dragDepth.current = 0;
      setDragActive(false);
      if (writable) uploadFiles(event.dataTransfer?.files);
    },
  };

  // ---- render -------------------------------------------------------------------

  const crumbs = breadcrumbModel(path, view.root);
  const absolute = absoluteWorkspacePath(view.root, path);
  const selectState = selectAllState(selection, selectablePaths);
  const previewMutable = Boolean(preview?.path) && isMutableEntry({ path: preview.path, type: 'file' });

  return html`<div class="page ws-page" data-testid="page-workspace">
    <div class="page-inner ws-inner">
      <${PageHeader}
        title=${t('shell.page.workspace.title')}
        description=${t('workspace.page.description')}
        actions=${html`
          <${Button} icon="plus" buttonRef=${newFolderRef} disabled=${!writable || Boolean(pending)} pending=${pending === 'mkdir'} onClick=${createFolder} data-testid="workspace-new-folder">${t('workspace.action.newFolder')}<//>
          <${Button} variant="primary" icon="upload" buttonRef=${uploadRef} disabled=${!writable || Boolean(pending)} pending=${pending === 'upload'} onClick=${() => fileInputRef.current?.click()} data-testid="workspace-upload">${t('workspace.action.upload')}<//>
        `}
      />
      <input ref=${fileInputRef} type="file" multiple hidden data-testid="workspace-upload-input"
        onChange=${(event) => { const files = Array.from(event.currentTarget.files || []); event.currentTarget.value = ''; uploadFiles(files); }} />

      <div class="ws-shell">
        <div class="ws-toolbar">
          <nav class="ws-breadcrumb" aria-label=${t('workspace.breadcrumb.label')} data-testid="workspace-breadcrumb">
            <ol role="list">
              ${crumbs.map((crumb, index) => {
                const last = index === crumbs.length - 1;
                const label = crumb.kind === 'root'
                  ? html`<${Icon} name="folder" size=${14} /><span>${t('workspace.root')}</span>`
                  : crumb.kind === 'parent'
                    ? (crumb.label ? html`<${Raw} value=${crumb.label} />` : html`<span>${t('workspace.breadcrumb.parent')}</span>`)
                    : html`<${Raw} value=${crumb.label} />`;
                return html`<li class="ws-crumb-item">
                  ${index > 0 ? html`<span class="ws-crumb-sep" aria-hidden="true">/</span>` : null}
                  ${last
                    ? html`<span class="ws-crumb is-current" aria-current="page" title=${absolute || undefined} data-testid="workspace-crumb" data-path=${crumb.path}>${label}</span>`
                    : html`<button type="button" class="ws-crumb" data-testid="workspace-crumb" data-path=${crumb.path} onClick=${() => navigateTo(crumb.path)}>${label}</button>`}
                </li>`;
              })}
            </ol>
          </nav>
          <div class="ws-toolbar-actions">
            ${listLoading && listing ? html`<${Spinner} size=${14} label=${t('workspace.list.loading')} class="ws-toolbar-spinner" />` : null}
            <${Button}
              variant="ghost"
              size="sm"
              icon=${selectionMode ? 'check' : 'list-checks'}
              aria-pressed=${String(selectionMode)}
              disabled=${!selectablePaths.length && !selectionMode}
              onClick=${toggleSelectionMode}
              data-testid="workspace-select-toggle"
            >${selectionMode ? t('workspace.action.doneSelecting') : t('workspace.action.select')}<//>
            <${IconButton} icon="refresh" buttonRef=${refreshRef} label=${t('workspace.action.refresh')} pending=${pending === 'refresh'} disabled=${Boolean(pending) && pending !== 'refresh'} onClick=${refresh} data-testid="workspace-refresh" />
            <${Menu}
              label=${t('workspace.action.more')}
              testId="workspace-more"
              items=${[
                { id: 'copy', label: t('workspace.action.copyPath'), icon: 'copy', onSelect: copyCurrentPath, testId: 'workspace-copy-path' },
                { separator: true },
                { id: 'delete', label: t('workspace.action.deleteFolder'), icon: 'trash', danger: true, disabled: atRoot || !writable || Boolean(pending), onSelect: deleteCurrentFolder, testId: 'workspace-delete-folder' },
              ]}
            />
          </div>
        </div>

        ${!writable ? html`<div class="ws-banner tone-info" role="note" data-testid="workspace-readonly">
          <${Icon} name="info" size=${16} />
          <span class="ws-banner-text">${t('workspace.readOnly.notice')}</span>
          <${Button} size="sm" variant="ghost" onClick=${() => navigateTo('')}>${t('workspace.readOnly.back')}<//>
        </div>` : null}
        ${notice ? html`<div class="ws-banner tone-info" role="status" data-testid="workspace-notice">
          <${Icon} name="info" size=${16} />
          <span class="ws-banner-text"><${SlotText} slot=${notice} /></span>
          <${IconButton} icon="x" size="sm" label=${t('common.dismiss')} onClick=${() => setNotice(null)} />
        </div>` : null}
        ${listError && listing ? html`<div class="ws-banner tone-danger" role="alert" data-testid="workspace-list-error">
          <${Icon} name="alert-circle" size=${16} />
          <div class="ws-banner-text"><strong>${t('workspace.list.loadFailed')}</strong><${ErrorLines} error=${listError} /></div>
          <${Button} size="sm" variant="ghost" onClick=${() => loadDirectory(listError.target)}>${t('workspace.retry')}<//>
        </div>` : null}
        ${uploadFailures.length ? html`<div class="ws-banner tone-danger" role="alert" data-testid="workspace-upload-failures">
          <${Icon} name="alert-circle" size=${16} />
          <div class="ws-banner-text">
            <strong>${uploadFailures.length === 1 ? t('workspace.upload.failedOne') : t('workspace.upload.failedMany', { count: uploadFailures.length })}</strong>
            <ul class="ws-failure-list" role="list">${uploadFailures.map((failure) => html`<li><${Raw} value=${failure.name} class="ws-inline-path" /><span class="ws-failure-sep" aria-hidden="true">—</span><${ErrorLines} error=${failure} /></li>`)}</ul>
          </div>
          <${IconButton} icon="x" size="sm" label=${t('common.dismiss')} onClick=${() => setUploadFailures([])} />
        </div>` : null}

        ${selectionMode ? html`<div class="ws-selection-bar" role="region" aria-label=${t('workspace.selection.label')} data-testid="workspace-selection-bar">
          <label class="ws-select-all">
            <input
              type="checkbox"
              checked=${selectState.checked}
              indeterminate=${selectState.indeterminate}
              disabled=${!selectablePaths.length}
              onChange=${(event) => setSelection(event.currentTarget.checked ? new Set(selectablePaths) : new Set())}
              data-testid="workspace-select-all"
            />
            <span>${t('workspace.selection.selectAll')}</span>
          </label>
          <span class="ws-selection-count" aria-live="polite">${t('workspace.selection.count', { count: selectState.count })}</span>
          <span class="ws-spacer"></span>
          ${batchError ? html`<span class="ws-selection-error" role="alert" data-testid="workspace-batch-error"><${ErrorLines} error=${batchError} /></span>` : null}
          <${Button} size="sm" variant="danger" icon="trash" disabled=${!selectState.count || (Boolean(pending) && pending !== 'delete-selected')} pending=${pending === 'delete-selected'} onClick=${deleteSelected} data-testid="workspace-delete-selected">
            ${t('workspace.selection.delete')}
          <//>
        </div>` : null}

        <div class=${`ws-panes ${preview ? 'has-preview' : ''}`}>
          <${WorkspaceList}
            rows=${rows}
            focusIndex=${focusIndex}
            setFocusIndex=${setFocusIndex}
            listRef=${listRef}
            activePath=${preview?.path || ''}
            selectionMode=${selectionMode}
            selection=${selection}
            onActivate=${onActivate}
            onToggle=${toggleSelection}
            onParent=${goParent}
            loading=${listLoading}
            error=${listError}
            onRetry=${() => loadDirectory(listError?.target ?? pathRef.current)}
            writable=${writable}
            atRoot=${atRoot}
            dragActive=${dragActive}
            dragHandlers=${dragHandlers}
            entryCount=${listing?.entries.length ?? 0}
            onUpload=${() => fileInputRef.current?.click()}
            hasListing=${Boolean(listing)}
            inert=${sheet}
          />
          <${WorkspacePreview}
            preview=${preview}
            sheet=${sheet}
            backRef=${backRef}
            onClose=${closePreview}
            onLoadMore=${loadMore}
            onRetry=${() => preview && openFile(preview.path)}
            onRename=${renameFile}
            renameRef=${renameRef}
            onDelete=${deleteFile}
            canMutate=${previewMutable}
            pending=${pending}
          />
        </div>
      </div>
      <p class="sr-only" role="status" aria-live="polite">${liveStatus}</p>
    </div>
  </div>`;
}
