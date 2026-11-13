// PLACEHOLDER — owned by the composer feature worker.
// Contract (spec §7.2):
//   export function Composer(): reads useStore(s => s.composer, s.session.id,
//   s.session.detail, s.route.name); edits composer.text via
//   actions.setComposerText; sends via session-actions.submitComposer();
//   focuses its textarea whenever composer.focusToken changes (⌘K command,
//   "/" shortcut, newSession()); Enter sends, Shift+Enter newline, never sends
//   during IME composition. Test ids: "composer" (root), "composer-input",
//   "send-button", "stop-button".
import { html, useEffect, useRef } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { setComposerText } from '../../state/actions.js';
import { submitComposer } from '../../state/session-actions.js';
import { IconButton } from '../../ui/IconButton.js';

export function Composer() {
  const view = useStore((state) => ({ text: state.composer.text, focusToken: state.composer.focusToken, locale: state.ui.locale }));
  const inputRef = useRef(null);
  useEffect(() => {
    if (view.focusToken > 0) inputRef.current?.focus();
  }, [view.focusToken]);
  const onKeyDown = (event) => {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
      event.preventDefault();
      submitComposer();
    }
  };
  return html`<form class="composer" data-testid="composer" onSubmit=${(event) => { event.preventDefault(); submitComposer(); }}>
    <label class="sr-only" for="composer-input">${t('shell.composer.label')}</label>
    <textarea
      id="composer-input"
      ref=${inputRef}
      class="composer-input"
      rows="2"
      placeholder=${t('shell.composer.placeholder')}
      value=${view.text}
      data-testid="composer-input"
      onInput=${(event) => setComposerText(event.currentTarget.value)}
      onKeyDown=${onKeyDown}
    ></textarea>
    <div class="composer-toolbar">
      <span class="composer-toolbar-spacer"></span>
      <${IconButton} type="submit" icon="arrow-up" variant="primary" label=${t('shell.composer.send')} disabled=${!view.text.trim()} data-testid="send-button" />
    </div>
  </form>`;
}
