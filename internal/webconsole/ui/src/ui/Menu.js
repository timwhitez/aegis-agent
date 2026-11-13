import { html, useEffect, useId, useRef, useState } from '../lib/html.js';
import { Icon } from './Icon.js';
import { IconButton } from './IconButton.js';

/**
 * Menu button: <Menu label icon="more-horizontal" items=${[{ id, label, icon?, onSelect, disabled?, danger? }]} align="end" testId? />
 * ↑/↓/Home/End move focus, Enter/Space select, Esc/Tab close and return focus to the trigger.
 */
export function Menu({ label, icon = 'more-horizontal', items = [], align = 'end', testId, triggerVariant = 'ghost', disabled = false }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef(null);
  const triggerRef = useRef(null);
  const menuId = useId();

  const enabledItems = () => Array.from(rootRef.current?.querySelectorAll('[role="menuitem"]:not([aria-disabled="true"])') || []);
  const focusItem = (index) => {
    const nodes = enabledItems();
    if (!nodes.length) return;
    nodes[(index + nodes.length) % nodes.length].focus();
  };
  const close = (restore = true) => {
    setOpen(false);
    if (restore) triggerRef.current?.focus();
  };

  useEffect(() => {
    if (!open) return undefined;
    focusItem(0);
    const onPointerDown = (event) => {
      if (!rootRef.current?.contains(event.target)) setOpen(false);
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    return () => document.removeEventListener('pointerdown', onPointerDown, true);
  }, [open]);

  const onMenuKeyDown = (event) => {
    const nodes = enabledItems();
    const index = nodes.indexOf(document.activeElement);
    if (event.key === 'ArrowDown') { event.preventDefault(); focusItem(index + 1); }
    else if (event.key === 'ArrowUp') { event.preventDefault(); focusItem(index - 1); }
    else if (event.key === 'Home') { event.preventDefault(); focusItem(0); }
    else if (event.key === 'End') { event.preventDefault(); focusItem(nodes.length - 1); }
    else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); }
    else if (event.key === 'Tab') { close(false); }
  };

  const select = (item) => {
    if (item.disabled) return;
    close(true);
    item.onSelect?.();
  };

  return html`<div class="menu" ref=${rootRef} data-testid=${testId}>
    <${IconButton}
      icon=${icon}
      label=${label}
      variant=${triggerVariant}
      buttonRef=${triggerRef}
      disabled=${disabled}
      aria-haspopup="menu"
      aria-expanded=${String(open)}
      aria-controls=${open ? menuId : undefined}
      onClick=${() => setOpen(!open)}
      onKeyDown=${(event) => { if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); setOpen(true); } }}
    />
    ${open ? html`<div class=${`menu-popover align-${align}`} role="menu" id=${menuId} aria-label=${label} onKeyDown=${onMenuKeyDown}>
      ${items.map((item) => item.separator
        ? html`<div class="menu-separator" role="separator"></div>`
        : html`<button
          type="button"
          role="menuitem"
          tabindex="-1"
          class=${`menu-item ${item.danger ? 'is-danger' : ''}`}
          aria-disabled=${item.disabled ? 'true' : undefined}
          data-testid=${item.testId}
          onClick=${() => select(item)}
        >
          ${item.icon ? html`<${Icon} name=${item.icon} size=${16} />` : html`<span class="menu-item-spacer"></span>`}
          <span>${item.label}</span>
        </button>`)}
    </div>` : null}
  </div>`;
}
