/**
 * Composer send intents, optimistic messages and failed drafts
 * (spec/17-web-console.md §8.1.1). Pure in-memory model: no DOM, no fetch,
 * no localStorage. Failed drafts deliberately never leave memory.
 *
 * Rules implemented here:
 *   - Every send gets a client request ID bound to its original session
 *     (including ephemeral new-session identities) and its send kind
 *     (start | continue | steer | revise).
 *   - The payload text is trimmed; the draft keeps the exact original text
 *     (multi-line and surrounding whitespace preserved).
 *   - Explicit rejection (HTTP 4xx) => status 'failed'; anything else
 *     (network error, unreadable body, 5xx gateway) => 'unconfirmed'
 *     (delivery unknown: never auto-resend, never infer identity from text).
 *   - Restore only succeeds when the composer is empty; it never resends.
 *   - Responses only touch their own intent id, so stale responses cannot
 *     remove newer pending entries or pollute another session.
 *   - Failed drafts never take part in optimistic text reconciliation;
 *     successful requests never create drafts.
 *
 * i18n keys emitted:
 *   send.launchInFlight            a new-session launch is still pending
 *   send.notReady                  session state does not allow continue/steer
 *   send.draft.title               failed draft card heading
 *   send.draft.rejected            explicit rejection notice
 *   send.draft.unconfirmed         unknown delivery notice
 *   send.draft.restore             restore button label
 *   send.draft.session             label before the raw original session id
 *   send.draft.newSession          label when the draft belonged to a new session
 *   send.restore.composerNotEmpty  restore refused because the composer has text
 *   send.restore.unconfirmed       reminder after restoring an unconfirmed draft
 *   send.success.start | send.success.continue | send.success.planStarted |
 *   send.success.steer | send.success.interruptSteer | send.success.revise
 *   activity.launching.title|copy, activity.continuing.title|copy,
 *   activity.steerQueued.title|copy, activity.interruptSteer.title|copy
 *   kind.start | kind.continue | kind.steer | kind.revise   send kind labels
 */

/** Allowed send kinds. */
export const SEND_KINDS = Object.freeze(['start', 'continue', 'steer', 'revise']);

/** Session statuses that route a composer send to continue. */
export const CONTINUABLE_STATUSES = Object.freeze(['awaiting_input', 'paused', 'failed', 'completed']);

function str(value) {
  return value === undefined || value === null ? '' : String(value);
}

/** True for client-side ephemeral new-session ids such as 0xA11CE0. */
export function isEphemeralSessionId(sessionID) {
  return /^0x[0-9a-f]+$/i.test(str(sessionID).trim());
}

/** New ephemeral identity for the new-session composer. */
export function nextEphemeralSessionId(random = Math.random) {
  const value = Math.floor(random() * 0x1000000) & 0xffffff;
  return '0x' + value.toString(16).padStart(6, '0').toUpperCase();
}

/**
 * Normalized session reference.
 * @param {{sessionId?: string, backed?: boolean}} input
 * @returns {{sessionId: string, ephemeral: boolean, key: string}}
 */
export function sessionRef(input = {}) {
  const sessionId = str(input?.sessionId).trim();
  const backed = input?.backed !== undefined ? Boolean(input.backed) : input?.ephemeral === false;
  const ephemeral = !backed || !sessionId || isEphemeralSessionId(sessionId);
  return { sessionId, ephemeral, key: (ephemeral ? 'ephemeral:' : 'durable:') + sessionId };
}

/** True when two refs denote the same session identity. */
export function sameSessionRef(a, b) {
  return Boolean(a && b) && sessionRef(a).key === sessionRef(b).key;
}

/** True for POST /api/sessions/start|continue async 202 responses. */
export function isAcceptedLaunchResponse(value) {
  return str(value?.status).toLowerCase() === 'accepted' && Boolean(value?.session_id);
}

/**
 * Decide how a composer submission is routed. Must run (and any Goal/Plan
 * draft validation must pass) before the composer is cleared.
 * @param {{rawText: string, sessionId?: string, backed?: boolean, status?: string,
 *   generating?: boolean, planModeStatus?: string, launchInFlight?: boolean}} ctx
 * @returns {{ok: true, kind: string, text: string, rawText: string, session: object,
 *   allowsGoalDraft: boolean, allowsPlanDraft: boolean} |
 *   {ok: false, reason: 'empty'|'launchInFlight'|'notReady', notice: object|null}}
 */
