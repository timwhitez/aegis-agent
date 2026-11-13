// Cards shared by the Overview and Agents tabs: child budget inspector,
// parent-coordination / checkpoint recovery facts, background notification
// and sub-agent rows. Lib descriptors are rendered with Txt/text(); durable
// values stay in translate="no" nodes.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { backgroundNotificationView, effectiveBudgetSummary, shortId } from '../../lib/session-model.js';
import { budgetInspector } from './inspector-model.js';
import { Fact, Facts, Raw, RawChips, SessionLink, StatusPill, TimeText, Txt, duration, num, statusText, text } from './parts.js';

const COORD_STATE_KEYS = {
  parked: 'inspector.coord.state.parked',
  waiting: 'inspector.coord.state.waiting',
  ready: 'inspector.coord.state.ready',
};

/** Inline list of effectiveBudgetSummary() parts. */
export function BudgetSummary({ budget, pauseReason = '' }) {
  if (!budget) return null;
  const summary = effectiveBudgetSummary(budget, pauseReason);
  return html`<p class="insp-card-meta insp-budget-line" data-testid="inspector-budget-summary">
    <span class="insp-meta-label">${t('inspector.budget.label')}</span>
    ${summary.parts.map((part, index) => html`${index ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}<${Txt} d=${part} />`)}
  </p>`;
}

