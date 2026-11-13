// Key coverage for the Workspace and Settings pages: every i18n key emitted by
// lib/workspace-model.js and lib/settings-model.js (literals, header-declared
// keys and dynamic families) and every literal key used by the two feature
// directories exists in both locales.
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import test from 'node:test';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';
import workspaceDict from '../../internal/webconsole/ui/src/i18n/workspace.js';
import settingsDict from '../../internal/webconsole/ui/src/i18n/settings.js';
import * as S from '../../internal/webconsole/ui/src/lib/settings-model.js';

const UI = new URL('../../internal/webconsole/ui/src/', import.meta.url);
const LOCALES = ['zh-CN', 'en'];
const read = (rel) => readFileSync(new URL(rel, UI), 'utf8');

const FAMILIES = {
  'settings.reasoning.': [...new Set([
    ...['openai-compatible', 'anthropic-compatible', 'google', ''].flatMap((provider) => [...S.reasoningModesFor(provider), ...S.reasoningSummaryModesFor(provider)]),
    ...S.ROLE_REASONING_EFFORTS.map((value) => value || 'inherit'),
  ])],
  'settings.apiProvider.': [...S.API_PROVIDERS, 'inherit'],
  'settings.role.': [...S.ROLE_PROVIDER_ROLES],
  'settings.roleDescription.': [...S.ROLE_PROVIDER_ROLES],
  'settings.guardrails.': [...S.GUARDRAILS_MODES],
};

function missing(keys) {
  const out = [];
  for (const key of keys) {
    for (const locale of LOCALES) {
      if (!Object.prototype.hasOwnProperty.call(dictionaries[locale], key)) out.push(locale + ' ' + key);
    }
  }
  return out;
}

function literalKeys(source, prefixes) {
  const pattern = new RegExp("['\"\`]((?:" + prefixes.join('|') + ")\\.[A-Za-z][\\w.-]*)['\"\`]", 'g');
  return [...new Set([...source.matchAll(pattern)].map((match) => match[1]))];
}

/** Keys declared in a lib module header ("i18n keys emitted:"), expanding "a.b.c | .d" shorthands. */
function headerKeys(source) {
  const header = (source.match(/^\/\*\*[\s\S]*?\*\//) || [''])[0];
  const section = header.split('i18n keys emitted:')[1] || '';
  const keys = new Set();
  const stems = new Set();
  for (const line of section.split('\n')) {
    let last = '';
    for (const token of line.replace(/^\s*\*\s?/, '').split('|')) {
      const trimmed = token.trim();
      const full = /^((?:settings|workspace)\.[A-Za-z][\w.-]*?)(?:\.<\w+>)?(?:\s|$)/.exec(trimmed);
      if (full) {
        if (/\.<\w+>/.test(trimmed.split(/\s/)[0])) stems.add(full[1] + '.');
        else { keys.add(full[1]); last = full[1]; }
        continue;
      }
      const short = /^\.([A-Za-z][\w-]*)(?:\s|$)/.exec(trimmed);
      if (short && last) {
        const key = last.slice(0, last.lastIndexOf('.') + 1) + short[1];
        keys.add(key);
        last = key;
      }
    }
  }
  return { keys: [...keys], stems: [...stems] };
}

test('area dictionaries cover every literal key emitted by the lib models', () => {
  const ws = literalKeys(read('lib/workspace-model.js'), ['workspace']);
  const st = literalKeys(read('lib/settings-model.js'), ['settings']);
  const families = [...ws, ...st].filter((key) => key.endsWith('.'));
  for (const family of families) assert.ok(FAMILIES[family], 'dynamic family ' + family + ' needs an explicit member list');
  const keys = [...ws, ...st].filter((key) => !key.endsWith('.'));
  assert.ok(ws.length >= 5 && keys.length >= 30, 'scan found the lib keys');
  assert.deepEqual(missing(keys), []);
});

test('area dictionaries cover every key declared in the lib headers', () => {
  for (const file of ['lib/workspace-model.js', 'lib/settings-model.js']) {
    const { keys, stems } = headerKeys(read(file));
    assert.ok(keys.length >= 5, file + ' header parsed');
    assert.deepEqual(missing(keys), [], file);
    for (const stem of stems) assert.ok(FAMILIES[stem], file + ': header family ' + stem + ' is covered');
  }
});

test('dynamic key families exist in both locales', () => {
  const keys = Object.entries(FAMILIES).flatMap(([prefix, members]) => members.map((member) => prefix + member));
  assert.deepEqual(missing(keys), []);
});

test('every literal key used by the workspace and settings features exists', () => {
  for (const area of ['workspace', 'settings']) {
    const dir = new URL('features/' + area + '/', UI);
    for (const name of readdirSync(dir).filter((file) => file.endsWith('.js'))) {
      const source = readFileSync(new URL(name, dir), 'utf8');
      const keys = literalKeys(source, ['workspace', 'settings', 'common', 'shell']).filter((key) => !key.endsWith('.'));
      assert.deepEqual(missing(keys), [], area + '/' + name);
      for (const family of literalKeys(source, ['workspace', 'settings']).filter((key) => key.endsWith('.'))) {
        assert.ok(FAMILIES[family], area + '/' + name + ': dynamic family ' + family + ' is covered');
      }
    }
  }
});

test('page dictionaries are complete, prefixed and free of the removed legacy toggle', () => {
  for (const [area, dict] of [['workspace', workspaceDict], ['settings', settingsDict]]) {
    const zh = Object.keys(dict['zh-CN']).sort();
    assert.deepEqual(Object.keys(dict.en).sort(), zh, area + ' locales match');
    assert.ok(zh.every((key) => key.startsWith(area + '.')), area + ' prefix');
    assert.ok(zh.length > 40, area + ' has copy');
    assert.ok(!zh.some((key) => /legacy/i.test(key)), area + ' has no legacy keys');
  }
});
