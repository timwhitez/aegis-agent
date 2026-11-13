// PLACEHOLDER — owned by the approvals feature worker.
// Contract (spec §7.3): export function PlanInputCard(); renders only while
// session.detail.plan_mode.status === 'awaiting_user_input': single/multi
// choice options plus free-form "other", submit via session-actions
// answerPlanInput or cancel via cancelPlan. Test id: "plan-input-card".
import { html } from '../../lib/html.js';
import { useStore } from '../../state/hooks.js';

export function PlanInputCard() {
  const plan = useStore((state) => state.session.detail?.plan_mode || null, Object.is);
  if (plan?.status !== 'awaiting_user_input') return null;
  return html`<section class="inline-card tone-warning" data-testid="plan-input-card"></section>`;
}
