/**
 * Workspace browser model (spec/17-web-console.md §7 Workspace rules and
 * §7.19 byte paging). Pure: no DOM, no fetch, no storage access. Loaders are
 * injected where a flow needs I/O.
 *
 * - Paths are workspace-relative, '/'-separated, without leading/trailing
 *   slashes; '' is the workspace root.
 * - The browsed directory is a UI preference bucketed by
 *   JSON([workspace_root, sessionId || 'new-session']); the new-session
 *   composer has its own bucket. Polling/refresh never overwrite a manually
 *   browsed path; syncWorkspaceToSession() only moves to the session workdir
 *   on first select, session switch or a real workdir change.
 * - Preview paging uses only server byte cursors (offset/next_offset/eof);
 *   JavaScript string length is never used as a position. Legacy final pages
 *   may use the byte size; legacy truncated pages without a cursor fail.
 *
 * i18n keys emitted:
 *   workspace.pathFallback          {requested, shown} path unavailable, showing a parent
 *   workspace.pathFallbackRoot      {requested} path unavailable, showing the root
 *   workspace.preview.missingCursor preview response lacks a valid byte cursor
 *   workspace.preview.loadFailed    {path} generic preview failure fallback
 *   workspace.preview.progress      {shown, total} bytes shown so far (formatted)
 *   workspace.sizeUnknown           size is not a valid byte count
 */

/** Preview page size in bytes. */
export const WORKSPACE_FILE_PREVIEW_CHUNK_SIZE = 256 * 1024;

/** Max persisted preference buckets. */
export const WORKSPACE_PREFERENCE_LIMIT = 100;

const NEW_SESSION_BUCKET = 'new-session';

function str(value) {
  return value === undefined || value === null ? '' : String(value);
}

/** Workspace-relative path: backslashes to '/', no ./ prefix, no edge slashes, '.' => ''. */
export function normalizeWorkspacePath(path = '') {
  const normalized = str(path)
    .replace(/\\/g, '/')
    .replace(/^\.\//, '')
    .replace(/^\/+|\/+$/g, '');
  return normalized === '.' ? '' : normalized;
}

/** Absolute path with '/' separators and no trailing slash. */
export function normalizeAbsoluteWorkspacePath(value) {
  return str(value).replace(/\\/g, '/').replace(/\/+$/g, '');
}

/** Parent of a relative path ('' for top-level entries and the root). */
export function parentWorkspacePath(path) {
  const normalized = normalizeWorkspacePath(path);
  const index = normalized.lastIndexOf('/');
  return index > 0 ? normalized.slice(0, index) : '';
}

/** Join a directory and a child name. */
export function joinWorkspacePath(directory, name) {
  const dir = normalizeWorkspacePath(directory);
  const child = normalizeWorkspacePath(name);
  if (!dir) return child;
  if (!child) return dir;
  return dir + '/' + child;
}

/** Breadcrumb segments [{name, path}] for a relative path (root excluded). */
export function workspaceBreadcrumbs(path) {
  const parts = normalizeWorkspacePath(path).split('/').filter(Boolean);
  return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join('/') }));
}

/** Bucket key for a workspace root + durable session id ('' => new-session composer). */
export function workspaceBucketKey(workspaceRoot, sessionId = '') {
  const root = normalizeAbsoluteWorkspacePath(workspaceRoot);
  if (!root) return '';
  return JSON.stringify([root, str(sessionId).trim() || NEW_SESSION_BUCKET]);
}

/**
 * Relative path of a session workdir inside the workspace root: '' for the
 * root itself, the relative path when inside, null when unknown or outside.
 */
export function sessionWorkspacePath(workspaceRoot, workdir) {
  const root = str(workspaceRoot).trim();
  const dir = str(workdir).trim();
  if (!root || !dir) return null;
  const rootPath = normalizeAbsoluteWorkspacePath(root);
  const workdirPath = normalizeAbsoluteWorkspacePath(dir);
  if (workdirPath === rootPath) return '';
  const prefix = rootPath.endsWith('/') ? rootPath : rootPath + '/';
  if (!workdirPath.startsWith(prefix)) return null;
  return normalizeWorkspacePath(workdirPath.slice(prefix.length));
}

