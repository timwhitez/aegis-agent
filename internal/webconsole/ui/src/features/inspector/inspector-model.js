// Pure view-model helpers for the session inspector (no DOM, no fetch).
// Everything here derives from the GET /api/sessions/{id} detail payload or
// the GET /api/sessions/{id}/context report and returns plain data or text
// descriptors ({ key, params } / { raw }) that the tab components translate.
// Imported directly by validation/scripts/ui_inspector_model_test.mjs.
import { INSPECTOR_TABS } from '../../state/prefs.js';
import { collectFileChanges, humanizeToken, maybeArray, shortId } from '../../lib/session-model.js';

const str = (value) => (value === undefined || value === null ? '' : String(value));
const lower = (value) => str(value).trim().toLowerCase();
const isObject = (value) => Boolean(value) && typeof value === 'object' && !Array.isArray(value);

// ---- tabs ---------------------------------------------------------------------

/** Compact tab labels (literal keys so the i18n scan can verify them). */
export const TAB_LABEL_KEYS = Object.freeze({
  overview: 'inspector.tab.overview',
  plan: 'inspector.tab.plan',
  goal: 'inspector.tab.goal',
  tasks: 'inspector.tab.tasks',
  agents: 'inspector.tab.agents',
  context: 'inspector.tab.context',
  timeline: 'inspector.tab.timeline',
  files: 'inspector.tab.files',
});

export function hasPlanMode(detail) {
  const planMode = detail?.plan_mode;
  if (!isObject(planMode)) return false;
  return Boolean(planMode.plan_mode_id) || Boolean(lower(planMode.status) && lower(planMode.status) !== 'off');
}

export function hasGoal(detail) {
  return isObject(detail?.goal);
}

export function hasTasks(detail) {
  const board = detail?.task_board;
  if (!isObject(board)) return false;
  if (maybeArray(board.todo).length || maybeArray(board.tasks).length) return true;
  return isObject(board.groups) && Object.values(board.groups).some((items) => maybeArray(items).length > 0);
}

export function hasAgents(detail) {
  return maybeArray(detail?.children?.sessions).length > 0
    || maybeArray(detail?.children?.jobs).length > 0
    || maybeArray(detail?.background_notifications).length > 0;
}

/** A loaded durable session (Context and Timeline need a session id). */
export function isDurableSession(detail) {
  return Boolean(str(detail?.metadata?.id));
}

/** Detail with the merged message window (older pages + polled tail). */
export function withMessages(detail, messages) {
  if (!detail) return detail;
  const list = maybeArray(messages);
  return list.length ? { ...detail, messages: list } : detail;
}

/** Number of changed files (durable file_changes, else inferred from messages). */
export function fileChangeCount(detail) {
  return detail ? collectFileChanges(detail).files.length : 0;
}

/**
 * Tabs relevant for the detail, in INSPECTOR_TABS order. Overview is always
 * present; the others appear only when the session has matching facts.
 * @param {object|null} detail
 * @param {{fileCount?: number}} [options] precomputed file-change count
 */
export function visibleInspectorTabs(detail, options = {}) {
  const fileCount = options.fileCount ?? fileChangeCount(detail);
  const durable = isDurableSession(detail);
  const rules = {
    overview: true,
    plan: hasPlanMode(detail),
    goal: hasGoal(detail),
    tasks: hasTasks(detail),
    agents: hasAgents(detail),
    context: durable,
    timeline: durable,
    files: fileCount > 0,
  };
  return INSPECTOR_TABS.filter((id) => rules[id]);
}

/** Persisted tab when visible, otherwise overview (the preference is kept). */
export function resolveInspectorTab(tab, visible) {
  return maybeArray(visible).includes(tab) ? tab : 'overview';
}

/**
 * Tabs, active tab and loading flag for the inspector. While the open
 * session's detail is still loading (e.g. right after a session switch) the
 * persisted tab stays selected and its panel shows a skeleton, instead of
 * falling back to Overview and jumping back once the detail arrives.
 * @param {{tab: string, detail: object|null, sessionId?: string, loading?: boolean, fileCount?: number}} input
 * @returns {{tabs: string[], active: string, pending: boolean}}
 */
export function inspectorTabState({ tab, detail = null, sessionId = '', loading = false, fileCount } = {}) {
  if (sessionId && !detail && loading) {
    const persisted = INSPECTOR_TABS.includes(tab) ? tab : 'overview';
    return { tabs: INSPECTOR_TABS.filter((id) => id === 'overview' || id === persisted), active: persisted, pending: true };
  }
  const tabs = visibleInspectorTabs(detail, { fileCount });
  return { tabs, active: resolveInspectorTab(tab, tabs), pending: false };
}

