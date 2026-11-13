// Global keyboard shortcut matching (pure). ⌘ on macOS, Ctrl elsewhere.

export function isMacPlatform(nav = globalThis.navigator) {
  const platform = nav?.userAgentData?.platform || nav?.platform || nav?.userAgent || '';
  return /mac|iphone|ipad|ipod/i.test(platform);
}

/** Label for the primary modifier: '⌘' on macOS, 'Ctrl' elsewhere. */
export function modKeyLabel(mac = isMacPlatform()) {
  return mac ? '⌘' : 'Ctrl';
}

/**
 * matchShortcut(event, { mac, inEditable }) -> action name or null.
 * Actions: palette · newSession · sidebar · inspector · focusComposer · help.
 * Never matches during IME composition.
 */
export function matchShortcut(event, { mac = isMacPlatform(), inEditable = false } = {}) {
  if (!event || event.isComposing || event.keyCode === 229) return null;
  const key = String(event.key || '');
  const lowerKey = key.toLowerCase();
  const mod = mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
  if (mod && !event.altKey) {
    if (lowerKey === 'k' && !event.shiftKey) return 'palette';
    if (lowerKey === 'o' && event.shiftKey) return 'newSession';
    if (lowerKey === 'b' && !event.shiftKey) return 'sidebar';
    if (key === '.' && !event.shiftKey) return 'inspector';
    return null;
  }
  if (event.metaKey || event.ctrlKey || event.altKey || inEditable) return null;
  if (key === '/') return 'focusComposer';
  if (key === '?') return 'help';
  return null;
}

/** True when keyboard input targets a text-entry control. */
export function isEditableTarget(target) {
  if (!target || typeof target !== 'object') return false;
  if (target.isContentEditable) return true;
  const tag = String(target.tagName || '').toLowerCase();
  if (tag === 'textarea' || tag === 'select') return true;
  if (tag !== 'input') return false;
  const type = String(target.type || 'text').toLowerCase();
  return !['button', 'checkbox', 'radio', 'submit', 'reset', 'range', 'color', 'file'].includes(type);
}