/** Absolute directory submitted as a start workdir ('' when no root is known). */
export function selectedWorkspaceWorkdir(workspaceRoot, path) {
  const root = str(workspaceRoot).trim();
  if (!root) return '';
  const rel = normalizeWorkspacePath(path);
  return rel ? root.replace(/\/+$/g, '') + '/' + rel : root;
}

/**
 * Durable session id used for bucketing ('' for ephemeral/new sessions).
 * @param {string} sessionId
 * @param {boolean} backed
 */
export function workspaceSessionId(sessionId, backed) {
  const id = str(sessionId).trim();
  if (!backed || !id || /^0x[0-9a-f]+$/i.test(id)) return '';
  return id;
}

/** Session workdir only when the loaded detail belongs to sessionId. */
export function sessionWorkdirFromDetail(sessionId, detail) {
  const id = str(sessionId).trim();
  if (!id || str(detail?.metadata?.id).trim() !== id) return '';
  return str(detail?.metadata?.workdir).trim();
}

/**
 * Fresh workspace sync state.
 * @param {{pathByBucket?: object}} [input] restored preferences
 */
export function createWorkspaceState(input = {}) {
  return {
    path: '',
    pathByBucket: restoreWorkspacePathPreferences(input.pathByBucket),
    activeBucket: '',
    pendingInitialWorkdirBucket: '',
    syncedSessionId: '',
    syncedSessionWorkdir: ''
  };
}

/** Restore persisted buckets (last WORKSPACE_PREFERENCE_LIMIT string entries). */
export function restoreWorkspacePathPreferences(preferences) {
  const out = {};
  if (preferences && typeof preferences === 'object' && !Array.isArray(preferences)) {
    Object.entries(preferences).slice(-WORKSPACE_PREFERENCE_LIMIT).forEach(([bucket, path]) => {
      if (typeof bucket === 'string' && typeof path === 'string') out[bucket] = normalizeWorkspacePath(path);
    });
  }
  return out;
}

/** Preferences to persist (plain object copy). */
export function workspacePathPreferences(state) {
  return restoreWorkspacePathPreferences(state?.pathByBucket);
}

/** Clear per-session sync tracking (keeps preferences and current path). */
export function resetWorkspaceSessionSync(state) {
  return { ...state, activeBucket: '', pendingInitialWorkdirBucket: '', syncedSessionId: '', syncedSessionWorkdir: '' };
}

function persistInto(state, bucket, path) {
  if (!bucket) return state;
  const pathByBucket = { ...state.pathByBucket };
  delete pathByBucket[bucket];
  pathByBucket[bucket] = normalizeWorkspacePath(path);
  const keys = Object.keys(pathByBucket);
  if (keys.length > WORKSPACE_PREFERENCE_LIMIT) {
    keys.slice(0, keys.length - WORKSPACE_PREFERENCE_LIMIT).forEach((key) => { delete pathByBucket[key]; });
  }
  return { ...state, activeBucket: bucket, pathByBucket };
}

/**
 * Set the browsed directory and persist it into the active bucket.
 * @param {object} state
 * @param {string} path
 * @param {{workspaceRoot?: string, sessionId?: string, persist?: boolean}} [ctx]
 */
export function setWorkspacePath(state, path, ctx = {}) {
  const next = { ...state, path: normalizeWorkspacePath(path) };
  if (ctx.persist === false) return next;
  const bucket = state.activeBucket || workspaceBucketKey(ctx.workspaceRoot, ctx.sessionId);
  return persistInto(next, bucket, next.path);
}

/**
 * Port of syncWorkspaceToCurrentSession(): decide whether the browsed path
 * should follow the session workdir.
 * @param {object} state
 * @param {{workspaceRoot: string, sessionId?: string, sessionWorkdir?: string, force?: boolean}} ctx
 *   sessionId: durable id ('' for the new-session composer)
 * @returns {{state: object, changed: boolean}} changed => clear selection/preview and reload
 */