// ---- tones ----------------------------------------------------------------------

/** Badge tone (ui/Badge.js) for any runtime / queue / task / goal status token. */
export function badgeTone(status) {
  switch (lower(status)) {
    case 'running':
    case 'in_progress':
    case 'active':
    case 'executing':
    case 'planning':
    case 'current_process':
      return 'info';
    case 'completed':
    case 'complete':
    case 'done':
    case 'success':
    case 'accepted':
    case 'approved':
    case 'ready':
      return 'success';
    case 'awaiting_input':
    case 'awaiting_plan_approval':
    case 'awaiting_plan_input':
    case 'awaiting_approval':
    case 'awaiting_user_input':
    case 'needs_approval':
    case 'paused':
    case 'blocked':
    case 'budget_limited':
    case 'deferred':
    case 'retry':
    case 'auto_resume':
    case 'waiting':
    case 'parked':
    case 'exhausted':
    case 'pending':
      return 'warning';
    case 'failed':
    case 'failure':
    case 'error':
    case 'rejected':
      return 'danger';
    default:
      return 'neutral';
  }
}

// ---- overview -----------------------------------------------------------------

export const HANDLE_STATE_KEYS = Object.freeze({
  current_process: 'inspector.handle.currentProcess',
  running_not_owned: 'inspector.handle.runningNotOwned',
  settled: 'inspector.handle.settled',
});

/** Active run handle owner (current_process | running_not_owned | settled). */
export function activeHandleView(detail) {
  const owner = isObject(detail?.active_handle_owner) ? detail.active_handle_owner : {};
  const state = lower(owner.state) || (detail?.active_handle ? 'current_process' : '');
  if (!state) return null;
  return {
    state,
    key: HANDLE_STATE_KEYS[state] || '',
    tone: state === 'current_process' ? 'info' : state === 'running_not_owned' ? 'warning' : 'neutral',
    owned: owner.owned_by_current_process === true,
    pid: Number(owner.pid || 0) || 0,
    processStartId: str(owner.process_start_id),
    startedAt: str(owner.started_at),
    releasedAt: str(owner.released_at),
    lastEventAt: str(owner.last_event_at),
    action: str(owner.action),
  };
}

export const FAILURE_CLASSES = Object.freeze(['authRouting', 'credential', 'rateLimit', 'requestContract', 'transport', 'serverError']);

/**
 * Provider failure class for state.last_error (port of the v1
 * summarizeProviderFailure heuristics) or '' when nothing matches.
 */
export function providerFailureClass(lastError) {
  const text = lower(lastError);
  if (!text) return '';
  if (text.includes('auth_unavailable') || text.includes('no auth available')) return 'authRouting';
  if (['token_invalidated', 'token_revoked', 'invalid api key', 'auth_error', 'unauthorized', 'forbidden'].some((part) => text.includes(part))) return 'credential';
  if (text.includes('rate_limit') || text.includes('429')) return 'rateLimit';
  if (['invalid_request', 'unprocessable', 'unsupported parameter'].some((part) => text.includes(part))) return 'requestContract';
  if (['upstream_timeout', 'transport timeout', 'context deadline exceeded', 'i/o timeout', 'connection reset', 'stream disconnected', 'stream disconnect'].some((part) => text.includes(part))) return 'transport';
  if (text.includes('server_error')) return 'serverError';
  return '';
}

/** provider.retry events visible in the detail event window. */
export function providerRetryCount(detail) {
  return maybeArray(detail?.events).filter((event) => event?.type === 'provider.retry').length;
}

/** Links to related sessions: parent, root (when distinct) and queue job id. */
export function relatedSessions(detail) {
  const meta = detail?.metadata || {};
  const id = str(meta.id);
  const parent = str(meta.parent_session_id);
  const root = str(meta.root_session_id);
  return {
    parent,
    root: root && root !== id && root !== parent ? root : '',
    queueJob: str(meta.queue_job_id),
  };
}

/** Steer requests still waiting for the run (pending/deferred), newest first. */
export function pendingSteers(detail) {
  return maybeArray(detail?.steer_requests)
    .filter((item) => ['pending', 'deferred'].includes(lower(item?.status)))
    .slice()
    .reverse();
}

