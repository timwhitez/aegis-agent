// Session display status and tone mapping (pure). Semantics ported from the
// v2 console: plan-mode actions outrank the runtime status, and a run handle
// owned by the current server process means "running" unless completed.

export const ATTENTION_STATUSES = Object.freeze(['running', 'awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input']);
export const ACTIVE_RUNTIME_STATUSES = Object.freeze(['queued', 'pending', 'running']);
export const KNOWN_STATUSES = Object.freeze([
  'running', 'queued', 'pending', 'paused', 'completed', 'failed', 'cancelled', 'error',
  'awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input', 'idle', 'unknown',
]);

const lower = (value) => String(value || '').toLowerCase();

/** Maps plan-mode status to the display status it implies, or ''. */
export function planModeActionDisplayStatus(planModeStatus) {
  switch (lower(planModeStatus)) {
    case 'awaiting_approval':
      return 'awaiting_plan_approval';
    case 'awaiting_user_input':
      return 'awaiting_plan_input';
    default:
      return '';
  }
}

export function sessionHasCurrentProcessHandle(detail) {
  return detail?.active_handle === true || detail?.active_handle_owner?.owned_by_current_process === true;
}

/** True when the current server process can directly interrupt/stop the run. */
export function sessionHasDirectControl(detail) {
  return detail?.active_handle === true && detail?.active_handle_owner?.owned_by_current_process === true;
}

export function isActiveRuntimeStatus(status) {
  return ACTIVE_RUNTIME_STATUSES.includes(lower(status));
}

export function sessionDetailHasActiveDescendants(detail) {
  if (!detail) return false;
  const sessions = Array.isArray(detail.children?.sessions) ? detail.children.sessions : [];
  const jobs = Array.isArray(detail.children?.jobs) ? detail.children.jobs : [];
  return sessions.some((item) => isActiveRuntimeStatus(item?.status))
    || jobs.some((item) => isActiveRuntimeStatus(item?.status) || isActiveRuntimeStatus(item?.session_status));
}

export function sessionDetailDisplayStatus(detail) {
  const status = detail?.state?.status || '';
  const planStatus = planModeActionDisplayStatus(detail?.plan_mode?.status);
  if (planStatus) return planStatus;
  if (sessionHasCurrentProcessHandle(detail) && status !== 'completed') return 'running';
  return status || 'unknown';
}

export function sessionSummaryDisplayStatus(item) {
  return planModeActionDisplayStatus(item?.plan_mode_status) || item?.status || 'unknown';
}

/** Tone used by badges and status dots: info | warning | success | danger | neutral. */
export function statusTone(status) {
  switch (lower(status)) {
    case 'running':
      return 'info';
    case 'awaiting_input':
    case 'awaiting_plan_approval':
    case 'awaiting_plan_input':
    case 'paused':
      return 'warning';
    case 'completed':
      return 'success';
    case 'failed':
    case 'error':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** Sessions pinned in the sidebar "In progress / needs you" group. */
export function isAttentionStatus(status) {
  return ATTENTION_STATUSES.includes(lower(status));
}

export function isStoppableStatus(status) {
  return lower(status) === 'running';
}

/** i18n key for a status label, or '' when the raw value must be shown. */
export function statusLabelKey(status) {
  const value = lower(status);
  return KNOWN_STATUSES.includes(value) ? `common.status.${value}` : '';
}
