import { html } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { useStore } from '../state/hooks.js';
import { closeShortcuts } from '../state/actions.js';
import { Dialog } from '../ui/Dialog.js';
import { Kbd } from '../ui/Kbd.js';

const ROWS = [
  ['shell.shortcuts.palette', ['mod', 'K']],
  ['shell.shortcuts.newSession', ['mod', 'shift', 'O']],
  ['shell.shortcuts.sidebar', ['mod', 'B']],
  ['shell.shortcuts.inspector', ['mod', '.']],
  ['shell.shortcuts.focusComposer', ['/']],
  ['shell.shortcuts.help', ['?']],
  ['shell.shortcuts.send', ['Enter']],
  ['shell.shortcuts.newline', ['shift', 'Enter']],
  ['shell.shortcuts.close', ['Esc']],
];

export function ShortcutHelp() {
  useStore((state) => state.ui.locale, Object.is);
  return html`<${Dialog} title=${t('shell.shortcuts.title')} size="md" testId="shortcut-help" onClose=${closeShortcuts}>
    <dl class="shortcut-list">
      ${ROWS.map(([key, keys]) => html`<div class="shortcut-row" key=${key}>
        <dt>${t(key)}</dt>
        <dd><${Kbd} keys=${keys} /></dd>
      </div>`)}
    </dl>
  <//>`;
}