export function resolveSendRoute(ctx = {}) {
  const rawText = str(ctx.rawText);
  const text = rawText.trim();
  if (!text) return { ok: false, reason: 'empty', notice: null };
  const session = sessionRef({ sessionId: ctx.sessionId, backed: ctx.backed });
  const durable = !session.ephemeral;
  if (ctx.launchInFlight && !durable) {
    return { ok: false, reason: 'launchInFlight', notice: { key: 'send.launchInFlight', tone: 'info' } };
  }
  const status = str(ctx.status).toLowerCase();
  const routesToContinue = durable && CONTINUABLE_STATUSES.includes(status);
  const routesToSteer = Boolean(ctx.generating) && durable && !routesToContinue;
  if (durable && !routesToContinue && !routesToSteer) {
    return { ok: false, reason: 'notReady', notice: { key: 'send.notReady', tone: 'error' } };
  }
  const revising = routesToContinue && str(ctx.planModeStatus).toLowerCase() === 'awaiting_approval';
  const kind = routesToSteer ? 'steer' : revising ? 'revise' : routesToContinue ? 'continue' : 'start';
  return {
    ok: true,
    kind,
    text,
    rawText,
    session,
    allowsGoalDraft: !durable,
    allowsPlanDraft: kind !== 'steer' && kind !== 'revise'
  };
}

/**
 * Route + optional Goal/Plan draft validation without changing any state.
 * Validators return {error} (an i18n slot or message) or a draft object.
 */
export function prepareSend(ctx = {}, validators = {}) {
  const route = resolveSendRoute(ctx);
  if (!route.ok) return route;
  const goalDraft = route.allowsGoalDraft && typeof validators.goal === 'function' ? validators.goal(route.text) : null;
  const planDraft = route.allowsPlanDraft && typeof validators.plan === 'function' ? validators.plan(route.text) : null;
  const error = goalDraft?.error || planDraft?.error;
  if (error) return { ok: false, reason: 'invalid', notice: typeof error === 'string' ? { raw: error, tone: 'error' } : { ...error, tone: 'error' } };
  return { ...route, goalDraft: goalDraft || null, planDraft: planDraft || null };
}

/** 'failed' for explicit HTTP 4xx rejection, otherwise 'unconfirmed'. */
export function classifySendFailure(err) {
  const status = Number(err?.status || 0);
  return status >= 400 && status < 500 ? 'failed' : 'unconfirmed';
}

/** Success toast + live activity for a completed send. */
export function sendSuccessFeedback(kind, { interrupt = false, planDraft = false } = {}) {
  switch (kind) {
    case 'steer':
      return interrupt
        ? { toast: { key: 'send.success.interruptSteer', tone: 'success' }, activity: { title: { key: 'activity.interruptSteer.title' }, copy: { key: 'activity.interruptSteer.copy' }, tone: 'queued' } }
        : { toast: { key: 'send.success.steer', tone: 'success' }, activity: { title: { key: 'activity.steerQueued.title' }, copy: { key: 'activity.steerQueued.copy' }, tone: 'live' } };
    case 'revise':
      return { toast: { key: 'send.success.revise', tone: 'success' }, activity: { title: { key: 'activity.continuing.title' }, copy: { key: 'activity.continuing.copy' }, tone: 'live' } };
    case 'continue':
      return { toast: { key: planDraft ? 'send.success.planStarted' : 'send.success.continue', tone: 'success' }, activity: { title: { key: 'activity.continuing.title' }, copy: { key: 'activity.continuing.copy' }, tone: 'live' } };
    default:
      return { toast: null, activity: { title: { key: 'activity.launching.title' }, copy: { key: 'activity.launching.copy' }, tone: 'live' } };
  }
}

/** Run statuses during which local send feedback may still be pending. */
const LIVE_RUN_STATUSES = Object.freeze(['running', 'queued', 'pending']);

