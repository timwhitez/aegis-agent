import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import * as W from '../../internal/webconsole/ui/src/lib/workspace-model.js';

const ROOT = '/tmp/workspace';

function syncTo(state, sessionId, workdir, options = {}) {
  return W.syncWorkspaceToSession(state, { workspaceRoot: ROOT, sessionId, sessionWorkdir: workdir, ...options }).state;
}

test('path normalization helpers', () => {
  assert.equal(W.normalizeWorkspacePath('./a\\b/c/'), 'a/b/c');
  assert.equal(W.normalizeWorkspacePath('/a/b'), 'a/b');
  assert.equal(W.normalizeWorkspacePath('.'), '');
  assert.equal(W.normalizeWorkspacePath(null), '');
  assert.equal(W.parentWorkspacePath('a/b/c'), 'a/b');
  assert.equal(W.parentWorkspacePath('a'), '');
  assert.equal(W.parentWorkspacePath(''), '');
  assert.equal(W.joinWorkspacePath('a/', '/b.txt'), 'a/b.txt');
  assert.equal(W.joinWorkspacePath('', 'b'), 'b');
  assert.deepEqual(W.workspaceBreadcrumbs('a/b'), [{ name: 'a', path: 'a' }, { name: 'b', path: 'a/b' }]);
  assert.equal(W.normalizeAbsoluteWorkspacePath('C:\\work\\'), 'C:/work');
  assert.equal(W.sessionWorkspacePath(ROOT, '/tmp/workspace/'), '');
  assert.equal(W.sessionWorkspacePath(ROOT, '/tmp/workspace/session-root'), 'session-root');
  assert.equal(W.sessionWorkspacePath(ROOT, '/tmp/workspace-other/x'), null);
  assert.equal(W.sessionWorkspacePath(ROOT, ''), null);
  assert.equal(W.selectedWorkspaceWorkdir(ROOT + '/', 'a/b'), '/tmp/workspace/a/b');
  assert.equal(W.selectedWorkspaceWorkdir(ROOT, ''), ROOT);
  assert.equal(W.selectedWorkspaceWorkdir('', 'a'), '');
  assert.equal(W.workspaceBucketKey(ROOT, ''), JSON.stringify([ROOT, 'new-session']));
  assert.equal(W.workspaceBucketKey('', 's'), '');
  assert.equal(W.workspaceSessionId('0xABCDEF', true), '');
  assert.equal(W.workspaceSessionId('session_a', false), '');
  assert.equal(W.workspaceSessionId('session_a', true), 'session_a');
  assert.equal(W.sessionWorkdirFromDetail('s', { metadata: { id: 'other', workdir: '/x' } }), '');
  assert.equal(W.sessionWorkdirFromDetail('s', { metadata: { id: 's', workdir: ' /x ' } }), '/x');
});

// Ported: "Workspace fetch preserves a manually browsed path across view changes".
test('a manually browsed path survives refreshes of the same session', () => {
  let state = syncTo(W.createWorkspaceState(), 'session_workspace_keep', '/tmp/workspace/session-root');
  assert.equal(state.path, 'session-root');
  state = W.setWorkspacePath(state, 'session-root/deep');
  const refreshed = W.syncWorkspaceToSession(state, { workspaceRoot: ROOT, sessionId: 'session_workspace_keep', sessionWorkdir: '/tmp/workspace/session-root' });
  assert.equal(refreshed.changed, false);
  assert.equal(refreshed.state.path, 'session-root/deep');
  assert.equal(W.sessionWorkspacePath(ROOT, '/tmp/workspace/session-root'), 'session-root');
  const forced = W.syncWorkspaceToSession(state, { workspaceRoot: ROOT, sessionId: 'session_workspace_keep', sessionWorkdir: '/tmp/workspace/session-root', force: true });
  assert.equal(forced.state.path, 'session-root');
  assert.equal(forced.changed, true);
});

