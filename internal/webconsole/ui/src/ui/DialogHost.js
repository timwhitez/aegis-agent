// Renders the top entry of state.dialogs (confirmDialog / promptDialog).
import { html, useRef, useState } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { useStore } from '../state/hooks.js';
import { resolveDialog } from '../state/dialogs.js';
import { Dialog } from './Dialog.js';
import { Button } from './Button.js';

export function DialogHost() {
  const dialog = useStore((state) => state.dialogs[state.dialogs.length - 1] || null, Object.is);
  if (!dialog) return null;
  return dialog.kind === 'prompt'
    ? html`<${PromptDialogView} key=${dialog.id} dialog=${dialog} />`
    : html`<${ConfirmDialogView} key=${dialog.id} dialog=${dialog} />`;
}

function ConfirmDialogView({ dialog }) {
  const confirmRef = useRef(null);
  const cancelRef = useRef(null);
  const close = (value) => resolveDialog(dialog.id, value);
  return html`<${Dialog}
    title=${dialog.title}
    size="sm"
    role="alertdialog"
    testId="confirm-dialog"
    initialFocusRef=${dialog.danger ? cancelRef : confirmRef}
    showClose=${false}
    onClose=${() => close(false)}
    footer=${html`
      <${Button} variant="secondary" buttonRef=${cancelRef} onClick=${() => close(false)}>${t('common.cancel')}<//>
      <${Button} variant=${dialog.danger ? 'danger' : 'primary'} buttonRef=${confirmRef} data-testid="confirm-dialog-confirm" onClick=${() => close(true)}>
        ${dialog.confirmLabel || t('common.confirm')}
      <//>`}
  >
    ${dialog.body ? html`<p class="dialog-text">${dialog.body}</p>` : null}
  <//>`;
}

function PromptDialogView({ dialog }) {
  const [value, setValue] = useState(dialog.initialValue || '');
  const [error, setError] = useState('');
  const inputRef = useRef(null);
  const submit = (event) => {
    event?.preventDefault();
    const message = typeof dialog.validate === 'function' ? dialog.validate(value) : (value.trim() ? '' : t('common.dialog.promptInvalid'));
    if (message) {
      setError(message);
      inputRef.current?.focus();
      return;
    }
    resolveDialog(dialog.id, value);
  };
  return html`<${Dialog} title=${dialog.title} size="sm" testId="prompt-dialog" initialFocusRef=${inputRef} onClose=${() => resolveDialog(dialog.id, null)}>
    <form class="prompt-form" onSubmit=${submit}>
      ${dialog.body ? html`<p class="dialog-text">${dialog.body}</p>` : null}
      <label class="field">
        <span class="field-label">${dialog.label}</span>
        <input
          ref=${inputRef}
          class="input"
          value=${value}
          aria-invalid=${error ? 'true' : undefined}
          onInput=${(event) => { setValue(event.currentTarget.value); setError(''); }}
        />
      </label>
      ${error ? html`<p class="field-error" role="alert">${error}</p>` : null}
      <div class="dialog-footer is-inline">
        <${Button} variant="secondary" onClick=${() => resolveDialog(dialog.id, null)}>${t('common.cancel')}<//>
        <${Button} variant="primary" type="submit">${dialog.confirmLabel || t('common.confirm')}<//>
      </div>
    </form>
  <//>`;
}
