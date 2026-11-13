// New-session hero shown on "/": title, subtitle and starter suggestions
// that fill (not send) the composer. Owned by the session feature worker for
// later refinement; this version is complete.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { setComposerText } from '../../state/actions.js';
import { Icon } from '../../ui/Icon.js';
import { BrandMark } from '../../shell/Sidebar.js';

const SUGGESTIONS = [
  { id: 'explore', icon: 'folder-open', label: 'shell.empty.suggest.explore', prompt: 'shell.empty.suggest.explorePrompt' },
  { id: 'tests', icon: 'terminal', label: 'shell.empty.suggest.tests', prompt: 'shell.empty.suggest.testsPrompt' },
  { id: 'review', icon: 'eye', label: 'shell.empty.suggest.review', prompt: 'shell.empty.suggest.reviewPrompt' },
  { id: 'plan', icon: 'clipboard-list', label: 'shell.empty.suggest.plan', prompt: 'shell.empty.suggest.planPrompt' },
];

export function EmptySession() {
  const workspace = useStore((state) => [state.ui.locale, state.meta.data?.workspace_root || ''], (a, b) => a[0] === b[0] && a[1] === b[1])[1];
  return html`<section class="empty-session" data-testid="empty-session">
    <${BrandMark} size=${44} />
    <h2 class="empty-session-title">${t('shell.empty.title')}</h2>
    <p class="empty-session-subtitle">${t('shell.empty.subtitle')}</p>
    ${workspace ? html`<p class="empty-session-workspace"><${Icon} name="folder" size=${14} /><span translate="no">${workspace}</span></p>` : null}
    <h3 class="sr-only">${t('shell.empty.suggestions')}</h3>
    <ul class="suggestion-grid" role="list">
      ${SUGGESTIONS.map((item) => html`<li key=${item.id}>
        <button type="button" class="suggestion" data-testid="starter-suggestion" onClick=${() => setComposerText(t(item.prompt), { focus: true })}>
          <span class="suggestion-icon"><${Icon} name=${item.icon} size=${16} /></span>
          <span class="suggestion-label">${t(item.label)}</span>
          <span class="suggestion-prompt">${t(item.prompt)}</span>
        </button>
      </li>`)}
    </ul>
  </section>`;
}