/** Maximum age of local live-activity feedback without any confirmation. */
export const LOCAL_ACTIVITY_MAX_AGE_MS = 120000;

/**
 * Local live activity for session.activity right after an accepted send:
 * sendSuccessFeedback(kind).activity plus the send kind and the time it was
 * set (epoch ms, `at`). The stream prefers it over engine events older
 * than `at`, so feedback shows immediately instead of after the next poll.
 */
export function localSendActivity(kind, options = {}, now = Date.now()) {
  const activity = sendSuccessFeedback(kind, options).activity;
  return activity ? { ...activity, kind: str(kind), at: Number(now) || 0 } : null;
}

/**
 * True when session.activity no longer applies: it has no timestamp, is
 * older than maxAgeMs, or the session settled after it was set (the run is
 * not live and state.updated_at is at or after `at`).
 * @param {{title?: object, at?: number}|null} activity
 * @param {{detail?: object|null, generating?: boolean, now?: number, maxAgeMs?: number}} ctx
 */
export function isLocalActivitySettled(activity, { detail = null, generating = false, now = Date.now(), maxAgeMs = LOCAL_ACTIVITY_MAX_AGE_MS } = {}) {
  if (!activity) return false;
  const at = Number(activity.at) || 0;
  if (!at || now - at > maxAgeMs) return true;
  if (generating || !detail) return false;
  const status = str(detail.state?.status).toLowerCase();
  if (LIVE_RUN_STATUSES.includes(status)) return false;
  const updated = Date.parse(str(detail.state?.updated_at));
  return Number.isFinite(updated) && updated >= at;
}

/** Card view for a failed draft (raw text and session id stay raw). */
export function failedDraftView(draft) {
  return {
    id: str(draft?.id),
    title: { key: 'send.draft.title' },
    kind: { key: 'kind.' + str(draft?.kind), params: { fallback: str(draft?.kind) } },
    session: draft?.ephemeral ? { key: 'send.draft.newSession', raw: str(draft?.sessionId) } : { key: 'send.draft.session', raw: str(draft?.sessionId) },
    notice: { key: draft?.status === 'unconfirmed' ? 'send.draft.unconfirmed' : 'send.draft.rejected' },
    text: { raw: str(draft?.rawText) },
    restore: { key: 'send.draft.restore' },
    status: str(draft?.status)
  };
}

function normalizeText(value) {
  return str(value).trim().replace(/\s+/g, ' ');
}

function defaultIdFactory() {
  let serial = 0;
  return () => {
    serial += 1;
    const random = Math.floor(Math.random() * 0xffffffff).toString(16).padStart(8, '0');
    return 'send_' + Date.now().toString(36) + '_' + serial.toString(36) + '_' + random;
  };
}

