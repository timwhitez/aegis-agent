import { html } from '../lib/html.js';

/** Loading placeholder lines; aria-hidden (pair with a visible/aria-live loading text). */
export function Skeleton({ lines = 3, class: className = '' }) {
  return html`<div class=${`skeleton ${className}`.trim()} aria-hidden="true">
    ${Array.from({ length: lines }, (_, index) => html`<span class="skeleton-line" style=${`width: ${index === lines - 1 ? 60 : 100 - index * 8}%`}></span>`)}
  </div>`;
}
