// New-session hero shown on "/": brand, title, workspace, and four starter
// suggestions that fill (never send) the composer. The planning suggestion
// also switches Plan mode on. Test ids: empty-session, starter-suggestion.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { setComposerText, togglePlanMode } from '../../state/actions.js';
import { Icon } from '../../ui/Icon.js';
import { Kbd } from '../../ui/Kbd.js';
import { BrandMark } from '../../shell/Sidebar.js';

const SUGGESTIONS = [
  { id: 'explore', icon: 'folder-open', label: 'session.empty.suggest.explore', prompt: 'session.empty.suggest.explorePrompt' },
  { id: 'tests', icon: 'terminal', label: 'session.empty.suggest.tests', prompt: 'session.empty.suggest.testsPrompt' },
  { id: 'review', icon: 'eye', label: 'session.empty.suggest.review', prompt: 'session.empty.suggest.reviewPrompt' },
  { id: 'plan', icon: 'clipboard-list', label: 'session.empty.suggest.plan', prompt: 'session.empty.suggest.planPrompt', plan: true },
];

function applySuggestion(item) {
  if (item.plan) togglePlanMode(true);
  setComposerText(t(item.prompt), { focus: true });
}

export function EmptySession() {
  const view = useStore((state) => ({ locale: state.ui.locale, workspace: state.meta.data?.workspace_root || '' }));
  return html`<section class="empty-session" data-testid="empty-session" aria-labelledby="empty-session-title">
    <${BrandMark} size=${44} />
    <h2 class="empty-session-title" id="empty-session-title">${t('session.empty.title')}</h2>
    <p class="empty-session-subtitle">${t('session.empty.subtitle')}</p>
    ${view.workspace ? html`<p class="empty-session-workspace" title=${view.workspace}>
      <${Icon} name="folder" size=${14} />
      <span class="sr-only">${t('session.empty.workspace')}</span>
      <span class="empty-session-path" translate="no">${view.workspace}</span>
    </p>` : null}
    <h3 class="empty-session-suggestions-title">${t('session.empty.suggestions')}</h3>
    <ul class="suggestion-grid" role="list">
      ${SUGGESTIONS.map((item) => html`<li key=${item.id}>
        <button type="button" class="suggestion" data-testid="starter-suggestion" data-suggestion=${item.id} onClick=${() => applySuggestion(item)}>
          <span class="suggestion-icon"><${Icon} name=${item.icon} size=${16} /></span>
          <span class="suggestion-label">${t(item.label)}</span>
          <span class="suggestion-prompt">${t(item.prompt)}</span>
        </button>
      </li>`)}
    </ul>
    <p class="empty-session-hint"><${Kbd} keys=${['mod', 'K']} /><span>${t('session.empty.hintPalette')}</span></p>
  </section>`;
}
