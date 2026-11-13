// Pure helpers for the Workspace page (no DOM, no fetch): entry partitioning,
// name validation, write-scope rules, keyboard focus movement, path
// preference persistence and template splitting. Tested by
// validation/scripts/ui_workspace_view_helpers_test.mjs.
//
// i18n keys returned (rendered by WorkspaceView via t()):
//   workspace.name.required | workspace.name.reserved | workspace.name.separator |
//   workspace.name.unchanged
import { normalizeWorkspacePath, workspaceBreadcrumbs, restoreWorkspacePathPreferences } from '../../lib/workspace-model.js';

/** localStorage key of the browsed-path preference buckets (UI preference only). */
export const WORKSPACE_PATHS_STORAGE_KEY = 'aegis-agent.workspace-paths.v1';

/**
 * True when a relative path points outside the server's workspace/ root
 * (the browser may show the parent cwd, but every mutation is rejected there).
 */
export function isOutsideWorkspaceRoot(path) {
  const normalized = normalizeWorkspacePath(path);
  return normalized === '..' || normalized.startsWith('../');
}

/** Mutations (upload, mkdir, rename, delete) are allowed for this directory. */
export function canWriteDirectory(path) {
  return !isOutsideWorkspaceRoot(path);
}

/** A listed entry can be renamed/deleted/selected (inside the root, not the root, not "..") */
export function isMutableEntry(entry) {
  if (!entry || entry.navigation === 'parent') return false;
  const path = normalizeWorkspacePath(entry.path);
  return Boolean(path) && !isOutsideWorkspaceRoot(path) && (entry.type === 'file' || entry.type === 'directory');
}

/**
 * Split a GET /api/files payload into the parent navigation entry and the
 * listed children (folders first, then case-insensitive name order; the
 * server already sorts this way, the stable sort only guards older servers).
 * @returns {{parent: object|null, entries: object[]}}
 */
export function partitionEntries(tree) {
  const list = Array.isArray(tree) ? tree.filter((entry) => entry && typeof entry === 'object') : [];
  const parent = list.find((entry) => entry.navigation === 'parent') || null;
  const entries = list
    .filter((entry) => entry.navigation !== 'parent')
    .map((entry, index) => ({ entry, index }))
    .sort((a, b) => {
      const dirA = a.entry.type === 'directory' ? 0 : 1;
      const dirB = b.entry.type === 'directory' ? 0 : 1;
      if (dirA !== dirB) return dirA - dirB;
      const nameA = String(a.entry.name || '').toLowerCase();
      const nameB = String(b.entry.name || '').toLowerCase();
      if (nameA !== nameB) return nameA < nameB ? -1 : 1;
      return a.index - b.index;
    })
    .map(({ entry }) => ({ ...entry, path: normalizeWorkspacePath(entry.path) }));
  return { parent: parent ? { ...parent, path: normalizeWorkspacePath(parent.path) } : null, entries };
}

/** Optional per-entry metadata when the server provides it ({size, modified}). */
export function entryMeta(entry) {
  const size = Number(entry?.size);
  const modified = entry?.modified_at || entry?.mod_time || entry?.modified || '';
  return {
    size: entry?.type === 'file' && entry?.size !== undefined && entry?.size !== null && Number.isFinite(size) && size >= 0 ? size : null,
    modified: modified ? String(modified) : '',
  };
}

/** Whether any entry carries size/modified metadata (columns are shown only then). */
export function listHasMeta(entries) {
  return (Array.isArray(entries) ? entries : []).some((entry) => {
    const meta = entryMeta(entry);
    return meta.size !== null || Boolean(meta.modified);
  });
}

/**
 * Validate a new file/folder name. Mirrors the server rules that can be
 * checked locally (the server stays authoritative for sensitive names).
 * @param {string} value
 * @param {{current?: string}} [options] current name for renames
 * @returns {string} '' when valid, otherwise an i18n key
 */
export function validateEntryName(value, { current = '' } = {}) {
  const name = String(value ?? '').trim();
  if (!name) return 'workspace.name.required';
  if (name === '.' || name === '..') return 'workspace.name.reserved';
  if (/[\\/]/.test(name)) return 'workspace.name.separator';
  if (current && name === current) return 'workspace.name.unchanged';
  return '';
}

/** Last path segment ('' for the root). */
export function entryName(path) {
  const normalized = normalizeWorkspacePath(path);
  const index = normalized.lastIndexOf('/');
  return index >= 0 ? normalized.slice(index + 1) : normalized;
}