/** Provider option rows (label key + raw value); empty values are skipped. */
export function providerOptionRows(options) {
  const o = isObject(options) ? options : {};
  const rows = [];
  const push = (key, value) => {
    if (value === undefined || value === null || value === '' || value === 0) return;
    rows.push({ key, value: typeof value === 'boolean' ? value : str(value) });
  };
  push('inspector.option.apiProvider', o.api_provider);
  push('inspector.option.reasoningEffort', o.reasoning_effort);
  push('inspector.option.reasoningSummary', o.reasoning_summary);
  push('inspector.option.textVerbosity', o.text_verbosity);
  push('inspector.option.maxOutputTokens', Number(o.max_output_tokens || 0) || '');
  push('inspector.option.contextWindow', Number(o.context_window_tokens || 0) || '');
  push('inspector.option.thinkingBudget', Number(o.thinking_budget || 0) || '');
  if (typeof o.temperature === 'number') rows.push({ key: 'inspector.option.temperature', value: String(o.temperature) });
  if (typeof o.top_p === 'number') rows.push({ key: 'inspector.option.topP', value: String(o.top_p) });
  push('inspector.option.includeThoughts', typeof o.include_thoughts === 'boolean' ? o.include_thoughts : '');
  push('inspector.option.promptCache', typeof o.prompt_cache === 'boolean' ? o.prompt_cache : '');
  push('inspector.option.store', typeof o.store === 'boolean' ? o.store : '');
  push('inspector.option.baseURL', o.base_url);
  if (isObject(o.retry_policy) && Number(o.retry_policy.max_attempts || 0) > 0) push('inspector.option.retryAttempts', Number(o.retry_policy.max_attempts));
  if (isObject(o.timeout_policy)) {
    push('inspector.option.requestTimeout', Number(o.timeout_policy.request_timeout_sec || o.timeout_policy.timeout_sec || 0) ? (Number(o.timeout_policy.request_timeout_sec || o.timeout_policy.timeout_sec) + 's') : '');
    push('inspector.option.streamIdleTimeout', Number(o.timeout_policy.stream_idle_timeout_ms || 0) ? (Number(o.timeout_policy.stream_idle_timeout_ms) + 'ms') : '');
  }
  return rows;
}

/** Counters over provider attempts (retry evidence). */
export function providerAttemptStats(attempts) {
  return maybeArray(attempts).reduce((stats, attempt) => {
    const outcome = lower(attempt?.outcome);
    stats.total += 1;
    if (outcome === 'success') stats.success += 1;
    else if (outcome === 'failure') stats.failure += 1;
    else if (outcome === 'retry') stats.retry += 1;
    else if (outcome === 'auto_resume') stats.autoResume += 1;
    stats.cacheRead += Number(attempt?.cache_read_input_tokens || 0);
    stats.cacheCreation += Number(attempt?.cache_creation_input_tokens || 0);
    return stats;
  }, { total: 0, success: 0, failure: 0, retry: 0, autoResume: 0, cacheRead: 0, cacheCreation: 0 });
}

/** One provider attempt row (raw facts kept apart from labels). */
export function providerAttemptView(attempt) {
  const a = attempt || {};
  const outcome = lower(a.outcome) || 'unknown';
  return {
    outcome,
    tone: badgeTone(outcome),
    turn: Number(a.turn || 0),
    attempt: Number(a.attempt || 0),
    model: [str(a.provider), str(a.model)].filter(Boolean).join(' / '),
    error: str(a.error),
    errorClass: str(a.error_class),
    timeoutKind: str(a.timeout_kind),
    statusCode: Number(a.status_code || 0),
    backoffMs: Number(a.backoff_ms || 0),
    responseId: str(a.provider_response_id),
    cacheRead: Number(a.cache_read_input_tokens || 0),
    cacheCreation: Number(a.cache_creation_input_tokens || 0),
    retryable: a.retryable === true,
    createdAt: str(a.created_at),
  };
}

/** Recent provider attempts, newest first. */
export function recentProviderAttempts(detail, limit = 4) {
  return maybeArray(detail?.provider_attempts).slice(-limit).reverse().map(providerAttemptView);
}

