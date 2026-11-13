/**
 * Plan Mode / linked mission approval intents and receipt coordination
 * (spec/17-web-console.md §7.13). Port of assets/approval-operations.mjs with
 * unchanged storage and receipt semantics, plus the pure approval-target,
 * admission-presentation and notice helpers that lived in app.js.
 *
 * The controller stores pending user intent only. Receipts and current run
 * facts always come from the server; a saved phase never authorizes
 * execution after a reload.
 *
 * Errors thrown by this module are ApprovalOperationError instances whose
 * .key (also .message) is an i18n key. Server-provided messages are kept as
 * raw text (record.notice, err.message of API errors).
 *
 * i18n keys emitted:
 *   approval.error.savedUnreadable   saved approval cannot be read
 *   approval.error.saveFailed        approval intent could not be saved (storage)
 *   approval.error.noSaved           no saved approval to check
 *   approval.error.checkBeforeRetry  retry requested before a check allowed it
 *   approval.error.unverified        response does not prove the saved request
 *   approval.error.secureRandomUnavailable  no crypto randomness for request IDs
 *   approval.error.reloadPlan        displayed plan lacks a reviewable target
 *   approval.planChanged             target changed: review again (both locales)
 *   approval.alreadyPending          another approval for this session is in flight
 *   approval.notOverridden           coverage override was declined
 *   approval.goalPlanUpdated         linked facts-only mission response
 *   approval.goalPlanApproved        unlinked mission facts approval
 *   approval.executionStarted        new admission started execution
 *   approval.alreadyAdmitted         replayed/historical admission
 *   approval.coverage.title | approval.coverage.message | approval.coverage.confirm
 *   approval.notice.title | approval.notice.currentSession
 *   approval.notice.pending | .unknown | .not_found | .prepared | .admitted |
 *     .rejected | .recovery_required | .failed
 *   approval.action.check | approval.action.retry | approval.action.continue
 *   approval.continueHint            toast after choosing ordinary Continue
 *   activity.executingApprovedPlan.title | activity.executingApprovedPlan.copy
 *   status.<token>                   current session status (params.fallback)
 */

export const APPROVAL_STORAGE_KEY = 'aegis-agent.webconsole.approval-operations.v1';

/** Error carrying an i18n key (message === key). */
export class ApprovalOperationError extends Error {
  /**
   * @param {string} key i18n key
   * @param {{status?: number, code?: string}} [extra]
   */
  constructor(key, extra = {}) {
    super(key);
    this.name = 'ApprovalOperationError';
    this.key = key;
    if (extra.status !== undefined) this.status = extra.status;
    if (extra.code !== undefined) this.code = extra.code;
  }
}

const ERR = Object.freeze({
  savedUnreadable: 'approval.error.savedUnreadable',
  saveFailed: 'approval.error.saveFailed',
  noSaved: 'approval.error.noSaved',
  checkBeforeRetry: 'approval.error.checkBeforeRetry',
  unverified: 'approval.error.unverified',
  secureRandom: 'approval.error.secureRandomUnavailable',
  reloadPlan: 'approval.error.reloadPlan',
  planChanged: 'approval.planChanged'
});

function copy(value) {
  return JSON.parse(JSON.stringify(value));
}

function sameTarget(a, b) {
  return a?.plan_mode_id === b?.plan_mode_id && a?.plan_version === b?.plan_version &&
    a?.expected_revision === b?.expected_revision;
}

/**
 * Whether Approve on `parameters` starts a NEW operation (new request id)
 * instead of re-checking the saved intent (spec/17-web-console.md §7.13).
 * Admitted and explicitly failed operations are complete. A rejected or
 * recovery_required operation proves its target was not admitted: the same
 * target is only re-checked (never silently resent with the old id), while a
 * newly reviewed, different target may be approved with a new id; the server
 * ledger still gates corrupt or prepared receipts for any id. Pending, unknown,
 * not_found and prepared intents keep their id until a check resolves them,
 * even when the displayed target changes.
 */
