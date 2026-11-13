// PLACEHOLDER — owned by the approvals feature worker.
// Contract (spec §7.3): export function ApprovalCard(); renders only while
// session.detail.plan_mode.status === 'awaiting_approval': summary, version,
// Markdown plan, assumptions/risks/validation and actions (approve & run /
// request changes / cancel) through approval-operations.js and
// session-actions approvePlan/revisePlan/cancelPlan. Test id: "approval-card".
import { html } from '../../lib/html.js';
import { useStore } from '../../state/hooks.js';

export function ApprovalCard() {
  const plan = useStore((state) => state.session.detail?.plan_mode || null, Object.is);
  if (plan?.status !== 'awaiting_approval') return null;
  return html`<section class="inline-card tone-warning" data-testid="approval-card">
    <p class="inline-card-title" translate="no">${plan.summary || ''}</p>
  </section>`;
}