function cloneJSON(value) {
  return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

/**
 * In-memory registry of send intents, optimistic messages and failed drafts.
 */
export class SendRecoveryStore {
  /**
   * @param {{createId?: () => string, now?: () => number, onChange?: () => void}} [options]
   */
  constructor(options = {}) {
    this.createId = typeof options.createId === 'function' ? options.createId : defaultIdFactory();
    this.now = typeof options.now === 'function' ? options.now : () => Date.now();
    this.onChange = typeof options.onChange === 'function' ? options.onChange : () => {};
    this.intents = new Map();
    this.drafts = new Map();
  }

  /**
   * Register a send before its request is issued.
   * @param {{session: {sessionId: string, backed?: boolean}, kind: string, rawText: string,
   *   meta?: object, baseline?: {messageIds?: Array<string>, steerIds?: Array<string>}}} input
   *   baseline: durable message/steer ids already loaded; those never reconcile
   *   this optimistic message (so an older identical text cannot hide it).
   * @returns {object} intent (copy) whose id is the client request ID
   */
  begin(input = {}) {
    const kind = SEND_KINDS.includes(input.kind) ? input.kind : 'start';
    const session = sessionRef(input.session || {});
    const rawText = str(input.rawText);
    const id = str(this.createId());
    const createdAt = new Date(this.now()).toISOString();
    const baseline = input.baseline ? {
      messageIds: new Set((input.baseline.messageIds || []).map(str)),
      steerIds: new Set((input.baseline.steerIds || []).map(str))
    } : null;
    const intent = {
      id,
      kind,
      sessionId: session.sessionId,
      ephemeral: session.ephemeral,
      sessionKey: session.key,
      rawText,
      text: rawText.trim(),
      status: 'pending',
      createdAt,
      baseline,
      optimistic: {
        id,
        optimisticKey: id,
        role: 'user',
        text: rawText.trim(),
        meta: { source: kind === 'steer' ? 'steer' : 'user', ...(input.meta || {}) },
        created_at: createdAt,
        pending: true
      },
      showOptimistic: true
    };
    this.intents.set(id, intent);
    this.onChange();
    return this.publicIntent(intent);
  }

  publicIntent(intent) {
    if (!intent) return null;
    const { baseline, optimistic, ...rest } = intent;
    return { ...rest, optimistic: cloneJSON(optimistic) };
  }

  /** Intent by client request id (copy) or null. */
  intent(id) {
    return this.publicIntent(this.intents.get(str(id)));
  }

  /** Pending (unanswered) intents, optionally for one session. */
  pendingIntents(session) {
    const key = session ? sessionRef(session).key : '';
    return Array.from(this.intents.values())
      .filter((intent) => intent.status === 'pending' && (!key || intent.sessionKey === key))
      .map((intent) => this.publicIntent(intent));
  }

  /** True when the intent belongs to the given (current) session. */
  isForSession(id, session) {
    const intent = this.intents.get(str(id));
    return Boolean(intent) && intent.sessionKey === sessionRef(session || {}).key;
  }

  /**
   * Mark a request as accepted. The optimistic message stays until durable
   * history reconciles it. Unknown ids are ignored (stale responses).
   */
  succeed(id, { durableSessionId = '' } = {}) {
    const intent = this.intents.get(str(id));
    if (!intent || intent.status !== 'pending') return { ok: false, intent: null };
    intent.status = 'accepted';
    if (durableSessionId && intent.ephemeral) {
      const durable = sessionRef({ sessionId: durableSessionId, backed: true });
      intent.sessionId = durable.sessionId;
      intent.ephemeral = false;
      intent.sessionKey = durable.key;
    }
    this.onChange();
    return { ok: true, intent: this.publicIntent(intent) };
  }

  /**
   * Record a failed request: removes its optimistic message and stores an
   * in-memory draft bound to the ORIGINAL session and kind.
   * @returns {{draft: object|null, refresh: {overview: boolean, session: boolean, sessionId: string}}}
   */
  fail(id, err) {
    const intent = this.intents.get(str(id));
    if (!intent || intent.status !== 'pending') return { draft: null, refresh: { overview: false, session: false, sessionId: '' } };
    const status = classifySendFailure(err);
    intent.status = 'failed';
    intent.showOptimistic = false;
    this.intents.delete(intent.id);
    const draft = {
      id: intent.id,
      kind: intent.kind,
      sessionId: intent.sessionId,
      ephemeral: intent.ephemeral,
      sessionKey: intent.sessionKey,
      rawText: intent.rawText,
      status,
      error: { message: str(err?.message), code: str(err?.code), status: Number(err?.status || 0) || null },
      createdAt: intent.createdAt,
      failedAt: new Date(this.now()).toISOString()
    };
    this.drafts.set(draft.id, draft);
    this.onChange();
    return {
      draft: { ...draft },
      refresh: { overview: status === 'unconfirmed', session: status === 'unconfirmed' && !intent.ephemeral, sessionId: intent.sessionId }
    };
  }

  /** All failed drafts (oldest first); visible across session switches. */
  failedDrafts() {
    return Array.from(this.drafts.values()).sort((a, b) => a.createdAt.localeCompare(b.createdAt)).map((draft) => ({ ...draft, error: { ...draft.error } }));
  }

  /**
   * Restore a draft into an EMPTY composer. Never retries the request.
   * @param {string} id
   * @param {{composerValue?: string}} [options] current composer text ('' when empty)
   */
  restore(id, { composerValue = '' } = {}) {
    const draft = this.drafts.get(str(id));
    if (!draft) return { ok: false, reason: 'missing', notice: null };
    if (str(composerValue) !== '') {
      return { ok: false, reason: 'composerNotEmpty', notice: { key: 'send.restore.composerNotEmpty', tone: 'info' } };
    }
    this.drafts.delete(draft.id);
    this.onChange();
    return {
      ok: true,
      rawText: draft.rawText,
      kind: draft.kind,
      sessionId: draft.sessionId,
      ephemeral: draft.ephemeral,
      notice: draft.status === 'unconfirmed' ? { key: 'send.restore.unconfirmed', tone: 'info' } : null
    };
  }

  /** Drop a failed draft without restoring it. */
  dismiss(id) {
    const removed = this.drafts.delete(str(id));
    if (removed) this.onChange();
    return removed;
  }

  /** Optimistic (not yet reconciled) messages for one session, oldest first. */
  optimisticMessages(session) {
    const key = sessionRef(session || {}).key;
    return Array.from(this.intents.values())
      .filter((intent) => intent.showOptimistic && intent.sessionKey === key)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt))
      .map((intent) => cloneJSON(intent.optimistic));
  }

  /**
   * Remove optimistic messages that durable history now contains. Each
   * durable message/steer request reconciles at most one optimistic message,
   * and ids in an intent's baseline never reconcile it. Failed drafts are
   * never touched.
   * @returns {Array<string>} reconciled intent ids
   */
  reconcile(session, detail) {
    const key = sessionRef(session || {}).key;
    const durable = (Array.isArray(detail?.messages) ? detail.messages : [])
      .filter((item) => item?.role === 'user' || item?.role === 'assistant')
      .map((item) => ({ id: 'm:' + str(item.id), rawId: str(item.id), text: normalizeText(item.text), kind: 'message' }));
    const steers = (Array.isArray(detail?.steer_requests) ? detail.steer_requests : [])
      .map((item) => ({ id: 's:' + str(item.id), rawId: str(item.id), text: normalizeText(item.text), kind: 'steer' }));
    const candidates = durable.concat(steers);
    const claimed = new Set();
    const removed = [];
    Array.from(this.intents.values())
      .filter((intent) => intent.showOptimistic && intent.sessionKey === key)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt))
      .forEach((intent) => {
        const text = normalizeText(intent.text);
        const match = candidates.find((candidate) => {
          if (claimed.has(candidate.id) || candidate.text !== text) return false;
          if (!intent.baseline) return true;
          const seen = candidate.kind === 'steer' ? intent.baseline.steerIds : intent.baseline.messageIds;
          return !candidate.rawId || !seen.has(candidate.rawId);
        });
        if (!match) return;
        if (match.rawId) claimed.add(match.id);
        intent.showOptimistic = false;
        removed.push(intent.id);
        if (intent.status !== 'pending') this.intents.delete(intent.id);
      });
    if (removed.length) this.onChange();
    return removed;
  }

  /**
   * Rebind an accepted ephemeral start to its durable session id so its
   * optimistic prompt follows the adopted session.
   */
  adoptSession(fromSession, durableSessionId) {
    const fromKey = sessionRef(fromSession || {}).key;
    const durable = sessionRef({ sessionId: durableSessionId, backed: true });
    let changed = 0;
    this.intents.forEach((intent) => {
      if (intent.sessionKey !== fromKey || intent.kind !== 'start') return;
      intent.sessionId = durable.sessionId;
      intent.ephemeral = false;
      intent.sessionKey = durable.key;
      changed += 1;
    });
    if (changed) this.onChange();
    return changed;
  }

  /** Forget optimistic messages of a session (failed drafts are kept). */
  discardOptimistic(session) {
    const key = sessionRef(session || {}).key;
    let changed = 0;
    Array.from(this.intents.values()).forEach((intent) => {
      if (intent.sessionKey !== key || !intent.showOptimistic) return;
      intent.showOptimistic = false;
      if (intent.status !== 'pending') this.intents.delete(intent.id);
      changed += 1;
    });
    if (changed) this.onChange();
    return changed;
  }

  /** JSON-safe snapshot for a UI store. */
  snapshot() {
    return {
      pending: Array.from(this.intents.values()).map((intent) => this.publicIntent(intent)),
      failedDrafts: this.failedDrafts()
    };
  }
}