export function startsNewApprovalOperation(old, parameters) {
  if (!old) return true;
  if (['admitted', 'failed'].includes(old.phase)) return true;
  if (['rejected', 'recovery_required'].includes(old.phase)) return !sameTarget(old.parameters, parameters);
  return false;
}

function missionFactsResponse(record, response) {
  return record.entrypoint === 'mission' && !response?.approval && response?.schema_version === 1 &&
    response.session_id === record.session_id && typeof response.goal_id === 'string' && response.goal_id !== '' &&
    response.mode === 'mission' && response.mission?.plan_status === 'approved';
}

/**
 * Persisted approval intent + receipt coordinator (one operation per session).
 * Constructor deps are injected: storage (getItem/setItem), requestID(),
 * submit(sessionID, entrypoint, payload), query(sessionID, requestID), onChange.
 */
export class ApprovalOperationController {
  constructor({ storage, requestID, submit, query, onChange = () => {} }) {
    this.storage = storage;
    this.requestID = requestID;
    this.submit = submit;
    this.query = query;
    this.onChange = onChange;
    this.inFlight = new Map();
    this.responses = new Map();
    this.checked = new Set();
  }

  read() {
    const raw = this.storage.getItem(APPROVAL_STORAGE_KEY);
    if (!raw) return { version: 1, sessions: {} };
    let saved;
    try { saved = JSON.parse(raw); } catch { throw new ApprovalOperationError(ERR.savedUnreadable); }
    if (saved?.version !== 1 || !saved.sessions || typeof saved.sessions !== 'object' || Array.isArray(saved.sessions)) {
      throw new ApprovalOperationError(ERR.savedUnreadable);
    }
    return saved;
  }

  get(sessionID) {
    const record = this.read().sessions[sessionID];
    if (!record) return null;
    if (record.session_id !== sessionID || !record.approval_request_id ||
      !['planmode', 'mission'].includes(record.entrypoint) || !record.parameters?.plan_mode_id ||
      !record.parameters.expected_revision || !Number.isInteger(record.parameters.plan_version) ||
      record.parameters.plan_version < 1 || typeof record.parameters.override_coverage !== 'boolean') {
      throw new ApprovalOperationError(ERR.savedUnreadable);
    }
    return copy(record);
  }

  save(record, expectedID = '') {
    const saved = this.read();
    if (expectedID && saved.sessions[record.session_id]?.approval_request_id !== expectedID) return false;
    saved.sessions[record.session_id] = { ...copy(record), updated_at: new Date().toISOString() };
    try { this.storage.setItem(APPROVAL_STORAGE_KEY, JSON.stringify(saved)); }
    catch { throw new ApprovalOperationError(ERR.saveFailed); }
    this.onChange(record.session_id);
    return true;
  }

  release(record) {
    const saved = this.read();
    if (saved.sessions[record.session_id]?.approval_request_id !== record.approval_request_id) return false;
    delete saved.sessions[record.session_id];
    try { this.storage.setItem(APPROVAL_STORAGE_KEY, JSON.stringify(saved)); }
    catch { throw new ApprovalOperationError(ERR.saveFailed); }
    this.responses.delete(record.session_id);
    this.onChange(record.session_id);
    return true;
  }

  busy(sessionID) { return this.inFlight.has(sessionID); }

  response(sessionID) {
    const response = this.responses.get(sessionID);
    return response?.approval?.lookup?.binding?.approval_request_id === this.get(sessionID)?.approval_request_id ? response : null;
  }

