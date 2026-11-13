// Tasks tab: Todo snapshot (in-progress counted from the todo snapshot
// itself) and the runtime-derived task board groups in_progress / ready /
// blocked / completed / cancelled (lib/session-model.js taskBoardGroups).
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { maybeArray, taskBoardSummary, todoItemView } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Metrics, Section, StatusPill, TimeText, Txt, num, text } from './parts.js';

function Chip({ d }) {
  if (d.raw !== undefined && d.raw !== null && !d.key) return html`<span class="chip task-chip" translate="no">${String(d.raw)}</span>`;
  return html`<span class="chip task-chip">${text(d)}</span>`;
}

export function TasksTab() {
  const view = useStore((state) => ({ locale: state.ui.locale, detail: state.session.detail }));
  const board = view.detail?.task_board || {};
  const summary = taskBoardSummary(board);
  const todos = maybeArray(board.todo).map(todoItemView);
  if (!todos.length && !summary.taskCount) {
    return html`<${EmptyState} icon="list-checks" title=${t('inspector.tasks.emptyTitle')} body=${t('inspector.tasks.emptyBody')} data-testid="inspector-tasks-empty" />`;
  }
  return html`<div class="insp-tab insp-tasks" data-testid="inspector-tasks">
    <${Metrics} items=${[
      { label: t('inspector.tasks.todoItems'), value: num(summary.todoCount), sub: t('inspector.tasks.inProgress', { count: summary.todoInProgress }), testId: 'inspector-tasks-todo-metric' },
      { label: t('inspector.tasks.ready'), value: num(summary.readyCount), sub: t('inspector.tasks.blocked', { count: summary.blockedCount }) },
      { label: t('inspector.tasks.completed'), value: num(summary.completedCount), sub: t('inspector.tasks.cancelled', { count: summary.cancelledCount }) },
    ]} />

    <${Section} title=${t('inspector.tasks.todo')} testId="inspector-todo">
      ${todos.length ? html`<ul class="insp-list" role="list">
        ${todos.map((item) => html`<li class="insp-list-item insp-todo" data-testid="inspector-todo-item" data-status=${item.status}>
          <div class="insp-list-head">
            <span class="insp-list-title todo-card-title"><${Txt} d=${item.title} /></span>
            <${StatusPill} status=${item.status} />
          </div>
          ${item.priority || item.updatedAt ? html`<p class="insp-card-meta">
            ${item.priority ? html`<span class="chip task-chip" translate="no">${item.priority.raw}</span>` : null}
            <${TimeText} at=${item.updatedAt} />
          </p>` : null}
        </li>`)}
      </ul>` : html`<p class="insp-muted">${t('inspector.tasks.noTodo')}</p>`}
    <//>

    <${Section} title=${t('inspector.tasks.board')} testId="inspector-task-board">
      ${summary.groups.length ? summary.groups.map((group) => html`<section class="insp-task-group" data-task-group=${group.key} data-testid="inspector-task-group">
        <h4 class="insp-group-title task-group-heading"><span>${text(group.label)}</span><span class="insp-count">${group.items.length}</span></h4>
        <ul class="insp-list" role="list">
          ${group.items.map((task) => html`<li class="insp-list-item task-card" data-task-id=${task.id || undefined} data-derived-status=${task.status}>
            <div class="insp-list-head">
              <span class="insp-list-title task-card-title"><${Txt} d=${task.title} /></span>
              <${StatusPill} status=${task.status} />
            </div>
            <p class="insp-card-copy task-card-copy"><${Txt} d=${task.description} /></p>
            ${task.chips.length ? html`<p class="insp-chips">${task.chips.map((chip) => html`<${Chip} d=${chip} />`)}</p>` : null}
          </li>`)}
        </ul>
      </section>`) : html`<p class="insp-muted">${t('inspector.tasks.noTasks')}</p>`}
    <//>
  </div>`;
}
