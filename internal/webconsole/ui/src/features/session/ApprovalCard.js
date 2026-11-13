// Inline Plan Mode approval card (spec/17-web-console-frontend.md §7.3,
// spec/17-web-console.md §7.13). Rendered after the stream while
// plan_mode.status === 'awaiting_approval' (and, as a compact receipt card,
// while a saved approval is unresolved or belongs to the displayed plan). This is the only place with
// approval buttons. Approval target, coverage override, stale review and
// receipts are handled by state/session-actions.js approvePlan(). Actions
// follow controls/composer-model.js approvalCardState(): while a revision is
// being sent, the planning run settles or a new run launches, approve and
// cancel are disabled and approval-settling (data-reason) explains why.
// Test ids: approval-card, approval-settling, approve-plan, revise-plan, cancel-plan,
// approval-plan-toggle, approval-receipt, approval-check, approval-retry, approval-continue.
import { html, useEffect, useId, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { renderMarkdown } from '../../lib/markdown.js';
import { useStore } from '../../state/hooks.js';
import { approvePlan, cancelPlan, revisePlan, RESTORE_APPROVAL_PHASES } from '../../state/session-actions.js';
import { Button } from '../../ui/Button.js';
import { Icon } from '../../ui/Icon.js';
import { Spinner } from '../../ui/Spinner.js';
import { ApprovalReceipt } from './controls/ApprovalReceipt.js';
import { APPROVAL_WAIT_KEYS, approvalCardState } from './controls/composer-model.js';

const LONG_PLAN_CHARS = 1400;
const LONG_PLAN_LINES = 28;

function isLongPlan(text) {
  const value = String(text || '');
  return value.length > LONG_PLAN_CHARS || value.split('\n').length > LONG_PLAN_LINES;
}

function list(items) {
  return (Array.isArray(items) ? items : []).map((item) => String(item ?? '').trim()).filter(Boolean);
}

/** The saved record belongs to the displayed target (or is still unresolved). */
function receiptApplies(record, plan) {
  if (!record) return false;
  if (RESTORE_APPROVAL_PHASES.includes(record.phase)) return true;
  return record.parameters?.plan_mode_id === plan?.plan_mode_id && record.parameters?.plan_version === plan?.plan_version;
}

function FactList({ title, items, icon }) {
  if (!items.length) return null;
  return html`<div class="approval-facts">
    <h4 class="approval-facts-title"><${Icon} name=${icon} size=${14} />${title}</h4>
    <ul class="approval-facts-list" translate="no">
      ${items.map((item, index) => html`<li key=${index}>${item}</li>`)}
    </ul>
  </div>`;
}

export function ApprovalCard() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    sessionId: state.session.id,
    plan: state.session.detail?.plan_mode || null,
    goal: state.session.detail?.goal || null,
    status: state.session.detail?.state?.status || '',
    approval: state.session.approval,
    pendingAction: state.session.pendingAction,
    revising: state.composer.revising,
    // Flat primitives so the shallow store equality skips unrelated updates.
    ...approvalCardState(state),
  }));
  const [expanded, setExpanded] = useState(false);
  const planBodyId = useId();
  const headingId = useId();
  const waitId = useId();
  const plan = view.plan;
  const awaiting = plan?.status === 'awaiting_approval';
  const markdown = String(plan?.plan_markdown || '');
  const long = isLongPlan(markdown);

  useEffect(() => setExpanded(false), [plan?.plan_mode_id, plan?.plan_version]);

  const record = view.approval?.record || null;
  const showReceipt = Boolean(view.approval?.error) || receiptApplies(record, plan);
  if (!awaiting) {
    // Unresolved receipts, or the settled receipt of the plan still displayed (e.g. admitted v2).
    if (!receiptApplies(record, plan)) return null;
    return html`<section class="approval-card is-receipt" data-testid="approval-card" data-variant="receipt" aria-labelledby=${headingId}>
      <h3 class="approval-eyebrow" id=${headingId} tabindex="-1" data-card-focus>
        <${Icon} name="clipboard-list" size=${14} />${t('composer.approval.receiptHeading')}
      </h3>
      <${ApprovalReceipt} sessionId=${view.sessionId} approval=${view.approval} loadedStatus=${view.status} pending=${view.pendingAction === 'approval-check'} />
    </section>`;
  }

  // Same launch-window rule as the composer: approve/cancel would be rejected
  // (409) while a revision is sent, the planning run settles or a run launches.
  const busy = view.approving;
  const wait = view.wait;
  const assumptions = list(plan.assumptions);
  const risks = list(plan.risks);
  const verification = list(plan.verification);
  const linkedGoal = plan.linked_goal_id && view.goal?.objective ? view.goal : null;

  return html`<section
    class="approval-card"
    data-testid="approval-card"
    data-plan-version=${plan.plan_version || ''}
    aria-labelledby=${headingId}
  >
    <header class="approval-head">
      <p class="approval-eyebrow">
        <${Icon} name="clipboard-list" size=${14} />
        <span>${t('composer.approval.eyebrow')}</span>
        ${plan.plan_version ? html`<span class="badge tone-warning approval-version">${t('composer.approval.version', { version: plan.plan_version })}</span>` : null}
      </p>
      <h3 class="approval-title" id=${headingId} tabindex="-1" data-card-focus>
        ${plan.summary ? html`<span translate="no">${plan.summary}</span>` : t('composer.approval.untitled')}
      </h3>
      ${plan.objective && plan.objective !== plan.summary ? html`<p class="approval-objective">
        <span class="approval-label">${t('composer.approval.objective')}</span>
        <span translate="no">${plan.objective}</span>
      </p>` : null}
    </header>

    ${markdown ? html`<div class="approval-plan">
      <div
        id=${planBodyId}
        class=${`approval-plan-body md ${long && !expanded ? 'is-collapsed' : ''}`}
        translate="no"
        dangerouslySetInnerHTML=${{ __html: renderMarkdown(markdown, { t }) }}
      ></div>
      ${long ? html`<button
        type="button"
        class="approval-plan-toggle"
        aria-expanded=${String(expanded)}
        aria-controls=${planBodyId}
        data-testid="approval-plan-toggle"
        onClick=${() => setExpanded(!expanded)}
      >
        <${Icon} name=${expanded ? 'chevron-up' : 'chevron-down'} size=${14} />
        ${expanded ? t('composer.approval.collapse') : t('composer.approval.expand')}
      </button>` : null}
    </div>` : null}

    ${assumptions.length || risks.length || verification.length ? html`<div class="approval-facts-grid">
      <${FactList} title=${t('composer.approval.assumptions')} items=${assumptions} icon="info" />
      <${FactList} title=${t('composer.approval.risks')} items=${risks} icon="alert-triangle" />
      <${FactList} title=${t('composer.approval.verification')} items=${verification} icon="list-checks" />
    </div>` : null}

    ${linkedGoal ? html`<p class="approval-goal">
      <${Icon} name="target" size=${14} />
      <span class="approval-label">${t('composer.approval.linkedGoal')}</span>
      <span class="approval-goal-text" translate="no">${linkedGoal.objective}</span>
    </p>` : null}

    ${showReceipt ? html`<${ApprovalReceipt} sessionId=${view.sessionId} approval=${view.approval} loadedStatus=${view.status} pending=${view.pendingAction === 'approval-check'} />`
      : html`<div class="approval-receipt-region" role="status" aria-live="polite"></div>`}

    ${wait ? html`<p class="approval-settling" id=${waitId} role="status" data-testid="approval-settling" data-reason=${wait}><${Spinner} size=${14} />${t(APPROVAL_WAIT_KEYS[wait])}</p>` : null}
    <footer class="approval-actions">
      <${Button}
        variant="primary"
        icon="play"
        pending=${busy}
        disabled=${view.approveDisabled}
        aria-describedby=${wait ? waitId : undefined}
        data-testid="approve-plan"
        onClick=${() => approvePlan(view.sessionId)}
      >${busy ? t('composer.approval.approving') : t('composer.approval.approve')}<//>
      <${Button}
        variant="secondary"
        icon="pencil"
        aria-pressed=${String(Boolean(view.revising))}
        disabled=${view.reviseDisabled}
        data-testid="revise-plan"
        onClick=${() => revisePlan(view.sessionId)}
      >${t('composer.approval.revise')}<//>
      <span class="approval-actions-spacer"></span>
      <${Button}
        variant="ghost"
        class="is-danger-text"
        icon="x"
        pending=${view.pendingAction === 'cancel-plan'}
        disabled=${view.cancelDisabled}
        aria-describedby=${wait ? waitId : undefined}
        data-testid="cancel-plan"
        onClick=${() => cancelPlan(view.sessionId)}
      >${t('composer.approval.cancel')}<//>
    </footer>
  </section>`;
}
