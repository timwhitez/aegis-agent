// Pure view-model helpers for the session message stream (no DOM, no store).
// Everything operator-facing comes back as lib text slots ({ key, params } /
// { raw }) or is translated here through t()/tx(); raw durable values are never
// passed through t(). Imported by the stream components and by
// validation/scripts/ui_session_view_model_test.mjs.
import {
  buildConversation,
  collectFileChanges,
  isGoalToolName,
  isMultiAgentTool,
  liveActivityForRefresh,
  liveActivityFromEvent,
  maybeArray,
  messageVariant,
  parseMaybeJSON,
  sessionActivityForDetail,
} from '../../../lib/session-model.js';
import { formatDateTime, t, tx } from '../../../lib/i18n.js';

const lower = (value) => String(value ?? '').trim().toLowerCase();

// ---- text slots --------------------------------------------------------------

/** Resolves nested descriptor params ({ statusLabel: { key } }) and ISO `at` params to strings. */
export function resolveSlotParams(params) {
  const out = {};
  for (const [name, value] of Object.entries(params || {})) {
    if (value && typeof value === 'object' && typeof value.key === 'string') out[name] = tx(value);
    else if (name === 'at' && typeof value === 'string' && value) out[name] = formatDateTime(value) || value;
    else out[name] = value;
  }
  return out;
}

/** Operator text of a slot's key part ('' when the slot only carries raw text). */
export function slotText(slot) {
  if (!slot || typeof slot !== 'object' || !slot.key) return '';
  return tx({ key: slot.key, params: resolveSlotParams(slot.params) });
}

// ---- tool kinds ------------------------------------------------------------------

export const TOOL_KIND_ICONS = Object.freeze({
  shell: 'terminal',
  read: 'file-text',
  write: 'pencil',
  search: 'search',
  agent: 'git-branch',
  todo: 'list-checks',
  task: 'clipboard-list',
  goal: 'target',
  plan: 'clipboard-list',
  finish: 'check',
  await: 'hand',
  skill: 'box',
  web: 'globe',
  thinking: 'brain',
  other: 'wrench',
});

/** Coarse tool family used for step icons. */
export function toolKind(name) {
  const value = lower(name);
  if (value === 'thinking') return 'thinking';
  if (value === 'shell' || value === 'bash' || value === 'exec_command') return 'shell';
  if (value === 'write_file' || value === 'edit_file' || value === 'apply_patch') return 'write';
  if (value === 'read_file' || value === 'list_dir' || value === 'read_session_history') return 'read';
  if (value === 'grep' || value === 'glob' || value === 'grep_files' || value.includes('search')) return 'search';
  if (isMultiAgentTool(value)) return 'agent';
  if (value.startsWith('todo_')) return 'todo';
  if (value.startsWith('task_')) return 'task';
  if (isGoalToolName(value)) return 'goal';
  if (value === 'submit_plan' || value === 'request_user_input' || value === 'get_plan_mode') return 'plan';
  if (value === 'finish') return 'finish';
  if (value === 'await_input') return 'await';
  if (value === 'load_skill') return 'skill';
  if (value.startsWith('browser') || value.startsWith('web_') || value === 'fetch_url') return 'web';
  return 'other';
}

export function toolIcon(name) {
  return TOOL_KIND_ICONS[toolKind(name)] || TOOL_KIND_ICONS.other;
}

// ---- durations ---------------------------------------------------------------------