// Ported: "Workspace path preferences are isolated per session and new-session composer".
test('path preferences are isolated per session and the new-session composer', () => {
  let state = W.createWorkspaceState();
  state = syncTo(W.resetWorkspaceSessionSync(state), 'session_workspace_a', '/tmp/workspace/a');
  state = W.setWorkspacePath(state, 'a/deep');

  state = syncTo(W.resetWorkspaceSessionSync(state), 'session_workspace_b', '/tmp/workspace/b');
  const firstBPath = state.path;
  state = W.setWorkspacePath(state, 'b/review');

  state = syncTo(W.resetWorkspaceSessionSync(state), 'session_workspace_a', '/tmp/workspace/a');
  const restoredAPath = state.path;
  state = syncTo(state, 'session_workspace_a', '/tmp/workspace/a');
  const stableAPath = state.path;
  state = syncTo(state, 'session_workspace_a', '/tmp/workspace/a-next');
  const changedWorkdirPath = state.path;

  state = syncTo(W.resetWorkspaceSessionSync(state), '', '');
  const initialComposerPath = state.path;
  state = W.setWorkspacePath(state, 'drafts/new-session');

  state = syncTo(W.resetWorkspaceSessionSync(state), 'session_workspace_b', '/tmp/workspace/b');
  const restoredBPath = state.path;

  state = syncTo(W.resetWorkspaceSessionSync(state), '', '');
  assert.deepEqual({ firstBPath, restoredAPath, stableAPath, changedWorkdirPath, initialComposerPath, restoredBPath, restoredComposerPath: state.path }, {
    firstBPath: 'b',
    restoredAPath: 'a/deep',
    stableAPath: 'a/deep',
    changedWorkdirPath: 'a-next',
    initialComposerPath: '',
    restoredBPath: 'b/review',
    restoredComposerPath: 'drafts/new-session'
  });
});

// Ported: "Workspace fetch preserves the new-session composer path without an active session".
test('the new-session composer path is preserved without an active session', () => {
  let state = syncTo(W.createWorkspaceState(), '', '');
  state = W.setWorkspacePath(state, 'drafts/composer');
  const again = W.syncWorkspaceToSession(state, { workspaceRoot: ROOT, sessionId: '', sessionWorkdir: '/ignored' });
  assert.equal(again.changed, false);
  assert.equal(again.state.path, 'drafts/composer');
});

// Ported: "Workspace path preference survives browser UI state restore".
test('path preferences survive a browser reload, even before the detail arrives', () => {
  let first = syncTo(W.createWorkspaceState(), 'session_workspace_restore', '/tmp/workspace/session-home');
  first = W.setWorkspacePath(first, 'session-home/restored');
  const persisted = JSON.parse(JSON.stringify(W.workspacePathPreferences(first)));

  const restored = syncTo(W.createWorkspaceState({ pathByBucket: persisted }), 'session_workspace_restore', '/tmp/workspace/session-home');
  assert.equal(restored.path, 'session-home/restored');

  let early = syncTo(W.createWorkspaceState({ pathByBucket: persisted }), 'session_workspace_restore', '');
  assert.equal(early.path, 'session-home/restored');
  assert.equal(early.pendingInitialWorkdirBucket !== '', true);
  const arrived = W.syncWorkspaceToSession(early, { workspaceRoot: ROOT, sessionId: 'session_workspace_restore', sessionWorkdir: '/tmp/workspace/session-home' });
  assert.equal(arrived.changed, false, 'late detail records the workdir without overriding the restored path');
  assert.equal(arrived.state.path, 'session-home/restored');
  early = arrived.state;
  const moved = W.syncWorkspaceToSession(early, { workspaceRoot: ROOT, sessionId: 'session_workspace_restore', sessionWorkdir: '/tmp/workspace/elsewhere' });
  assert.equal(moved.changed, true, 'a real workdir change still syncs');
  assert.equal(moved.state.path, 'elsewhere');
});

