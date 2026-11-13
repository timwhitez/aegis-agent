// In-app confirm/prompt dialogs (spec §7.5). Rendered by ui/DialogHost.js.
import { update } from './store.js';

let nextDialogId = 1;

function openDialog(dialog) {
  return new Promise((resolve) => {
    const id = nextDialogId++;
    update('dialogs', (dialogs) => [...dialogs, { ...dialog, id, resolve }]);
  });
}

/** confirmDialog({ title, body, confirmLabel, danger }) -> Promise<boolean> */
export function confirmDialog({ title, body = '', confirmLabel = '', danger = false } = {}) {
  return openDialog({ kind: 'confirm', title, body, confirmLabel, danger });
}

/**
 * promptDialog({ title, label, initialValue, validate }) -> Promise<string|null>
 * validate(value) returns '' when valid or an error message to show inline.
 */
export function promptDialog({ title, label = '', initialValue = '', validate, confirmLabel = '', body = '' } = {}) {
  return openDialog({ kind: 'prompt', title, label, initialValue, validate, confirmLabel, body });
}

/** Settles and removes a dialog (false / null on cancel). */
export function resolveDialog(id, value) {
  update('dialogs', (dialogs) => {
    const dialog = dialogs.find((item) => item.id === id);
    if (dialog) dialog.resolve(value);
    return dialogs.filter((item) => item.id !== id);
  });
}