/**
 * Breadcrumb model. Inside the root: [root, ...segments]. Outside (paths
 * under ".."): the leading ".." crumb is labelled with the parent directory
 * name of the workspace root when known.
 * @returns {Array<{path: string, label: string, kind: 'root'|'parent'|'segment'}>}
 */
export function breadcrumbModel(path, workspaceRoot = '') {
  const normalized = normalizeWorkspacePath(path);
  const crumbs = workspaceBreadcrumbs(normalized);
  if (isOutsideWorkspaceRoot(normalized)) {
    const rootParts = String(workspaceRoot || '').replace(/\\/g, '/').replace(/\/+$/, '').split('/').filter(Boolean);
    const parentName = rootParts.length >= 2 ? rootParts[rootParts.length - 2] : '';
    return crumbs.map((crumb, index) => (index === 0
      ? { path: crumb.path, label: parentName, kind: 'parent' }
      : { path: crumb.path, label: crumb.name, kind: 'segment' }));
  }
  return [{ path: '', label: '', kind: 'root' }, ...crumbs.map((crumb) => ({ path: crumb.path, label: crumb.name, kind: 'segment' }))];
}

/**
 * Roving focus movement for the file list.
 * @returns {number} next index (clamped) or -1 when the key is not a move key
 */
export function nextFocusIndex(current, key, count) {
  if (count <= 0) return -1;
  const index = Number.isInteger(current) && current >= 0 ? Math.min(current, count - 1) : 0;
  switch (key) {
    case 'ArrowDown':
      return Math.min(index + 1, count - 1);
    case 'ArrowUp':
      return Math.max(index - 1, 0);
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
    case 'PageDown':
      return Math.min(index + 10, count - 1);
    case 'PageUp':
      return Math.max(index - 10, 0);
    default:
      return -1;
  }
}

/** Select-all checkbox state for the selection bar. */
export function selectAllState(selection, selectablePaths) {
  const paths = Array.isArray(selectablePaths) ? selectablePaths : [];
  const size = selection instanceof Set ? paths.filter((path) => selection.has(path)).length : 0;
  return { checked: paths.length > 0 && size === paths.length, indeterminate: size > 0 && size < paths.length, count: size };
}

/** Read persisted bucket preferences from a Storage-like object. */
export function loadPathPreferences(storage) {
  try {
    const raw = storage?.getItem?.(WORKSPACE_PATHS_STORAGE_KEY);
    return restoreWorkspacePathPreferences(raw ? JSON.parse(raw) : {});
  } catch {
    return {};
  }
}

/** Persist bucket preferences (best effort). */
export function savePathPreferences(storage, preferences) {
  try {
    storage?.setItem?.(WORKSPACE_PATHS_STORAGE_KEY, JSON.stringify(restoreWorkspacePathPreferences(preferences)));
    return true;
  } catch {
    return false;
  }
}

/**
 * Split a translated template into text and {slot} parts so raw values can
 * be rendered as translate="no" nodes instead of being interpolated by t().
 * @returns {Array<{text: string}|{slot: string}>}
 */
export function splitTemplate(template) {
  const parts = [];
  const text = String(template ?? '');
  const pattern = /\{(\w+)\}/g;
  let last = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > last) parts.push({ text: text.slice(last, match.index) });
    parts.push({ slot: match[1] });
    last = match.index + match[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return parts;
}

/** Status of a GET /api/file/read failure: 'unsupported' (415) | 'error'. */
export function previewFailureKind(error) {
  return Number(error?.status || 0) === 415 ? 'unsupported' : 'error';
}

/**
 * Absolute path of a browsed directory for display/copy. Paths under ".."
 * resolve against the parent of the workspace root (the server cwd).
 */
export function absoluteWorkspacePath(workspaceRoot, path) {
  const base = String(workspaceRoot || '').replace(/\\/g, '/').replace(/\/+$/, '');
  if (!base) return '';
  const rel = normalizeWorkspacePath(path);
  if (!rel) return base;
  if (isOutsideWorkspaceRoot(rel)) {
    const cut = base.lastIndexOf('/');
    const parent = cut > 0 ? base.slice(0, cut) : '/';
    const rest = rel === '..' ? '' : rel.slice(3);
    return rest ? parent.replace(/\/+$/, '') + '/' + rest : parent;
  }
  return base + '/' + rel;
}
