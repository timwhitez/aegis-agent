// Merges the per-area dictionaries and registers them with lib/i18n.js.
// Adding an area: create src/i18n/<area>.js exporting { 'zh-CN': {...}, en: {...} }
// (and optionally prefixes: [...]) and list it in AREAS below. Keys must start
// with "<area>." or with one of the extra prefixes the area declares; extra
// prefixes cover key families emitted by lib/ modules (see their headers) and
// may not be claimed by two areas.
import { registerDictionaries } from '../lib/i18n.js';
import common from './common.js';
import shell from './shell.js';
import session from './session.js';
import composer from './composer.js';
import inspector from './inspector.js';
import history from './history.js';
import skills from './skills.js';
import workspace from './workspace.js';
import settings from './settings.js';

export const AREAS = Object.freeze({ common, shell, session, composer, inspector, history, skills, workspace, settings });

const LOCALE_KEYS = ['zh-CN', 'en'];

/** Prefixes owned by an area: "<area>." plus its declared extra prefixes. */
export function areaPrefixes(area, dict) {
  return [area + '.', ...((dict && Array.isArray(dict.prefixes)) ? dict.prefixes : [])];
}

/** Merges area dictionaries; throws on a key defined by two areas. */
export function mergeAreas(areas = AREAS) {
  const merged = { 'zh-CN': {}, en: {} };
  for (const [area, dict] of Object.entries(areas)) {
    for (const locale of LOCALE_KEYS) {
      for (const [key, value] of Object.entries(dict?.[locale] || {})) {
        if (Object.prototype.hasOwnProperty.call(merged[locale], key)) {
          throw new Error(`duplicate i18n key ${key} (area ${area}, locale ${locale})`);
        }
        merged[locale][key] = value;
      }
    }
  }
  return merged;
}

export const dictionaries = mergeAreas();
registerDictionaries(dictionaries);