/** Parent coordination (this session waiting on its children). */
export function parentCoordinationView(detail) {
  const c = detail?.parent_coordination;
  if (!isObject(c) || !c.parent_session_id) return null;
  const list = (key) => maybeArray(c[key]).map(str).filter(Boolean);
  const unresolvedChildren = list('unresolved_child_sessions');
  const unresolvedJobs = list('unresolved_queue_jobs');
  return {
    waitMode: str(c.wait_mode) || 'wait-all',
    waitState: c.parked ? 'parked' : unresolvedChildren.length || unresolvedJobs.length ? 'waiting' : 'ready',
    unresolvedChildren,
    unresolvedJobs,
    counts: {
      unresolved: [unresolvedChildren.length, unresolvedJobs.length],
      completed: [list('completed_child_sessions').length, list('completed_queue_jobs').length],
      failed: [list('failed_child_sessions').length, list('failed_queue_jobs').length],
      cancelled: [list('cancelled_child_sessions').length, list('cancelled_queue_jobs').length],
    },
    updatedAt: str(c.updated_at),
  };
}

/** Long-run checkpoint recovery facts. */
export function checkpointView(detail) {
  const c = detail?.longrun_checkpoint;
  if (!isObject(c) || !c.session_id) return null;
  return {
    waitState: str(c.parent_wait_state),
    unresolvedChildren: maybeArray(c.unresolved_child_sessions).map(str).filter(Boolean),
    unresolvedJobs: maybeArray(c.unresolved_queue_jobs).map(str).filter(Boolean),
    hints: maybeArray(c.resume_hints).map(str).filter(Boolean),
    backgroundNotifications: Number(c.background_notifications || 0),
    compactionArtifact: str(c.latest_compaction_artifact),
    owner: isObject(c.recent_owner) ? { state: str(c.recent_owner.handle_state), pid: Number(c.recent_owner.pid || 0), at: str(c.recent_owner.last_event_at || c.recent_owner.released_at || c.recent_owner.started_at) } : null,
    createdAt: str(c.created_at),
  };
}

/**
 * Child budget inspector rows from an EffectiveBudget snapshot.
 * Durations are milliseconds (format in the view).
 */
export function budgetInspector(budget, pauseReason = '') {
  if (!isObject(budget)) return null;
  const turnsLimit = Number(budget.max_turns_per_attempt || 0);
  const usedTurns = Number(budget.used_turns || 0);
  const runtimeLimit = Number(budget.max_active_runtime_ms || 0);
  const usedRuntime = Number(budget.used_active_runtime_ms || 0);
  const remainingTurns = budget.remaining_turns === undefined || budget.remaining_turns === null ? Math.max(0, turnsLimit - usedTurns) : Number(budget.remaining_turns || 0);
  const remainingRuntime = budget.remaining_active_runtime_ms === undefined || budget.remaining_active_runtime_ms === null
    ? Math.max(0, runtimeLimit - usedRuntime) : Number(budget.remaining_active_runtime_ms || 0);
  return {
    status: lower(budget.status) || 'active',
    disabled: lower(budget.status) === 'disabled',
    attempt: Number(budget.attempt || 0),
    source: str(budget.source),
    turnScope: str(budget.turn_scope),
    timeScope: str(budget.time_scope),
    turns: turnsLimit > 0 ? { limit: turnsLimit, used: usedTurns, remaining: remainingTurns, overrun: Number(budget.overrun_turns || 0) } : null,
    runtime: runtimeLimit > 0 ? { limit: runtimeLimit, used: usedRuntime, remaining: remainingRuntime, overrun: Number(budget.overrun_active_runtime_ms || 0) } : null,
    totals: { turns: Number(budget.total_used_turns || 0), runtime: Number(budget.total_active_runtime_ms || 0) },
    deadline: str(budget.absolute_deadline_at),
    checkpointAt: str(budget.active_runtime_checkpoint_at),
    leaseOpen: budget.active_runtime_lease_open === true,
    recoveryMs: Number(budget.active_runtime_last_recovery_ms || 0),
    recoveryAt: str(budget.active_runtime_last_recovery_at),
    lastReason: str(pauseReason || budget.last_reason).trim(),
    updatedAt: str(budget.updated_at),
  };
}

/** Required artifacts with a presence/validation status token. */
export function requiredArtifactRows(detail) {
  const list = maybeArray(detail?.required_artifacts).length ? detail.required_artifacts : maybeArray(detail?.contract?.required_artifacts);
  return maybeArray(list).filter((item) => item?.path).map((item) => {
    const status = item.status || {};
    const validation = lower(status.validation_status);
    const state = validation === 'failed' || validation === 'invalid' ? 'failed' : status.present ? (status.changed_from_baseline || status.touched_by_session ? 'completed' : 'ready') : 'pending';
    return { path: str(item.display_path || item.path), required: item.required !== false, state, validation, issues: maybeArray(status.validation_issues).map(str) };
  });
}

// ---- plan -----------------------------------------------------------------------

