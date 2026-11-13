// Reusable i18n coverage scan for the Web console frontend (not a test file).
// collectSourceKeys() finds every literal t('…') key under ui/src; dynamic
// template keys (t(`a.${x}`)) are reported separately so tests can cover
// them with explicit lists.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

export const UI_SRC_DIR = fileURLToPath(new URL('../../internal/webconsole/ui/src/', import.meta.url));

export function listSourceFiles(dir = UI_SRC_DIR) {
  const files = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) files.push(...listSourceFiles(path));
    else if (name.endsWith('.js')) files.push(path);
  }
  return files.sort();
}

const LITERAL_CALL = /(?<![\w$.])t\(\s*(['"`])((?:(?!\1)[^\\\n])+)\1/g;

/** -> { literal: Map<key, string[] files>, dynamic: [{ file, expression }] } */
export function collectSourceKeys(dir = UI_SRC_DIR) {
  const literal = new Map();
  const dynamic = [];
  for (const file of listSourceFiles(dir)) {
    const text = readFileSync(file, 'utf8');
    const rel = relative(dir, file);
    for (const match of text.matchAll(LITERAL_CALL)) {
      const [, quote, key] = match;
      if (quote === '`' && key.includes('${')) {
        dynamic.push({ file: rel, expression: key });
        continue;
      }
      if (!literal.has(key)) literal.set(key, []);
      literal.get(key).push(rel);
    }
  }
  return { literal, dynamic };
}

/** Keys referenced in source but missing from a locale: [{ key, locale, files }] */
export function missingSourceKeys(dictionaries, dir = UI_SRC_DIR) {
  const { literal } = collectSourceKeys(dir);
  const missing = [];
  for (const [key, files] of literal) {
    for (const locale of Object.keys(dictionaries)) {
      if (!Object.prototype.hasOwnProperty.call(dictionaries[locale], key)) missing.push({ key, locale, files });
    }
  }
  return missing;
}

/**
 * Dictionary keys never referenced by ui/src code (i18n/ excluded): a key
 * counts as referenced when it appears as a quoted literal ('k', "k", `k`;
 * covers t('…') calls, descriptor keys and key maps) or when it belongs to an
 * explicit dynamic family { prefix: [suffix, …] } that source builds with
 * template keys. -> sorted list of unreferenced keys
 */
export function unreferencedKeys(dictionary, families = {}, dir = UI_SRC_DIR) {
  const text = listSourceFiles(dir)
    .filter((file) => !relative(dir, file).startsWith('i18n'))
    .map((file) => readFileSync(file, 'utf8'))
    .join('\n');
  const quoted = (key) => ["'", '"', '`'].some((quote) => text.includes(quote + key + quote));
  const inFamily = (key) => Object.entries(families).some(([prefix, suffixes]) => key.startsWith(prefix) && suffixes.includes(key.slice(prefix.length)));
  return Object.keys(dictionary || {}).filter((key) => !quoted(key) && !inFamily(key)).sort();
}

export function placeholders(text) {
  return [...String(text).matchAll(/\{(\w+)\}/g)].map((match) => match[1]).sort();
}
