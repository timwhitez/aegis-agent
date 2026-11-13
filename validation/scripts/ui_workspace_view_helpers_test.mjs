import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import * as H from '../../internal/webconsole/ui/src/features/workspace/workspace-helpers.js';

test('write scope: paths under ".." are read-only, the root itself is not mutable', () => {
  assert.equal(H.isOutsideWorkspaceRoot('..'), true);
  assert.equal(H.isOutsideWorkspaceRoot('../notes.txt'), true);
  assert.equal(H.isOutsideWorkspaceRoot('..data/x'), false);
  assert.equal(H.isOutsideWorkspaceRoot(''), false);
  assert.equal(H.canWriteDirectory('docs'), true);
  assert.equal(H.canWriteDirectory('../src'), false);
  assert.equal(H.isMutableEntry({ path: 'a.txt', type: 'file' }), true);
  assert.equal(H.isMutableEntry({ path: 'dir', type: 'directory' }), true);
  assert.equal(H.isMutableEntry({ path: '', type: 'directory' }), false);
  assert.equal(H.isMutableEntry({ path: '..', type: 'directory', navigation: 'parent' }), false);
  assert.equal(H.isMutableEntry({ path: '../x.txt', type: 'file' }), false);
  assert.equal(H.isMutableEntry({ path: 'link', type: 'symlink' }), false);
});

test('entries: parent split out, folders first, case-insensitive stable order', () => {
  const { parent, entries } = H.partitionEntries([
    { name: 'b.txt', path: 'b.txt', type: 'file' },
    { name: '..', path: '..', type: 'directory', navigation: 'parent' },
    { name: 'Zeta', path: 'Zeta', type: 'directory' },
    { name: 'A.txt', path: './A.txt', type: 'file' },
    { name: 'alpha', path: 'alpha/', type: 'directory' },
    null,
  ]);
  assert.deepEqual(parent, { name: '..', path: '..', type: 'directory', navigation: 'parent' });
  assert.deepEqual(entries.map((entry) => entry.path), ['alpha', 'Zeta', 'A.txt', 'b.txt']);
  assert.deepEqual(H.partitionEntries(null), { parent: null, entries: [] });
});

test('optional size/modified metadata is only shown when the server sends it', () => {
  assert.deepEqual(H.entryMeta({ type: 'file', name: 'x' }), { size: null, modified: '' });
  assert.deepEqual(H.entryMeta({ type: 'file', size: 12, modified_at: '2026-11-13T00:00:00Z' }), { size: 12, modified: '2026-11-13T00:00:00Z' });
  assert.equal(H.entryMeta({ type: 'directory', size: 12 }).size, null);
  assert.equal(H.listHasMeta([{ type: 'file' }]), false);
  assert.equal(H.listHasMeta([{ type: 'file', size: 0 }]), true);
});

test('name validation mirrors the server rules that can be checked locally', () => {
  assert.equal(H.validateEntryName(''), 'workspace.name.required');
  assert.equal(H.validateEntryName('   '), 'workspace.name.required');
  assert.equal(H.validateEntryName('.'), 'workspace.name.reserved');
  assert.equal(H.validateEntryName('..'), 'workspace.name.reserved');
  assert.equal(H.validateEntryName('a/b'), 'workspace.name.separator');
  assert.equal(H.validateEntryName('a\\b'), 'workspace.name.separator');
  assert.equal(H.validateEntryName('same.txt', { current: 'same.txt' }), 'workspace.name.unchanged');
  assert.equal(H.validateEntryName(' report.md ', { current: 'same.txt' }), '');
  assert.equal(H.entryName('a/b/c.txt'), 'c.txt');
  assert.equal(H.entryName(''), '');
});

test('breadcrumbs: root crumb inside the workspace, parent-dir label outside', () => {
  assert.deepEqual(H.breadcrumbModel('', '/srv/proj/workspace'), [{ path: '', label: '', kind: 'root' }]);
  assert.deepEqual(H.breadcrumbModel('docs/api', '/srv/proj/workspace'), [
    { path: '', label: '', kind: 'root' },
    { path: 'docs', label: 'docs', kind: 'segment' },
    { path: 'docs/api', label: 'api', kind: 'segment' },
  ]);
  assert.deepEqual(H.breadcrumbModel('../src', '/srv/proj/workspace'), [
    { path: '..', label: 'proj', kind: 'parent' },
    { path: '../src', label: 'src', kind: 'segment' },
  ]);
  assert.equal(H.breadcrumbModel('..', '')[0].label, '');
});