  run(sessionID, action) {
    if (this.inFlight.has(sessionID)) return this.inFlight.get(sessionID);
    // Register before invoking an action: onChange can rerender other Approve
    // controls, and every alias in this page must share the same pending ID.
    let resolve;
    let reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    this.inFlight.set(sessionID, promise);
    try { Promise.resolve(action()).then(resolve, reject); } catch (err) { reject(err); }
    promise.finally(() => {
      if (this.inFlight.get(sessionID) === promise) this.inFlight.delete(sessionID);
      this.onChange(sessionID);
    }).catch(() => {});
    return promise;
  }

  start(sessionID, entrypoint, parameters) {
    return this.run(sessionID, async () => {
      const old = this.get(sessionID);
      if (!startsNewApprovalOperation(old, parameters)) return this.checkRecord(old);
      const record = {
        session_id: sessionID,
        approval_request_id: this.requestID(),
        entrypoint,
        parameters: { ...copy(parameters), override_coverage: Boolean(parameters.override_coverage) },
        phase: 'pending'
      };
      this.save(record);
      return this.post(record);
    });
  }

  check(sessionID) {
    return this.run(sessionID, () => {
      const record = this.get(sessionID);
      if (!record) throw new ApprovalOperationError(ERR.noSaved);
      return this.checkRecord(record);
    });
  }

  restore(sessionID) {
    const record = this.get(sessionID);
    if (!record || this.busy(sessionID) || this.checked.has(record.approval_request_id)) return null;
    this.checked.add(record.approval_request_id);
    return this.check(sessionID);
  }

  retry(sessionID) {
    return this.run(sessionID, () => {
      const record = this.get(sessionID);
      if (!record || !['not_found', 'prepared', 'recovery_required'].includes(record.phase)) {
        throw new ApprovalOperationError(ERR.checkBeforeRetry);
      }
      return this.post(record);
    });
  }

  override(sessionID, parameters) {
    return this.run(sessionID, () => {
      const old = this.get(sessionID);
      if (!old || old.phase !== 'rejected' || !sameTarget(old.parameters, parameters)) {
        throw new ApprovalOperationError(ERR.planChanged);
      }
      const record = {
        ...old,
        approval_request_id: this.requestID(),
        parameters: { ...copy(old.parameters), override_coverage: true },
        phase: 'pending',
        notice: ''
      };
      this.save(record, old.approval_request_id);
      return this.post(record);
    });
  }

  observe(record, response) {
    const lookup = response?.approval?.lookup;
    const stage = lookup?.receipt?.stage;
    if (!lookup?.found || lookup.binding?.approval_request_id !== record.approval_request_id ||
      !['prepared', 'admitted', 'rejected'].includes(stage)) {
      throw new ApprovalOperationError(ERR.unverified);
    }
    const phase = response.approval.recovery_required ? 'recovery_required' : stage;
    if (this.save({ ...record, phase, notice: '' }, record.approval_request_id)) {
      this.responses.set(record.session_id, response);
      this.checked.add(record.approval_request_id);
      return response;
    }
    return null;
  }

  async checkRecord(record) {
    try {
      return this.observe(record, await this.query(record.session_id, record.approval_request_id));
    } catch (err) {
      if (err.status === 404) {
        // A receipt miss is new information about unknown delivery, but cannot
        // undo an explicit POST rejection that already proved non-admission.
        const phase = ['failed', 'recovery_required'].includes(record.phase) ? record.phase : 'not_found';
        this.save({ ...record, phase }, record.approval_request_id);
      } else if (err.code === 'APPROVAL_RECOVERY_REQUIRED') {
        this.save({ ...record, phase: 'recovery_required', notice: err.message }, record.approval_request_id);
      } else {
        this.save({ ...record, phase: record.phase === 'failed' ? 'failed' : 'unknown' }, record.approval_request_id);
      }
      this.responses.delete(record.session_id);
      return null;
    }
  }

