import { html } from '../lib/html.js';

/** Shared page header: <PageHeader title description actions=${node} /> */
export function PageHeader({ title, description = '', actions = null, children }) {
  return html`<header class="page-header">
    <div class="page-header-text">
      <h1 class="page-title">${title}</h1>
      ${description ? html`<p class="page-description">${description}</p>` : null}
    </div>
    ${actions ? html`<div class="page-actions">${actions}</div>` : null}
    ${children}
  </header>`;
}
