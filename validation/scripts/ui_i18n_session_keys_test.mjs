// Session area i18n coverage: every key that a lib/ module header declares
// under a prefix owned by src/i18n/session.js must exist in both locales.
// The headers are parsed directly (the "i18n keys" block of each lib/*.js
// header comment), so a new lib key cannot silently miss its translation.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import session from '../../internal/webconsole/ui/src/i18n/session.js';

const LIB_DIR = fileURLToPath(new URL('../../internal/webconsole/ui/src/lib/', import.meta.url));
const PREFIXES = ['session.', ...session.prefixes];

/** The "i18n keys …" block of a module header comment, flattened to one line. */
export function headerKeyBlock(source) {
  const text = String(source);
  if (!text.trimStart().startsWith('/**') && !text.trimStart().startsWith('/*')) return '';
  const header = text.slice(0, text.indexOf('*/'));
  const start = header.search(/i18n keys/i);
  if (start < 0) return '';
  return header.slice(start).split('\n').map((line) => line.replace(/^\s*\*\s?/, '')).join(' ');
}

/** "a.b.c|d|e" -> ["a.b.c", "a.b.d", "a.b.e"] (alternatives replace the last segment). */
export function expandAlternatives(token) {
  const [first, ...rest] = token.split('|');
  const base = first.slice(0, first.lastIndexOf('.') + 1);
  return [first, ...rest.map((segment) => base + segment)];
}

const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** Keys under the given prefixes declared in one header block. */
export function declaredKeys(block, prefixes = PREFIXES) {
  const keys = new Set();
  const prefixPattern = prefixes.map(escape).join('|');
  // Templates: "activity.<state>.title|copy for state in completed|failed".
  const template = new RegExp(`(?<![\\w.])((?:${prefixPattern})[\\w.]*<(\\w+)>[\\w.|]*)\\s+for\\s+\\2\\s+in\\s+([\\w|]+)`, 'g');
  let rest = block;
  for (const match of block.matchAll(template)) {
    const [whole, pattern, name, values] = match;
    for (const value of values.split('|')) {
      expandAlternatives(pattern.replace(`<${name}>`, value)).forEach((key) => keys.add(key));
    }
    rest = rest.replace(whole, ' ');
  }
  const token = new RegExp(`(?<![\\w.])(?:${prefixPattern})[A-Za-z0-9_]+(?:[.|][A-Za-z0-9_]+)*`, 'g');
  for (const match of rest.matchAll(token)) {
    expandAlternatives(match[0]).forEach((key) => keys.add(key));
  }
  return keys;
}

function libDeclaredKeys() {
  const byFile = new Map();
  for (const name of readdirSync(LIB_DIR).filter((file) => file.endsWith('.js')).sort()) {
    const keys = declaredKeys(headerKeyBlock(readFileSync(join(LIB_DIR, name), 'utf8')));
    if (keys.size) byFile.set(name, keys);
  }
  return byFile;
}

test('header parser expands alternatives and templates', () => {
  const keys = declaredKeys('i18n keys: activity.<state>.title|copy for state in done|failed, goal.metric.tokens|providerTime {value}; tool.noOutput | event.toolFallback');
  assert.deepEqual([...keys].sort(), ['activity.done.copy', 'activity.done.title', 'activity.failed.copy', 'activity.failed.title', 'goal.metric.providerTime', 'goal.metric.tokens', 'tool.noOutput']);
});

test('every lib-declared key under session-owned prefixes exists in both locales', () => {
  const byFile = libDeclaredKeys();
  const all = new Set([...byFile.values()].flatMap((keys) => [...keys]));
  // Sanity: the parser must find the main families.
  for (const sample of ['actor.youSteer', 'activity.failed.copy', 'activity.launching.title', 'activity.executingApprovedPlan.copy',
    'stage.interruptArmed', 'step.thinking', 'tool.search.pattern', 'goal.chip.kind', 'goal.line.at', 'background.accepted',
    'markdown.blockedImage', 'session.masterFallback']) {
    assert.ok(all.has(sample), `parser found ${sample}`);
  }
  assert.ok(all.size >= 100, `parsed ${all.size} keys`);
  const missing = [];
  for (const [file, keys] of byFile) {
    for (const key of keys) {
      for (const locale of ['zh-CN', 'en']) {
        if (!Object.prototype.hasOwnProperty.call(session[locale], key)) missing.push(`${locale} ${key} (${file})`);
      }
    }
  }
  assert.deepEqual(missing, []);
});

test('templates that receive descriptor params interpolate the resolved label', () => {
  for (const locale of ['zh-CN', 'en']) {
    assert.match(session[locale]['goal.tool.update'], /\{statusLabel\}/);
    assert.match(session[locale]['goal.line.session'], /\{statusLabel\}/);
    assert.match(session[locale]['goal.line.latest'], /\{typeLabel\}/);
    assert.match(session[locale]['background.accepted'], /\{count\}/);
  }
});