/** Plan history: approvals and planmode.* timeline events, newest first. */
export function planHistory(detail) {
  const planMode = detail?.plan_mode || {};
  const entries = [];
  maybeArray(planMode.approvals).forEach((approval, index) => {
    entries.push({ id: 'approval:' + index, kind: 'approval', time: str(approval?.approved_at), version: Number(approval?.version || 0), source: str(approval?.source), by: str(approval?.approved_by) });
  });
  maybeArray(detail?.timeline).forEach((item, index) => {
    if (item?.kind !== 'event' || !str(item?.event_type).startsWith('planmode.')) return;
    entries.push({ id: 'event:' + (item.event_id || index), kind: 'event', time: str(item.time), item });
  });
  return entries.sort((left, right) => right.time.localeCompare(left.time));
}

// ---- goal -----------------------------------------------------------------------

/**
 * How the Goal tab offers mission plan approval (one approval path only):
 *   'mission' — no pending linked Plan Mode: the tab's button calls
 *               approvePlan(sessionId, { entrypoint: 'mission' }), which uses
 *               approval-operations.js approveUnlinkedMission because the
 *               plan mode is absent or unlinked;
 *   'inline'  — a linked Plan Mode awaits approval: the tab links to the
 *               inline approval card (approvePlan with the reviewed target);
 *   'none'    — nothing to approve (no mission plan, already approved, goal
 *               complete/cleared) or the linked Plan Mode is not reviewable.
 * "Linked" mirrors session-actions.js approvePlan: plan_mode.linked_goal_id is set.
 */
export function goalPlanApprovalEntry(detail) {
  const goal = detail?.goal;
  if (!isObject(goal)) return 'none';
  const mission = isObject(goal.mission) ? goal.mission : null;
  if (!mission || lower(mission.plan_status) === 'approved') return 'none';
  const status = lower(goal.status) || 'active';
  if (status === 'complete' || status === 'cleared') return 'none';
  const planMode = isObject(detail?.plan_mode) ? detail.plan_mode : null;
  if (planMode && str(planMode.linked_goal_id).trim()) {
    return lower(planMode.status) === 'awaiting_approval' ? 'inline' : 'none';
  }
  return 'mission';
}

/** Goal control availability (port of the v1 goal panel rules). */
export function goalActions(detail) {
  const goal = detail?.goal;
  if (!isObject(goal)) return { canPause: false, canResume: false, canComplete: false, canApprovePlan: false, canClear: false };
  const status = lower(goal.status) || 'active';
  return {
    canPause: status === 'active' || status === 'budget_limited',
    canResume: status === 'paused' || status === 'budget_limited',
    canComplete: status !== 'complete' && status !== 'cleared',
    canApprovePlan: goalPlanApprovalEntry(detail) === 'mission',
    canClear: status !== 'cleared',
  };
}

/** Title + whether it is a durable raw value for a goal item. */
export function goalItemTitle(item, kind) {
  const value = kind === 'validation'
    ? item?.command || item?.artifact || item?.description || item?.kind
    : kind === 'role'
      ? item?.name || item?.role
      : item?.title || item?.text || item?.description || item?.id;
  return str(value);
}

/** Linked task / child / queue / evaluator facts of a goal item (short ids). */
export function goalLinkedFacts(item) {
  const ids = (key) => maybeArray(item?.[key]).map(str).filter(Boolean);
  const evaluator = maybeArray(item?.evaluator_evidence)
    .map((evidence) => str(evidence?.child_session_id || evidence?.queue_job_id || evidence?.artifact || evidence?.summary))
    .filter(Boolean);
  return {
    tasks: ids('task_ids'),
    children: ids('child_session_ids'),
    jobs: ids('queue_job_ids'),
    evaluator,
    evaluatorCount: maybeArray(item?.evaluator_evidence).length,
  };
}

/** Coverage summary from goal_facts.coverage. */
export function goalCoverage(detail) {
  const coverage = detail?.goal_facts?.coverage;
  if (!isObject(coverage)) return null;
  const total = Number(coverage.validation_total || 0);
  return {
    total,
    covered: Number(coverage.covered_assertions || 0),
    blocked: coverage.approval_blocked === true,
    uncovered: maybeArray(coverage.uncovered_assertions).map(str).filter(Boolean),
    featuresWithout: maybeArray(coverage.features_without_assertions).map(str).filter(Boolean),
    milestonesWithout: maybeArray(coverage.milestones_without_validation).map(str).filter(Boolean),
  };
}