  async post(record) {
    this.save({ ...record, phase: 'pending' }, record.approval_request_id);
    try {
      const response = await this.submit(record.session_id, record.entrypoint,
        { ...copy(record.parameters), approval_request_id: record.approval_request_id });
      // Legacy linked executing missions can repair their goal facts without
      // admitting execution or generating a receipt. Complete this user intent
      // only for the documented SessionGoal response from the mission alias.
      if (missionFactsResponse(record, response)) return this.release(record) ? response : null;
      return this.observe(record, response);
    } catch (err) {
      if (err.payload?.approval?.lookup?.found) return this.observe(record, err.payload);
      if (err.code === 'APPROVAL_RECOVERY_REQUIRED') {
        this.save({ ...record, phase: 'recovery_required', notice: err.message }, record.approval_request_id);
        return null;
      }
      // A structured, definitive rejection has no admitted receipt. Transport
      // failures (including unreadable success bodies) must be queried first.
      if (err.status >= 400 && err.status < 500) {
        this.save({ ...record, phase: 'failed', notice: err.message }, record.approval_request_id);
        throw err;
      }
      this.save({ ...record, phase: 'unknown' }, record.approval_request_id);
      return this.checkRecord(record);
    }
  }
}

// ---------------------------------------------------------------------------
// Request IDs and targets
// ---------------------------------------------------------------------------

/**
 * Secure approval request ID: crypto.randomUUID, else a getRandomValues
 * UUIDv4 (trusted LAN HTTP is not a secure context), else explicit failure.
 * @param {object} [cryptoImpl] defaults to globalThis.crypto (read lazily)
 */
export function generateApprovalRequestID(cryptoImpl = globalThis.crypto) {
  if (cryptoImpl && typeof cryptoImpl.randomUUID === 'function') return cryptoImpl.randomUUID();
  if (!cryptoImpl || typeof cryptoImpl.getRandomValues !== 'function') {
    throw new ApprovalOperationError(ERR.secureRandom);
  }
  const bytes = cryptoImpl.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  return hex.slice(0, 8) + '-' + hex.slice(8, 12) + '-' + hex.slice(12, 16) + '-' + hex.slice(16, 20) + '-' + hex.slice(20);
}

/**
 * Target the operator actually reviewed: {plan_mode_id, plan_version,
 * expected_revision}. Throws approval.error.reloadPlan when incomplete.
 * @param {object} planMode detail.plan_mode
 */
export function displayedApprovalTarget(planMode) {
  const id = String(planMode?.plan_mode_id || '').trim();
  const revision = String(planMode?.approval_revision || '').trim();
  const version = planMode?.plan_version;
  if (!id || !revision || !Number.isInteger(version) || version < 1) {
    throw new ApprovalOperationError(ERR.reloadPlan);
  }
  return { plan_mode_id: id, plan_version: version, expected_revision: revision };
}

/** True when target still matches the displayed plan_mode. */
export function isDisplayedApprovalTarget(planMode, target) {
  return planMode?.plan_mode_id === target?.plan_mode_id && planMode?.plan_version === target?.plan_version &&
    planMode?.approval_revision === target?.expected_revision;
}

/** HTTP 409 conflicts that mean "the reviewed target is stale". */
export function isStaleApprovalTarget(err) {
  if (err?.key === ERR.planChanged) return true;
  const text = [err?.code, err?.message, err?.detail].filter(Boolean).join(' ').toLowerCase();
  return err?.status === 409 && (text.includes('approval_target') || text.includes('approval target') ||
    text.includes('approval_conflict') || text.includes('approval content changed') || text.includes('review the current plan') ||
    text.includes('revision') || text.includes('plan has changed') || text.includes('plan updated'));
}

/** HTTP 409 validation coverage blocks (eligible for an explicit override). */
export function isCoverageApprovalBlock(err) {
  const haystack = [err?.message, err?.detail, err?.action, err?.code].filter(Boolean).join(' ').toLowerCase();
  return err?.status === 409 && haystack.includes('coverage') && (
    haystack.includes('blocks approval') ||
    haystack.includes('approval block') ||
    haystack.includes('validation coverage')
  );
}

