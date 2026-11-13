// Approval receipt notice (aria-live): pending / unconfirmed / not found /
// prepared / admitted / rejected / recovery required / failed, with
// 查询回执 / 重试 / 继续会话 actions. Copy and actions come from
// approval-operations.js approvalNoticeState(); the request id stays raw.
import { html } from '../../../lib/html.js';
import { tx } from '../../../lib/i18n.js';
import { approvalNoticeState } from '../../../lib/approval-operations.js';
import { checkApproval, continueAfterApprovalRecovery, retryApproval } from '../../../state/session-actions.js';
import { Button } from '../../../ui/Button.js';
import { Icon } from '../../../ui/Icon.js';

const PHASE_TONE = {
  pending: 'info', unknown: 'warning', not_found: 'warning', prepared: 'warning',
  admitted: 'success', rejected: 'danger', recovery_required: 'danger', failed: 'danger',
};
const PHASE_ICON = { admitted: 'check', pending: 'loader', rejected: 'alert-circle', failed: 'alert-circle', recovery_required: 'alert-triangle' };

/** Renders the notice for { record, response, busy } (or nothing). Always keep the live region mounted. */
export function ApprovalReceipt({ sessionId, approval, loadedStatus = '', pending = false }) {
  const notice = approval?.record ? approvalNoticeState({ record: approval.record, response: approval.response, busy: Boolean(approval.busy) || pending, loadedStatus }) : { visible: false };
  const run = (action) => {
    if (action === 'check') checkApproval(sessionId);
    else if (action === 'retry') retryApproval(sessionId);
    else continueAfterApprovalRecovery();
  };
  return html`<div class="approval-receipt-region" role="status" aria-live="polite">
    ${approval?.error ? html`<p class="approval-receipt is-error" data-testid="approval-receipt-error">
      <${Icon} name="alert-circle" size=${16} />
      <span>${approval.error.raw !== undefined ? approval.error.raw : tx(approval.error)}</span>
    </p>` : null}
    ${notice.visible ? html`<div
      class=${`approval-receipt tone-${PHASE_TONE[notice.phase] || 'warning'}`}
      data-testid="approval-receipt"
      data-approval-operation-phase=${notice.phase}
      data-approval-request-id=${notice.requestID.raw}
    >
      <span class="approval-receipt-icon"><${Icon} name=${PHASE_ICON[notice.phase] || 'info'} size=${16} /></span>
      <div class="approval-receipt-body">
        <p class="approval-receipt-title">
          <span>${tx(notice.title)}</span>
          <code class="approval-receipt-id" translate="no" title=${notice.requestID.raw}>${notice.requestID.raw}</code>
        </p>
        <p class="approval-receipt-message">${tx(notice.message)}</p>
        ${notice.serverNotice ? html`<p class="approval-receipt-server" translate="no">${notice.serverNotice.raw}</p>` : null}
        ${notice.currentStatus ? html`<p class="approval-receipt-status">
          ${tx(notice.currentStatus.key)}<span aria-hidden="true">: </span><strong>${tx(notice.currentStatus.statusLabel)}</strong>
        </p>` : null}
        <div class="approval-receipt-actions">
          ${notice.actions.map((item) => html`<${Button}
            key=${item.action}
            size="sm"
            variant=${item.action === 'continue' ? 'secondary' : 'ghost'}
            disabled=${item.disabled}
            pending=${item.action === 'check' && item.disabled}
            data-testid=${`approval-${item.action}`}
            data-approval-operation-action=${item.action}
            onClick=${() => run(item.action)}
          >${tx(item.label)}<//>`)}
        </div>
      </div>
    </div>` : null}
  </div>`;
}

