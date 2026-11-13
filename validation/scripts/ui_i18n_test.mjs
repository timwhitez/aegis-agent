import test from 'node:test';
import assert from 'node:assert/strict';
import { AREAS, dictionaries, mergeAreas } from '../../internal/webconsole/ui/src/i18n/index.js';
import { LOCALES, LOCALE_STORAGE_KEY, formatRelativeTime, getLocale, initLocale, interpolate, setLocale, subscribeLocale, t } from '../../internal/webconsole/ui/src/lib/i18n.js';
import { KNOWN_STATUSES } from '../../internal/webconsole/ui/src/lib/status.js';
import { SESSION_GROUPS } from '../../internal/webconsole/ui/src/lib/session-list.js';
import { INSPECTOR_TABS } from '../../internal/webconsole/ui/src/state/prefs.js';
import { collectSourceKeys, missingSourceKeys, placeholders } from './ui_i18n_keys.mjs';

test('every area file has identical zh-CN / en key sets, prefixed by its area', () => {
  for (const [area, dict] of Object.entries(AREAS)) {
    assert.deepEqual(Object.keys(dict).sort(), [...LOCALES].sort(), `${area} exports both locales`);
    const zh = Object.keys(dict['zh-CN']).sort();
    const en = Object.keys(dict.en).sort();
    assert.deepEqual(en, zh, `${area}: en and zh-CN keys differ`);
    for (const key of zh) {
      assert.ok(key.startsWith(`${area}.`), `${area}: key ${key} must start with "${area}."`);
      assert.equal(typeof dict['zh-CN'][key], 'string');
      assert.ok(dict['zh-CN'][key].trim() && dict.en[key].trim(), `${key} is empty`);
      assert.deepEqual(placeholders(dict.en[key]), placeholders(dict['zh-CN'][key]), `${key}: {placeholders} differ`);
    }
  }
});

test('merged dictionaries reject keys defined by two areas', () => {
  assert.throws(() => mergeAreas({ a: { 'zh-CN': { 'x.k': '1' }, en: { 'x.k': '1' } }, b: { 'zh-CN': { 'x.k': '2' }, en: {} } }), /duplicate i18n key x\.k/);
  assert.deepEqual(Object.keys(dictionaries['zh-CN']).sort(), Object.keys(dictionaries.en).sort());
});

test('every literal t() key used under ui/src exists in both locales', () => {
  const missing = missingSourceKeys(dictionaries);
  assert.deepEqual(missing, [], `missing keys:\n${missing.map((entry) => `${entry.locale} ${entry.key} (${entry.files.join(', ')})`).join('\n')}`);
  assert.ok(collectSourceKeys().literal.size > 50, 'scan found the literal keys');
});

test('dynamic t() keys are covered by explicit key families', () => {
  const families = {
    'shell.group.': SESSION_GROUPS,
    'shell.inspector.tab.': INSPECTOR_TABS,
    'shell.palette.group.': ['actions', 'navigation', 'session', 'composer', 'view', 'recent'],
    'common.status.': KNOWN_STATUSES,
    'shell.page.': ['sessions', 'skills', 'workspace', 'settings'].flatMap((page) => [`${page}.title`, `${page}.description`]),
  };
  for (const [prefix, suffixes] of Object.entries(families)) {
    for (const suffix of suffixes) {
      for (const locale of LOCALES) {
        assert.ok(Object.prototype.hasOwnProperty.call(dictionaries[locale], prefix + suffix), `${locale} missing ${prefix}${suffix}`);
      }
    }
  }
  const unknown = collectSourceKeys().dynamic.filter((entry) => !Object.keys(families).some((prefix) => entry.expression.startsWith(prefix)));
  assert.deepEqual(unknown, [], 'dynamic t() keys must belong to a covered family');
});

test('locale persistence, <html lang>, interpolation and fallbacks', () => {
  const store = new Map();
  globalThis.localStorage = { getItem: (key) => store.get(key) ?? null, setItem: (key, value) => store.set(key, String(value)) };
  globalThis.document = { documentElement: { lang: '' } };
  try {
    assert.equal(initLocale(), 'zh-CN', 'default locale');
    assert.equal(globalThis.document.documentElement.lang, 'zh-CN');
    const seen = [];
    const off = subscribeLocale((locale) => seen.push(locale));
    setLocale('en-US');
    assert.equal(getLocale(), 'en');
    assert.equal(store.get(LOCALE_STORAGE_KEY), 'en');
    assert.equal(globalThis.document.documentElement.lang, 'en');
    assert.deepEqual(seen, ['en']);
    assert.equal(t('shell.newSession'), 'New session');
    assert.equal(t('shell.session.missing', { id: 'abc' }), 'Session abc does not exist or was deleted.');
    assert.equal(t('no.such.key'), 'no.such.key');
    off();
    store.set(LOCALE_STORAGE_KEY, 'zh-CN');
    assert.equal(initLocale(), 'zh-CN');
    assert.equal(t('shell.newSession'), '新建会话');
    assert.equal(interpolate('{a} {b} {a}', { a: 1 }), '1 {b} 1');
  } finally {
    setLocale('zh-CN', { persist: false });
    delete globalThis.localStorage;
    delete globalThis.document;
  }
});

test('relative time helper', () => {
  const now = Date.parse('2026-11-13T12:00:00Z');
  setLocale('en', { persist: false });
  try {
    assert.equal(formatRelativeTime(new Date(now - 10_000).toISOString(), now), 'just now');
    assert.match(formatRelativeTime(new Date(now - 5 * 60_000).toISOString(), now), /5 min/);
    assert.equal(formatRelativeTime('not a date', now), '');
  } finally {
    setLocale('zh-CN', { persist: false });
  }
});