/**
 * Toast slot for an approval failure: planChanged for stale targets, the
 * module key for ApprovalOperationError, raw server text, or fallbackKey.
 */
export function approvalActionError(err, fallbackKey = 'approval.error.unverified') {
  if (isStaleApprovalTarget(err)) return { key: ERR.planChanged };
  if (err instanceof ApprovalOperationError || (err?.key && String(err.key).startsWith('approval.'))) return { key: err.key };
  if (err?.message) return { raw: String(err.message) };
  return { key: fallbackKey };
}

/** Confirmation dialog for the explicit coverage override. */
export function coverageOverrideConfirmation() {
  return { title: { key: 'approval.coverage.title' }, message: { key: 'approval.coverage.message' }, confirm: { key: 'approval.coverage.confirm' }, tone: 'danger' };
}

function identity(parts) {
  return parts.map((part) => String(part || '')).join('\n');
}

/** Identity string used to drop stale goal-action completions. */
export function goalActionIdentity(goal) {
  if (!goal || typeof goal !== 'object') return '';
  return identity([goal.goal_id || goal.id, goal.updated_at, goal.status, goal.objective]);
}

/** Identity string used to drop stale Plan Mode action completions. */
export function planModeActionIdentity(planMode) {
  if (!planMode || typeof planMode !== 'object') return '';
  return identity([planMode.plan_mode_id || planMode.id, planMode.updated_at, planMode.status, planMode.objective,
    planMode.plan_id, planMode.plan_version, planMode.approved_version, planMode.linked_goal_id, planMode.approval_revision]);
}

/** Identity for a pending request_user_input request ('' when not pending). */
export function planInputActionIdentity(planMode, requestID) {
  const current = String(planMode?.pending_request?.request_id || '').trim();
  if (!current || current !== String(requestID || '').trim()) return '';
  return planModeActionIdentity(planMode) + '\n' + current;
}

// ---------------------------------------------------------------------------
// View tokens and admission presentation
// ---------------------------------------------------------------------------

/**
 * Snapshot of what the operator saw when the approval started.
 * @param {{sessionID: string, epoch: number, state?: object}} input state = detail.state
 */
export function approvalViewToken({ sessionID, epoch = 0, state } = {}) {
  return {
    sessionID: String(sessionID || ''),
    epoch,
    observed: {
      generation: String(state?.run_generation || ''),
      status: state?.status || '',
      updatedAt: state?.updated_at || ''
    }
  };
}

/** Same session and same navigation epoch. */
export function isCurrentApprovalViewToken(token, { sessionID, epoch = 0 } = {}) {
  return token?.sessionID === String(sessionID || '') && token.epoch === epoch;
}

/** Fresh (non-replay) admission that started a new run. */
export function isNewApprovalAdmission(response) {
  return response?.status === 'accepted' && response?.approval?.lookup?.receipt?.stage === 'admitted' &&
    response.approval.replay === false && response.approval.recovery_required !== true;
}

/**
 * Whether a new admission may be shown as "executing": requires a provable
 * receipt run generation, never overrides newer loaded facts, and allows a
 * fresh claim while the view still shows its pre-claim state.
 * @param {object} response approval response
 * @param {object} viewToken approvalViewToken() captured at start
 * @param {object} loadedState latest loaded detail.state
 */
export function canPresentApprovalAdmission(response, viewToken, loadedState) {
  const generation = String(response?.approval?.lookup?.receipt?.recovery?.run_generation || '');
  const loaded = loadedState;
  if (!generation || !loaded) return false;
  const returned = response?.current_state;
  const returnedGeneration = String(returned?.run_generation || '');
  if (returnedGeneration && returnedGeneration !== generation) return false;
  if (returnedGeneration === generation && returned.status && returned.status !== 'running') return false;
  const loadedGeneration = String(loaded.run_generation || '');
  if (loadedGeneration === generation) return loaded.status === 'running';
  return loadedGeneration === viewToken?.observed?.generation &&
    loaded.status === viewToken?.observed?.status && (loaded.updated_at || '') === viewToken?.observed?.updatedAt;
}