test('absolute paths for copy/display resolve ".." against the server cwd', () => {
  assert.equal(H.absoluteWorkspacePath('/srv/proj/workspace/', ''), '/srv/proj/workspace');
  assert.equal(H.absoluteWorkspacePath('/srv/proj/workspace', 'docs/a'), '/srv/proj/workspace/docs/a');
  assert.equal(H.absoluteWorkspacePath('/srv/proj/workspace', '..'), '/srv/proj');
  assert.equal(H.absoluteWorkspacePath('/srv/proj/workspace', '../src/x'), '/srv/proj/src/x');
  assert.equal(H.absoluteWorkspacePath('C:\\work\\workspace', 'a'), 'C:/work/workspace/a');
  assert.equal(H.absoluteWorkspacePath('', 'a'), '');
});

test('roving focus keys clamp inside the list', () => {
  assert.equal(H.nextFocusIndex(0, 'ArrowDown', 3), 1);
  assert.equal(H.nextFocusIndex(2, 'ArrowDown', 3), 2);
  assert.equal(H.nextFocusIndex(0, 'ArrowUp', 3), 0);
  assert.equal(H.nextFocusIndex(1, 'Home', 3), 0);
  assert.equal(H.nextFocusIndex(0, 'End', 3), 2);
  assert.equal(H.nextFocusIndex(9, 'ArrowUp', 3), 1, 'stale index is clamped first');
  assert.equal(H.nextFocusIndex(0, 'PageDown', 25), 10);
  assert.equal(H.nextFocusIndex(0, 'Enter', 3), -1);
  assert.equal(H.nextFocusIndex(0, 'ArrowDown', 0), -1);
});

test('select-all state', () => {
  assert.deepEqual(H.selectAllState(new Set(), ['a', 'b']), { checked: false, indeterminate: false, count: 0 });
  assert.deepEqual(H.selectAllState(new Set(['a']), ['a', 'b']), { checked: false, indeterminate: true, count: 1 });
  assert.deepEqual(H.selectAllState(new Set(['a', 'b', 'gone']), ['a', 'b']), { checked: true, indeterminate: false, count: 2 });
  assert.deepEqual(H.selectAllState(new Set(), []), { checked: false, indeterminate: false, count: 0 });
});

test('path preferences persist through a Storage-like object and tolerate bad data', () => {
  const store = new Map();
  const storage = { getItem: (key) => store.get(key) ?? null, setItem: (key, value) => store.set(key, value) };
  assert.deepEqual(H.loadPathPreferences(storage), {});
  assert.equal(H.savePathPreferences(storage, { '["/w","new-session"]': './docs/', bad: 3 }), true);
  assert.deepEqual(JSON.parse(store.get(H.WORKSPACE_PATHS_STORAGE_KEY)), { '["/w","new-session"]': 'docs' });
  assert.deepEqual(H.loadPathPreferences(storage), { '["/w","new-session"]': 'docs' });
  store.set(H.WORKSPACE_PATHS_STORAGE_KEY, '{not json');
  assert.deepEqual(H.loadPathPreferences(storage), {});
  assert.equal(H.savePathPreferences({ setItem() { throw new Error('quota'); } }, {}), false);
  assert.deepEqual(H.loadPathPreferences(null), {});
});

test('templates split into text and raw slots', () => {
  assert.deepEqual(H.splitTemplate('将在 {path} 中创建。'), [{ text: '将在 ' }, { slot: 'path' }, { text: ' 中创建。' }]);
  assert.deepEqual(H.splitTemplate('{name} will be deleted'), [{ slot: 'name' }, { text: ' will be deleted' }]);
  assert.deepEqual(H.splitTemplate('plain'), [{ text: 'plain' }]);
  assert.deepEqual(H.splitTemplate(''), []);
});

test('415 previews are "unsupported", everything else is an error', () => {
  assert.equal(H.previewFailureKind({ status: 415 }), 'unsupported');
  assert.equal(H.previewFailureKind({ status: 400 }), 'error');
  assert.equal(H.previewFailureKind(null), 'error');
});

test('helpers module is pure', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/features/workspace/workspace-helpers.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage)\b/);
});
