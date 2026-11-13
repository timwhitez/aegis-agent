// Todo / task progress strip above the composer: done/total, the current
// in-progress item and an expandable full list (session-model.js
// todoProgress). Test ids: todo-strip, todo-strip-toggle, todo-item.
import { html, useId, useState } from '../../lib/html.js';
import { t, tx } from '../../lib/i18n.js';
import { todoProgress } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { Icon } from '../../ui/Icon.js';

const lower = (value) => String(value || '').toLowerCase();
const isActive = (status) => ['in_progress', 'running'].includes(lower(status));
const isDone = (status) => ['completed', 'done'].includes(lower(status));
const isCancelled = (status) => lower(status) === 'cancelled';

function Label({ slot }) {
  if (!slot) return null;
  return slot.raw !== undefined ? html`<span translate="no">${slot.raw}</span>` : html`<span>${tx(slot)}</span>`;
}

function StatusMark({ status }) {
  if (isDone(status)) return html`<span class="todo-mark is-done"><${Icon} name="check" size=${12} strokeWidth=${2.4} /></span>`;
  if (isCancelled(status)) return html`<span class="todo-mark is-cancelled"><${Icon} name="x" size=${12} strokeWidth=${2.2} /></span>`;
  if (isActive(status)) return html`<span class="todo-mark is-active"></span>`;
  return html`<span class="todo-mark"></span>`;
}

function statusLabel(status) {
  if (isDone(status)) return t('composer.todo.status.done');
  if (isCancelled(status)) return t('composer.todo.status.cancelled');
  if (isActive(status)) return t('composer.todo.status.active');
  return t('composer.todo.status.pending');
}

export function TodoStrip() {
  const view = useStore((state) => ({ locale: state.ui.locale, taskBoard: state.session.detail?.task_board || null, sessionId: state.session.id }));
  const [open, setOpen] = useState(false);
  const listId = useId();
  const progress = todoProgress(view.taskBoard);
  if (!view.sessionId || !progress.total) return null;
  const current = progress.items.find((item) => isActive(item.status)) || progress.items.find((item) => !isDone(item.status) && !isCancelled(item.status)) || null;
  const allDone = progress.done === progress.total;
  return html`<section class=${`todo-strip ${open ? 'is-open' : ''}`} data-testid="todo-strip" aria-label=${t('composer.todo.label')}>
    ${open ? html`<ol class="todo-list" id=${listId} role="list">
      ${progress.items.map((item, index) => html`<li key=${index} class=${`todo-item ${isDone(item.status) ? 'is-done' : ''} ${isCancelled(item.status) ? 'is-cancelled' : ''} ${isActive(item.status) ? 'is-active' : ''}`} data-testid="todo-item" data-status=${item.status}>
        <${StatusMark} status=${item.status} />
        <span class="todo-item-text"><${Label} slot=${item.label} /></span>
        <span class="sr-only">${statusLabel(item.status)}</span>
        ${item.owner ? html`<span class="todo-item-owner" translate="no">${item.owner}</span>` : null}
      </li>`)}
    </ol>` : null}
    <button type="button" class="todo-strip-summary" aria-expanded=${String(open)} aria-controls=${open ? listId : undefined} data-testid="todo-strip-toggle" onClick=${() => setOpen(!open)}>
      <span class="todo-progress" role="img" aria-label=${t('composer.todo.progress', { done: progress.done, total: progress.total })}>
        <span class="todo-progress-fill" style=${{ width: `${progress.percent}%` }}></span>
      </span>
      <span class="todo-count">${progress.done}/${progress.total}</span>
      <span class="todo-current">
        ${allDone ? t('composer.todo.allDone') : current ? html`<${Label} slot=${current.label} />` : t('composer.todo.none')}
      </span>
      <span class="todo-strip-toggle-label">${open ? t('composer.todo.collapse') : t('composer.todo.expand')}</span>
      <${Icon} name=${open ? 'chevron-down' : 'chevron-up'} size=${14} />
    </button>
  </section>`;
}
