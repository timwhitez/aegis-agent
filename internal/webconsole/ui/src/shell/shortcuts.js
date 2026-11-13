// Global keyboard shortcuts (spec §7.4). Matching is pure (lib/shortcuts.js).
import { isEditableTarget, isMacPlatform, matchShortcut } from '../lib/shortcuts.js';
import { getState } from '../state/store.js';
import { closePalette, focusComposer, newSession, openPalette, openShortcuts, toggleInspector, toggleSidebar } from '../state/actions.js';

export function installGlobalShortcuts(target = globalThis) {
  const mac = isMacPlatform();
  const onKeyDown = (event) => {
    if (event.defaultPrevented) return;
    const state = getState();
    if (state.dialogs.length > 0) return;
    const action = matchShortcut(event, { mac, inEditable: isEditableTarget(event.target) });
    if (!action) return;
    const sessionRoute = state.route.name === 'new' || state.route.name === 'session';
    switch (action) {
      case 'palette':
        event.preventDefault();
        if (state.ui.paletteOpen) closePalette();
        else openPalette();
        return;
      case 'newSession':
        event.preventDefault();
        newSession();
        return;
      case 'sidebar':
        event.preventDefault();
        toggleSidebar();
        return;
      case 'inspector':
        if (!sessionRoute) return;
        event.preventDefault();
        toggleInspector();
        return;
      case 'focusComposer':
        if (!sessionRoute || state.ui.paletteOpen || state.ui.shortcutsOpen) return;
        event.preventDefault();
        focusComposer();
        return;
      case 'help':
        if (state.ui.paletteOpen) return;
        event.preventDefault();
        openShortcuts();
        return;
      default:
    }
  };
  target.addEventListener('keydown', onKeyDown);
  return () => target.removeEventListener('keydown', onKeyDown);
}