export function syncWorkspaceToSession(state, ctx = {}) {
  const bucket = workspaceBucketKey(ctx.workspaceRoot, ctx.sessionId);
  if (!bucket) return { state, changed: false };
  const sessionId = str(ctx.sessionId).trim();
  const sessionWorkdir = sessionId ? str(ctx.sessionWorkdir).trim() : '';
  const sessionPath = sessionWorkspacePath(ctx.workspaceRoot, sessionWorkdir);
  let next = { ...state };
  let nextPath = state.path || '';
  if (state.activeBucket !== bucket) {
    next.activeBucket = bucket;
    const hasSavedPath = Object.prototype.hasOwnProperty.call(state.pathByBucket || {}, bucket);
    if (hasSavedPath) nextPath = normalizeWorkspacePath(state.pathByBucket[bucket]);
    else if (sessionPath !== null) nextPath = sessionPath;
    else nextPath = '';
    next.pendingInitialWorkdirBucket = hasSavedPath && sessionId && !sessionWorkdir ? bucket : '';
  } else if (state.pendingInitialWorkdirBucket === bucket && state.syncedSessionId === sessionId &&
    !state.syncedSessionWorkdir && sessionWorkdir) {
    // The detail arrived after a restored preference: record the workdir
    // without overriding the operator's restored path.
    return { state: { ...state, pendingInitialWorkdirBucket: '', syncedSessionWorkdir: sessionWorkdir }, changed: false };
  } else if (!ctx.force && state.syncedSessionId === sessionId && state.syncedSessionWorkdir === sessionWorkdir) {
    return { state, changed: false };
  } else if (sessionPath !== null) {
    nextPath = sessionPath;
  }
  const changed = nextPath !== (state.path || '');
  next.syncedSessionId = sessionId;
  next.syncedSessionWorkdir = sessionWorkdir;
  next.path = nextPath;
  next = persistInto(next, next.activeBucket, nextPath);
  return { state: next, changed };
}

/** Errors that mean "this directory is gone/invalid; try a parent". */
export function isUnavailableWorkspacePathError(err) {
  const status = Number(err?.status || 0);
  return status === 400 || status === 404;
}

/** [path, parent, ..., ''] candidates for fallback loading. */
export function directoryFallbackCandidates(path) {
  const candidates = [];
  let candidate = normalizeWorkspacePath(path);
  candidates.push(candidate);
  while (candidate) {
    candidate = parentWorkspacePath(candidate);
    candidates.push(candidate);
  }
  return candidates;
}

/** Notice for a fallback (null when the requested path loaded). */
export function workspaceFallbackNotice(requested, shown) {
  const req = normalizeWorkspacePath(requested);
  const got = normalizeWorkspacePath(shown);
  if (req === got) return null;
  return got
    ? { key: 'workspace.pathFallback', params: { requested: req, shown: got }, tone: 'info' }
    : { key: 'workspace.pathFallbackRoot', params: { requested: req }, tone: 'info' };
}

/**
 * Load a directory, walking up to the nearest accessible parent (then the
 * root) when the requested path is unavailable. Exactly one notice is
 * returned for a fallback; other errors propagate.
 * @param {string} path
 * @param {(path: string) => Promise<any>} loadDirectory injected loader (receives '.' for root)
 * @returns {Promise<{path: string, tree: any, notice: object|null}>}
 */
export async function loadDirectoryWithFallback(path, loadDirectory) {
  const requested = normalizeWorkspacePath(path);
  const candidates = directoryFallbackCandidates(requested);
  let lastError = null;
  for (const candidate of candidates) {
    try {
      const tree = await loadDirectory(candidate || '.');
      return { path: candidate, tree, notice: workspaceFallbackNotice(requested, candidate) };
    } catch (err) {
      lastError = err;
      if (!candidate || !isUnavailableWorkspacePathError(err)) throw err;
    }
  }
  throw lastError;
}

/**
 * Monotonic request sequence: start a request with next(), drop its response
 * unless isCurrent(seq). Directory loads, file loads and load-more share one
 * sequence so later navigation always wins.
 */
export function createRequestSequence() {
  let current = 0;
  return {
    next() {
      current += 1;
      return current;
    },
    isCurrent(seq) {
      return seq === current;
    },
    get current() {
      return current;
    }
  };
}

function nonNegative(value, fallback) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}