/** { key: 'eventType.<type>', params: { fallback } } for goal history entry types. */
export function eventTypeLabelFor(type) {
  const value = str(type) || 'event';
  return { key: 'eventType.' + value, params: { fallback: humanizeToken(value) } };
}

/** Latest goal history entries (newest first). */
export function goalHistory(detail, limit = 6) {
  const history = maybeArray(detail?.goal_facts?.history);
  const latest = detail?.goal_facts?.latest_history;
  const list = history.length ? history : latest ? [latest] : [];
  return list.slice().sort((left, right) => str(right?.created_at).localeCompare(str(left?.created_at))).slice(0, limit);
}

/** Latest progress records (newest first). */
export function goalProgress(detail, limit = 3) {
  const progress = maybeArray(detail?.goal_facts?.progress).length ? detail.goal_facts.progress : maybeArray(detail?.goal?.progress);
  return maybeArray(progress).slice(-limit).reverse();
}

// ---- context report -------------------------------------------------------------

export const CONTEXT_PEAK_METRICS = Object.freeze([
  { key: 'inspector.context.rootPeak', field: 'root_peak_estimated_input_tokens' },
  { key: 'inspector.context.childPeak', field: 'child_peak_estimated_input_tokens' },
  { key: 'inspector.context.rootAggregate', field: 'root_aggregate_estimated_input_tokens' },
  { key: 'inspector.context.childAggregate', field: 'child_aggregate_estimated_input_tokens' },
  { key: 'inspector.context.totalInput', field: 'total_estimated_input_tokens' },
  { key: 'inspector.context.unknownUsage', field: 'unknown_usage_request_count' },
]);

export const CONTEXT_USAGE_METRICS = Object.freeze([
  { key: 'inspector.context.inputTokens', field: 'input_tokens' },
  { key: 'inspector.context.outputTokens', field: 'output_tokens' },
  { key: 'inspector.context.cacheRead', field: 'cache_read_input_tokens' },
  { key: 'inspector.context.cacheCreation', field: 'cache_creation_input_tokens' },
]);

export const CONTEXT_LINEAGE_METRICS = Object.freeze([
  { key: 'inspector.context.sessions', field: 'session_count' },
  { key: 'inspector.context.childSessions', field: 'child_session_count' },
  { key: 'inspector.context.requests', field: 'total_request_count' },
  { key: 'inspector.context.turns', field: 'total_turn_count' },
  { key: 'inspector.context.toolCalls', field: 'total_tool_call_count' },
  { key: 'inspector.context.compactions', field: 'total_compaction_count' },
]);

function metricNumber(value) {
  const number = Number(value || 0);
  return Number.isFinite(number) ? Math.trunc(number) : 0;
}

/** Context report view: metric groups, per-session rows and truncation. */
export function contextReportView(report) {
  if (!isObject(report)) return null;
  const aggregate = isObject(report.aggregate) ? report.aggregate : {};
  const usage = isObject(aggregate.total_provider_usage) ? aggregate.total_provider_usage : {};
  const rootId = str(report.root_session_id);
  const truncation = isObject(report.truncation) && report.truncation.truncated ? {
    sessions: metricNumber(report.truncation.omitted_session_count),
    requests: metricNumber(report.truncation.omitted_request_count),
  } : null;
  return {
    version: metricNumber(report.schema_version) || 1,
    rootId,
    requestedId: str(report.requested_session_id),
    peaks: CONTEXT_PEAK_METRICS.map((metric) => ({ ...metric, value: metricNumber(aggregate[metric.field]) })),
    usage: CONTEXT_USAGE_METRICS.map((metric) => ({ ...metric, value: metricNumber(usage[metric.field]) })),
    lineage: CONTEXT_LINEAGE_METRICS.map((metric) => ({ ...metric, value: metricNumber(aggregate[metric.field]) })),
    wallTimeMs: metricNumber(aggregate.wall_time_ms),
    sessions: maybeArray(report.sessions).map((item) => ({
      id: str(item?.session_id),
      shortId: shortId(item?.session_id),
      isRoot: str(item?.session_id) === rootId,
      role: str(item?.agent_role),
      peak: metricNumber(item?.metrics?.peak_estimated_input_tokens),
      aggregate: metricNumber(item?.metrics?.aggregate_estimated_input_tokens),
      requests: metricNumber(item?.metrics?.request_count),
      failed: metricNumber(item?.metrics?.failed_request_count),
      cancelled: metricNumber(item?.metrics?.cancelled_request_count),
    })),
    truncation,
  };
}
