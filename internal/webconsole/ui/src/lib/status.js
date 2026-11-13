// Session display status and tone mapping (pure). Semantics ported from the
// v2 console: plan-mode actions outrank the runtime status, and a run handle
// owned by the current server process means "running" unless it is only
// settling (the run already ended and the handle release is pending).

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

// RFC 3339 timestamp (Go time.RFC3339Nano) -> [epoch ms of the whole second, nanoseconds], or null.
function timestampParts(value) {
  const match = /^(.+T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/i.exec(String(value || '').trim());
  if (!match) return null;
  const seconds = Date.parse(match[1] + match[3]);
  return Number.isFinite(seconds) ? [seconds, Number((match[2] || '').padEnd(9, '0'))] : null;
}

/**
 * Mirrors service.go handleStartedNoLaterThanState with nanosecond precision:
 * true when the handle started at or before the last state write; false when
 * either timestamp is missing or unparseable.
 */
export function handleStartedNoLaterThanState(handleStartedAt, stateUpdatedAt) {
  const started = timestampParts(handleStartedAt);
  const updated = timestampParts(stateUpdatedAt);
  if (!started || !updated) return false;
  return started[0] < updated[0] || (started[0] === updated[0] && started[1] <= updated[1]);
}

/**
 * True when the current-process run handle is only settling: the state is not
 * running and was written at or after the handle started (the run ended, the
 * release is pending). The server then accepts a continue (it waits for the
 * release, service.go waitForSettlingActiveHandle). A handle that started after
 * the last state write is the launch window instead (the run has not claimed
 * the session yet; continue and steer are both refused).
 */
export function sessionHandleIsSettling(detail) {
  return sessionHasCurrentProcessHandle(detail)
    && lower(detail?.state?.status) !== 'running'
    && handleStartedNoLaterThanState(detail?.active_handle_owner?.started_at, detail?.state?.updated_at);
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
  // A live (not settling) handle means the run is starting or running, also for a
  // completed root whose follow-up was accepted (the server prunes settled
  // handles of completed/failed/cancelled sessions before answering).
  if (sessionHasCurrentProcessHandle(detail) && !sessionHandleIsSettling(detail)) return 'running';
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