/**
 * Interpret a GET /api/file/read page using server byte cursors only.
 * @param {object} data response {content, offset, limit, size, truncated, next_offset, eof}
 * @param {{path: string, offset?: number, previous?: object|null, append?: boolean}} ctx
 * @returns {{ok: true, preview: {path, content, offset, nextOffset, size, truncated}} |
 *   {ok: false, error: {key: string}}}
 */
export function interpretPreviewPage(data, ctx = {}) {
  const offset = nonNegative(ctx.offset, 0);
  const contentChunk = str(data?.content);
  const pageOffset = nonNegative(data?.offset, offset);
  const truncated = data?.eof === undefined ? Boolean(data?.truncated) : !data.eof;
  // Legacy final pages may use the server's byte size, never UTF-16 text length.
  const nextOffset = nonNegative(data?.next_offset ?? (truncated ? undefined : data?.size), pageOffset);
  if (truncated && nextOffset <= pageOffset) {
    return { ok: false, error: { key: 'workspace.preview.missingCursor' } };
  }
  const size = nonNegative(data?.size, nextOffset);
  const previous = ctx.previous;
  const previousContent = ctx.append && previous?.path === ctx.path ? str(previous.content) : '';
  return {
    ok: true,
    preview: { path: str(ctx.path), content: previousContent + contentChunk, offset: pageOffset, nextOffset, size, truncated }
  };
}

/** Error slot for a failed preview load (server message raw, else keyed fallback). */
export function previewLoadError(err, path) {
  if (err?.key) return { key: String(err.key), params: err.params || {} };
  if (err?.message) return { raw: String(err.message) };
  return { key: 'workspace.preview.loadFailed', params: { path: normalizeWorkspacePath(path) } };
}

/** Request parameters for the next preview page, or null at EOF. */
export function nextPreviewRequest(preview, limit = WORKSPACE_FILE_PREVIEW_CHUNK_SIZE) {
  if (!preview || !preview.truncated) return null;
  return { path: preview.path, offset: preview.nextOffset, limit };
}

/** Footer progress (bytes) for a truncated preview, or null when complete. */
export function previewProgress(preview) {
  if (!preview || !preview.truncated) return null;
  const shown = Math.min(preview.nextOffset, preview.size);
  return {
    shownBytes: shown,
    totalBytes: preview.size,
    label: { key: 'workspace.preview.progress', params: { shown: formatBytes(shown) || '', total: formatBytes(preview.size) || '' } }
  };
}

/** Human byte size ("12 B", "1.5 KiB", "3.0 MiB", "1.2 GiB"); null when invalid. */
export function formatBytes(bytes) {
  const value = Number(bytes);
  if (bytes === null || bytes === undefined || bytes === '' || !Number.isFinite(value) || value < 0) return null;
  if (value < 1024) return value + ' B';
  if (value < 1024 * 1024) return (value / 1024).toFixed(1) + ' KiB';
  if (value < 1024 * 1024 * 1024) return (value / (1024 * 1024)).toFixed(1) + ' MiB';
  return (value / (1024 * 1024 * 1024)).toFixed(1) + ' GiB';
}

/** formatBytes or the workspace.sizeUnknown slot. */
export function bytesLabel(bytes) {
  const text = formatBytes(bytes);
  return text === null ? { key: 'workspace.sizeUnknown' } : { raw: text };
}

/** New selection Set with path added/removed (root '' is never selectable). */
export function toggleSelection(selection, path, checked) {
  const next = new Set(selection instanceof Set ? selection : []);
  const normalized = normalizeWorkspacePath(path);
  if (!normalized) return next;
  if (checked) next.add(normalized);
  else next.delete(normalized);
  return next;
}

/** Sorted selected paths. */
export function sortedSelection(selection) {
  return Array.from(selection instanceof Set ? selection : []).sort((left, right) => left.localeCompare(right));
}

/** Membership test with normalization. */
export function isPathSelected(selection, path) {
  return selection instanceof Set && selection.has(normalizeWorkspacePath(path));
}

/** Keep only selected paths that still exist in the listed tree entries. */
export function pruneSelection(selection, entries) {
  const visible = new Set((Array.isArray(entries) ? entries : []).map((entry) => normalizeWorkspacePath(entry?.path)));
  return new Set(sortedSelection(selection).filter((path) => visible.has(path)));
}