/**
 * Run the reviewed approval flow (port of executeReviewedApproval).
 * @param {{controller: ApprovalOperationController, sessionID: string,
 *   entrypoint: 'planmode'|'mission', target: object,
 *   isViewCurrent: () => boolean, isTargetDisplayed: (target) => boolean,
 *   isGoalIdentityCurrent?: () => boolean,
 *   confirmCoverageOverride: () => Promise<boolean>}} deps
 * @returns {Promise<{outcome: 'busy'|'done'|'stale_view'|'override_declined'|'goal_changed', response: object|null}>}
 * @throws ApprovalOperationError approval.planChanged (status 409) when the
 *   displayed target changed during coverage confirmation.
 */
export async function executeReviewedApproval(deps) {
  const { controller, sessionID, entrypoint, target } = deps;
  const isViewCurrent = deps.isViewCurrent || (() => true);
  const isTargetDisplayed = deps.isTargetDisplayed || (() => true);
  const goalCurrent = deps.isGoalIdentityCurrent || (() => true);
  if (controller.busy(sessionID)) return { outcome: 'busy', response: null };
  let response = await controller.start(sessionID, entrypoint, { ...target, override_coverage: false });
  if (!isViewCurrent()) return { outcome: 'stale_view', response };
  const receipt = response?.approval?.lookup?.receipt;
  if (receipt?.stage === 'rejected' && isCoverageApprovalBlock({ status: 409, message: response.error || receipt.rejection, code: response.code })) {
    const changed = () => new ApprovalOperationError(ERR.planChanged, { status: 409, code: 'APPROVAL_TARGET_CONFLICT' });
    if (!isTargetDisplayed(target)) throw changed();
    if (!goalCurrent()) return { outcome: 'goal_changed', response };
    if (!await deps.confirmCoverageOverride()) {
      return { outcome: isViewCurrent() ? 'override_declined' : 'stale_view', response };
    }
    if (!isViewCurrent()) return { outcome: 'stale_view', response };
    if (!isTargetDisplayed(target)) throw changed();
    if (!goalCurrent()) return { outcome: 'goal_changed', response };
    response = await controller.override(sessionID, target);
  }
  return { outcome: 'done', response };
}

/**
 * Unlinked mission facts approval (no receipt): retries once with
 * override_coverage after explicit confirmation.
 * @param {{approve: (payload) => Promise<object>, isCurrent: () => boolean,
 *   confirmCoverageOverride: () => Promise<boolean>}} deps
 * @returns {Promise<{outcome: 'approved'|'declined'|'stale', response: object|null}>}
 */
export async function approveUnlinkedMission(deps) {
  try {
    const response = await deps.approve({});
    return { outcome: deps.isCurrent() ? 'approved' : 'stale', response };
  } catch (err) {
    if (!deps.isCurrent()) return { outcome: 'stale', response: null };
    if (!isCoverageApprovalBlock(err)) throw err;
    if (!await deps.confirmCoverageOverride()) return { outcome: 'declined', response: null };
    if (!deps.isCurrent()) return { outcome: 'stale', response: null };
    const response = await deps.approve({ override_coverage: true });
    return { outcome: deps.isCurrent() ? 'approved' : 'stale', response };
  }
}

/**
 * What the UI should do with an approval response (port of
 * presentApprovalResponse, minus side effects).
 * @param {object|null} response
 * @param {{viewCurrent: boolean, sessionID: string, goalID?: string,
 *   savedRequestID?: string, loadedState?: object, viewToken?: object}} ctx
 * @returns {{kind: 'ignore'|'goal_facts_updated'|'execution_started'|'already_admitted'|'none',
 *   toast: object|null, generating: boolean, activity: object|null}}
 */
