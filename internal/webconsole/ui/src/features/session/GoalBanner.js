// Compact Goal banner at the top of the stream: objective (raw), status,
// progress and budget, a 详情 link to the inspector Goal tab and a menu with
// pause / resume / complete / clear (complete and clear confirm first).
// A completed goal on a session that keeps running shows as historical.
// Test ids: goal-banner, goal-details, goal-menu, goal-pause, goal-resume,
// goal-complete, goal-clear.
import { html } from '../../lib/html.js';
import { formatNumber, t, tx } from '../../lib/i18n.js';
import { goalRuntimeFacts, goalStatusLabel } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { setInspectorTab, toggleInspector } from '../../state/actions.js';
import { clearGoal, completeGoal, pauseGoal, resumeGoal } from '../../state/session-actions.js';
import { Badge } from '../../ui/Badge.js';
import { Button } from '../../ui/Button.js';
import { Icon } from '../../ui/Icon.js';
import { Menu } from '../../ui/Menu.js';

const TONES = { active: 'brand', paused: 'warning', budget_limited: 'warning', complete: 'success', cleared: 'neutral' };

function statusText(goal, status) {
  switch (status) {
    case 'active': return t('composer.goal.status.active');
    case 'paused': return t('composer.goal.status.paused');
    case 'budget_limited': return t('composer.goal.status.budgetLimited');
    case 'complete': return t('composer.goal.status.complete');
    case 'cleared': return t('composer.goal.status.cleared');
    default: return tx(goalStatusLabel(goal));
  }
}

function openGoalDetails() {
  setInspectorTab('goal');
  toggleInspector(true);
}

export function GoalBanner() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    sessionId: state.session.id,
    detail: state.session.detail,
    pending: state.session.pendingAction === 'goal',
  }));
  const facts = goalRuntimeFacts(view.detail);
  if (!facts?.goal?.objective) return null;
  const goal = facts.goal;
  const status = String(goal.status || 'active').toLowerCase();
  const historical = facts.historical;
  const tokensBudget = Number(goal.token_budget || 0);
  const meta = [
    facts.progressCount ? t('composer.goal.progressCount', { count: facts.progressCount }) : '',
    tokensBudget > 0 ? t('composer.goal.tokens', { used: formatNumber(goal.tokens_used || 0), budget: formatNumber(tokensBudget) }) : '',
  ].filter(Boolean);
  const id = view.sessionId;
  const items = [
    status === 'active' ? { id: 'pause', label: t('composer.goal.pause'), icon: 'pause', onSelect: () => pauseGoal(id), testId: 'goal-pause' } : null,
    status === 'paused' || status === 'budget_limited' ? { id: 'resume', label: t('composer.goal.resume'), icon: 'play', onSelect: () => resumeGoal(id), testId: 'goal-resume' } : null,
    status !== 'complete' ? { id: 'complete', label: t('composer.goal.complete'), icon: 'check', onSelect: () => completeGoal(id), testId: 'goal-complete' } : null,
    { id: 'details', label: t('composer.goal.details'), icon: 'panel-right', onSelect: openGoalDetails, testId: 'goal-menu-details' },
    { separator: true },
    { id: 'clear', label: t('composer.goal.clear'), icon: 'trash', danger: true, onSelect: () => clearGoal(id), testId: 'goal-clear' },
  ].filter(Boolean);

  return html`<section
    class=${`goal-banner ${historical ? 'is-historical' : ''}`}
    data-testid="goal-banner"
    data-status=${status}
    aria-label=${t('composer.goal.label')}
    aria-busy=${view.pending ? 'true' : undefined}
  >
    <span class="goal-banner-icon" aria-hidden="true"><${Icon} name="target" size=${16} /></span>
    <div class="goal-banner-main">
      <p class="goal-banner-line">
        <span class="goal-banner-label">${historical ? t('composer.goal.historical') : t('composer.goal.label')}</span>
        <span class="goal-banner-objective" translate="no" title=${goal.objective}>${goal.objective}</span>
      </p>
      <p class="goal-banner-meta">
        <${Badge} tone=${TONES[status] || 'neutral'} dot>${statusText(goal, status)}<//>
        ${meta.map((text) => html`<span class="goal-banner-fact">${text}</span>`)}
      </p>
    </div>
    <div class="goal-banner-actions">
      <${Button} size="sm" variant="ghost" class="goal-banner-details" data-testid="goal-details" onClick=${openGoalDetails}>${t('composer.goal.details')}<//>
      <${Menu} label=${t('composer.goal.menu')} testId="goal-menu" items=${items} disabled=${view.pending} />
    </div>
  </section>`;
}