/** Localized compact duration: "不到 1 秒" / "12 秒" / "2 分 5 秒" (en: "<1s", "12s", "2m 5s"). */
export function formatElapsed(ms) {
  const value = Number(ms);
  if (ms === null || ms === undefined || !Number.isFinite(value) || value < 0) return '';
  if (value < 1000) return t('session.duration.subsecond');
  const total = Math.floor(value / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return t('session.duration.hours', { h, m });
  if (m > 0) return t('session.duration.minutes', { m, s });
  return t('session.duration.seconds', { s });
}

function timeMs(value) {
  if (!value) return null;
  const ms = Date.parse(String(value));
  return Number.isFinite(ms) ? ms : null;
}

/** Duration of one step; running steps use now. null when unknown. */
export function stepDurationMs(step, now) {
  const start = timeMs(step?.startedAt);
  if (start === null) return null;
  if (step.status === 'running') return Number.isFinite(now) ? Math.max(0, now - start) : null;
  const end = timeMs(step?.endedAt);
  return end === null ? null : Math.max(0, end - start);
}

/** Duration of a step group; running groups use now. */
export function stepGroupDurationMs(group, now) {
  if (group?.status === 'running') {
    const start = timeMs(group.startedAt);
    return start !== null && Number.isFinite(now) ? Math.max(0, now - start) : null;
  }
  return Number.isFinite(group?.durationMs) ? group.durationMs : null;
}

// ---- conversation ------------------------------------------------------------------

/** Cheap identity of a durable message window (messages are append-only by id). */
export function conversationKey(messages) {
  const list = maybeArray(messages);
  const first = list[0];
  const last = list[list.length - 1];
  return [list.length, first?.id || '', last?.id || '', last?.created_at || ''].join('|');
}

/**
 * Returns compute({ messages, optimistic, live, agentName, agentRole }) that
 * re-runs buildConversation() only when an input changed (polls hand in a new
 * but equivalent messages array every 1.5s).
 */
export function createConversationMemo(build = buildConversation) {
  let previous = null;
  return function compute({ messages, optimistic = [], live = false, agentName = '', agentRole = '' } = {}) {
    const key = conversationKey(messages);
    if (previous && previous.key === key && previous.optimistic === optimistic && previous.live === live
      && previous.agentName === agentName && previous.agentRole === agentRole) {
      return previous.items;
    }
    const items = foldFinishOnlyGroups(build(maybeArray(messages), {
      live,
      metadata: { agent_name: agentName, agent_role: agentRole },
      optimistic: maybeArray(optimistic),
    }));
    previous = { key, optimistic, live, agentName, agentRole, items };
    return items;
  };
}

/**
 * A step group made only of finish calls whose final answer is rendered right
 * after it adds no information to the stream: attach those raw steps to the
 * final assistant item (finishSteps) so they stay reachable from the answer.
 */
export function foldFinishOnlyGroups(items) {
  const list = maybeArray(items);
  const out = [];
  for (let index = 0; index < list.length; index += 1) {
    const item = list[index];
    const next = list[index + 1];
    if (item?.kind === 'steps' && item.steps.length && item.steps.every((step) => step.compactFinal)
      && next?.kind === 'message' && next.final) {
      out.push({ ...next, finishSteps: item.steps });
      index += 1;
      continue;
    }
    out.push(item);
  }
  return out;
}

/** Render signature of a stream item; equal signatures may reuse the previous vnode. */
export function itemSignature(item) {
  if (!item) return '';
  if (item.kind === 'steps') {
    return ['g', item.id, item.status, item.durationMs ?? '', item.steps.map((step) => `${step.id}:${step.status}:${step.results.length}`).join(',')].join('|');
  }
  const actor = item.actor?.raw || item.actor?.key || '';
  const finish = maybeArray(item.finishSteps).map((step) => `${step.id}:${step.status}`).join(',');
  return ['m', item.id, item.variant, item.pending ? 1 : 0, item.final ? 1 : 0, item.text.length, item.thinking.length, item.createdAt, actor, item.background?.count ?? '', finish].join('|');
}

/** Stream data-role: user | assistant | harness | system | background | tool. */
export function displayRole(variant) {
  switch (variant) {
    case 'user':
    case 'steer':
      return 'user';
    case 'harness_reminder':
      return 'harness';
    case 'background_results':
      return 'background';
    case 'system':
      return 'system';
    case 'tool':
      return 'tool';
    default:
      return 'assistant';
  }
}

// ---- turns ---------------------------------------------------------------------------

/** Messages after the latest operator prompt (steers stay inside the turn). */
export function latestTurnMessages(messages) {
  const list = maybeArray(messages);
  for (let index = list.length - 1; index >= 0; index -= 1) {
    if (messageVariant(list[index]) === 'user') return list.slice(index + 1);
  }
  return list.slice();
}

/** Start of the running turn (epoch ms): latest operator prompt, durable or optimistic. */
export function turnStartMs(messages, optimistic = []) {
  const all = maybeArray(messages).concat(maybeArray(optimistic));
  for (let index = all.length - 1; index >= 0; index -= 1) {
    if (messageVariant(all[index]) === 'user') {
      const ms = timeMs(all[index].created_at);
      if (ms !== null) return ms;
    }
  }
  return null;
}

/** File changes inferred from the latest turn only (collectFileChanges view model). */
export function latestTurnFileChanges(messages) {
  return collectFileChanges({ messages: latestTurnMessages(messages) });
}

/** Reason of the latest await_input call ('' when none). */
export function awaitInputReason(messages) {
  const list = maybeArray(messages);
  for (let index = list.length - 1; index >= 0; index -= 1) {
    const calls = maybeArray(list[index]?.tool_calls);
    for (let c = calls.length - 1; c >= 0; c -= 1) {
      if (calls[c]?.name !== 'await_input') continue;
      const args = parseMaybeJSON(calls[c].arguments);
      return String(args?.reason || '').trim();
    }
    if (messageVariant(list[index]) === 'user') return '';
  }
  return '';
}

// ---- end-of-stream state -------------------------------------------------------------

const FAILURE_KEYS = Object.freeze({
  authRouting: { label: 'session.failure.authRouting.label', hint: 'session.failure.authRouting.hint' },
  credentials: { label: 'session.failure.credentials.label', hint: 'session.failure.credentials.hint' },
  rateLimit: { label: 'session.failure.rateLimit.label', hint: 'session.failure.rateLimit.hint' },
  request: { label: 'session.failure.request.label', hint: 'session.failure.request.hint' },
  transport: { label: 'session.failure.transport.label', hint: 'session.failure.transport.hint' },
  server: { label: 'session.failure.server.label', hint: 'session.failure.server.hint' },
});

/**
 * Provider failure family of a session last_error (port of the old
 * summarizeProviderFailure). Returns { kind, label, hint, retries } or null.
 */
export function classifyProviderFailure(lastError, events = []) {
  const raw = String(lastError || '').trim();
  if (!raw) return null;
  const text = raw.toLowerCase();
  const retries = maybeArray(events).filter((event) => event?.type === 'provider.retry').length;
  const make = (kind) => ({
    kind,
    label: { key: FAILURE_KEYS[kind].label },
    hint: { key: FAILURE_KEYS[kind].hint },
    retries: kind === 'transport' && retries > 0 ? { key: 'session.failure.transport.retries', params: { count: retries } } : null,
  });
  if (text.includes('auth_unavailable') || text.includes('no auth available')) return make('authRouting');
  if (['token_invalidated', 'token_revoked', 'invalid api key', 'auth_error', 'unauthorized', 'forbidden'].some((needle) => text.includes(needle))) return make('credentials');
  if (text.includes('rate_limit') || text.includes('429')) return make('rateLimit');
  if (['invalid_request', 'unprocessable', 'unsupported parameter'].some((needle) => text.includes(needle))) return make('request');
  if (['upstream_timeout', 'transport timeout', 'context deadline exceeded', 'i/o timeout', 'connection reset', 'stream disconnected', 'stream disconnect'].some((needle) => text.includes(needle))) return make('transport');
  if (text.includes('server_error')) return make('server');
  return null;
}

/**
 * Inline note shown after the stream when no run is active.
 * @returns {null | { kind, tone, icon, title, copy, reason, error, failure, at }}
 *   kind: completed | awaiting | stopped | interrupted | paused | cancelled | failed.
 *   Plan Mode gates return null (their cards own the end of the stream).
 */
export function streamEndNote(detail, { generating = false, messages = [] } = {}) {
  if (!detail || generating) return null;
  const planStatus = lower(detail.plan_mode?.status);
  if (planStatus === 'awaiting_approval' || planStatus === 'awaiting_user_input') return null;
  const state = detail.state || {};
  const status = lower(state.status);
  const activity = sessionActivityForDetail(detail);
  const base = { title: activity.title, copy: activity.copy, reason: null, error: null, failure: null, at: String(state.updated_at || '') };
  switch (status) {
    case 'completed':
      return { ...base, kind: 'completed', tone: 'success', icon: 'check', copy: null };
    case 'awaiting_input': {
      const reason = awaitInputReason(messages);
      return { ...base, kind: 'awaiting', tone: 'warning', icon: 'hand', reason: reason ? { raw: reason } : null };
    }
    case 'paused': {
      const pauseReason = String(state.pause_reason || '');
      const kind = pauseReason === 'manual_stop' ? 'stopped' : pauseReason === 'manual_interrupt' ? 'interrupted' : 'paused';
      return {
        ...base,
        kind,
        tone: kind === 'stopped' ? 'neutral' : 'warning',
        icon: kind === 'stopped' ? 'square' : 'pause',
        reason: kind === 'paused' && pauseReason ? { raw: pauseReason } : null,
      };
    }
    case 'cancelled':
      return { ...base, kind: 'cancelled', tone: 'danger', icon: 'x' };
    case 'failed': {
      const lastError = String(state.last_error || '').trim();
      return {
        ...base,
        kind: 'failed',
        tone: 'danger',
        icon: 'alert-circle',
        copy: { key: 'activity.failed.copy' },
        error: lastError ? { raw: lastError } : null,
        failure: classifyProviderFailure(lastError, detail.events),
      };
    }
    default:
      return null;
  }
}

const TERMINAL_EVENTS = new Set(['session.completed', 'session.failed', 'session.paused', 'session.cancelled']);

/**
 * Activity shown while a run is live: the newest promotable engine event of
 * the running turn (relay live events first, then polled detail.events), then
 * an optional override (e.g. a just-sent steer), then the refresh-derived
 * phase. Returns { title, subject, copy, tone, eventType? }.
 */
export function selectLiveActivity({ detail = null, liveEvents = [], sinceMs = null, override = null } = {}) {
  let best = null;
  let bestTime = -Infinity;
  const consider = (event, fallbackTime) => {
    if (!event || TERMINAL_EVENTS.has(String(event.type || ''))) return;
    const activity = liveActivityFromEvent(event);
    if (!activity) return;
    const ms = timeMs(event.time) ?? fallbackTime;
    if (sinceMs !== null && Number.isFinite(sinceMs) && ms < sinceMs - 2000) return;
    if (ms >= bestTime) {
      best = activity;
      bestTime = ms;
    }
  };
  maybeArray(detail?.events).forEach((event) => consider(event, -Infinity));
  maybeArray(liveEvents).forEach((event) => consider(event, Number.MAX_SAFE_INTEGER));
  if (best) return best;
  if (override?.title) return { subject: null, tone: 'live', ...override };
  if (detail) {
    const refreshed = liveActivityForRefresh(detail, { hasLiveEvents: false });
    if (refreshed.generating && refreshed.activity) return { subject: null, ...refreshed.activity };
  }
  return { title: { key: 'activity.startingTurn' }, subject: null, copy: { key: 'activity.runnerActive' }, tone: 'live' };
}

// ---- step groups -----------------------------------------------------------------------

/** Collapsed-row facts of a step group. */
export function stepGroupSummary(group) {
  const steps = maybeArray(group?.steps);
  const errors = steps.filter((step) => step.status === 'error').length;
  const names = [];
  steps.forEach((step) => {
    if (step.kind !== 'thinking' && step.name && !names.includes(step.name)) names.push(step.name);
  });
  const running = group?.status === 'running';
  const current = running ? steps.slice().reverse().find((step) => step.status === 'running') || null : null;
  return {
    count: steps.length,
    toolCount: Number(group?.toolCount ?? steps.filter((step) => step.kind !== 'thinking').length),
    errors,
    running,
    names: names.slice(0, 3),
    moreNames: Math.max(0, names.length - 3),
    current,
  };
}

/** "已执行 N 个步骤" / "正在执行第 N 步" / "思考过程". */
export function stepGroupLabel(summary) {
  if (summary.running) return t('session.steps.running', { count: summary.count });
  if (summary.toolCount === 0) return t('session.steps.thoughtOnly');
  return summary.count === 1 ? t('session.steps.one') : t('session.steps.many', { count: summary.count });
}

/** Group opens by default while running or when a step failed. */
export function stepGroupAutoOpen(summary) {
  return Boolean(summary.running || summary.errors > 0);
}

/** Arguments pane of a step: shell commands verbatim, everything else pretty JSON. */
export function stepArgumentsText(step) {
  if (!step || step.kind !== 'tool') return '';
  const args = step.args && typeof step.args === 'object' ? step.args : null;
  if (step.name === 'shell' && typeof args?.command === 'string') return args.command;
  const raw = step.rawArguments;
  if (raw === null || raw === undefined || raw === '') return '';
  if (typeof raw === 'string') {
    const trimmed = raw.trim();
    if (!trimmed || trimmed === '{}') return '';
    try {
      return JSON.stringify(JSON.parse(trimmed), null, 2);
    } catch {
      return raw;
    }
  }
  try {
    const text = JSON.stringify(raw, null, 2);
    return text === '{}' ? '' : text;
  } catch {
    return String(raw);
  }
}

/** Primitive metadata entries of a tool result ([[key, value]], at most limit). */
export function resultMetadataEntries(result, limit = 8) {
  const meta = result?.metadata;
  if (!meta || typeof meta !== 'object' || Array.isArray(meta)) return [];
  return Object.entries(meta)
    .filter(([, value]) => ['string', 'number', 'boolean'].includes(typeof value) && String(value) !== '')
    .slice(0, limit)
    .map(([key, value]) => [key, String(value)]);
}

// ---- text clipping -----------------------------------------------------------------------

/** Clip long text for previews: { text, truncated, totalLines, totalChars }. */
export function clipText(text, { maxLines = 20, maxChars = 4000 } = {}) {
  const value = String(text ?? '');
  const lines = value.split('\n');
  let clipped = value;
  let truncated = false;
  if (lines.length > maxLines) {
    clipped = lines.slice(0, maxLines).join('\n');
    truncated = true;
  }
  if (clipped.length > maxChars) {
    clipped = clipped.slice(0, maxChars);
    truncated = true;
  }
  return { text: clipped, truncated, totalLines: lines.length, totalChars: value.length };
}

/** One-line preview of long text ('' when empty). */
export function previewLine(text, limit = 140) {
  const line = String(text ?? '').replace(/\s+/g, ' ').trim();
  return line.length > limit ? line.slice(0, limit - 1) + '\u2026' : line;
}

// ---- tones -----------------------------------------------------------------------------------

/** Maps session-model tones (live | queued | danger | neutral) to Badge tones. */
export function badgeTone(libTone) {
  switch (libTone) {
    case 'live':
      return 'success';
    case 'queued':
      return 'warning';
    case 'danger':
      return 'danger';
    default:
      return 'neutral';
  }
}