test('restored preferences keep the last 100 string buckets', () => {
  const prefs = {};
  for (let i = 0; i < 120; i += 1) prefs['b' + i] = './p' + i + '/';
  prefs.bad = 42;
  const restored = W.restoreWorkspacePathPreferences(prefs);
  assert.equal(Object.keys(restored).length, 99);
  assert.equal(restored.b119, 'p119');
  assert.equal(restored.b0, undefined);
  assert.deepEqual(W.restoreWorkspacePathPreferences([1, 2]), {});
});

// Ported: "Workspace invalid persisted path falls back to the nearest accessible parent once".
test('an invalid persisted path falls back to the nearest accessible parent once', async () => {
  const requested = [];
  const result = await W.loadDirectoryWithFallback('gone/deep', async (path) => {
    requested.push(path);
    if (path === 'gone/deep') throw { status: 404, message: 'missing deep path' };
    return [];
  });
  assert.deepEqual(requested, ['gone/deep', 'gone']);
  assert.equal(result.path, 'gone');
  assert.deepEqual(result.notice, { key: 'workspace.pathFallback', params: { requested: 'gone/deep', shown: 'gone' }, tone: 'info' });
  const ok = await W.loadDirectoryWithFallback('here', async () => ['x']);
  assert.equal(ok.notice, null);
  const rooted = await W.loadDirectoryWithFallback('a/b', async (path) => { if (path !== '.') throw { status: 400 }; return []; });
  assert.deepEqual(rooted.notice, { key: 'workspace.pathFallbackRoot', params: { requested: 'a/b' }, tone: 'info' });
  await assert.rejects(W.loadDirectoryWithFallback('a/b', async () => { throw { status: 500, message: 'boom' }; }), { status: 500 });
  await assert.rejects(W.loadDirectoryWithFallback('', async () => { throw { status: 404 }; }), { status: 404 });
  assert.deepEqual(W.directoryFallbackCandidates('a/b/c'), ['a/b/c', 'a/b', 'a', '']);
});

// Ported: loadFile paging, stale responses and Unicode byte cursors.
test('paged preview appends by server byte cursor and drops stale load-more pages', () => {
  const seq = W.createRequestSequence();
  const firstSeq = seq.next();
  const first = W.interpretPreviewPage({ content: 'chunk-one\n', offset: 0, limit: 262144, size: 20, truncated: true, next_offset: 10 }, { path: 'large.txt', offset: 0 });
  assert.equal(seq.isCurrent(firstSeq), true);
  assert.deepEqual(first, { ok: true, preview: { path: 'large.txt', content: 'chunk-one\n', offset: 0, nextOffset: 10, size: 20, truncated: true } });
  assert.deepEqual(W.nextPreviewRequest(first.preview), { path: 'large.txt', offset: 10, limit: W.WORKSPACE_FILE_PREVIEW_CHUNK_SIZE });
  assert.equal(W.WORKSPACE_FILE_PREVIEW_CHUNK_SIZE, 262144);
  assert.deepEqual(W.previewProgress(first.preview).label, { key: 'workspace.preview.progress', params: { shown: '10 B', total: '20 B' } });

  const moreSeq = seq.next();
  const otherSeq = seq.next();
  const other = W.interpretPreviewPage({ content: 'other body', size: 10 }, { path: 'other.txt' });
  assert.equal(seq.isCurrent(otherSeq), true);
  assert.deepEqual(other.preview, { path: 'other.txt', content: 'other body', offset: 0, nextOffset: 10, size: 10, truncated: false });
  assert.equal(seq.isCurrent(moreSeq), false, 'the load-more response for large.txt must be dropped');
  assert.equal(W.nextPreviewRequest(other.preview), null);
  assert.equal(W.previewProgress(other.preview), null);
});

