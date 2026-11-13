/**
 * Pure derivations from the GET /api/sessions/{id} detail JSON
 * (internal/webconsole/service.go SessionDetailResponse). No DOM, no fetch.
 *
 * Text slot convention used by every function that produces operator-facing
 * text:
 *   { key, params }        operator text; the UI renders t(key, params)
 *   { raw }                durable fact (prompt, tool output, path, id, agent
 *                          name...); render inside a translate="no" node
 *   { key, params, raw }   fixed label followed by a separate raw value node
 * Dynamic keys built from server tokens (status.*, phase.*, goalStatus.*,
 * eventType.*, timeline.message.*, notification.action.*, tasks.group.*)
 * always carry params.fallback = humanized token so the UI can fall back
 * when a dictionary entry is missing.
 *
 * i18n keys emitted (params in braces; "+raw" = separate raw value):
 *   actor.agent | actor.backgroundAgents | actor.harness | actor.system |
 *     actor.tool | actor.you | actor.youSteer
 *   activity.ready.title|copy, activity.loaded.copy, activity.startingTurn,
 *     activity.runnerActive, activity.awaitingPlanApproval.title|copy,
 *     activity.awaitingPlanInput.title|copy, activity.<state>.title|copy for
 *     state in completed|awaitingInput|stopped|interrupted|paused|cancelled|failed
 *   stage.running | stage.settling | stage.interruptArmed
 *   step.thinking {chars}
 *   tool.finish.captured | tool.finish.call | tool.shell.empty | tool.file.noPath |
 *     tool.search.pattern (+raw pattern, secondaryRaw path) | tool.awaitInput |
 *     tool.loadSkill | tool.agentSpawn | tool.agentList | tool.todoItems {count} |
 *     tool.todoRead | tool.todoSaved {count} | tool.todoUnchanged {count} |
 *     tool.taskCreate | tool.taskUpdate | tool.taskList | tool.submitPlan |
 *     tool.requestUserInput {count} | tool.getPlanMode | tool.readSessionHistory |
 *     tool.noArguments | tool.noOutput
 *   goal.tool.read | goal.tool.create {chars} | goal.tool.recordProgress |
 *     goal.tool.update {status, statusLabel} | goal.tool.generic | goal.tool.noGoal |
 *     goal.tool.state; goal.progress.kindFallback | goal.progress.summaryFallback;
 *     goal.objectiveStored {chars}; goal.metric.tokens|providerTime {value};
 *     goal.count.criteria|validation|progress|evidence|blockers {count};
 *     goal.chip.mode|status|kind (+raw); goal.chip.tokenBudget {value};
 *     goal.chip.providerTimeBudget {minutes}; goal.latestProgress; goal.off;
 *     goal.noneAttached; goal.historical; goal.line.session {status, statusLabel};
 *     goal.line.tokens|providerTime {value}; goal.line.latest {type, typeLabel};
 *     goal.line.at {at: ISO time}
 *   background.accepted {count, completed, failed} | background.childAgent |
 *     background.noFinalText
 *   event.providerCall(.copy) | event.requestPrepared(.copy) |
 *     event.assistantMessage(.copy) | event.toolStarted(.copy) |
 *     event.toolFinished(.copy) | event.toolBlocked(.copy) |
 *     event.toolInterrupted(.copy) | event.toolFallback | event.childQueued(.copy) |
 *     event.childSpawned(.copy) | event.queueClaimed(.copy) | event.queueCompleted |
 *     event.queueCancelled | event.queueFailed | event.queueRole (+raw role) |
 *     event.queueChanged | event.queueNotified(.copy) | event.goal.copy |
 *     event.mission.planUpdated|planApproved|validationUpdated|changed |
 *     event.mission.planStatus {status, statusLabel} |
 *     event.planmode.created|inputRequested|inputAnswered|inputCancelled|
 *       planSubmitted|planApproved|planRevised|cancelled|executionStarted|
 *       awaitingApproval|changed | event.planmode.executionStarted.copy |
 *     event.planmode.questions {count} | event.planVersion {version} |
 *     event.steerRequested|steerQueued|steerAccepted|steerDeferred|
 *       steerInterruptRequested | event.steer.copy |
 *     event.providerCancelled(.copy) | event.runStopped(.copy) |
 *     event.runPaused(.copy) | event.runCancelled(.copy) |
 *     event.sessionCompleted(.copy) | event.sessionFailed(.copy) |
 *     event.sessionAwaitingInput(.copy) | event.recorded
 *   timeline.backgroundResults(.copy) | timeline.toolResults(.copy) |
 *     timeline.emptyMessage
 *   tasks.untitledTask | tasks.untitledTodo | tasks.noDescription |
 *     tasks.blockedBy {count} | tasks.blocks {count}
 *   files.partial | files.disclosure
 *   budget.unknown | budget.offSnapshotted | budget.attempt {attempt} |
 *     budget.turns {used, limit, remaining} | budget.active {usedSec, limitSec,
 *     remainingSec} | budget.checkpointLeaseOpen|checkpointLeaseClosed {at} |
 *     budget.crashRecovery|crashRecoveryAt {value, at} | budget.deadline {at}
 *   subagent.childAgent
 *   notification.fallbackTitle | notification.noFinalText |
 *     notification.pendingDelivery | notification.pendingHint
 *   session.untitled | session.masterFallback
 *   Dynamic (always with params.fallback): status.<status>, phase.<phase>,
 *     goalStatus.<status>, planStatus.<status>, budgetStatus.<status>,
 *     eventType.<event type>, timeline.message.<variant>, tasks.group.<group>,
 *     notification.action.<action>
 */

// ---------------------------------------------------------------------------
// Generic helpers
// ---------------------------------------------------------------------------

