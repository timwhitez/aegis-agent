// PLACEHOLDER — owned by the session feature worker (goal area).
// Contract: export function GoalBanner(); renders when session.detail.goal is
// set: objective (translate="no"), status, budget/progress from
// session.detail.goal_facts, and pause/resume/complete via session-actions.
// Test id: "goal-banner".
import { html } from '../../lib/html.js';
import { useStore } from '../../state/hooks.js';

export function GoalBanner() {
  const goal = useStore((state) => state.session.detail?.goal || null, Object.is);
  if (!goal?.objective) return null;
  return html`<section class="goal-banner" data-testid="goal-banner">
    <span class="goal-banner-objective" translate="no">${goal.objective}</span>
  </section>`;
}
