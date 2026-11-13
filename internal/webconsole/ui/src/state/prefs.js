// Display preferences persisted in localStorage (never authoritative state).
export const UI_PREFS_KEY = 'aegis-agent.ui-prefs.v1';
export const INSPECTOR_TABS = Object.freeze(['overview', 'plan', 'goal', 'tasks', 'agents', 'context', 'timeline', 'files']);

const DEFAULT_PREFS = Object.freeze({ sidebarCollapsed: false, inspectorOpen: false, inspectorTab: 'overview' });

function storage() {
  try {
    return globalThis.localStorage || null;
  } catch {
    return null;
  }
}

export function loadUIPrefs() {
  try {
    const raw = storage()?.getItem(UI_PREFS_KEY);
    const parsed = raw ? JSON.parse(raw) : {};
    return {
      sidebarCollapsed: parsed.sidebarCollapsed === true,
      inspectorOpen: parsed.inspectorOpen === true,
      inspectorTab: INSPECTOR_TABS.includes(parsed.inspectorTab) ? parsed.inspectorTab : DEFAULT_PREFS.inspectorTab,
    };
  } catch {
    return { ...DEFAULT_PREFS };
  }
}

export function saveUIPrefs(prefs) {
  try {
    storage()?.setItem(UI_PREFS_KEY, JSON.stringify({
      sidebarCollapsed: Boolean(prefs.sidebarCollapsed),
      inspectorOpen: Boolean(prefs.inspectorOpen),
      inspectorTab: prefs.inspectorTab,
    }));
  } catch {
    // Preferences are best effort.
  }
}
