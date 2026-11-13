// Focus management helpers shared by Dialog, Menu and drawers.

const FOCUSABLE = [
  'a[href]', 'area[href]', 'button:not([disabled])', 'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])', 'textarea:not([disabled])', 'iframe', '[tabindex]:not([tabindex="-1"])',
  '[contenteditable="true"]',
].join(',');

export function focusableElements(container) {
  if (!container) return [];
  return Array.from(container.querySelectorAll(FOCUSABLE)).filter((element) => !element.hasAttribute('inert')
    && !element.closest('[hidden],[inert]') && (element.offsetParent !== null || element === document.activeElement));
}

/** Keeps Tab / Shift+Tab inside container. Call from a keydown handler. */
export function trapTabKey(event, container) {
  if (event.key !== 'Tab') return;
  const items = focusableElements(container);
  if (items.length === 0) {
    event.preventDefault();
    container.focus();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  if (event.shiftKey && (active === first || !container.contains(active))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (active === last || !container.contains(active))) {
    event.preventDefault();
    first.focus();
  }
}

let inertCount = 0;

/** Makes #app-layout inert while at least one modal layer is open. Returns release(). */
export function acquireLayoutInert() {
  inertCount += 1;
  const layout = document.getElementById('app-layout');
  if (layout) layout.inert = true;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    inertCount = Math.max(0, inertCount - 1);
    const current = document.getElementById('app-layout');
    if (current && inertCount === 0) current.inert = false;
  };
}