test('Unicode preview uses server byte cursors including final and legacy pages', () => {
  const first = W.interpretPreviewPage({ content: 'A中', offset: 0, next_offset: 4, size: 8, truncated: true, eof: false }, { path: 'unicode.txt' });
  assert.equal(W.nextPreviewRequest(first.preview).offset, 4);
  const second = W.interpretPreviewPage({ content: '🙂', offset: 4, next_offset: 8, size: 8, truncated: false, eof: true }, { path: 'unicode.txt', offset: 4, previous: first.preview, append: true });
  assert.deepEqual(second.preview, { path: 'unicode.txt', content: 'A中🙂', offset: 4, nextOffset: 8, size: 8, truncated: false });
  assert.notEqual('A中🙂'.length, 8, 'UTF-16 length differs from the byte cursor');
  const legacy = W.interpretPreviewPage({ content: '中🙂', size: 7, truncated: false }, { path: 'legacy.txt' });
  assert.equal(legacy.preview.nextOffset, 7);
});

test('legacy truncated preview without a byte cursor fails explicitly', () => {
  assert.deepEqual(W.interpretPreviewPage({ content: '中', size: 7, truncated: true }, { path: 'legacy.txt' }), { ok: false, error: { key: 'workspace.preview.missingCursor' } });
  assert.equal(W.interpretPreviewPage({ content: 'x', offset: 5, next_offset: 5, eof: false }, { path: 'a', offset: 5 }).ok, false);
});

test('preview load errors keep server text raw and fall back to a keyed message', () => {
  assert.deepEqual(W.previewLoadError({ message: 'unsupported text encoding' }, 'a.bin'), { raw: 'unsupported text encoding' });
  assert.deepEqual(W.previewLoadError({ key: 'workspace.preview.missingCursor' }, 'a'), { key: 'workspace.preview.missingCursor', params: {} });
  assert.deepEqual(W.previewLoadError(null, './dir/a.txt'), { key: 'workspace.preview.loadFailed', params: { path: 'dir/a.txt' } });
});

test('appending never mixes content from another file', () => {
  const previous = { path: 'a.txt', content: 'AAA', offset: 0, nextOffset: 3, size: 6, truncated: true };
  const page = W.interpretPreviewPage({ content: 'BBB', offset: 3, next_offset: 6, eof: true }, { path: 'b.txt', offset: 3, previous, append: true });
  assert.equal(page.preview.content, 'BBB');
});

test('request sequence lets the latest navigation win', () => {
  const seq = W.createRequestSequence();
  const fileSeq = seq.next();
  const dirSeq = seq.next();
  assert.equal(seq.isCurrent(fileSeq), false);
  assert.equal(seq.isCurrent(dirSeq), true);
  assert.equal(seq.current, 2);
});

test('byte formatting', () => {
  assert.equal(W.formatBytes(0), '0 B');
  assert.equal(W.formatBytes(1023), '1023 B');
  assert.equal(W.formatBytes(1536), '1.5 KiB');
  assert.equal(W.formatBytes(3 * 1024 * 1024), '3.0 MiB');
  assert.equal(W.formatBytes(5 * 1024 * 1024 * 1024), '5.0 GiB');
  assert.equal(W.formatBytes(-1), null);
  assert.equal(W.formatBytes('x'), null);
  assert.equal(W.formatBytes(undefined), null);
  assert.deepEqual(W.bytesLabel(null), { key: 'workspace.sizeUnknown' });
  assert.deepEqual(W.bytesLabel(12), { raw: '12 B' });
});

test('selection helpers normalize, sort and prune', () => {
  let selection = W.toggleSelection(new Set(), './b/', true);
  selection = W.toggleSelection(selection, 'a', true);
  selection = W.toggleSelection(selection, '', true);
  assert.deepEqual(W.sortedSelection(selection), ['a', 'b']);
  assert.equal(W.isPathSelected(selection, '/a'), true);
  const removed = W.toggleSelection(selection, 'a', false);
  assert.deepEqual(W.sortedSelection(removed), ['b']);
  assert.deepEqual(W.sortedSelection(selection), ['a', 'b'], 'inputs are not mutated');
  assert.deepEqual(W.sortedSelection(W.pruneSelection(selection, [{ path: 'a' }])), ['a']);
  assert.deepEqual(W.sortedSelection(W.toggleSelection(null, 'x', true)), ['x']);
});

test('module is pure', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/lib/workspace-model.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage)\b/);
});
