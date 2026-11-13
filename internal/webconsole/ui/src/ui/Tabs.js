import { html, useRef } from '../lib/html.js';

export const tabId = (prefix, id) => `${prefix}-tab-${id}`;
export const panelId = (prefix, id) => `${prefix}-panel-${id}`;

/**
 * <Tabs idPrefix label tabs=${[{ id, label, icon?, badge? }]} value onChange />
 * Renders the tablist only (roving tabindex, ←/→/Home/End, automatic activation).
 * Pair with <TabPanel idPrefix id>…</TabPanel>.
 */
export function Tabs({ idPrefix, label, tabs, value, onChange, class: className = '' }) {
  const listRef = useRef(null);
  const focusTab = (id) => {
    listRef.current?.querySelector(`#${CSS.escape(tabId(idPrefix, id))}`)?.focus();
  };
  const onKeyDown = (event) => {
    const index = tabs.findIndex((tab) => tab.id === value);
    let next = -1;
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') next = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    event.preventDefault();
    onChange(tabs[next].id);
    focusTab(tabs[next].id);
  };
  return html`<div ref=${listRef} class=${`tabs ${className}`.trim()} role="tablist" aria-label=${label} onKeyDown=${onKeyDown}>
    ${tabs.map((tab) => html`<button
      type="button"
      role="tab"
      id=${tabId(idPrefix, tab.id)}
      class="tab"
      aria-selected=${String(tab.id === value)}
      aria-controls=${panelId(idPrefix, tab.id)}
      tabindex=${tab.id === value ? '0' : '-1'}
      data-testid=${`${idPrefix}-tab-${tab.id}`}
      onClick=${() => onChange(tab.id)}
    >${tab.label}${tab.badge ? html`<span class="tab-badge">${tab.badge}</span>` : null}</button>`)}
  </div>`;
}

export function TabPanel({ idPrefix, id, class: className = '', children }) {
  return html`<div role="tabpanel" id=${panelId(idPrefix, id)} aria-labelledby=${tabId(idPrefix, id)} tabindex="0" class=${`tab-panel ${className}`.trim()}>
    ${children}
  </div>`;
}