/** Read-only child budget inspector: limit / used / remaining per dimension. */
export function BudgetInspector({ budget, pauseReason = '' }) {
  const view = budgetInspector(budget, pauseReason);
  if (!view) return null;
  if (view.disabled) {
    return html`<div class="insp-budget" data-testid="inspector-budget"><p class="insp-muted">${text({ key: 'budget.offSnapshotted' })}</p></div>`;
  }
  const rows = [
    view.turns ? { label: t('inspector.budget.turns'), limit: num(view.turns.limit), used: num(view.turns.used), remaining: num(view.turns.remaining) } : null,
    view.runtime ? { label: t('inspector.budget.activeRuntime'), limit: duration(view.runtime.limit), used: duration(view.runtime.used), remaining: duration(view.runtime.remaining) } : null,
  ].filter(Boolean);
  return html`<div class="insp-budget" data-testid="inspector-budget">
    ${rows.length ? html`<table class="insp-table">
      <thead><tr>
        <th scope="col"><span class="sr-only">${t('inspector.budget.dimension')}</span></th>
        <th scope="col">${t('inspector.budget.limit')}</th>
        <th scope="col">${t('inspector.budget.used')}</th>
        <th scope="col">${t('inspector.budget.remaining')}</th>
      </tr></thead>
      <tbody>${rows.map((row) => html`<tr>
        <th scope="row">${row.label}</th><td>${row.limit}</td><td>${row.used}</td><td>${row.remaining}</td>
      </tr>`)}</tbody>
    </table>` : null}
    <${Facts}>
      <${Fact} label=${t('inspector.budget.status')}>${text({ key: 'budgetStatus.' + view.status, params: { fallback: statusText(view.status) } })}<//>
      <${Fact} label=${t('inspector.budget.attempt')}>${view.attempt ? num(view.attempt) : null}<//>
      <${Fact} label=${t('inspector.budget.deadline')}>${view.deadline ? html`<${TimeText} at=${view.deadline} />` : null}<//>
      <${Fact} label=${t('inspector.budget.totals')}>${view.totals.turns || view.totals.runtime ? t('inspector.budget.totalsValue', { turns: num(view.totals.turns), runtime: duration(view.totals.runtime) }) : null}<//>
      <${Fact} label=${t('inspector.budget.checkpoint')}>${view.checkpointAt ? html`<span><${TimeText} at=${view.checkpointAt} /> · ${view.leaseOpen ? t('inspector.budget.leaseOpen') : t('inspector.budget.leaseClosed')}</span>` : null}<//>
      <${Fact} label=${t('inspector.budget.recovery')}>${view.recoveryMs ? html`<span>+${duration(view.recoveryMs)}${view.recoveryAt ? html` · <${TimeText} at=${view.recoveryAt} />` : null}</span>` : null}<//>
      <${Fact} label=${t('inspector.budget.source')}>${view.source ? html`<${Raw}>${view.source}<//>` : null}<//>
      <${Fact} label=${t('inspector.budget.lastReason')}>${view.lastReason ? html`<${Raw}>${view.lastReason}<//>` : null}<//>
    <//>
  </div>`;
}

/** Ids rendered as session links (children) or raw chips (queue jobs). */
export function IdList({ label, ids, link = false, mode }) {
  const list = (ids || []).filter(Boolean);
  if (!list.length) return null;
  return html`<p class="insp-card-meta insp-id-list">
    <span class="insp-meta-label">${label}</span>
    ${list.map((id) => (link
      ? html`<${SessionLink} id=${id} mode=${mode}><span translate="no">${shortId(id)}</span><//>`
      : html`<span class="insp-raw insp-mono" translate="no" title=${id}>${shortId(id)}</span>`))}
  </p>`;
}

/** Parent coordination: this session waiting on its children / queue jobs. */
export function CoordinationCard({ view, mode }) {
  if (!view) return null;
  const counts = ['unresolved', 'completed', 'failed', 'cancelled']
    .filter((name) => view.counts[name][0] || view.counts[name][1])
    .map((name) => ({ name, children: view.counts[name][0], jobs: view.counts[name][1] }));
  const countLabel = {
    unresolved: t('inspector.coord.unresolved'),
    completed: t('inspector.coord.completed'),
    failed: t('inspector.coord.failed'),
    cancelled: t('inspector.coord.cancelled'),
  };
  return html`<article class="insp-card" data-testid="inspector-coordination">
    <header class="insp-card-head">
      <span class="insp-card-title">${t('inspector.coord.title')}</span>
      <${StatusPill} status=${view.waitState} label=${t(COORD_STATE_KEYS[view.waitState] || 'inspector.coord.state.waiting')} />
    </header>
    <p class="insp-card-meta">
      <span class="insp-meta-label">${t('inspector.coord.waitMode')}</span> <${Raw}>${view.waitMode}<//>
      ${view.updatedAt ? html`<span class="insp-sep" aria-hidden="true">·</span><${TimeText} at=${view.updatedAt} />` : null}
    </p>
    ${counts.length ? html`<ul class="insp-count-list" role="list">${counts.map((item) => html`<li>
      <span>${countLabel[item.name]}</span>
      <span class="insp-count-value">${t('inspector.coord.childrenJobs', { children: item.children, jobs: item.jobs })}</span>
    </li>`)}</ul>` : null}
    <${IdList} label=${t('inspector.coord.unresolvedChildren')} ids=${view.unresolvedChildren} link mode=${mode} />
    <${IdList} label=${t('inspector.coord.unresolvedJobs')} ids=${view.unresolvedJobs} />
  </article>`;
}

/** Long-run checkpoint recovery facts. */
export function CheckpointCard({ view, mode }) {
  if (!view) return null;
  return html`<article class="insp-card" data-testid="inspector-checkpoint">
    <header class="insp-card-head">
      <span class="insp-card-title">${t('inspector.checkpoint.title')}</span>
      ${view.waitState ? html`<${StatusPill} status=${view.waitState} />` : null}
    </header>
    <p class="insp-card-meta">
      ${view.createdAt ? html`<span><span class="insp-meta-label">${t('inspector.checkpoint.created')}</span> <${TimeText} at=${view.createdAt} /></span>` : null}
      ${view.backgroundNotifications ? html`<span class="insp-sep" aria-hidden="true">·</span><span>${t('inspector.checkpoint.notifications', { count: view.backgroundNotifications })}</span>` : null}
    </p>
    <${IdList} label=${t('inspector.checkpoint.children')} ids=${view.unresolvedChildren} link mode=${mode} />
    <${IdList} label=${t('inspector.checkpoint.jobs')} ids=${view.unresolvedJobs} />
    ${view.hints.length ? html`<div class="insp-card-block">
      <p class="insp-meta-label">${t('inspector.checkpoint.hints')}</p>
      <ul class="insp-bullets" role="list">${view.hints.map((hint) => html`<li><${Raw}>${hint}<//></li>`)}</ul>
    </div>` : null}
  </article>`;
}

/** Background notification (pending delivery ≠ child still running). */
export function NotificationCard({ item, mode }) {
  const view = backgroundNotificationView(item);
  return html`<article class="insp-card" data-testid="inspector-notification" data-notification-id=${view.id} data-delivery=${item?.delivery_status || undefined}>
    <header class="insp-card-head">
      <${Txt} d=${view.title} class="insp-card-title" />
      <${StatusPill} status=${view.status} />
    </header>
    <p class=${`insp-card-copy ${item?.last_error ? 'is-error' : ''}`}><${Txt} d=${view.copy} /></p>
    ${view.pendingHint ? html`<p class="insp-hint" data-testid="inspector-notification-pending">${text(view.pendingHint)}</p>` : null}
    ${view.actions.length ? html`<p class="insp-card-meta">
      <span class="insp-meta-label">${t('inspector.notification.parentActions')}</span>
      ${view.actions.map((action, index) => html`${index ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}<span>${text(action)}</span>`)}
    </p>` : null}
    <${BudgetSummary} budget=${item?.effective_budget} pauseReason=${item?.effective_budget?.last_reason || ''} />
    <p class="insp-card-meta">
      <${Txt} d=${view.meta.id} class="insp-mono" />
      ${view.meta.deliveryStatus ? html`<span class="insp-sep" aria-hidden="true">·</span><span class="insp-meta-label">${t('inspector.notification.delivery')}</span> <${Txt} d=${view.meta.deliveryStatus} />` : null}
      ${view.meta.sessionStatus ? html`<span class="insp-sep" aria-hidden="true">·</span><span>${t('inspector.notification.sessionStatus', { status: text(view.meta.sessionStatus) })}</span>` : null}
      ${item?.created_at ? html`<span class="insp-sep" aria-hidden="true">·</span><${TimeText} at=${item.created_at} />` : null}
    </p>
    <${RawChips} values=${view.visiblePaths} />
    ${view.sessionId ? html`<div class="insp-card-actions">
      <${SessionLink} id=${view.sessionId} mode=${mode} testId="inspector-notification-open">${t('inspector.openChild')}<//>
    </div>` : null}
  </article>`;
}

/** Merged child session / queue job row (subAgentRowView output). */
export function SubAgentCard({ row, mode, currentSessionId = '' }) {
  const phase = row.phase?.key ? text(row.phase) : '';
  return html`<article class="insp-card" data-testid="inspector-agent-row" data-session-id=${row.sessionId || undefined} data-job-id=${row.jobId || undefined} data-status=${row.status}>
    <header class="insp-card-head">
      <${Txt} d=${row.label} class="insp-card-title" />
      <${StatusPill} status=${row.status} />
    </header>
    <p class="insp-card-meta">
      ${row.model?.raw ? html`<${Raw}>${row.model.raw}<//>` : null}
      ${row.model?.raw && (phase || row.phase?.raw) ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}
      ${phase ? html`<span>${phase}</span>` : row.phase?.raw ? html`<${Raw}>${row.phase.raw}<//>` : null}
    </p>
    ${row.error ? html`<p class="insp-card-copy is-error"><${Txt} d=${row.error} /></p>` : null}
    ${row.finalText ? html`<p class="insp-card-copy"><${Txt} d=${row.finalText} /></p>` : null}
    <${BudgetSummary} budget=${row.session?.effective_budget || row.job?.effective_budget} pauseReason=${row.session?.pause_reason || ''} />
    <p class="insp-card-meta">
      ${row.sessionId ? html`<span><span class="insp-meta-label">${t('inspector.agents.session')}</span> <span class="insp-raw insp-mono" translate="no" title=${row.sessionId}>${shortId(row.sessionId)}</span></span>` : null}
      ${row.sessionId && row.jobId ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}
      ${row.jobId ? html`<span><span class="insp-meta-label">${t('inspector.agents.job')}</span> <span class="insp-raw insp-mono" translate="no" title=${row.jobId}>${shortId(row.jobId)}</span></span>` : null}
      ${row.mode ? html`<span class="insp-sep" aria-hidden="true">·</span><${Raw}>${row.mode}<//>` : null}
    </p>
    <${RawChips} values=${row.visiblePaths} />
    ${row.sessionId || (row.parentSessionId && row.parentSessionId !== currentSessionId) ? html`<div class="insp-card-actions">
      ${row.sessionId ? html`<${SessionLink} id=${row.sessionId} mode=${mode} testId="inspector-agent-open">${t('inspector.openChild')}<//>` : null}
      ${row.parentSessionId && row.parentSessionId !== currentSessionId ? html`<${SessionLink} id=${row.parentSessionId} mode=${mode} testId="inspector-agent-parent">${t('inspector.openParent')}<//>` : null}
    </div>` : null}
  </article>`;
}