/** @returns {Array} value when it is an array, otherwise []. */
export function maybeArray(value) {
  return Array.isArray(value) ? value : [];
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function str(value) {
  return value === undefined || value === null ? '' : String(value);
}

/** Short display form of an id ('' when empty). */
export function shortId(value) {
  const text = str(value);
  if (!text) return '';
  if (text.length <= 14) return text;
  return text.slice(0, 6) + '\u2026' + text.slice(-4);
}

/** Trim and truncate with an ellipsis. */
export function truncateText(value, limit = 160) {
  const text = str(value).trim();
  if (text.length <= limit) return text;
  return text.slice(0, Math.max(limit - 1, 0)) + '\u2026';
}

/** Collapse whitespace, then truncate. */
export function compactText(value, limit = 120) {
  return truncateText(str(value).replace(/\s+/g, ' ').trim(), limit);
}

/** Parse JSON strings; objects pass through; anything else is null. */
export function parseMaybeJSON(value) {
  if (typeof value !== 'string') {
    return value && typeof value === 'object' ? value : null;
  }
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

/** Pretty JSON for raw detail panes ('{}' for empty values). */
export function prettyJSON(value) {
  if (value === undefined || value === null || value === '') return '{}';
  try {
    if (typeof value === 'string') {
      const trimmed = value.trim();
      if (!trimmed) return '{}';
      return JSON.stringify(JSON.parse(trimmed), null, 2);
    }
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/** Keep the tail of long paths. */
export function shortenPath(path) {
  const text = str(path);
  if (text.length <= 42) return text;
  return '\u2026' + text.slice(-41);
}

/** "name · role", either part, or ''. */
export function agentLabel(name, role) {
  const n = str(name).trim();
  const r = str(role).trim();
  if (n && r) return n + ' \u00b7 ' + r;
  return n || r;
}

/** Last path segment of a workdir ('' when empty). */
export function workdirBase(path) {
  const text = str(path).replaceAll('\\', '/');
  if (!text) return '';
  return text.split('/').filter(Boolean).pop() || text;
}

/** "awaiting_input" -> "Awaiting input" (raw fallback only, never a sentence). */
export function humanizeToken(value) {
  const normalized = str(value).replaceAll('_', ' ').replaceAll('-', ' ').replaceAll('.', ' ').replace(/\s+/g, ' ').trim();
  return normalized ? normalized.charAt(0).toUpperCase() + normalized.slice(1) : '';
}

function lower(value) {
  return str(value).trim().toLowerCase();
}

function dynamicLabel(prefix, token, defaultToken = 'unknown') {
  const value = lower(token) || defaultToken;
  return { key: prefix + '.' + value, params: { fallback: humanizeToken(value) } };
}

/** Status tone used for badges: live | queued | danger | neutral. */
export function toneForStatus(status) {
  switch (lower(status)) {
    case 'running':
    case 'completed':
    case 'complete':
    case 'accepted':
    case 'active':
      return 'live';
    case 'queued':
    case 'paused':
    case 'awaiting_input':
    case 'awaiting_plan_approval':
    case 'awaiting_plan_input':
    case 'pending':
    case 'deferred':
    case 'blocked':
      return 'queued';
    case 'failed':
    case 'rejected':
    case 'cancelled':
    case 'error':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** { key:'status.<token>', params:{fallback} } */
export function statusLabel(status) {
  return dynamicLabel('status', status);
}

/** { key:'phase.<token>', params:{fallback} }; empty phase is 'prepare'. */
export function phaseLabel(phase) {
  return dynamicLabel('phase', phase, 'prepare');
}

/** True only for 'running' (the production stop/interrupt rule). */
export function isStoppableSessionStatus(status) {
  return lower(status) === 'running';
}

/** True when the detail reports an active handle owned by this web process. */
export function sessionDetailHasCurrentProcessHandle(detail) {
  return detail?.active_handle === true || detail?.active_handle_owner?.owned_by_current_process === true;
}

function timeMs(value) {
  if (!value) return null;
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : null;
}

// ---------------------------------------------------------------------------
// Message classification
// ---------------------------------------------------------------------------

/** Message meta object (meta, or timeline data), never null. */
export function messageMeta(message) {
  if (isPlainObject(message?.meta)) return message.meta;
  if (isPlainObject(message?.data)) return message.data;
  return {};
}

/** meta.source as a string. */
export function messageSource(message) {
  return str(messageMeta(message).source);
}

/** Harness reminders are stored as user-role messages with meta.source=harness_reminder. */
export function isHarnessReminder(message) {
  return messageSource(message) === 'harness_reminder';
}

/** Parse a <background-agent-results> payload (or bare JSON object). */
export function parseBackgroundResultsPayload(text) {
  const raw = str(text).trim();
  const match = raw.match(/<background-agent-results>\s*([\s\S]*?)\s*<\/background-agent-results>/);
  const parsed = parseMaybeJSON(match ? match[1] : raw);
  return isPlainObject(parsed) ? parsed : null;
}

/** Durable background_results message detection (meta.source or tagged payload). */
export function isBackgroundResultsMessage(message) {
  if (!message) return false;
  if (messageSource(message) === 'background_results') return true;
  const text = str(message.text);
  if (!text.includes('<background-agent-results>')) return false;
  return maybeArray(parseBackgroundResultsPayload(text)?.background_results).length > 0;
}

/** Raw status token of one background result. */
export function backgroundResultStatus(item) {
  return str(item?.status || item?.session_status || 'unknown');
}

/** Tone of one background result (errors are always danger). */
export function backgroundResultTone(item) {
  if (item?.last_error) return 'danger';
  return toneForStatus(backgroundResultStatus(item));
}

/** View model of one background result entry. */
export function backgroundResultView(item = {}) {
  const value = item || {};
  const label = agentLabel(value.agent_name, value.agent_role) || shortId(value.session_id);
  const body = value.last_error || value.final_text || '';
  const status = backgroundResultStatus(value);
  return {
    status,
    statusLabel: statusLabel(status),
    tone: backgroundResultTone(value),
    label: label ? { raw: label } : { key: 'background.childAgent' },
    body: body ? { raw: body } : { key: 'background.noFinalText' },
    isError: Boolean(value.last_error),
    queueJobId: str(value.queue_job_id),
    sessionId: str(value.session_id),
    workdir: str(value.effective_workdir || value.requested_workdir),
    visiblePaths: maybeArray(value.visible_paths).slice(0, 6)
  };
}

/** Counts and per-item views for a background_results payload. */
export function summarizeBackgroundResults(payload) {
  const results = maybeArray(payload?.background_results);
  const items = results.map(backgroundResultView);
  const completed = results.filter((item) => lower(backgroundResultStatus(item)) === 'completed').length;
  const failed = results.filter((item) => backgroundResultTone(item) === 'danger').length;
  const first = results[0] || {};
  const firstCopy = first.last_error || first.final_text || agentLabel(first.agent_name, first.agent_role) || '';
  return {
    count: results.length,
    completed,
    failed,
    tone: failed ? 'danger' : 'live',
    headline: { key: 'background.accepted', params: { count: results.length, completed, failed } },
    first: firstCopy ? { raw: truncateText(firstCopy, 180) } : null,
    items
  };
}

/**
 * Visual variant of a message:
 * background_results | harness_reminder | steer | user | system | tool | assistant.
 */
export function messageVariant(message) {
  if (isBackgroundResultsMessage(message)) return 'background_results';
  if (isHarnessReminder(message)) return 'harness_reminder';
  const role = str(message?.role || 'assistant');
  if (role === 'user') return messageSource(message) === 'steer' ? 'steer' : 'user';
  if (role === 'system') return 'system';
  if (role === 'tool') return 'tool';
  return 'assistant';
}

/** Actor label for a message header (raw agent identity or a fixed key). */
export function messageActor(message, metadata = {}) {
  switch (messageVariant(message)) {
    case 'background_results':
      return { key: 'actor.backgroundAgents' };
    case 'harness_reminder':
      return { key: 'actor.harness' };
    case 'steer':
      return { key: 'actor.youSteer' };
    case 'user':
      return { key: 'actor.you' };
    case 'system':
      return { key: 'actor.system' };
    case 'tool':
      return { key: 'actor.tool' };
    default: {
      const label = agentLabel(metadata?.agent_name, metadata?.agent_role);
      return label ? { raw: label } : { key: 'actor.agent' };
    }
  }
}

/** Icon name for a message variant. */
export function messageIcon(message) {
  switch (messageVariant(message)) {
    case 'background_results':
      return 'git-branch';
    case 'harness_reminder':
      return 'bell-ring';
    case 'user':
    case 'steer':
      return 'user';
    case 'system':
      return 'terminal';
    case 'tool':
      return 'wrench';
    default:
      return 'sparkles';
  }
}

// ---------------------------------------------------------------------------
// Tool call / result merging (tool lane)
// ---------------------------------------------------------------------------

/** Ids a tool call can be matched by (id and provider_call_id). */
export function toolCallIDs(call) {
  const ids = [];
  if (call?.id) ids.push(call.id);
  if (call?.provider_call_id && call.provider_call_id !== call.id) ids.push(call.provider_call_id);
  return ids;
}

function cloneDisplayMessage(message) {
  const copy = { ...(message || {}) };
  if (Array.isArray(message?.tool_calls)) copy.tool_calls = message.tool_calls.slice();
  if (Array.isArray(message?.tool_results)) copy.tool_results = message.tool_results.slice();
  if (isPlainObject(message?._tool_result_times)) copy._tool_result_times = { ...message._tool_result_times };
  return copy;
}

function isMergeableToolResultMessage(message) {
  return message?.role === 'tool' &&
    !str(message.text).trim() &&
    !str(message.thinking).trim() &&
    maybeArray(message.tool_calls).length === 0 &&
    maybeArray(message.tool_results).length > 0;
}

function partitionMatchingToolResults(previous, toolMessage) {
  const callIDs = new Set(maybeArray(previous.tool_calls).flatMap(toolCallIDs));
  if (!callIDs.size) return { matched: [], unmatched: maybeArray(toolMessage.tool_results) };
  const matched = [];
  const unmatched = [];
  maybeArray(toolMessage.tool_results).forEach((result) => {
    if (result?.tool_call_id && callIDs.has(result.tool_call_id)) matched.push(result);
    else unmatched.push(result);
  });
  return { matched, unmatched };
}

/**
 * Merge tool-result messages into the preceding assistant message whose
 * tool_calls they answer (by tool_call_id / provider_call_id). The durable
 * messages stay separate; only the display copy is merged. Merged messages
 * record _merged_tool_result_message_ids and _tool_result_times.
 * @param {Array} messages
 * @returns {Array} display messages (shallow copies)
 */
export function buildDisplayMessageStream(messages) {
  const display = [];
  maybeArray(messages).forEach((message) => {
    const current = cloneDisplayMessage(message);
    const previous = display[display.length - 1];
    if (previous && isMergeableToolResultMessage(current)) {
      const partition = partitionMatchingToolResults(previous, current);
      if (partition.matched.length) {
        previous.tool_results = maybeArray(previous.tool_results).concat(partition.matched);
        const mergedIDs = maybeArray(previous._merged_tool_result_message_ids).slice();
        if (current.id && !mergedIDs.includes(current.id)) mergedIDs.push(current.id);
        previous._merged_tool_result_message_ids = mergedIDs;
        const times = { ...(previous._tool_result_times || {}) };
        partition.matched.forEach((result) => {
          if (result?.tool_call_id && current.created_at) times[result.tool_call_id] = current.created_at;
        });
        previous._tool_result_times = times;
        if (!partition.unmatched.length) return;
        current.tool_results = partition.unmatched;
      }
    }
    display.push(current);
  });
  return display;
}

/** finish result that carries the user-visible final answer. */
export function isFinalFinishResult(result) {
  return result?.name === 'finish' && Boolean(result?.final) && !result?.is_error;
}

/** First non-error final finish result with text, or null. */
export function primaryFinalFinishResult(message) {
  return maybeArray(message?.tool_results).find((result) =>
    isFinalFinishResult(result) && str(result?.display_output || result?.llm_output).trim()) || null;
}

/** Goal progress tools rendered with compact goal cards. */
export function isGoalToolName(name) {
  return ['get_goal', 'create_goal', 'record_goal_progress', 'update_goal'].includes(str(name));
}

/** Child/background agent tools. */
export function isMultiAgentTool(name) {
  return ['agent_spawn', 'agent_status', 'agent_list', 'agent_wait', 'agent_stop', 'agent_prompt'].includes(str(name));
}

function toolArgs(call) {
  const parsed = parseMaybeJSON(call?.arguments);
  return isPlainObject(parsed) ? parsed : null;
}

function firstLine(value) {
  return str(value).split('\n').map((line) => line.trim()).find(Boolean) || '';
}

function rawSlot(value, limit = 160) {
  const text = truncateText(value, limit);
  return text ? { raw: text } : null;
}

// ---------------------------------------------------------------------------
// Goal tool summaries (raw facts kept separate from fallback keys)
// ---------------------------------------------------------------------------

/**
 * Summary of a goal tool call. Raw model values (kind, summary, mode, status)
 * are returned in rawFacts; fallbackKeys name the operator text to show when
 * a fact is missing.
 */
export function goalToolCallSummary(call) {
  const parsed = toolArgs(call) || {};
  switch (str(call?.name)) {
    case 'get_goal':
      return { key: 'goal.tool.read', params: {}, rawFacts: {} };
    case 'create_goal':
      return {
        key: 'goal.tool.create',
        params: { chars: parsed.objective ? str(parsed.objective).length : 0 },
        rawFacts: { mode: str(parsed.mode || 'goal') }
      };
    case 'record_goal_progress': {
      const kind = str(parsed.kind);
      const summary = truncateText(parsed.summary, 110);
      return {
        key: 'goal.tool.recordProgress',
        params: {},
        rawFacts: { kind, summary },
        fallbackKeys: {
          kind: kind ? '' : 'goal.progress.kindFallback',
          summary: summary ? '' : 'goal.progress.summaryFallback'
        }
      };
    }
    case 'update_goal': {
      const status = lower(parsed.status) || 'complete';
      return { key: 'goal.tool.update', params: { status, statusLabel: dynamicLabel('goalStatus', status) }, rawFacts: {} };
    }
    default:
      return { key: 'goal.tool.generic', params: {}, rawFacts: {} };
  }
}

function numberOrNull(value) {
  if (value === undefined || value === null || value === '') return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function latestProgressFacts(progress) {
  const latest = maybeArray(progress).slice(-1)[0];
  if (!latest || (!latest.kind && !latest.summary)) return null;
  return { id: str(latest.id), kind: str(latest.kind), summary: str(latest.summary) };
}

/**
 * Summary of a goal tool result. parsed===null (payload "null") means no goal
 * is attached. Raw values: mode, goalId, latestProgress.kind/summary.
 */
export function goalToolResultSummary(result, parsed = parseMaybeJSON(result?.display_output || result?.llm_output || '')) {
  if (parsed === null) return { key: 'goal.tool.noGoal', goalOff: true };
  if (!isPlainObject(parsed)) {
    return { raw: compactText(result?.display_output || result?.llm_output, 140) };
  }
  return {
    key: 'goal.tool.state',
    goalOff: false,
    status: lower(parsed.status),
    statusLabel: parsed.status ? dynamicLabel('goalStatus', parsed.status) : null,
    rawFacts: { mode: str(parsed.mode || 'goal'), goalId: shortId(parsed.goal_id) },
    tokensUsed: numberOrNull(parsed.tokens_used),
    providerTimeUsedSeconds: numberOrNull(parsed.provider_time_used_seconds ?? parsed.time_used_seconds),
    latestProgress: result?.name === 'record_goal_progress' ? latestProgressFacts(parsed.progress) : null
  };
}

/** "Objective stored in Goal panel (N chars)" reference; never the objective text. */
export function goalObjectiveReference(objective) {
  const chars = str(objective).length;
  return chars ? { key: 'goal.objectiveStored', params: { chars } } : null;
}

/** used / budget display string ("12" or "12 / 100"). */
export function formatBudget(used, budget) {
  const usedValue = Number(used || 0);
  const budgetValue = Number(budget || 0);
  return budgetValue > 0 ? usedValue + ' / ' + budgetValue : String(usedValue);
}

/** Seconds variant of formatBudget ("6s" or "6s / 20s"). */
export function formatSecondsBudget(used, budget) {
  const usedValue = Number(used || 0);
  const budgetValue = Number(budget || 0);
  return budgetValue > 0 ? usedValue + 's / ' + budgetValue + 's' : usedValue + 's';
}

/**
 * Compact goal card for a goal tool result. The objective text is never
 * included (only a character-count reference).
 */
export function goalToolCard(result, parsed = parseMaybeJSON(result?.display_output || result?.llm_output || '')) {
  if (parsed === null) return { goalOff: true, title: { key: 'goal.tool.noGoal' } };
  if (!isPlainObject(parsed)) return null;
  const status = lower(parsed.status || result?.metadata?.status || 'active');
  const progress = maybeArray(parsed.progress);
  return {
    goalOff: false,
    status,
    statusLabel: dynamicLabel('goalStatus', status),
    tone: goalStatusTone(status),
    goalId: { raw: shortId(parsed.goal_id || result?.metadata?.goal_id || '') },
    objective: goalObjectiveReference(parsed.objective),
    metrics: [
      { key: 'goal.metric.tokens', params: { value: formatBudget(parsed.tokens_used, parsed.token_budget) } },
      { key: 'goal.metric.providerTime', params: { value: formatSecondsBudget(parsed.provider_time_used_seconds ?? parsed.time_used_seconds, parsed.provider_time_budget_seconds ?? parsed.time_budget_seconds) } }
    ],
    updatedAt: str(parsed.updated_at),
    counts: [
      maybeArray(parsed.success_criteria).length ? { key: 'goal.count.criteria', params: { count: maybeArray(parsed.success_criteria).length } } : null,
      maybeArray(parsed.validation_plan).length ? { key: 'goal.count.validation', params: { count: maybeArray(parsed.validation_plan).length } } : null,
      progress.length ? { key: 'goal.count.progress', params: { count: progress.length } } : null
    ].filter(Boolean),
    latestProgress: latestProgressFacts(progress),
    latestProgressTitle: progress.length ? { key: 'goal.latestProgress' } : null
  };
}

/** Structured body for a goal tool call (raw summary/kind separate from labels). */
export function goalToolCallBody(call) {
  const parsed = toolArgs(call);
  const name = str(call?.name || 'goal tool');
  const action = goalToolCallSummary(call);
  if (!parsed) return { name: { raw: name }, action, objective: null, summary: null, chips: [] };
  const chips = [
    parsed.mode ? { key: 'goal.chip.mode', raw: str(parsed.mode) } : null,
    parsed.status ? { key: 'goal.chip.status', raw: str(parsed.status) } : null,
    parsed.kind ? { key: 'goal.chip.kind', raw: str(parsed.kind) } : null,
    parsed.token_budget ? { key: 'goal.chip.tokenBudget', params: { value: Number(parsed.token_budget) } } : null,
    (parsed.provider_time_budget_minutes || parsed.time_budget_minutes)
      ? { key: 'goal.chip.providerTimeBudget', params: { minutes: Number(parsed.provider_time_budget_minutes || parsed.time_budget_minutes) } } : null,
    maybeArray(parsed.success_criteria).length ? { key: 'goal.count.criteria', params: { count: maybeArray(parsed.success_criteria).length } } : null,
    maybeArray(parsed.validation_plan).length ? { key: 'goal.count.validation', params: { count: maybeArray(parsed.validation_plan).length } } : null,
    maybeArray(parsed.evidence).length ? { key: 'goal.count.evidence', params: { count: maybeArray(parsed.evidence).length } } : null,
    maybeArray(parsed.blockers).length ? { key: 'goal.count.blockers', params: { count: maybeArray(parsed.blockers).length } } : null
  ].filter(Boolean);
  return {
    name: { raw: name },
    action,
    objective: goalObjectiveReference(parsed.objective),
    summary: parsed.summary ? { raw: truncateText(parsed.summary, 220) } : null,
    chips
  };
}

/** Badge tone for goal statuses. */
export function goalStatusTone(status) {
  switch (lower(status)) {
    case 'active':
    case 'complete':
      return 'live';
    case 'paused':
    case 'budget_limited':
      return 'queued';
    case 'cleared':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** Goal badge label: goal.off or goalStatus.<status>. */
export function goalStatusLabel(goal) {
  if (!goal) return { key: 'goal.off' };
  return dynamicLabel('goalStatus', goal.status || 'active');
}

// ---------------------------------------------------------------------------
// Tool step summaries
// ---------------------------------------------------------------------------

/**
 * One-line summary of a tool call.
 * @param {object} call
 * @param {{finalTextRendered?: boolean, pairedResults?: Array}} [options]
 * @returns {object} text slot; goal tools return goalToolCallSummary()
 */
export function summarizeToolCall(call, options = {}) {
  const name = str(call?.name);
  if (name === 'finish' && options.finalTextRendered && maybeArray(options.pairedResults).some(isFinalFinishResult)) {
    return { key: 'tool.finish.captured' };
  }
  if (isGoalToolName(name)) return goalToolCallSummary(call);
  const args = toolArgs(call);
  const a = args || {};
  switch (name) {
    case 'shell':
      return rawSlot(firstLine(a.command), 160) || { key: 'tool.shell.empty' };
    case 'read_file':
    case 'write_file':
    case 'edit_file':
      return a.path ? { raw: shortenPath(a.path) } : { key: 'tool.file.noPath' };
    case 'glob':
    case 'grep':
    case 'grep_files':
      if (a.pattern) return { key: 'tool.search.pattern', raw: str(a.pattern), secondaryRaw: str(a.path || '') };
      break;
    case 'finish':
      return rawSlot(a.message, 120) || { key: 'tool.finish.call' };
    case 'await_input':
      return rawSlot(a.reason, 120) || { key: 'tool.awaitInput' };
    case 'load_skill':
      return rawSlot(a.name || a.skill || a.skill_name, 80) || { key: 'tool.loadSkill' };
    case 'agent_spawn': {
      const label = agentLabel(a.agent_name, a.agent_role);
      return label ? { raw: label, secondaryRaw: truncateText(a.prompt, 120) } : (rawSlot(a.prompt, 120) || { key: 'tool.agentSpawn' });
    }
    case 'agent_status':
    case 'agent_wait':
    case 'agent_stop':
    case 'agent_prompt':
      if (a.session_id) return { raw: shortId(a.session_id), secondaryRaw: truncateText(a.prompt || a.message, 120) };
      break;
    case 'agent_list':
      return { key: 'tool.agentList' };
    case 'todo_write':
      return { key: 'tool.todoItems', params: { count: maybeArray(a.todos).length } };
    case 'todo_read':
      return { key: 'tool.todoRead' };
    case 'task_create':
      return rawSlot(a.subject, 120) || { key: 'tool.taskCreate' };
    case 'task_update':
    case 'task_get':
      return rawSlot(a.id || a.task_id || a.subject, 80) || { key: 'tool.taskUpdate' };
    case 'task_list':
      return { key: 'tool.taskList' };
    case 'submit_plan':
      return rawSlot(a.title || a.summary, 120) || { key: 'tool.submitPlan' };
    case 'request_user_input':
      return { key: 'tool.requestUserInput', params: { count: maybeArray(a.questions).length } };
    case 'get_plan_mode':
      return { key: 'tool.getPlanMode' };
    case 'read_session_history':
      return { key: 'tool.readSessionHistory' };
    default:
      break;
  }
  if (args) {
    if (typeof a.path === 'string' && a.path) return { raw: shortenPath(a.path) };
    if (typeof a.pattern === 'string' && a.pattern) return { key: 'tool.search.pattern', raw: a.pattern };
    if (typeof a.prompt === 'string' && a.prompt) return { raw: truncateText(a.prompt, 120) };
    if (Array.isArray(a.todos)) return { key: 'tool.todoItems', params: { count: a.todos.length } };
    if (typeof a.message === 'string' && a.message) return { raw: truncateText(a.message, 120) };
    if (typeof a.command === 'string' && a.command) return { raw: truncateText(firstLine(a.command), 160) };
  }
  const fallback = compactText(prettyJSON(call?.arguments), 120);
  return fallback && fallback !== '{}' ? { raw: fallback } : { key: 'tool.noArguments' };
}

/** Text of a tool result payload (display_output, then llm_output). */
export function toolResultText(result) {
  return str(result?.display_output || result?.llm_output);
}

/**
 * One-line summary of a tool result.
 * @param {object} result
 * @param {{compactFinal?: boolean}} [options]
 */
export function summarizeToolResult(result, options = {}) {
  const payloadText = toolResultText(result);
  const parsed = parseMaybeJSON(payloadText);
  if (options.compactFinal && isFinalFinishResult(result)) return { key: 'tool.finish.captured' };
  if (!payloadText.trim()) return { key: 'tool.noOutput' };
  if (result?.final) return { raw: truncateText(payloadText, 120) };
  if (result?.is_error) return { raw: truncateText(payloadText, 140) };
  if (isGoalToolName(result?.name)) return goalToolResultSummary(result, parsed);
  if (result?.name === 'todo_write' && Array.isArray(parsed)) {
    return { key: result.metadata?.noop ? 'tool.todoUnchanged' : 'tool.todoSaved', params: { count: parsed.length } };
  }
  if (isPlainObject(parsed)) {
    if (typeof parsed.final_text === 'string' && parsed.final_text) return { raw: truncateText(parsed.final_text, 120) };
    if (typeof parsed.status === 'string' && parsed.status) return statusLabel(parsed.status);
  }
  return { raw: compactText(payloadText, 140) };
}

/** Special view for agent_spawn / agent_status / agent_list results. */
export function agentToolResultView(result) {
  const parsed = parseMaybeJSON(toolResultText(result));
  if (!isPlainObject(parsed)) return null;
  if (result?.name === 'agent_spawn' || result?.name === 'agent_status') {
    const status = str(parsed.status || parsed.session_status || 'unknown');
    const label = agentLabel(parsed.agent_name, parsed.agent_role) || shortId(parsed.session_id);
    return {
      kind: 'agent',
      sessionId: str(parsed.session_id),
      status,
      statusLabel: statusLabel(status),
      tone: toneForStatus(status),
      label: label ? { raw: label } : { key: 'subagent.childAgent' },
      error: parsed.last_error ? { raw: truncateText(parsed.last_error, 120) } : null
    };
  }
  if (result?.name === 'agent_list') {
    const rows = mergeSubAgentRows(parsed.sessions, parsed.jobs);
    return rows.length ? { kind: 'agent_list', rows: rows.map(subAgentRowView) } : null;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Conversation stream with step groups
// ---------------------------------------------------------------------------

function messageTextItem(message, extra = {}) {
  const variant = extra.variant || messageVariant(message);
  const id = str(message.id || message.optimisticKey || '');
  const item = {
    kind: 'message',
    id: id || 'message:' + str(message.created_at),
    variant,
    role: str(message.role || 'assistant'),
    actor: messageActor(message, extra.metadata),
    icon: messageIcon(message),
    text: str(message.text),
    thinking: str(message.thinking).trim() ? str(message.thinking) : '',
    createdAt: str(message.created_at),
    pending: Boolean(message.pending),
    meta: messageMeta(message),
    markdownKey: id ? 'msg:' + id : 'msg:anon',
    final: false,
    message
  };
  if (variant === 'background_results') {
    item.background = summarizeBackgroundResults(parseBackgroundResultsPayload(message.text));
  }
  return { ...item, ...extra.override };
}

function stepFromCall(call, results, message, options) {
  const result = results[0] || null;
  const compactFinal = options.finalTextRendered && call?.name === 'finish' && results.some(isFinalFinishResult);
  const resultTime = result ? (message._tool_result_times?.[result.tool_call_id] || message.created_at) : '';
  return {
    id: str(call?.id || call?.provider_call_id) || 'call:' + str(message.id) + ':' + options.index,
    kind: 'tool',
    name: str(call?.name || 'tool'),
    summary: summarizeToolCall(call, { finalTextRendered: options.finalTextRendered, pairedResults: results }),
    status: result ? (results.some((item) => item?.is_error) ? 'error' : 'ok') : null,
    args: toolArgs(call) ?? (call?.arguments ?? null),
    rawArguments: call?.arguments ?? null,
    result,
    results,
    resultSummary: result ? summarizeToolResult(result, { compactFinal }) : null,
    startedAt: str(message.created_at),
    endedAt: str(resultTime),
    isGoal: isGoalToolName(call?.name),
    isDelegate: isMultiAgentTool(call?.name),
    final: results.some((item) => item?.final),
    compactFinal
  };
}

function stepFromResult(result, message, index) {
  return {
    id: 'result:' + str(result?.tool_call_id || message.id + ':' + index),
    kind: 'result',
    name: str(result?.name || 'tool'),
    summary: summarizeToolResult(result),
    status: result?.is_error ? 'error' : 'ok',
    args: null,
    rawArguments: null,
    result,
    results: [result],
    resultSummary: summarizeToolResult(result),
    startedAt: str(message.created_at),
    endedAt: str(message.created_at),
    isGoal: isGoalToolName(result?.name),
    isDelegate: isMultiAgentTool(result?.name),
    final: Boolean(result?.final),
    compactFinal: false
  };
}

function thinkingStep(message) {
  const thinking = str(message.thinking);
  return {
    id: 'thinking:' + str(message.id || message.created_at),
    kind: 'thinking',
    name: 'thinking',
    summary: { key: 'step.thinking', params: { chars: thinking.length } },
    status: 'ok',
    args: null,
    rawArguments: null,
    result: null,
    results: [],
    resultSummary: null,
    thinking,
    startedAt: str(message.created_at),
    endedAt: str(message.created_at),
    isGoal: false,
    isDelegate: false,
    final: false,
    compactFinal: false
  };
}

const STEP_STATUS_RANK = { running: 3, error: 2, pending: 1, ok: 0 };

/** Aggregate status of a step list: running > error > pending > ok. */
export function stepGroupStatus(steps) {
  let status = 'ok';
  maybeArray(steps).forEach((step) => {
    if ((STEP_STATUS_RANK[step.status] ?? 0) > STEP_STATUS_RANK[status]) status = step.status;
  });
  return status;
}

function finalizeGroup(group, options, isLast) {
  const steps = group.steps.map((step) => {
    if (step.status) return step;
    return { ...step, status: isLast && options.live ? 'running' : 'pending' };
  });
  const starts = steps.map((step) => timeMs(step.startedAt)).filter((value) => value !== null);
  const ends = steps.map((step) => timeMs(step.endedAt) ?? timeMs(step.startedAt)).filter((value) => value !== null);
  const status = stepGroupStatus(steps);
  const startedMs = starts.length ? Math.min(...starts) : null;
  let endedMs = ends.length ? Math.max(...ends) : null;
  if (status === 'running' && Number.isFinite(options.now)) endedMs = Math.max(endedMs ?? options.now, options.now);
  const counts = { total: steps.length, ok: 0, error: 0, running: 0, pending: 0 };
  steps.forEach((step) => { counts[step.status] = (counts[step.status] || 0) + 1; });
  return {
    kind: 'steps',
    id: 'steps:' + steps[0].id,
    steps,
    status,
    counts,
    toolCount: steps.filter((step) => step.kind !== 'thinking').length,
    startedAt: startedMs === null ? '' : new Date(startedMs).toISOString(),
    endedAt: endedMs === null ? '' : new Date(endedMs).toISOString(),
    durationMs: startedMs !== null && endedMs !== null ? Math.max(0, endedMs - startedMs) : null
  };
}

/**
 * Build the display stream for the session view: message items and step
 * groups. Consecutive tool steps (and thinking-only assistant turns) between
 * two assistant texts are folded into one { kind:'steps' } group. Tool calls
 * and their results are merged by tool_call_id; a finish final answer is shown
 * once as an assistant message (final:true) when the assistant had no text.
 *
 * @param {Array} messages durable messages (chronological)
 * @param {{live?: boolean, now?: number, metadata?: object, optimistic?: Array}} [options]
 *   live: session is running/generating (unresolved trailing calls => running)
 *   now: epoch ms used for running group duration
 *   metadata: session metadata (agent actor label)
 *   optimistic: pending local messages appended after durable ones
 * @returns {Array<object>} items: {kind:'message'} | {kind:'steps'}
 */
export function buildConversation(messages, options = {}) {
  const source = maybeArray(messages).concat(maybeArray(options.optimistic));
  const display = buildDisplayMessageStream(source);
  const items = [];
  let group = null;
  const groups = [];
  const flush = () => {
    if (group && group.steps.length) {
      items.push(group);
      groups.push(group);
    }
    group = null;
  };
  const addStep = (step) => {
    if (!group) group = { kind: 'steps-draft', steps: [] };
    group.steps.push(step);
  };

  display.forEach((message) => {
    const variant = messageVariant(message);
    if (['user', 'steer', 'harness_reminder', 'background_results', 'system'].includes(variant)) {
      flush();
      items.push(messageTextItem(message, { metadata: options.metadata }));
      return;
    }
    const hasText = str(message.text).trim().length > 0;
    const hasThinking = str(message.thinking).trim().length > 0;
    const finalResult = hasText ? null : primaryFinalFinishResult(message);
    if (hasText) {
      flush();
      items.push(messageTextItem(message, { metadata: options.metadata }));
    } else if (hasThinking) {
      addStep(thinkingStep(message));
    }
    const calls = maybeArray(message.tool_calls);
    const results = maybeArray(message.tool_results);
    const byCall = new Map();
    results.forEach((result) => {
      if (!result?.tool_call_id) return;
      if (!byCall.has(result.tool_call_id)) byCall.set(result.tool_call_id, []);
      byCall.get(result.tool_call_id).push(result);
    });
    const paired = new Set();
    calls.forEach((call, index) => {
      const callResults = toolCallIDs(call).flatMap((id) => byCall.get(id) || []);
      callResults.forEach((result) => paired.add(result));
      addStep(stepFromCall(call, callResults, message, { finalTextRendered: Boolean(finalResult), index }));
    });
    results.forEach((result, index) => {
      if (!paired.has(result)) addStep(stepFromResult(result, message, index));
    });
    if (finalResult) {
      flush();
      const callID = str(finalResult.tool_call_id);
      items.push(messageTextItem(message, {
        metadata: options.metadata,
        variant: 'assistant',
        override: {
          id: 'final:' + (callID || str(message.id)),
          text: toolResultText(finalResult),
          thinking: '',
          final: true,
          markdownKey: callID ? 'final:' + callID : 'final-anon:' + toolResultText(finalResult).length
        }
      }));
    }
  });
  flush();

  const lastGroup = groups[groups.length - 1];
  return items.map((item) => (item.kind === 'steps-draft' ? finalizeGroup(item, options, item === lastGroup) : item));
}

/** Pending/activity stage shown at the end of the stream while a run is live. */
export function pendingStage({ generating = false, liveActivity = null, interruptArmed = false } = {}) {
  const activity = liveActivity || DEFAULT_LIVE_ACTIVITY;
  return {
    visible: Boolean(generating),
    stage: generating ? { key: 'stage.running' } : { key: 'stage.settling' },
    title: activity.title,
    copy: activity.copy,
    tone: activity.tone || 'neutral',
    interruptArmed: Boolean(interruptArmed),
    interruptLabel: interruptArmed ? { key: 'stage.interruptArmed' } : null
  };
}

/** Flat list of recent tool calls/results (summary panel). */
export function collectRecentToolEntries(messages) {
  const out = [];
  maybeArray(messages).forEach((message) => {
    maybeArray(message.tool_calls).forEach((call) => {
      out.push({ kind: 'call', name: call.name, arguments: call.arguments, created_at: message.created_at });
    });
    maybeArray(message.tool_results).forEach((result) => {
      out.push({ kind: 'result', name: result.name, display_output: result.display_output, is_error: result.is_error, created_at: message.created_at });
    });
  });
  return out;
}

// ---------------------------------------------------------------------------
// Event descriptors (ported from events.js; keys instead of sentences)
// ---------------------------------------------------------------------------

const COMPACT_FLOW_EVENTS = new Set([
  'provider.call', 'tool.before', 'tool.after', 'tool.interrupted', 'tool.blocked',
  'session.child.spawned', 'session.child.queued', 'queue.job.claimed', 'queue.job.completed',
  'queue.job.cancelled', 'queue.job.failed', 'goal.created', 'goal.updated', 'goal.budget_limited',
  'goal.budget_wrapup_required', 'goal.progress.recorded', 'goal.completed', 'planmode.created',
  'planmode.input_requested', 'planmode.input_answered', 'planmode.input_cancelled',
  'planmode.plan_submitted', 'planmode.plan_approved', 'planmode.plan_revised', 'planmode.cancelled',
  'planmode.execution_started', 'mission.plan.updated', 'mission.plan.approved',
  'mission.validation.updated', 'provider.cancelled', 'session.paused', 'session.cancelled',
  'session.completed', 'session.failed'
]);

const LIVE_ACTIVITY_EVENTS = new Set([
  'provider.call', 'tool.before', 'tool.after', 'tool.blocked', 'tool.interrupted',
  'session.child.spawned', 'session.child.queued', 'queue.job.claimed', 'queue.job.completed',
  'queue.job.cancelled', 'queue.job.failed', 'planmode.input_requested', 'planmode.input_answered',
  'planmode.input_cancelled', 'planmode.plan_submitted', 'planmode.plan_approved',
  'planmode.plan_revised', 'planmode.cancelled', 'planmode.execution_started', 'provider.cancelled',
  'session.paused', 'session.cancelled', 'session.completed', 'session.failed'
]);

const REFRESH_EVENTS = new Set([
  'assistant.message', 'tool.after', 'tool.blocked', 'tool.interrupted', 'planmode.input_requested',
  'planmode.input_answered', 'planmode.input_cancelled', 'planmode.plan_submitted',
  'planmode.plan_approved', 'planmode.plan_revised', 'planmode.cancelled',
  'planmode.execution_started', 'session.steer.accepted', 'session.background.accepted',
  'session.child.spawned'
]);

/** Events shown in the compact flow lane. */
export function isCompactFlowEvent(type) {
  return COMPACT_FLOW_EVENTS.has(str(type));
}

/** Engine events that replace the live activity indicator. */
export function shouldPromoteLiveActivity(type) {
  return LIVE_ACTIVITY_EVENTS.has(str(type));
}

/** Engine events that should trigger a session detail refresh. */
export function shouldRefreshAfterEvent(type) {
  return REFRESH_EVENTS.has(str(type));
}

/** Engine events that should trigger a sidebar/overview refresh. */
export function needsOverviewRefresh(type) {
  return typeof type === 'string' && (type.startsWith('session.child') || type.startsWith('queue.') || type.startsWith('planmode.'));
}

function eventTypeLabel(eventType) {
  const value = str(eventType) || 'event';
  return { key: 'eventType.' + value, params: { fallback: humanizeToken(value) } };
}

function summarizeToolArgumentsData(value) {
  const parsed = parseMaybeJSON(value);
  if (isPlainObject(parsed)) {
    if (typeof parsed.path === 'string' && parsed.path) return { raw: shortenPath(parsed.path) };
    if (typeof parsed.pattern === 'string' && parsed.pattern) return { key: 'tool.search.pattern', raw: parsed.pattern };
    if (typeof parsed.prompt === 'string' && parsed.prompt) return { raw: truncateText(parsed.prompt, 100) };
    if (typeof parsed.command === 'string' && parsed.command) return { raw: truncateText(firstLine(parsed.command), 100) };
  }
  const text = compactText(typeof value === 'string' ? value : (value ? JSON.stringify(value) : ''), 100);
  return text ? { raw: text } : null;
}

function rawOrKey(value, key, limit = 180, params) {
  const text = truncateText(value, limit);
  return text ? { raw: text } : (params ? { key, params } : { key });
}

function idMeta(id, phase) {
  return id ? { raw: shortId(id) } : phaseLabel(phase);
}

function planModeIcon(eventType) {
  switch (eventType) {
    case 'planmode.plan_approved':
    case 'planmode.execution_started':
      return 'badge-check';
    case 'planmode.cancelled':
    case 'planmode.input_cancelled':
      return 'x-circle';
    case 'planmode.input_requested':
      return 'message-circle-question';
    case 'planmode.plan_submitted':
      return 'clipboard-check';
    default:
      return 'map';
  }
}

function planModeCopy(eventType, data) {
  if (eventType === 'planmode.plan_submitted') {
    return rawOrKey(data?.summary, 'event.planmode.awaitingApproval');
  }
  if (eventType === 'planmode.input_requested') {
    const count = Number(data?.questions || data?.pending_questions || 1);
    return { key: 'event.planmode.questions', params: { count } };
  }
  if (eventType === 'planmode.execution_started') return { key: 'event.planmode.executionStarted.copy' };
  if (data?.objective) return { raw: truncateText(data.objective, 180) };
  if (data?.status) return statusLabel(data.status);
  return { key: 'event.planmode.changed' };
}

const PLANMODE_TITLES = {
  'planmode.created': 'event.planmode.created',
  'planmode.input_requested': 'event.planmode.inputRequested',
  'planmode.input_answered': 'event.planmode.inputAnswered',
  'planmode.input_cancelled': 'event.planmode.inputCancelled',
  'planmode.plan_submitted': 'event.planmode.planSubmitted',
  'planmode.plan_approved': 'event.planmode.planApproved',
  'planmode.plan_revised': 'event.planmode.planRevised',
  'planmode.cancelled': 'event.planmode.cancelled',
  'planmode.execution_started': 'event.planmode.executionStarted'
};

const MISSION_TITLES = {
  'mission.plan.updated': 'event.mission.planUpdated',
  'mission.plan.approved': 'event.mission.planApproved',
  'mission.validation.updated': 'event.mission.validationUpdated'
};

/**
 * Describe a durable/live engine event.
 * @returns {{icon:string, tone:string, title:object, subject:object|null,
 *   copy:object|null, meta:object|null, data:string}}
 *   title/copy/meta are text slots; subject is the raw tool name when relevant.
 */
export function describeEvent(eventType, data, phase, eventID) {
  const type = str(eventType);
  const d = isPlainObject(data) ? data : {};
  const toolName = str(d.tool_name || d.tool);
  const subject = toolName ? { raw: toolName } : { key: 'event.toolFallback' };
  const base = { icon: 'dot', tone: 'neutral', title: eventTypeLabel(type), subject: null, copy: null, meta: phaseLabel(phase), data: '' };
  switch (type) {
    case 'provider.call':
      return { ...base, icon: 'radio', tone: 'live', title: { key: 'event.providerCall' }, copy: { key: 'event.providerCall.copy' } };
    case 'provider.request.prepared':
      return { ...base, icon: 'package-search', title: { key: 'event.requestPrepared' }, copy: { key: 'event.requestPrepared.copy' } };
    case 'assistant.message':
      return { ...base, icon: 'sparkles', tone: 'live', title: { key: 'event.assistantMessage' }, copy: rawOrKey(d.text, 'event.assistantMessage.copy') };
    case 'tool.before':
      return {
        ...base, icon: 'wrench', tone: 'live', title: { key: 'event.toolStarted' }, subject,
        copy: summarizeToolArgumentsData(d.arguments) || { key: 'event.toolStarted.copy' },
        data: truncateText(typeof d.arguments === 'string' ? d.arguments : (d.arguments ? JSON.stringify(d.arguments) : ''), 600)
      };
    case 'tool.after':
      return {
        ...base, icon: 'check-circle-2', tone: d.is_error ? 'danger' : 'live', title: { key: 'event.toolFinished' }, subject,
        copy: d.display_output ? { raw: compactText(d.display_output, 120) } : { key: 'event.toolFinished.copy' },
        data: d.metadata ? prettyJSON(d.metadata) : ''
      };
    case 'tool.blocked':
      return { ...base, icon: 'shield-alert', tone: 'danger', title: { key: 'event.toolBlocked' }, subject, copy: rawOrKey(d.reason, 'event.toolBlocked.copy') };
    case 'tool.interrupted':
      return { ...base, icon: 'hand', tone: 'queued', title: { key: 'event.toolInterrupted' }, subject, copy: { key: 'event.toolInterrupted.copy' } };
    case 'session.child.queued':
      return { ...base, icon: 'git-branch-plus', tone: 'queued', title: { key: 'event.childQueued' }, copy: rawOrKey(agentLabel(d.agent_name, d.agent_role), 'event.childQueued.copy'), meta: idMeta(d.job_id, phase) };
    case 'session.child.spawned':
      return { ...base, icon: 'git-branch', tone: 'live', title: { key: 'event.childSpawned' }, copy: rawOrKey(agentLabel(d.agent_name, d.agent_role), 'event.childSpawned.copy'), meta: idMeta(d.session_id, phase) };
    case 'queue.job.claimed':
      return { ...base, icon: 'list-todo', tone: 'queued', title: { key: 'event.queueClaimed' }, copy: { key: 'event.queueClaimed.copy' }, meta: idMeta(d.job_id, phase) };
    case 'queue.job.completed':
    case 'queue.job.cancelled':
    case 'queue.job.failed': {
      const suffix = type === 'queue.job.failed' ? 'Failed' : type === 'queue.job.cancelled' ? 'Cancelled' : 'Completed';
      return {
        ...base,
        icon: type === 'queue.job.failed' ? 'x-circle' : type === 'queue.job.cancelled' ? 'ban' : 'check-check',
        tone: type === 'queue.job.completed' ? 'live' : 'danger',
        title: { key: 'event.queue' + suffix },
        copy: d.agent_role ? { key: 'event.queueRole', raw: str(d.agent_role) } : { key: 'event.queueChanged' },
        meta: idMeta(d.job_id, phase)
      };
    }
    case 'queue.job.notified':
      return { ...base, icon: 'inbox', tone: 'queued', title: { key: 'event.queueNotified' }, copy: { key: 'event.queueNotified.copy' }, meta: idMeta(d.job_id, phase) };
    case 'goal.created':
    case 'goal.updated':
    case 'goal.paused':
    case 'goal.resumed':
    case 'goal.budget_limited':
    case 'goal.budget_wrapup_required':
    case 'goal.progress.recorded':
    case 'goal.completed':
    case 'goal.cleared': {
      const budget = type === 'goal.budget_limited' || type === 'goal.budget_wrapup_required';
      return {
        ...base,
        icon: type === 'goal.completed' ? 'badge-check' : budget ? 'timer-off' : 'target',
        tone: budget ? 'queued' : type === 'goal.completed' ? 'live' : 'neutral',
        copy: rawOrKey(d.summary || d.objective, 'event.goal.copy'),
        meta: d.status ? dynamicLabel('goalStatus', d.status) : phaseLabel(phase),
        data: Object.keys(d).length ? prettyJSON(d) : ''
      };
    }
    case 'mission.plan.updated':
    case 'mission.plan.approved':
    case 'mission.validation.updated':
      return {
        ...base,
        icon: type === 'mission.validation.updated' ? 'shield-check' : 'list-checks',
        tone: type === 'mission.plan.approved' ? 'live' : 'neutral',
        title: { key: MISSION_TITLES[type] },
        copy: d.plan_status
          ? { key: 'event.mission.planStatus', params: { status: lower(d.plan_status), statusLabel: dynamicLabel('planStatus', d.plan_status) } }
          : { key: 'event.mission.changed' },
        meta: d.goal_id ? { raw: shortId(d.goal_id) } : phaseLabel(phase),
        data: Object.keys(d).length ? prettyJSON(d) : ''
      };
    case 'planmode.created':
    case 'planmode.input_requested':
    case 'planmode.input_answered':
    case 'planmode.input_cancelled':
    case 'planmode.plan_submitted':
    case 'planmode.plan_approved':
    case 'planmode.plan_revised':
    case 'planmode.cancelled':
    case 'planmode.execution_started':
      return {
        ...base,
        icon: planModeIcon(type),
        tone: type === 'planmode.cancelled' || type === 'planmode.input_cancelled'
          ? 'danger'
          : type === 'planmode.execution_started' || type === 'planmode.plan_approved' ? 'live' : 'queued',
        title: { key: PLANMODE_TITLES[type] },
        copy: planModeCopy(type, d),
        meta: d.plan_version ? { key: 'event.planVersion', params: { version: Number(d.plan_version) } } : d.status ? statusLabel(d.status) : phaseLabel(phase),
        data: Object.keys(d).length ? prettyJSON(d) : ''
      };
    case 'session.steer.requested':
    case 'session.steer.queued':
    case 'session.steer.accepted':
    case 'session.steer.deferred':
    case 'session.steer.interrupt_requested': {
      const steerTitles = {
        'session.steer.requested': 'event.steerRequested',
        'session.steer.queued': 'event.steerQueued',
        'session.steer.accepted': 'event.steerAccepted',
        'session.steer.deferred': 'event.steerDeferred',
        'session.steer.interrupt_requested': 'event.steerInterruptRequested'
      };
      return {
        ...base, icon: 'corner-down-left', tone: type.includes('deferred') ? 'queued' : 'live',
        title: { key: steerTitles[type] }, copy: { key: 'event.steer.copy' },
        data: Object.keys(d).length ? prettyJSON(d) : ''
      };
    }
    case 'provider.cancelled':
      return { ...base, icon: 'ban', tone: 'danger', title: { key: 'event.providerCancelled' }, copy: rawOrKey(d.reason, 'event.providerCancelled.copy') };
    case 'session.paused': {
      const stopped = d.reason === 'manual_stop';
      return {
        ...base, icon: 'pause-circle', tone: 'queued',
        title: { key: stopped ? 'event.runStopped' : 'event.runPaused' },
        copy: { key: stopped ? 'event.runStopped.copy' : 'event.runPaused.copy' }
      };
    }
    case 'session.cancelled':
      return { ...base, icon: 'ban', tone: 'danger', title: { key: 'event.runCancelled' }, copy: rawOrKey(d.reason, 'event.runCancelled.copy') };
    case 'session.completed':
      return { ...base, icon: 'check-check', tone: 'live', title: { key: 'event.sessionCompleted' }, copy: { key: 'event.sessionCompleted.copy' } };
    case 'session.failed':
      return { ...base, icon: 'x-circle', tone: 'danger', title: { key: 'event.sessionFailed' }, copy: rawOrKey(d.error, 'event.sessionFailed.copy') };
    case 'session.awaiting_input':
      return { ...base, icon: 'message-circle-question', tone: 'queued', title: { key: 'event.sessionAwaitingInput' }, copy: { key: 'event.sessionAwaitingInput.copy' } };
    default: {
      const meta = [phase ? phaseLabel(phase) : null, eventID ? { raw: shortId(eventID) } : null].filter(Boolean);
      return {
        ...base,
        copy: { key: 'event.recorded' },
        meta: meta.length === 1 ? meta[0] : meta.length ? { ...meta[0], raw: meta[1].raw } : null,
        data: Object.keys(d).length ? truncateText(prettyJSON(d), 600) : ''
      };
    }
  }
}

/** Describe one detail.timeline entry (message or event). */
export function describeTimelineItem(item) {
  if (item?.kind === 'message') {
    const meta = item.message_id ? { raw: shortId(item.message_id) } : null;
    const asMessage = { role: item.role, text: item.text, meta: isPlainObject(item.data) ? item.data : {} };
    if (isBackgroundResultsMessage(asMessage)) {
      const summary = summarizeBackgroundResults(parseBackgroundResultsPayload(item.text));
      return {
        icon: 'git-branch', tone: summary.failed ? 'danger' : 'live', subject: null,
        title: { key: 'timeline.backgroundResults' },
        copy: summary.count ? { ...summary.headline, raw: summary.first?.raw || '' } : { key: 'timeline.backgroundResults.copy' },
        meta, data: ''
      };
    }
    if (item.role === 'tool') {
      return { icon: 'wrench', tone: 'live', subject: null, title: { key: 'timeline.toolResults' }, copy: rawOrKey(item.text, 'timeline.toolResults.copy'), meta, data: '' };
    }
    const variant = messageVariant(asMessage);
    return {
      icon: messageIcon(asMessage),
      tone: item.role === 'system' ? 'danger' : 'neutral',
      subject: null,
      title: { key: 'timeline.message.' + variant, params: { fallback: humanizeToken(variant) } },
      copy: rawOrKey(item.text, 'timeline.emptyMessage'),
      meta,
      data: ''
    };
  }
  return describeEvent(item?.event_type, item?.data, item?.phase, item?.event_id);
}

/** Live activity for an engine event, or null when the event is not promoted. */
export function liveActivityFromEvent(event) {
  if (!shouldPromoteLiveActivity(event?.type)) return null;
  const descriptor = describeEvent(event.type, event.data, event.phase, event.id);
  return { title: descriptor.title, subject: descriptor.subject, copy: descriptor.copy, tone: descriptor.tone || 'neutral', eventType: str(event.type) };
}

/** Default activity before any session/event is known. */
export const DEFAULT_LIVE_ACTIVITY = Object.freeze({
  title: { key: 'activity.ready.title' },
  copy: { key: 'activity.ready.copy' },
  tone: 'neutral'
});

/** Activity card derived from session state (status/pause_reason/last_error). */
export function sessionActivityForState(sessionState = {}) {
  const status = lower(sessionState?.status) || 'idle';
  const pauseReason = str(sessionState?.pause_reason || sessionState?.pauseReason);
  const lastError = str(sessionState?.last_error);
  const make = (name, tone, copy) => ({ title: { key: 'activity.' + name + '.title' }, copy: copy || { key: 'activity.' + name + '.copy' }, tone });
  if (status === 'completed') return make('completed', 'live');
  if (status === 'awaiting_input') return make('awaitingInput', 'queued');
  if (status === 'paused' && pauseReason === 'manual_stop') return make('stopped', 'danger');
  if (status === 'paused' && pauseReason === 'manual_interrupt') return make('interrupted', 'queued');
  if (status === 'paused') return make('paused', 'queued');
  if (status === 'cancelled') return make('cancelled', 'danger');
  if (status === 'failed') return make('failed', 'danger', lastError ? { raw: lastError } : null);
  return { title: statusLabel(status), copy: lastError ? { raw: lastError } : { key: 'activity.loaded.copy' }, tone: toneForStatus(status) };
}

/** Activity card derived from detail (Plan Mode gates take priority). */
export function sessionActivityForDetail(detail = {}) {
  const planStatus = lower(detail?.plan_mode?.status);
  if (planStatus === 'awaiting_approval') {
    return { title: { key: 'activity.awaitingPlanApproval.title' }, copy: { key: 'activity.awaitingPlanApproval.copy' }, tone: 'queued' };
  }
  if (planStatus === 'awaiting_user_input') {
    return { title: { key: 'activity.awaitingPlanInput.title' }, copy: { key: 'activity.awaitingPlanInput.copy' }, tone: 'queued' };
  }
  return sessionActivityForState(detail?.state);
}

/**
 * Generating flag and activity after a detail refresh (port of the
 * refreshCurrentSession rules). activity === null means keep the current one.
 */
export function liveActivityForRefresh(detail, { hasLiveEvents = false } = {}) {
  const status = lower(detail?.state?.status);
  if (status === 'running' || sessionDetailHasCurrentProcessHandle(detail)) {
    return {
      generating: true,
      clearInterrupt: false,
      activity: hasLiveEvents ? null : {
        title: status === 'running' ? phaseLabel(detail?.state?.phase) : { key: 'activity.startingTurn' },
        copy: { key: 'activity.runnerActive' },
        tone: 'live'
      }
    };
  }
  return {
    generating: false,
    clearInterrupt: true,
    activity: !hasLiveEvents || toneForStatus(status) !== 'live' ? sessionActivityForDetail(detail) : null
  };
}

// ---------------------------------------------------------------------------
// Message windows, paging and timeline merge
// ---------------------------------------------------------------------------

/**
 * Merge a refreshed tail window into already-loaded messages, keeping older
 * pages. When the tail no longer overlaps, a gap anchor is reported.
 */
export function mergeMessageWindows(currentMessages, nextMessages) {
  const current = maybeArray(currentMessages);
  const next = maybeArray(nextMessages);
  if (!current.length) return { messages: next.slice(), hasGap: false, gapAnchorId: '' };
  if (!next.length) return { messages: current.slice(), hasGap: false, gapAnchorId: '' };
  const nextIds = new Set(next.map((message) => message?.id).filter(Boolean));
  const firstOverlap = current.findIndex((message) => message?.id && nextIds.has(message.id));
  if (firstOverlap >= 0) {
    return {
      messages: current.slice(0, firstOverlap).filter((message) => !message?.id || !nextIds.has(message.id)).concat(next),
      hasGap: false,
      gapAnchorId: ''
    };
  }
  const preserved = current.filter((message) => !message?.id || !nextIds.has(message.id));
  return {
    messages: preserved.concat(next),
    hasGap: preserved.length > 0,
    gapAnchorId: preserved.length > 0 ? str(next[0]?.id) : ''
  };
}

/** Insert an older page before anchorId (or at the start), deduped by id. */
export function mergeMessagesBeforeAnchor(currentMessages, olderMessages, anchorId) {
  const current = maybeArray(currentMessages);
  const older = maybeArray(olderMessages);
  if (!older.length) return current.slice();
  const existingIds = new Set(current.map((message) => message?.id).filter(Boolean));
  const uniqueOlder = older.filter((message) => !message?.id || !existingIds.has(message.id));
  if (!uniqueOlder.length) return current.slice();
  const anchor = str(anchorId).trim();
  const anchorIndex = anchor ? current.findIndex((message) => message?.id === anchor) : -1;
  if (anchorIndex < 0) return uniqueOlder.concat(current);
  return current.slice(0, anchorIndex).concat(uniqueOlder, current.slice(anchorIndex));
}

/** Timeline text for a message (text, else first tool result output). */
export function messageTimelineText(message) {
  const text = str(message?.text).trim();
  if (text) return text;
  const first = maybeArray(message?.tool_results)[0];
  return first ? str(first.display_output || first.llm_output) : '';
}

/**
 * Add message entries for every loaded message missing from the timeline,
 * dedupe by message_id and return newest-first (the API order).
 */
export function mergeMessageTimelineEntries(timeline, messages) {
  const entries = maybeArray(timeline);
  const existing = new Set(entries.filter((item) => item?.kind === 'message' && item?.message_id).map((item) => item.message_id));
  const additions = maybeArray(messages)
    .filter((message) => message?.id && !existing.has(message.id))
    .map((message) => ({
      time: str(message.created_at),
      kind: 'message',
      message_id: message.id,
      role: str(message.role),
      text: messageTimelineText(message),
      data: message.meta || null
    }));
  const merged = entries.concat(additions).sort((left, right) => str(right?.time).localeCompare(str(left?.time)));
  const seen = new Set();
  return merged.filter((item) => {
    if (item?.kind !== 'message' || !item?.message_id) return true;
    if (seen.has(item.message_id)) return false;
    seen.add(item.message_id);
    return true;
  });
}

/** Initial paging state for a freshly opened session. */
export function createMessagePaging() {
  return { hasMore: false, oldestId: '', loadedAll: false, gapAnchorId: '' };
}

/**
 * Merge a polled detail into the loaded message window (port of
 * mergeLoadedMessagesIntoDetail). Returns new objects; inputs are not mutated.
 */
export function mergeLoadedMessagesIntoDetail(currentMessages, detail, paging = createMessagePaging()) {
  const current = maybeArray(currentMessages);
  const nextPaging = { ...createMessagePaging(), ...paging };
  if (!detail || !current.length) return { detail, paging: nextPaging };
  const merged = mergeMessageWindows(current, detail.messages);
  const nextDetail = { ...detail, messages: merged.messages };
  if (merged.hasGap) {
    nextPaging.gapAnchorId = merged.gapAnchorId;
    nextPaging.loadedAll = false;
  } else if (nextPaging.gapAnchorId && merged.messages.some((message) => message?.id === nextPaging.gapAnchorId)) {
    nextPaging.loadedAll = false;
  } else {
    nextPaging.gapAnchorId = '';
  }
  return { detail: nextDetail, paging: nextPaging };
}

/**
 * Apply a polled GET /api/sessions/{id} response. Earlier loaded messages
 * survive (deduped by id) unless the previous detail belonged to another
 * session; the timeline gains entries for loaded messages.
 * @returns {{detail: object, paging: object}}
 */
export function applySessionDetailRefresh(previousDetail, nextDetail, paging = createMessagePaging()) {
  if (!nextDetail) return { detail: previousDetail, paging };
  const sameSession = previousDetail && str(previousDetail?.metadata?.id) === str(nextDetail?.metadata?.id);
  const base = sameSession ? paging : createMessagePaging();
  const merged = mergeLoadedMessagesIntoDetail(sameSession ? previousDetail.messages : [], nextDetail, base);
  const detail = { ...merged.detail, timeline: mergeMessageTimelineEntries(merged.detail.timeline, merged.detail.messages) };
  const messages = maybeArray(detail.messages);
  return {
    detail,
    paging: {
      ...merged.paging,
      hasMore: merged.paging.loadedAll ? false : nextDetail.has_more_messages === true,
      oldestId: messages.length ? str(messages[0].id) : ''
    }
  };
}

/** Next "load earlier" request ({beforeId, fillingGap}) or null. */
export function earlierMessagesRequest(paging) {
  const p = paging || {};
  const beforeId = str(p.gapAnchorId || p.oldestId);
  if (!p.hasMore || !beforeId) return null;
  return { beforeId, fillingGap: Boolean(p.gapAnchorId) };
}

/**
 * Apply a GET /messages?before_id page. paging must be the CURRENT paging
 * (a polling refresh may have created a newer gap while the page was in flight).
 */
export function applyEarlierMessagesPage(detail, paging, request, response) {
  const older = maybeArray(response?.messages);
  const next = { ...createMessagePaging(), ...paging };
  if (!detail) return { detail, paging: next };
  if (!older.length) {
    return { detail, paging: { ...next, hasMore: false, loadedAll: true, gapAnchorId: '' } };
  }
  const messages = mergeMessagesBeforeAnchor(detail.messages, older, request?.beforeId);
  next.oldestId = messages.length ? str(messages[0].id) : '';
  if (next.gapAnchorId && next.gapAnchorId !== request?.beforeId) {
    next.hasMore = true;
    next.loadedAll = false;
  } else if (request?.fillingGap && response?.has_more === true) {
    next.gapAnchorId = str(older[0]?.id || request.beforeId);
    next.hasMore = true;
    next.loadedAll = false;
  } else {
    next.gapAnchorId = '';
    next.hasMore = response?.has_more === true;
    next.loadedAll = response?.has_more !== true;
  }
  const nextDetail = { ...detail, messages };
  nextDetail.timeline = mergeMessageTimelineEntries(detail.timeline, messages);
  return { detail: nextDetail, paging: next };
}

function timelineEntryId(item, index) {
  if (item?.kind === 'message' && item.message_id) return 'message:' + item.message_id;
  if (item?.event_id) return 'event:' + item.event_id;
  return 'entry:' + index + ':' + str(item?.time);
}

function timelineHaystack(item, descriptor, translate) {
  const parts = [item?.text, item?.event_type, item?.role, item?.message_id, item?.event_id, item?.phase];
  if (item?.data) {
    try { parts.push(JSON.stringify(item.data)); } catch { /* ignore */ }
  }
  ['title', 'subject', 'copy', 'meta'].forEach((slot) => {
    const value = descriptor?.[slot];
    if (!value) return;
    if (value.raw) parts.push(value.raw);
    if (value.key && translate) {
      try { parts.push(translate(value.key, value.params || {})); } catch { /* ignore */ }
    }
  });
  return parts.map(str).join('\n').toLowerCase();
}

/**
 * Timeline view items in chronological order with search + kind filter.
 * @param {object} detail session detail (timeline is newest-first from the API)
 * @param {{search?: string, filter?: 'all'|'message'|'event', t?: Function}} [options]
 *   t: optional translator so searches also match translated titles.
 */
export function buildTimelineItems(detail, options = {}) {
  const filter = ['message', 'event'].includes(options.filter) ? options.filter : 'all';
  const query = str(options.search).trim().toLowerCase();
  const entries = maybeArray(detail?.timeline).slice().reverse();
  const items = [];
  entries.forEach((item, index) => {
    if (filter !== 'all' && item?.kind !== filter) return;
    const descriptor = describeTimelineItem(item);
    if (query && !timelineHaystack(item, descriptor, options.t).includes(query)) return;
    items.push({ id: timelineEntryId(item, index), kind: str(item?.kind), time: str(item?.time), eventType: str(item?.event_type), item, descriptor });
  });
  return items;
}

/** Latest compact flow events (chronological, default 3). */
export function compactFlowItems(detail, limit = 3) {
  return maybeArray(detail?.timeline)
    .filter((item) => item?.kind === 'event' && isCompactFlowEvent(item.event_type))
    .slice(0, limit)
    .reverse()
    .map((item, index) => ({ id: timelineEntryId(item, index), time: str(item.time), item, descriptor: describeTimelineItem(item) }));
}

// ---------------------------------------------------------------------------
// Todo / task board
// ---------------------------------------------------------------------------

/** Task board group order (runtime-derived groups). */
export const TASK_GROUP_ORDER = Object.freeze(['in_progress', 'ready', 'blocked', 'completed', 'cancelled']);

/** Todo counters; in_progress is counted from the todo snapshot itself. */
export function todoStats(taskBoard) {
  const todos = maybeArray(taskBoard?.todo);
  const count = (status) => todos.filter((item) => lower(item?.status) === status).length;
  return {
    total: todos.length,
    inProgress: count('in_progress'),
    pending: count('pending'),
    completed: count('completed'),
    cancelled: count('cancelled')
  };
}

/** Non-empty task groups in TASK_GROUP_ORDER with derived status per task. */
export function taskBoardGroups(taskBoard) {
  const groups = isPlainObject(taskBoard?.groups) ? taskBoard.groups : {};
  return TASK_GROUP_ORDER
    .map((key) => ({
      key,
      label: { key: 'tasks.group.' + key, params: { fallback: humanizeToken(key) } },
      items: maybeArray(groups[key]).map((task) => taskItemView(task, key))
    }))
    .filter((group) => group.items.length);
}

/** Metrics for the Tasks inspector (never uses counters for Todo progress). */
export function taskBoardSummary(taskBoard) {
  const groups = isPlainObject(taskBoard?.groups) ? taskBoard.groups : {};
  const counters = isPlainObject(taskBoard?.counters) ? taskBoard.counters : {};
  const todo = todoStats(taskBoard);
  const grouped = taskBoardGroups(taskBoard);
  return {
    todoCount: todo.total,
    todoInProgress: todo.inProgress,
    readyCount: maybeArray(groups.ready).length,
    blockedCount: maybeArray(groups.blocked).length,
    completedCount: Number(counters.completed || 0),
    cancelledCount: Number(counters.cancelled || 0),
    taskCount: grouped.reduce((total, group) => total + group.items.length, 0),
    groups: grouped
  };
}

/** Task card view; durable facts are raw slots, fallbacks are keys. */
export function taskItemView(task, derivedStatus = '') {
  const status = str(derivedStatus || task?.status);
  const title = str(task?.subject || task?.id);
  return {
    id: str(task?.id),
    status,
    statusLabel: statusLabel(status),
    tone: toneForStatus(status),
    title: title ? { raw: title } : { key: 'tasks.untitledTask' },
    description: task?.description ? { raw: str(task.description) } : { key: 'tasks.noDescription' },
    chips: [
      task?.id ? { raw: str(task.id) } : null,
      task?.priority ? { raw: str(task.priority) } : null,
      task?.owner ? { raw: str(task.owner) } : null,
      maybeArray(task?.blocked_by).length ? { key: 'tasks.blockedBy', params: { count: task.blocked_by.length } } : null,
      maybeArray(task?.blocks).length ? { key: 'tasks.blocks', params: { count: task.blocks.length } } : null
    ].filter(Boolean),
    task
  };
}

/** Todo card view. */
export function todoItemView(item) {
  const status = str(item?.status || 'pending');
  return {
    status,
    statusLabel: statusLabel(status),
    tone: toneForStatus(status),
    title: item?.content ? { raw: str(item.content) } : { key: 'tasks.untitledTodo' },
    priority: item?.priority ? { raw: str(item.priority) } : null,
    updatedAt: str(item?.updated_at)
  };
}

function progressRank(status) {
  const value = lower(status);
  if (value === 'in_progress' || value === 'running') return 0;
  if (value === 'pending' || value === 'todo' || value === 'ready') return 1;
  if (value === 'completed' || value === 'done') return 2;
  return 3;
}

/** Composer progress bar: todos + tasks sorted active-first with done %. */
export function todoProgress(taskBoard) {
  const items = [];
  maybeArray(taskBoard?.todo).forEach((todo) => {
    items.push({ kind: 'todo', label: todo?.content ? { raw: str(todo.content) } : { key: 'tasks.untitledTodo' }, status: str(todo?.status || 'pending'), owner: '' });
  });
  maybeArray(taskBoard?.tasks).forEach((task) => {
    const title = str(task?.subject || task?.id);
    items.push({ kind: 'task', label: title ? { raw: title } : { key: 'tasks.untitledTask' }, status: str(task?.status || 'pending'), owner: str(task?.owner) });
  });
  items.sort((a, b) => progressRank(a.status) - progressRank(b.status));
  let done = 0;
  let active = 0;
  items.forEach((item) => {
    const value = lower(item.status);
    if (value === 'completed' || value === 'done' || value === 'cancelled') done += 1;
    if (value === 'in_progress' || value === 'running') active += 1;
  });
  return { items, total: items.length, done, active, percent: items.length ? Math.round((done / items.length) * 100) : 0 };
}

// ---------------------------------------------------------------------------
// Shell redirect parsing and file changes
// ---------------------------------------------------------------------------

function readShellOutputRedirect(source, index) {
  const first = source[index];
  let prefix = '';
  let cursor = index;
  if ((first >= '0' && first <= '9') || first === '&') {
    if (source[index + 1] !== '>') return null;
    prefix = first;
    cursor += 1;
  } else if (first !== '>') {
    return null;
  }
  if (source[cursor] !== '>') return null;
  if (source[cursor + 1] === '>') return { token: prefix + '>>', end: cursor + 1 };
  if (source[cursor + 1] === '|') return { token: prefix + '>|', end: cursor + 1 };
  return { token: prefix + '>', end: cursor };
}

/**
 * Split a shell command into words, keeping output redirect operators as
 * tokens. Mirrors internal/filechanges/shell.go: an '&' directly after a
 * redirect starts the target token (2>&1 => ['2>', '&1']).
 */
export function tokenizeShellCommand(command) {
  const source = str(command);
  const tokens = [];
  let current = '';
  let quote = '';
  let escaping = false;
  let expectRedirectTarget = false;
  const flush = () => {
    if (current) {
      tokens.push(current);
      current = '';
      expectRedirectTarget = false;
    }
  };
  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i];
    if (escaping) {
      current += ch;
      escaping = false;
      continue;
    }
    if (ch === '\\' && quote !== "'") {
      escaping = true;
      continue;
    }
    if (quote) {
      if (ch === quote) quote = '';
      else current += ch;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (/\s/.test(ch)) {
      flush();
      continue;
    }
    const redirect = readShellOutputRedirect(source, i);
    if (redirect) {
      flush();
      tokens.push(redirect.token);
      expectRedirectTarget = true;
      i = redirect.end;
      continue;
    }
    if (ch === '&' && expectRedirectTarget && !current) {
      current += ch;
      continue;
    }
    if (ch === ';' || ch === '|' || ch === '&') {
      flush();
      expectRedirectTarget = false;
      continue;
    }
    current += ch;
  }
  flush();
  return tokens;
}

function readShellHereDocDelimiter(line, start) {
  const source = str(line);
  let value = '';
  let quote = '';
  let escaping = false;
  for (let i = start; i < source.length; i += 1) {
    const ch = source[i];
    if (escaping) {
      value += ch;
      escaping = false;
      continue;
    }
    if (ch === '\\' && quote !== "'") {
      escaping = true;
      continue;
    }
    if (quote) {
      if (ch === quote) quote = '';
      else value += ch;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (/\s|[;|&<>]/.test(ch)) return { value, end: i };
    value += ch;
  }
  return { value, end: source.length };
}

/** Here-doc delimiters opened on one line ({value, stripTabs} for <<- ). */
export function collectShellHereDocDelimiters(line) {
  const source = str(line);
  const markers = [];
  let quote = '';
  let escaping = false;
  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i];
    if (escaping) {
      escaping = false;
      continue;
    }
    if (ch === '\\' && quote !== "'") {
      escaping = true;
      continue;
    }
    if (quote) {
      if (ch === quote) quote = '';
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (ch !== '<' || source[i + 1] !== '<') continue;
    if (source[i + 2] === '<') {
      i += 2;
      continue;
    }
    let cursor = i + 2;
    let stripTabs = false;
    if (source[cursor] === '-') {
      stripTabs = true;
      cursor += 1;
    }
    while (source[cursor] === ' ' || source[cursor] === '\t') cursor += 1;
    const marker = readShellHereDocDelimiter(source, cursor);
    if (marker.value) markers.push({ value: marker.value, stripTabs });
    if (marker.end > i) i = marker.end - 1;
  }
  return markers;
}

/** Remove here-doc bodies so their text is never parsed as redirects. */
export function stripShellHereDocBodies(command) {
  const source = str(command);
  if (!source.includes('<<')) return source;
  const lines = source.match(/[^\n]*\n|[^\n]+/g) || [];
  const pending = [];
  let output = '';
  lines.forEach((line) => {
    const lineText = line.replace(/[\r\n]+$/, '');
    if (pending.length) {
      const marker = pending[0];
      const target = marker.stripTabs ? lineText.replace(/^\t+/, '') : lineText;
      if (target === marker.value) pending.shift();
      return;
    }
    output += line;
    pending.push(...collectShellHereDocDelimiters(line));
  });
  return output;
}

/** True for >, >>, >|, N>, &> style operator tokens. */
export function isShellOutputRedirect(token) {
  return /^(?:\d+|&)?(?:>|>>|>\|)$/.test(str(token));
}

/** Redirect target or '' for fds, /dev/null, /dev/fd/*, process substitution. */
export function cleanShellRedirectTarget(target) {
  const value = str(target).trim();
  if (!value || value === '-' || value.startsWith('&') || value.startsWith('(')) return '';
  if (value === '/dev/null' || value.startsWith('/dev/fd/')) return '';
  return value;
}

/** File targets written by output redirects: [{path, mode:'write'|'append'}]. */
export function collectShellRedirectPaths(command) {
  const tokens = tokenizeShellCommand(stripShellHereDocBodies(command));
  const paths = [];
  for (let i = 0; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (!isShellOutputRedirect(token)) continue;
    const target = cleanShellRedirectTarget(tokens[i + 1]);
    if (!target) continue;
    paths.push({ path: target, mode: token.includes('>>') ? 'append' : 'write' });
    i += 1;
  }
  return paths;
}

function fileChangeOp(file) {
  if (file.writes > 0 && file.edits === 0) return 'new';
  if (file.edits > 0) return 'edit';
  return '';
}

/**
 * Best-effort file changes inferred from messages (fallback when the detail
 * has no durable file_changes). Mirrors the backend: only calls with a
 * successful (non-error) result count; unresolved calls are ignored.
 */
export function inferFileChangesFromMessages(messages) {
  const list = maybeArray(messages);
  const outcomes = new Map();
  list.forEach((message) => {
    maybeArray(message.tool_results).forEach((result) => {
      if (!result?.tool_call_id) return;
      const id = str(result.tool_call_id);
      outcomes.set(id, outcomes.get(id) === false ? false : !result.is_error);
    });
  });
  const fileMap = new Map();
  const entry = (path) => {
    if (!fileMap.has(path)) fileMap.set(path, { path, writes: 0, edits: 0, linesAdded: 0, linesRemoved: 0 });
    return fileMap.get(path);
  };
  list.forEach((message) => {
    maybeArray(message.tool_calls).forEach((call) => {
      const ok = toolCallIDs(call).some((id) => outcomes.get(str(id)) === true);
      if (!ok) return;
      const args = toolArgs(call);
      if (call.name === 'shell') {
        collectShellRedirectPaths(args?.command).forEach((redirect) => {
          const file = entry(redirect.path);
          if (redirect.mode === 'append') file.edits += 1;
          else file.writes += 1;
        });
        return;
      }
      if ((call.name !== 'write_file' && call.name !== 'edit_file') || !args?.path) return;
      const file = entry(str(args.path));
      if (call.name === 'write_file') {
        file.writes += 1;
        file.linesAdded += str(args.content).split('\n').length;
      } else {
        file.edits += 1;
        const oldLines = str(args.old_text).split('\n').length;
        const newLines = str(args.new_text).split('\n').length;
        file.linesAdded += Math.max(0, newLines - oldLines);
        file.linesRemoved += Math.max(0, oldLines - newLines);
      }
    });
  });
  return Array.from(fileMap.values());
}

/**
 * File-change panel model. Durable detail.file_changes win; otherwise changes
 * are inferred from loaded messages. Shell changes are always heuristic, so a
 * non-empty panel carries the "partial" disclosure.
 */
export function collectFileChanges(detail) {
  const durable = maybeArray(detail?.file_changes).filter((item) => item && item.path);
  const source = durable.length ? 'durable' : 'inferred';
  const raw = durable.length
    ? durable.map((item) => ({
      path: str(item.path),
      writes: Number(item.writes || 0),
      edits: Number(item.edits || 0),
      linesAdded: Number(item.lines_added || item.linesAdded || 0),
      linesRemoved: Number(item.lines_removed || item.linesRemoved || 0)
    }))
    : inferFileChangesFromMessages(detail?.messages);
  const files = raw.map((file) => ({ ...file, op: fileChangeOp(file) }));
  return {
    files,
    source: files.length ? source : 'none',
    totals: {
      files: files.length,
      added: files.reduce((sum, file) => sum + file.linesAdded, 0),
      removed: files.reduce((sum, file) => sum + file.linesRemoved, 0)
    },
    disclosure: files.length ? { badge: { key: 'files.partial' }, note: { key: 'files.disclosure' } } : null
  };
}

// ---------------------------------------------------------------------------
// Sub-agents, queue jobs and background notifications
// ---------------------------------------------------------------------------

/** Sort helper: created_at ascending, then id. */
export function compareCreatedAscending(left, right) {
  const leftCreated = str(left?.created_at);
  const rightCreated = str(right?.created_at);
  if (leftCreated !== rightCreated) return leftCreated < rightCreated ? -1 : 1;
  const leftId = str(left?.id || left?.session_id || left?.queue_job_id);
  const rightId = str(right?.id || right?.session_id || right?.queue_job_id);
  return leftId.localeCompare(rightId);
}

/** Merge child sessions and queue jobs into rows ({session, job}) by session/job id. */
export function mergeSubAgentRows(children, jobs) {
  const rows = [];
  const bySessionId = new Map();
  const byQueueJobId = new Map();
  maybeArray(children).slice().sort(compareCreatedAscending).forEach((sessionItem) => {
    const row = { session: sessionItem, job: null };
    rows.push(row);
    if (sessionItem?.id) bySessionId.set(sessionItem.id, row);
    if (sessionItem?.queue_job_id) byQueueJobId.set(sessionItem.queue_job_id, row);
  });
  maybeArray(jobs).slice().sort(compareCreatedAscending).forEach((job) => {
    const row = (job?.session_id && bySessionId.get(job.session_id)) || (job?.id && byQueueJobId.get(job.id));
    if (row) {
      row.job = job;
      return;
    }
    rows.push({ session: null, job });
  });
  return rows.sort((left, right) => compareCreatedAscending(left.session || left.job || {}, right.session || right.job || {}));
}

/** completed | cancelled | failed */
export function isTerminalStatus(status) {
  return status === 'completed' || status === 'cancelled' || status === 'failed';
}

/** Queue terminal/blocked status wins over the child session status. */
export function queueJobDisplayStatus(job) {
  if (!job) return 'unknown';
  if (isTerminalStatus(job.status) || job.status === 'blocked') return job.status;
  return job.session_status || job.status || 'unknown';
}

/** Row status: terminal/blocked queue status wins over resumable child status. */
export function subAgentDisplayStatus(sessionItem, job) {
  const jobStatus = job ? queueJobDisplayStatus(job) : '';
  if (jobStatus && (isTerminalStatus(jobStatus) || jobStatus === 'blocked')) return jobStatus;
  return sessionItem?.status || jobStatus || 'unknown';
}

/**
 * Structured child budget summary (metadata.effective_budget / job snapshot).
 * @returns {{status:string, parts:Array<object>}} parts are text slots; time
 *   values are ISO strings in params.at for locale formatting.
 */
export function effectiveBudgetSummary(budget, pauseReason = '') {
  if (!isPlainObject(budget)) return { status: 'unknown', parts: [{ key: 'budget.unknown' }] };
  if (budget.status === 'disabled') return { status: 'disabled', parts: [{ key: 'budget.offSnapshotted' }] };
  const parts = [];
  if (Number(budget.attempt || 0) > 0) parts.push({ key: 'budget.attempt', params: { attempt: Number(budget.attempt) } });
  if (Number(budget.max_turns_per_attempt || 0) > 0) {
    const used = Number(budget.used_turns || 0);
    const limit = Number(budget.max_turns_per_attempt);
    const remaining = budget.remaining_turns === undefined || budget.remaining_turns === null ? Math.max(0, limit - used) : Number(budget.remaining_turns || 0);
    parts.push({ key: 'budget.turns', params: { used, limit, remaining } });
  }
  if (Number(budget.max_active_runtime_ms || 0) > 0) {
    const usedSec = Math.ceil(Number(budget.used_active_runtime_ms || 0) / 1000);
    const limitSec = Math.ceil(Number(budget.max_active_runtime_ms) / 1000);
    const remainingMS = budget.remaining_active_runtime_ms === undefined || budget.remaining_active_runtime_ms === null
      ? Math.max(0, Number(budget.max_active_runtime_ms) - Number(budget.used_active_runtime_ms || 0))
      : Number(budget.remaining_active_runtime_ms || 0);
    parts.push({ key: 'budget.active', params: { usedSec, limitSec, remainingSec: Math.ceil(remainingMS / 1000) } });
    if (budget.active_runtime_checkpoint_at) {
      parts.push({ key: budget.active_runtime_lease_open ? 'budget.checkpointLeaseOpen' : 'budget.checkpointLeaseClosed', params: { at: str(budget.active_runtime_checkpoint_at) } });
    }
    const recoveryMS = Number(budget.active_runtime_last_recovery_ms || 0);
    if (recoveryMS > 0) {
      const value = recoveryMS < 1000 ? recoveryMS + 'ms' : Number((recoveryMS / 1000).toFixed(3)) + 's';
      parts.push({ key: budget.active_runtime_last_recovery_at ? 'budget.crashRecoveryAt' : 'budget.crashRecovery', params: { value, at: str(budget.active_runtime_last_recovery_at) } });
    }
  }
  if (budget.absolute_deadline_at) parts.push({ key: 'budget.deadline', params: { at: str(budget.absolute_deadline_at) } });
  const reason = str(pauseReason || budget.last_reason).trim();
  if (reason) parts.push({ raw: reason });
  if (budget.source) parts.push({ raw: str(budget.source) });
  if (!parts.length) parts.push(dynamicLabel('budgetStatus', budget.status || 'active'));
  return { status: str(budget.status), parts };
}

/** View model of one sub-agent row ({session, job}). */
export function subAgentRowView(row) {
  const sessionItem = row?.session || null;
  const job = row?.job || null;
  const status = subAgentDisplayStatus(sessionItem, job);
  const label = agentLabel(sessionItem?.agent_name || job?.agent_name, sessionItem?.agent_role || job?.agent_role) || shortId(sessionItem?.id || job?.id);
  const error = str(sessionItem?.last_error || job?.last_error);
  const finalText = str(job?.final_text || sessionItem?.final_text || sessionItem?.last_assistant_excerpt);
  const budget = sessionItem?.effective_budget || job?.effective_budget || null;
  const sessionId = str(sessionItem?.id || job?.session_id);
  return {
    id: sessionId || str(job?.id),
    sessionId,
    jobId: str(job?.id || sessionItem?.queue_job_id),
    parentSessionId: str(job?.parent_session_id || sessionItem?.parent_session_id),
    status,
    statusLabel: statusLabel(status),
    tone: toneForStatus(status),
    label: label ? { raw: label } : { key: 'subagent.childAgent' },
    model: { raw: str(sessionItem?.model || job?.model || sessionItem?.provider || job?.provider) },
    phase: sessionItem?.phase ? phaseLabel(sessionItem.phase) : { raw: str(job?.mode || 'exec') },
    mode: str(job?.mode),
    error: error ? { raw: truncateText(error, 180) } : null,
    finalText: !error && finalText ? { raw: truncateText(finalText, 180) } : null,
    budget: budget ? effectiveBudgetSummary(budget, sessionItem?.pause_reason || budget.last_reason) : null,
    visiblePaths: maybeArray(sessionItem?.visible_paths || job?.visible_paths).slice(0, 6),
    stoppable: Boolean(sessionId) && isStoppableSessionStatus(status),
    session: sessionItem,
    job
  };
}

/** Sub-agent rows for detail.children (sessions + jobs merged and sorted). */
export function subAgentRows(detail) {
  return mergeSubAgentRows(detail?.children?.sessions, detail?.children?.jobs).map(subAgentRowView);
}

/** delivery_status=pending means the parent has not consumed the result yet. */
export function isPendingBackgroundNotification(item) {
  return lower(item?.delivery_status) === 'pending';
}

/** View model of one background notification (errors win over final text). */
export function backgroundNotificationView(item) {
  const label = agentLabel(item?.agent_name, item?.agent_role);
  const status = str(item?.status || item?.session_status || 'unknown');
  const pending = isPendingBackgroundNotification(item);
  let copy;
  if (item?.last_error) copy = { raw: str(item.last_error) };
  else if (item?.final_text) copy = { raw: str(item.final_text) };
  else copy = { key: pending ? 'notification.pendingDelivery' : 'notification.noFinalText' };
  return {
    id: str(item?.id),
    title: label ? { raw: label } : { key: 'notification.fallbackTitle' },
    status,
    statusLabel: statusLabel(status),
    tone: toneForStatus(status),
    copy,
    pendingHint: pending ? { key: 'notification.pendingHint' } : null,
    actions: maybeArray(item?.available_actions).map((action) => ({ action: str(action), key: 'notification.action.' + str(action), params: { fallback: humanizeToken(action) } })),
    budget: item?.effective_budget ? effectiveBudgetSummary(item.effective_budget, item.effective_budget.last_reason) : null,
    meta: {
      id: { raw: shortId(item?.queue_job_id || item?.session_id || item?.id) },
      deliveryStatus: item?.delivery_status ? { raw: str(item.delivery_status) } : null,
      sessionStatus: item?.session_status ? statusLabel(item.session_status) : null
    },
    sessionId: str(item?.session_id),
    visiblePaths: maybeArray(item?.visible_paths).slice(0, 6)
  };
}

// ---------------------------------------------------------------------------
// Goal runtime facts
// ---------------------------------------------------------------------------

/** Completed goal while the session itself continues (historical goal). */
export function isHistoricalCompletedGoal(detail) {
  return lower(detail?.goal?.status) === 'complete' && lower(detail?.state?.status) !== 'completed';
}

/** Goal runtime facts (latest history/progress, session status/phase). */
export function goalRuntimeFacts(detail) {
  const goal = detail?.goal || null;
  if (!goal) return null;
  const latestHistory = detail?.goal_facts?.latest_history || null;
  const progress = maybeArray(detail?.goal_facts?.progress || goal.progress);
  const latestProgress = progress.length ? progress[progress.length - 1] : null;
  return {
    goal,
    latestHistory,
    latestProgress,
    latestAt: str(latestHistory?.created_at || latestProgress?.created_at || goal.updated_at || goal.created_at),
    latestType: str(latestHistory?.type || latestProgress?.kind),
    progressCount: progress.length,
    runStatus: str(detail?.state?.status),
    runPhase: str(detail?.state?.phase),
    historical: isHistoricalCompletedGoal(detail)
  };
}

/**
 * Session goal line: badge + ordered parts (text slots). Accounting values
 * are params; latest.at is an ISO time for locale formatting.
 */
export function sessionGoalLine(detail) {
  const facts = goalRuntimeFacts(detail);
  if (!facts) return { off: true, badge: { key: 'goal.off' }, copy: { key: 'goal.noneAttached' }, parts: [] };
  const goal = facts.goal;
  const parts = [
    facts.runStatus ? { key: 'goal.line.session', params: { status: lower(facts.runStatus), statusLabel: statusLabel(facts.runStatus) } } : null,
    facts.runPhase ? phaseLabel(facts.runPhase) : null,
    { key: 'goal.line.tokens', params: { value: formatBudget(goal.tokens_used, goal.token_budget) } },
    { key: 'goal.line.providerTime', params: { value: formatSecondsBudget(goal.provider_time_used_seconds ?? goal.time_used_seconds, goal.provider_time_budget_seconds ?? goal.time_budget_seconds) } },
    facts.latestType ? { key: 'goal.line.latest', params: { type: facts.latestType, typeLabel: eventTypeLabel(facts.latestType) } } : null,
    facts.latestAt ? { key: 'goal.line.at', params: { at: facts.latestAt } } : null
  ].filter(Boolean);
  return {
    off: false,
    status: lower(goal.status || 'active'),
    badge: goalStatusLabel(goal),
    tone: goalStatusTone(goal.status),
    historical: facts.historical,
    historicalBadge: facts.historical ? { key: 'goal.historical' } : null,
    objective: goal.objective ? { raw: str(goal.objective) } : null,
    parts
  };
}

// ---------------------------------------------------------------------------
// Session titles
// ---------------------------------------------------------------------------

function firstUserPrompt(messages) {
  const message = maybeArray(messages).find((item) => item?.role === 'user' &&
    !isHarnessReminder(item) && !isBackgroundResultsMessage(item) && str(item.text).trim());
  return message ? firstLine(message.text) : '';
}

/**
 * Title for session lists: first user prompt line, prompt-derived goal
 * objective, agent name/role, short id, then the session.untitled key.
 * @param {object} item SessionSummary or detail-like object
 * @param {{messages?: Array, limit?: number}} [options]
 */
export function deriveSessionTitle(item, options = {}) {
  const limit = options.limit || 80;
  const explicit = firstLine(item?.title || item?.first_prompt || item?.prompt);
  if (explicit) return { raw: truncateText(explicit, limit), source: 'prompt' };
  const fromMessages = firstUserPrompt(options.messages || item?.messages);
  if (fromMessages) return { raw: truncateText(fromMessages, limit), source: 'prompt' };
  const objective = firstLine(item?.goal_objective || item?.goal?.objective || item?.plan_mode_summary);
  if (objective) return { raw: truncateText(objective, limit), source: 'objective' };
  const label = agentLabel(item?.agent_name || item?.metadata?.agent_name, item?.agent_role || item?.metadata?.agent_role);
  if (label) return { raw: label, source: 'agent' };
  const id = shortId(item?.id || item?.metadata?.id);
  if (id) return { raw: id, source: 'id' };
  return { key: 'session.untitled', source: 'fallback' };
}

/** Sessions management title: custom agent identity raw, else master fallback key. */
export function historySessionTitle(item) {
  const label = agentLabel(item?.agent_name, item?.agent_role);
  return label ? { raw: label } : { key: 'session.masterFallback' };
}

// ---------------------------------------------------------------------------
// Plan Mode input answers
// ---------------------------------------------------------------------------

/** Wire label for free-form answers (protocol value, not UI text). */
export const PLAN_INPUT_OTHER_LABEL = 'Other';

/**
 * Build request_user_input answers. Returns [] unless every question has an
 * explicit, valid selection (offered option label/description or Other text).
 */
export function collectPlanInputAnswers(request, selections) {
  const questions = maybeArray(request?.questions);
  if (!questions.length || !selections || typeof selections !== 'object') return [];
  const answers = [];
  for (const question of questions) {
    const questionID = str(question?.id).trim();
    const selected = questionID ? selections[questionID] : null;
    if (!questionID || !selected || typeof selected !== 'object') return [];
    const label = str(selected.label).trim();
    const value = str(selected.value).trim();
    if (!label && !value) return [];
    if (!selected.is_other) {
      const option = maybeArray(question?.options).find((item) => {
        const itemLabel = str(item?.label).trim();
        const itemDescription = str(item?.description).trim();
        if (label) return itemLabel === label;
        return value && (itemLabel === value || itemDescription === value);
      });
      if (!option) return [];
      const optionLabel = str(option.label).trim();
      const optionDescription = str(option.description).trim();
      if ((label && label !== optionLabel) || (value && value !== optionLabel && value !== optionDescription)) return [];
      answers.push({ question_id: questionID, label: optionLabel, value: value || optionLabel, is_other: false });
      continue;
    }
    if (!value) return [];
    answers.push({ question_id: questionID, label: label || PLAN_INPUT_OTHER_LABEL, value, is_other: true });
  }
  return answers;
}

/** Keep only selections for the current request id and its questions. */
export function prunePlanInputSelections(request, selectionsByRequest) {
  const requestID = str(request?.request_id).trim();
  if (!requestID) return {};
  const current = isPlainObject(selectionsByRequest?.[requestID]) ? selectionsByRequest[requestID] : {};
  const valid = new Set(maybeArray(request?.questions).map((question) => str(question?.id).trim()).filter(Boolean));
  const kept = {};
  Object.keys(current).forEach((questionID) => {
    if (valid.has(questionID)) kept[questionID] = current[questionID];
  });
  return { [requestID]: kept };
}
