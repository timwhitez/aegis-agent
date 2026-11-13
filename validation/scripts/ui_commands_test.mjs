import test from 'node:test';
import assert from 'node:assert/strict';
import { createCommandRegistry, fuzzyScore, isCommandEnabled, normalizeText, rankCommands, scoreCommand } from '../../internal/webconsole/ui/src/lib/commands.js';
import { isEditableTarget, matchShortcut, modKeyLabel } from '../../internal/webconsole/ui/src/lib/shortcuts.js';

test('fuzzyScore prefers contiguous, prefix and word-boundary matches', () => {
  assert.equal(fuzzyScore('', 'anything'), 0);
  assert.equal(fuzzyScore('xyz', 'New session'), null);
  assert.equal(fuzzyScore('a', ''), null);
  assert.ok(fuzzyScore('new', 'New session') > fuzzyScore('new', 'Renew things'));
  assert.ok(fuzzyScore('ses', 'Go to Sessions') > fuzzyScore('ses', 'Toggle sidebar settings'));
  assert.ok(fuzzyScore('gs', 'Go to Settings') !== null, 'subsequence match');
  assert.ok(fuzzyScore('gs', 'Go to Settings') > fuzzyScore('gs', 'background jobs list'));
  assert.ok(fuzzyScore('会话', '新建会话') !== null, 'CJK substring');
  assert.equal(normalizeText('  Ünïcode '), 'unicode');
});

test('rankCommands filters, orders by score and keeps registration order on ties', () => {
  const commands = [
    { id: 'a', title: 'New session', keywords: ['create'] },
    { id: 'b', title: 'Go to Sessions' },
    { id: 'c', title: 'Go to Settings' },
    { id: 'd', title: 'Toggle inspector', keywords: ['details'] },
  ];
  assert.deepEqual(rankCommands(commands, '').map((entry) => entry.command.id), ['a', 'b', 'c', 'd']);
  assert.deepEqual(rankCommands(commands, 'settings').map((entry) => entry.command.id), ['c']);
  assert.equal(rankCommands(commands, 'sessions')[0].command.id, 'b');
  assert.deepEqual(rankCommands(commands, 'sess').map((entry) => entry.command.id).slice(0, 2), ['a', 'b'], 'shorter word-boundary match first');
  assert.deepEqual(rankCommands(commands, 'details').map((entry) => entry.command.id), ['d'], 'keywords match');
  assert.equal(rankCommands(commands, 'go', { limit: 1 }).length, 1);
  assert.equal(scoreCommand({ title: 'x' }, ''), 0);
});

test('command registry registers, unregisters and checks enabled state', () => {
  const registry = createCommandRegistry();
  const off = registry.register([{ id: 'one', title: 'One', run() {} }, { id: 'two', title: 'Two', enabled: () => false, run() {} }]);
  assert.deepEqual(registry.list().map((command) => command.id), ['one', 'two']);
  assert.equal(isCommandEnabled(registry.get('one')), true);
  assert.equal(isCommandEnabled(registry.get('two')), false);
  off();
  assert.equal(registry.list().length, 0);
  assert.throws(() => registry.register({ title: 'no id' }));
});

const key = (init) => ({ key: '', metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, isComposing: false, keyCode: 0, ...init });

test('global shortcuts use ⌘ on macOS and Ctrl elsewhere', () => {
  assert.equal(matchShortcut(key({ key: 'k', metaKey: true }), { mac: true }), 'palette');
  assert.equal(matchShortcut(key({ key: 'k', ctrlKey: true }), { mac: true }), null);
  assert.equal(matchShortcut(key({ key: 'k', ctrlKey: true }), { mac: false }), 'palette');
  assert.equal(matchShortcut(key({ key: 'O', metaKey: true, shiftKey: true }), { mac: true }), 'newSession');
  assert.equal(matchShortcut(key({ key: 'b', ctrlKey: true }), { mac: false }), 'sidebar');
  assert.equal(matchShortcut(key({ key: '.', metaKey: true }), { mac: true }), 'inspector');
  assert.equal(matchShortcut(key({ key: 'k', metaKey: true }), { mac: true, inEditable: true }), 'palette', 'mod shortcuts work in inputs');
  assert.equal(modKeyLabel(true), '⌘');
  assert.equal(modKeyLabel(false), 'Ctrl');
});

test('"/" and "?" only fire outside text inputs and never during IME composition', () => {
  assert.equal(matchShortcut(key({ key: '/' }), { mac: true }), 'focusComposer');
  assert.equal(matchShortcut(key({ key: '?', shiftKey: true }), { mac: true }), 'help');
  assert.equal(matchShortcut(key({ key: '/' }), { mac: true, inEditable: true }), null);
  assert.equal(matchShortcut(key({ key: 'k', metaKey: true, isComposing: true }), { mac: true }), null);
  assert.equal(matchShortcut(key({ key: 'Process', keyCode: 229 }), { mac: true }), null);
  assert.equal(isEditableTarget({ tagName: 'TEXTAREA' }), true);
  assert.equal(isEditableTarget({ tagName: 'INPUT', type: 'search' }), true);
  assert.equal(isEditableTarget({ tagName: 'INPUT', type: 'checkbox' }), false);
  assert.equal(isEditableTarget({ tagName: 'DIV', isContentEditable: true }), true);
  assert.equal(isEditableTarget({ tagName: 'BUTTON' }), false);
});