export function approvalPresentation(response, ctx = {}) {
  const none = { kind: 'none', toast: null, generating: false, activity: null };
  if (!ctx.viewCurrent) return { ...none, kind: 'ignore' };
  if (!response?.approval && response?.schema_version === 1 && response.session_id === ctx.sessionID &&
    response.goal_id === ctx.goalID && response.mode === 'mission' && response.mission?.plan_status === 'approved') {
    return { ...none, kind: 'goal_facts_updated', toast: { key: 'approval.goalPlanUpdated', tone: 'success' } };
  }
  if (response && response.approval?.lookup?.binding?.approval_request_id !== (ctx.savedRequestID || undefined)) {
    return { ...none, kind: 'ignore' };
  }
  if (isNewApprovalAdmission(response) && canPresentApprovalAdmission(response, ctx.viewToken, ctx.loadedState)) {
    return {
      kind: 'execution_started',
      toast: { key: 'approval.executionStarted', tone: 'success' },
      generating: true,
      activity: { title: { key: 'activity.executingApprovedPlan.title' }, copy: { key: 'activity.executingApprovedPlan.copy' }, tone: 'live' }
    };
  }
  if (response?.approval?.lookup?.receipt?.stage === 'admitted') {
    return { ...none, kind: 'already_admitted', toast: { key: 'approval.alreadyAdmitted', tone: 'info' } };
  }
  return none;
}

/**
 * Toast for a flow outcome that is not a server response:
 * executeReviewedApproval 'busy' | 'override_declined', approveUnlinkedMission
 * 'approved', and the ordinary 'continue' recovery choice. Others => null.
 */
export function approvalOutcomeNotice(outcome) {
  switch (outcome) {
    case 'busy':
      return { key: 'approval.alreadyPending', tone: 'info' };
    case 'override_declined':
      return { key: 'approval.notOverridden', tone: 'info' };
    case 'approved':
      return { key: 'approval.goalPlanApproved', tone: 'success' };
    case 'continue':
      return { key: 'approval.continueHint', tone: 'info' };
    default:
      return null;
  }
}

const NOTICE_PHASES = ['pending', 'unknown', 'not_found', 'prepared', 'admitted', 'rejected', 'recovery_required', 'failed'];

/**
 * Receipt notice state (port of renderApprovalOperationNotice, no markup).
 * The current session status prefers the latest loaded facts over the
 * historical response.current_state.
 * @param {{record: object|null, response?: object|null, busy?: boolean, loadedStatus?: string}} input
 */
export function approvalNoticeState({ record, response = null, busy = false, loadedStatus = '' } = {}) {
  if (!record) return { visible: false };
  const phase = String(record.phase || '');
  const messageKey = 'approval.notice.' + (NOTICE_PHASES.includes(phase) ? phase : 'unknown');
  const canRetry = ['not_found', 'prepared'].includes(phase) ||
    (phase === 'recovery_required' && response?.approval?.lookup?.receipt?.stage === 'prepared');
  const current = loadedStatus || response?.current_state?.status || '';
  return {
    visible: true,
    phase,
    requestID: { raw: String(record.approval_request_id || '') },
    title: { key: 'approval.notice.title' },
    message: { key: messageKey },
    serverNotice: record.notice ? { raw: String(record.notice) } : null,
    currentStatus: current ? { key: 'approval.notice.currentSession', status: current, statusLabel: { key: 'status.' + String(current).toLowerCase(), params: { fallback: String(current) } } } : null,
    actions: [
      { action: 'check', label: { key: 'approval.action.check' }, disabled: Boolean(busy) },
      canRetry ? { action: 'retry', label: { key: 'approval.action.retry' }, disabled: Boolean(busy) } : null,
      phase === 'recovery_required' ? { action: 'continue', label: { key: 'approval.action.continue' }, disabled: false } : null
    ].filter(Boolean)
  };
}
