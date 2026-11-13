// Goal tab: objective, status and usage, controls (pause / resume / complete
// / approve goal plan / clear via state/session-actions.js; a linked Plan
// Mode awaiting approval links to the inline approval card instead, see
// inspector-model.js goalPlanApprovalEntry), runtime facts,
// coverage and linked facts, completion audit, success criteria, validation
// plan, mission plan (features / milestones / roles), recent progress and
// goal history. A completed goal on a session that keeps running is shown as
// historical (spec 17 §7.6).
import { html, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import {
  formatBudget, formatSecondsBudget, goalRuntimeFacts, goalStatusLabel, isHistoricalCompletedGoal, maybeArray, phaseLabel, shortId,
} from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { approvePlan, clearGoal, completeGoal, pauseGoal, resumeGoal } from '../../state/session-actions.js';
import { Badge } from '../../ui/Badge.js';
import { Button } from '../../ui/Button.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { eventTypeLabelFor, goalActions, goalCoverage, goalHistory, goalItemTitle, goalLinkedFacts, goalPlanApprovalEntry, goalProgress } from './inspector-model.js';
import { Fact, Facts, Metrics, Notice, Raw, RawChips, Section, SessionLink, StatusPill, TimeText, Txt, num, revealInlineCard, statusText, text } from './parts.js';

function LinkedFacts({ item, mode }) {
  const facts = goalLinkedFacts(item);
  if (!facts.tasks.length && !facts.children.length && !facts.jobs.length && !facts.evaluatorCount) return null;
  return html`<p class="insp-card-meta insp-linked">
    ${facts.tasks.length ? html`<span><span class="insp-meta-label">${t('inspector.goal.linkedTasks')}</span> <${Raw} class="insp-mono">${facts.tasks.join(', ')}<//></span>` : null}
    ${facts.children.length ? html`<span><span class="insp-meta-label">${t('inspector.goal.linkedChildren')}</span> ${facts.children.map((id) => html`<${SessionLink} id=${id} mode=${mode}><span translate="no">${shortId(id)}</span><//>`)}</span>` : null}
    ${facts.jobs.length ? html`<span><span class="insp-meta-label">${t('inspector.goal.linkedJobs')}</span> <${Raw} class="insp-mono">${facts.jobs.map(shortId).join(', ')}<//></span>` : null}
    ${facts.evaluatorCount ? html`<span><span class="insp-meta-label">${t('inspector.goal.evaluator', { count: facts.evaluatorCount })}</span>${facts.evaluator.length ? html` <${Raw} class="insp-mono">${facts.evaluator.map(shortId).join(', ')}<//>` : null}</span>` : null}
  </p>`;
}

function GoalItems({ title, items, kind, mode, testId }) {
  const list = maybeArray(items);
  if (!list.length) return null;
  return html`<${Section} title=${title} testId=${testId}>
    <ul class="insp-list" role="list">
      ${list.map((item) => {
        const label = goalItemTitle(item, kind);
        const status = String(item?.status || 'pending');
        const meta = kind === 'validation' ? item?.kind : kind === 'role' ? [item?.role, item?.scope].filter(Boolean).join(' · ') : item?.milestone_id;
        return html`<li class="insp-list-item" data-goal-item=${item?.id || undefined}>
          <div class="insp-list-head">
            <span class="insp-list-title">${label ? html`<${Raw}>${label}<//>` : t('inspector.goal.untitledItem')}</span>
            ${kind !== 'role' ? html`<${StatusPill} status=${status} />` : null}
          </div>
          ${meta ? html`<p class="insp-card-meta"><${Raw}>${meta}<//></p>` : null}
          ${kind === 'feature' && item?.description && item.description !== label ? html`<p class="insp-card-copy"><${Raw}>${item.description}<//></p>` : null}
          <${LinkedFacts} item=${item} mode=${mode} />
          ${maybeArray(item?.evidence).length ? html`<ul class="insp-bullets is-compact" role="list">${item.evidence.map((value) => html`<li><${Raw}>${value}<//></li>`)}</ul>` : null}
        </li>`;
      })}
    </ul>
  <//>`;
}

function GoalControls({ sessionId, detail, mode }) {
  const actions = goalActions(detail);
  // One approval path: a pending linked Plan Mode is approved on the inline card.
  const approvalEntry = goalPlanApprovalEntry(detail);
  const [pending, setPending] = useState('');
  // session-actions.js owns confirmation (complete / clear), toasts and the
  // follow-up refresh; the tab only tracks which control is in flight.
  const run = async (name, fn) => {
    if (pending) return;
    setPending(name);
    try {
      await fn();
    } finally {
      setPending('');
    }
  };
  const onClear = () => run('clear', () => clearGoal(sessionId));
  const any = actions.canPause || actions.canResume || actions.canComplete || actions.canApprovePlan || approvalEntry === 'inline' || actions.canClear;
  if (!any) return null;
  return html`<div class="insp-actions" role="group" aria-label=${t('inspector.goal.controls')} data-testid="inspector-goal-actions">
    ${actions.canPause ? html`<${Button} size="sm" variant="secondary" icon="pause" pending=${pending === 'pause'} disabled=${Boolean(pending)} onClick=${() => run('pause', () => pauseGoal(sessionId))} data-testid="inspector-goal-pause">${t('inspector.goal.pause')}<//>` : null}
    ${actions.canResume ? html`<${Button} size="sm" variant="secondary" icon="play" pending=${pending === 'resume'} disabled=${Boolean(pending)} onClick=${() => run('resume', () => resumeGoal(sessionId))} data-testid="inspector-goal-resume">${t('inspector.goal.resume')}<//>` : null}
    ${actions.canComplete ? html`<${Button} size="sm" variant="secondary" icon="check" pending=${pending === 'complete'} disabled=${Boolean(pending)} onClick=${() => run('complete', () => completeGoal(sessionId))} data-testid="inspector-goal-complete">${t('inspector.goal.complete')}<//>` : null}
    ${approvalEntry === 'inline' ? html`<${Button} size="sm" variant="secondary" iconRight="arrow-down" onClick=${() => revealInlineCard('approval-card', mode)} data-testid="inspector-goal-goto-approval">${t('inspector.plan.gotoApproval')}<//>` : null}
    ${actions.canApprovePlan ? html`<${Button} size="sm" variant="secondary" icon="list-checks" pending=${pending === 'approve'} disabled=${Boolean(pending)} onClick=${() => run('approve', () => approvePlan(sessionId, { entrypoint: 'mission' }))} data-testid="inspector-goal-approve-plan">${t('inspector.goal.approvePlan')}<//>` : null}
    ${actions.canClear ? html`<${Button} size="sm" variant="ghost" class="insp-danger-text" icon="trash" pending=${pending === 'clear'} disabled=${Boolean(pending)} onClick=${onClear} data-testid="inspector-goal-clear">${t('inspector.goal.clear')}<//>` : null}
  </div>`;
}

export function GoalTab({ sessionId, mode = 'panel' }) {
  const view = useStore((state) => ({ locale: state.ui.locale, detail: state.session.detail }));
  const detail = view.detail;
  const goal = detail?.goal || null;
  if (!goal) {
    return html`<${EmptyState} icon="target" title=${t('inspector.goal.emptyTitle')} body=${t('inspector.goal.emptyBody')} data-testid="inspector-goal-empty" />`;
  }
  const facts = goalRuntimeFacts(detail);
  const historical = isHistoricalCompletedGoal(detail);
  const status = String(goal.status || 'active');
  const coverage = goalCoverage(detail);
  const goalFacts = detail.goal_facts || {};
  const audit = goal.completion_audit || null;
  const mission = goal.mission || null;
  const progress = goalProgress(detail, 3);
  const history = goalHistory(detail, 6);
  const unresolvedChildren = maybeArray(goalFacts.unresolved_child_session_ids);
  const unresolvedJobs = maybeArray(goalFacts.unresolved_queue_job_ids);
  return html`<div class="insp-tab insp-goal" data-testid="inspector-goal" data-goal-status=${status}>
    <header class="insp-tab-head">
      <div class="insp-tab-head-row">
        <${StatusPill} status=${status} label=${text(goalStatusLabel(goal))} testId="inspector-goal-status" />
        ${historical ? html`<${Badge} tone="neutral" data-testid="inspector-goal-historical">${t('inspector.goal.historical')}<//>` : null}
        ${goal.mode ? html`<${Badge} tone="neutral"><span translate="no">${goal.mode}</span><//>` : null}
        ${goal.goal_id ? html`<span class="insp-raw insp-mono insp-id" translate="no" title=${goal.goal_id}>${shortId(goal.goal_id)}</span>` : null}
      </div>
      ${goal.objective ? html`<p class="insp-objective" data-testid="inspector-goal-objective"><${Raw}>${goal.objective}<//></p>` : null}
    </header>
    ${historical ? html`<${Notice} tone="neutral" testId="inspector-goal-historical-note">${t('inspector.goal.historicalNote')}<//>` : null}
    <${Metrics} items=${[
      { label: t('inspector.goal.tokens'), value: formatBudget(goal.tokens_used, goal.token_budget), testId: 'inspector-goal-tokens' },
      { label: t('inspector.goal.providerTime'), value: formatSecondsBudget(goal.provider_time_used_seconds ?? goal.time_used_seconds, goal.provider_time_budget_seconds ?? goal.time_budget_seconds) },
      { label: t('inspector.goal.progressCount'), value: num(facts?.progressCount || 0) },
    ]} />
    <${GoalControls} sessionId=${sessionId} detail=${detail} mode=${mode} />

    <${Section} title=${t('inspector.goal.runtime')} testId="inspector-goal-runtime">
      <${Facts}>
        <${Fact} label=${t('inspector.goal.sessionStatus')}>${facts?.runStatus ? statusText(facts.runStatus) : null}<//>
        <${Fact} label=${t('inspector.fact.phase')}>${facts?.runPhase ? text(phaseLabel(facts.runPhase)) : null}<//>
        <${Fact} label=${t('inspector.goal.latestEvent')}>${facts?.latestType ? html`<span>${text(eventTypeLabelFor(facts.latestType))}${facts.latestAt ? html` · <${TimeText} at=${facts.latestAt} />` : null}</span>` : null}<//>
        <${Fact} label=${t('inspector.goal.source')}>${goal.source ? html`<${Raw}>${goal.source}<//>` : null}<//>
        <${Fact} label=${t('inspector.goal.updated')}>${goal.updated_at ? html`<${TimeText} at=${goal.updated_at} />` : null}<//>
        <${Fact} label=${t('inspector.goal.coverage')} testId="inspector-goal-coverage">${coverage && coverage.total ? t('inspector.goal.coverageValue', { covered: coverage.covered, total: coverage.total }) : null}<//>
        <${Fact} label=${t('inspector.goal.evaluatorEvidence')}>${Number(goalFacts.evaluator_evidence_count || 0) ? num(goalFacts.evaluator_evidence_count) : null}<//>
        <${Fact} label=${t('inspector.goal.unresolved')}>${unresolvedChildren.length || unresolvedJobs.length ? t('inspector.coord.childrenJobs', { children: unresolvedChildren.length, jobs: unresolvedJobs.length }) : null}<//>
        <${Fact} label=${t('inspector.goal.blocker')}>${goalFacts.latest_blocker ? html`<${Raw}>${goalFacts.latest_blocker}<//>` : null}<//>
        <${Fact} label=${t('inspector.goal.linkedChildren')}>${maybeArray(goalFacts.linked_child_session_ids).length ? html`<span class="insp-link-list">${goalFacts.linked_child_session_ids.map((id) => html`<${SessionLink} id=${id} mode=${mode}><span translate="no">${shortId(id)}</span><//>`)}</span>` : null}<//>
        <${Fact} label=${t('inspector.goal.linkedJobs')}>${maybeArray(goalFacts.linked_queue_job_ids).length ? html`<${RawChips} values=${goalFacts.linked_queue_job_ids.map(shortId)} />` : null}<//>
      <//>
      ${coverage?.blocked ? html`<${Notice} tone="warning" testId="inspector-goal-coverage-blocked" title=${t('inspector.goal.coverageBlocked')}>
        ${t('inspector.goal.coverageBlockedBody')}
        ${coverage.uncovered.length ? html`<p class="insp-card-meta"><span class="insp-meta-label">${t('inspector.goal.uncovered')}</span> <${Raw} class="insp-mono">${coverage.uncovered.join(', ')}<//></p>` : null}
      <//>` : null}
    <//>

    ${audit ? html`<${Section} title=${t('inspector.goal.audit')} testId="inspector-goal-audit">
      ${audit.summary ? html`<p class="insp-prose-text"><${Raw}>${audit.summary}<//></p>` : null}
      <p class="insp-card-meta">
        ${audit.completed_by ? html`<span><span class="insp-meta-label">${t('inspector.goal.completedBy')}</span> <${Raw}>${audit.completed_by}<//></span>` : null}
        ${audit.completed_by && audit.completed_at ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}
        <${TimeText} at=${audit.completed_at} />
      </p>
      ${maybeArray(audit.evidence).length ? html`<ul class="insp-bullets" role="list" data-testid="inspector-goal-evidence">${audit.evidence.map((value) => html`<li><${Raw}>${value}<//></li>`)}</ul>` : null}
    <//>` : null}

    <${GoalItems} title=${t('inspector.goal.criteria')} items=${goal.success_criteria} kind="text" mode=${mode} testId="inspector-goal-criteria" />
    <${GoalItems} title=${t('inspector.goal.validation')} items=${goal.validation_plan} kind="validation" mode=${mode} testId="inspector-goal-validation" />

    ${mission ? html`<${Section} title=${t('inspector.goal.plan')} testId="inspector-goal-mission">
      <p class="insp-card-meta">
        <${StatusPill} status=${mission.plan_status || 'draft'} label=${text({ key: 'planStatus.' + (mission.plan_status || 'draft'), params: { fallback: mission.plan_status || 'draft' } })} />
        ${mission.approved_at ? html`<span>${t('inspector.goal.approvedAt')} <${TimeText} at=${mission.approved_at} /></span>` : null}
      </p>
      ${!maybeArray(mission.features).length && !maybeArray(mission.milestones).length && !maybeArray(mission.role_plan).length ? html`<p class="insp-muted">${t('inspector.goal.planEmpty')}</p>` : null}
    <//>` : null}
    ${mission ? html`<${GoalItems} title=${t('inspector.goal.features')} items=${mission.features} kind="feature" mode=${mode} />` : null}
    ${mission ? html`<${GoalItems} title=${t('inspector.goal.milestones')} items=${mission.milestones} kind="milestone" mode=${mode} />` : null}
    ${mission ? html`<${GoalItems} title=${t('inspector.goal.roles')} items=${mission.role_plan} kind="role" mode=${mode} />` : null}

    ${progress.length ? html`<${Section} title=${t('inspector.goal.recentProgress')} testId="inspector-goal-progress">
      <ul class="insp-list" role="list">${progress.map((item) => html`<li class="insp-list-item">
        <div class="insp-list-head">
          <span class="insp-list-title">${item.summary ? html`<${Raw}>${item.summary}<//>` : t('inspector.goal.progressFallback')}</span>
          ${item.kind ? html`<${Badge} tone="neutral"><span translate="no">${item.kind}</span><//>` : null}
        </div>
        ${maybeArray(item.evidence).length ? html`<ul class="insp-bullets is-compact" role="list">${item.evidence.map((value) => html`<li><${Raw}>${value}<//></li>`)}</ul>` : null}
        ${maybeArray(item.blockers).length ? html`<p class="insp-card-meta"><span class="insp-meta-label">${t('inspector.goal.blockers')}</span> <${Raw}>${item.blockers.join(' · ')}<//></p>` : null}
        <${LinkedFacts} item=${item} mode=${mode} />
        <p class="insp-card-meta"><${TimeText} at=${item.created_at} /></p>
      </li>`)}</ul>
    <//>` : null}

    ${history.length ? html`<${Section} title=${t('inspector.goal.history')} testId="inspector-goal-history">
      <ol class="insp-history">${history.map((entry) => html`<li class="insp-history-item">
        <span class="insp-dot tone-neutral" aria-hidden="true"></span>
        <div class="insp-history-body">
          <p class="insp-history-title">${text(eventTypeLabelFor(entry.type))}</p>
          <p class="insp-card-meta">
            ${entry.status ? html`<span>${text({ key: 'goalStatus.' + entry.status, params: { fallback: entry.status } })}</span><span class="insp-sep" aria-hidden="true">·</span>` : null}
            ${entry.source ? html`<${Raw}>${entry.source}<//><span class="insp-sep" aria-hidden="true">·</span>` : null}
            <${TimeText} at=${entry.created_at} />
          </p>
        </div>
      </li>`)}</ol>
    <//>` : null}
  </div>`;
}
