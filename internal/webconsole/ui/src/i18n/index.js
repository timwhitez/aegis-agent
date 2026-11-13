// Merges the per-area dictionaries and registers them with lib/i18n.js.
// Adding an area: create src/i18n/<area>.js exporting { 'zh-CN': {...}, en: {...} }
// with keys prefixed "<area>." and list it in AREAS below.
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

/** Merges area dictionaries; throws on a key defined by two areas. */
export function mergeAreas(areas = AREAS) {
  const merged = { 'zh-CN': {}, en: {} };
  for (const [area, dict] of Object.entries(areas)) {
    for (const locale of Object.keys(merged)) {
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
