// PLACEHOLDER — owned by the composer feature worker.
// Contract (spec §7.2): export function TodoStrip(); compact progress of
// session.detail.task_board.todo above the composer, expandable to the full
// list. Test id: "todo-strip".
import { html } from '../../lib/html.js';
import { useStore } from '../../state/hooks.js';

export function TodoStrip() {
  const todo = useStore((state) => state.session.detail?.task_board?.todo || null, Object.is);
  if (!Array.isArray(todo) || todo.length === 0) return null;
  const done = todo.filter((item) => item.status === 'completed').length;
  return html`<div class="todo-strip" data-testid="todo-strip"><span>${done}/${todo.length}</span></div>`;
}
