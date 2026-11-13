// Session control actions (spec/17-web-console-frontend.md §5.1, §7.2–7.3;
// spec/17-web-console.md §7.5–7.13 and §8.1–8.3). Function names are fixed by
// the module contract; extra exports are additive.
//
// createSessionActions(deps) builds every action against injected
// dependencies (api module, store accessors, navigation, refresh, toasts,
// dialogs, composer drafts, approval storage and request IDs), so routing,
// payloads, approval flows and draft rules are unit-testable without a DOM
// (validation/scripts/ui_session_actions_*_test.mjs). The exports at the end
// are the app instance bound to the store.
//
// Rules:
//   - every request goes through lib/api.js;
//   - submitComposer routes with send-recovery.js prepareSend() BEFORE the
//     composer is cleared; payload text is trimmed, failed drafts keep the
//     raw text; failures never auto-resend; restore only into an empty composer;
//   - optimistic user messages live in session.optimistic, failed drafts in
//     composer.failedDrafts (memory only); composer.submitting guards double sends;
//   - approvals use ApprovalOperationController (localStorage intent, crypto
//     request IDs, receipts) and the approval-operations.js presentation rules;
//   - accepted actions refresh the session immediately;
//   - accepted start / continue / steer / revise set session.activity
//     (send-recovery.js localSendActivity) so the stream shows live feedback
//     at once; actions.js refreshSession clears it once the run settles and
//     the stream prefers newer engine events (stream/model.js selectLiveActivity).
import * as apiModule from '../lib/api.js';
import { t, tx } from '../lib/i18n.js';
import { shallowEqual } from '../lib/store.js';
import { SendRecoveryStore, detailHasLiveRunHandle, isEphemeralSessionId, localSendActivity, prepareSend, sendSuccessFeedback } from '../lib/send-recovery.js';
import {
  ApprovalOperationController,
  approvalActionError,
  approvalOutcomeNotice,
  approvalPresentation,
  approvalViewToken,
  approveUnlinkedMission,
  coverageOverrideConfirmation,
  displayedApprovalTarget,
  executeReviewedApproval,
  generateApprovalRequestID,
  goalActionIdentity,
  isCurrentApprovalViewToken,
  isDisplayedApprovalTarget,
  isStaleApprovalTarget,
} from '../lib/approval-operations.js';
import { collectPlanInputAnswers } from '../lib/session-model.js';
import { store } from './store.js';
import { navigate, sessionPath } from './router.js';
import { refreshOverview, refreshSession, toggleInspector } from './actions.js';
import { pushToast, toastError } from './toasts.js';
import { confirmDialog } from './dialogs.js';
import { composerDrafts } from './composer-drafts.js';

const TONES = Object.freeze({ error: 'danger', danger: 'danger', success: 'success', warning: 'warning', info: 'info' });
/** Saved approval phases that are re-checked once when their session loads. */
export const RESTORE_APPROVAL_PHASES = Object.freeze(['pending', 'unknown', 'not_found', 'prepared', 'recovery_required']);
/** Plan Mode statuses during which the composer never starts another Plan Mode. */
export const PENDING_PLAN_STATUSES = Object.freeze(['planning', 'awaiting_user_input', 'awaiting_approval']);

const str = (value) => (value === undefined || value === null ? '' : String(value));
const sameJSON = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
  };
}

function browserStorage() {
  try {
    return globalThis.localStorage || null;
  } catch {
    return null;
  }
}

/** Composer/session mode used by the UI: what a send would do right now. */
export function sendContextFromState(state, drafts, launching = new Set()) {
  const backed = state.route.name === 'session' && Boolean(state.session.id);
  const sessionId = backed ? state.session.id : drafts.newSessionIdentity();
  return {
    rawText: state.composer.text,
    sessionId,
    backed,
    status: backed ? str(state.session.detail?.state?.status) : '',
    generating: backed && Boolean(state.session.generating),
    liveHandle: backed && detailHasLiveRunHandle(state.session.detail),
    planModeStatus: backed ? str(state.session.detail?.plan_mode?.status) : '',
    launchInFlight: !backed && launching.has(sessionId),
  };
}

/**
 * createSessionActions(deps) -> { submitComposer, startRun, …, attach(store), recovery, approvals() }
 * deps: { api, getState, update, navigate, sessionPath, refreshSession, refreshOverview,
 *   toggleInspector, pushToast, toastError, confirmDialog, drafts, storage, requestID, createSendId, now }
 */
export function createSessionActions(deps) {
  const api = deps.api || apiModule;
  const { getState, update, drafts } = deps;
  const nav = deps.navigate || (() => {});
  const pathFor = deps.sessionPath || ((id) => '/s/' + encodeURIComponent(id));
  const refresh = deps.refreshSession || (async () => null);
  const refreshList = deps.refreshOverview || (async () => null);
  const setInspector = deps.toggleInspector || (() => {});
  const toast = deps.pushToast || (() => 0);
  const toastErr = deps.toastError || ((error) => toast({ tone: 'danger', message: str(error?.message) }));
  const confirm = deps.confirmDialog || (async () => false);
  const requestID = deps.requestID || (() => generateApprovalRequestID());
  const now = deps.now || (() => Date.now());
  const launching = new Set();
  const stopping = new Set();
  const failureInfo = new Map(); // failed draft id -> { detail, action } kept for the inline card
  const restoredApprovals = new Set();
  let approvalEpoch = 0;
  let controller = null;
  const recovery = new SendRecoveryStore({ createId: deps.createSendId, onChange: () => syncRecovery() });

  // ---- helpers ------------------------------------------------------------------

  const currentId = () => getState().session.id;
  const isCurrent = (id) => Boolean(id) && getState().session.id === id;

  function notify(descriptor, extra = {}) {
    if (!descriptor) return 0;
    const message = descriptor.raw !== undefined && descriptor.raw !== null ? str(descriptor.raw) : tx(descriptor);
    return toast({ tone: extra.tone || TONES[descriptor.tone] || 'info', message, detail: extra.detail || '', action: extra.action || '' });
  }

  function setPending(id, name) {
    update('session', (session) => (session.id === id ? { ...session, pendingAction: name } : session));
  }

  function clearPending(id, name) {
    update('session', (session) => (session.id === id && session.pendingAction === name ? { ...session, pendingAction: '' } : session));
  }

  /** Immediate live-activity feedback for an accepted send on the open session id. */
  function showSendActivity(id, kind, options) {
    const activity = localSendActivity(kind, options, now());
    if (!activity) return;
    update('session', (session) => (session.id === id ? { ...session, activity } : session));
  }

  function syncOptimistic() {
    const { session } = getState();
    const list = session.id ? recovery.optimisticMessages({ sessionId: session.id, backed: true }) : [];
    if (sameJSON(session.optimistic, list)) return;
    update('session', (current) => (current.id === session.id ? { ...current, optimistic: list } : current));
  }

  function syncRecovery() {
    const failedDrafts = recovery.failedDrafts().map((draft) => ({ ...draft, errorDetail: failureInfo.get(draft.id)?.detail || '', errorAction: failureInfo.get(draft.id)?.action || '' }));
    for (const id of failureInfo.keys()) if (!failedDrafts.some((draft) => draft.id === id)) failureInfo.delete(id);
    if (!sameJSON(getState().composer.failedDrafts, failedDrafts)) update('composer', { failedDrafts });
    syncOptimistic();
  }

  function reconcileCurrent() {
    const { session } = getState();
    if (!session.id || !session.detail) return;
    recovery.reconcile({ sessionId: session.id, backed: true }, { messages: session.messages, steer_requests: session.detail.steer_requests });
    syncOptimistic();
  }

  // ---- sending --------------------------------------------------------------------

  /**
   * Failed sends are object-level errors: the failed-draft card above the
   * composer shows them inline (with backend detail/action). A toast is only
   * added when no session view (and so no card) is visible.
   */
  function reportSendFailure(error, draft) {
    const route = getState().route.name;
    if (route === 'new' || route === 'session') return;
    if (draft?.status === 'unconfirmed') {
      toast({ tone: 'warning', message: t('composer.send.unconfirmed'), detail: str(error?.detail || error?.message), action: str(error?.action) });
      return;
    }
    toast({ tone: 'danger', message: str(error?.message) || t('composer.send.failed'), detail: str(error?.detail), action: str(error?.action) });
  }

  /**
   * Core send: registers a send intent (optimistic message, baseline of loaded
   * ids), calls the API for kind start|continue|steer|revise and records a
   * failed draft on error. input.draftKey ties it to a composer draft.
   */
  async function performSend(input) {
    const kind = input.kind;
    const rawText = str(input.rawText);
    const text = rawText.trim();
    if (!text) return { ok: false, reason: 'empty' };
    const sessionId = str(input.session?.sessionId);
    const backed = Boolean(input.session?.backed) && !isEphemeralSessionId(sessionId);
    const state = getState();
    const baseline = backed && state.session.id === sessionId ? {
      messageIds: state.session.messages.map((message) => str(message?.id)).filter(Boolean),
      steerIds: (Array.isArray(state.session.detail?.steer_requests) ? state.session.detail.steer_requests : []).map((item) => str(item?.id)).filter(Boolean),
    } : undefined;
    const interrupt = kind === 'steer' && Boolean(input.interrupt);
    const intent = recovery.begin({ session: { sessionId, backed }, kind, rawText, baseline, meta: interrupt ? { interrupt: true } : undefined });
    const key = input.draftKey || '';
    if (key) drafts.setSubmitting(key, true);
    if (kind === 'start') launching.add(sessionId);
    try {
      let response;
      if (kind === 'start') {
        response = await api.startSession({
          prompt: text,
          goal: input.goalDraft || undefined,
          planMode: input.planDraft || undefined,
          provider: input.provider || undefined,
          model: input.model || undefined,
          workdir: input.workdir || undefined,
        });
      } else if (kind === 'continue') {
        response = await api.continueSession(sessionId, { message: text, provider: input.provider || undefined, model: input.model || undefined, planMode: input.planDraft || undefined });
      } else if (kind === 'steer') {
        response = await api.steerSession(sessionId, { message: text, interrupt });
      } else {
        response = await api.revisePlanMode(sessionId, text);
      }
      const durableId = kind === 'start' ? str(response?.session_id).trim() : sessionId;
      if (!durableId) throw new apiModule.APIError(t('composer.error.noSessionId'), { status: 0 });
      recovery.succeed(intent.id, { durableSessionId: durableId });
      // Accepted: the composer is free again even while the follow-up refresh runs.
      if (key) drafts.setSubmitting(key, false);
      const feedbackOptions = { interrupt, planDraft: Boolean(input.planDraft) };
      const feedback = sendSuccessFeedback(kind, feedbackOptions);
      if (feedback.toast) notify(feedback.toast);
      if (key) {
        const reset = kind === 'start' ? { goalEnabled: false, planEnabled: false, provider: '', model: '' }
          : kind === 'continue' ? { planEnabled: false, provider: '', model: '' }
            : kind === 'steer' ? { interrupt: false } : { revising: false };
        drafts.patch(key, reset);
      }
      if (kind === 'start') {
        launching.delete(sessionId);
        if (!key || drafts.currentKey() === key) nav(pathFor(durableId));
        showSendActivity(durableId, kind, feedbackOptions);
        refreshList({ silent: true });
        return { ok: true, kind, sessionId: durableId, response };
      }
      if (isCurrent(sessionId)) {
        if (kind !== 'steer') update('session', (session) => (session.id === sessionId ? { ...session, generating: true } : session));
        showSendActivity(sessionId, kind, feedbackOptions);
        await refresh({ silent: true });
      }
      refreshList({ silent: true });
      return { ok: true, kind, sessionId, response };
    } catch (error) {
      if (kind === 'start') launching.delete(sessionId);
      if (key) drafts.setSubmitting(key, false);
      failureInfo.set(intent.id, { detail: str(error?.detail), action: str(error?.action) });
      const { draft, refresh: plan } = recovery.fail(intent.id, error);
      reportSendFailure(error, draft);
      if (plan.session && isCurrent(plan.sessionId)) refresh({ silent: true });
      if (plan.overview) refreshList({ silent: true });
      return { ok: false, reason: 'failed', kind, draft, error };
    } finally {
      if (key) drafts.setSubmitting(key, false);
    }
  }

  /** Sends the composer text: start (/), continue, steer or plan revision by state. */
  async function submitComposer() {
    const state = getState();
    if (state.route.name !== 'new' && state.route.name !== 'session') return { ok: false, reason: 'route' };
    const key = drafts.currentKey();
    if (drafts.isSubmitting(key)) return { ok: false, reason: 'busy' };
    const composer = state.composer;
    const ctx = sendContextFromState(state, drafts, launching);
    // A stale Plan toggle never starts a second Plan Mode while one is pending.
    const planAllowed = !PENDING_PLAN_STATUSES.includes(ctx.planModeStatus.toLowerCase());
    const route = prepareSend(ctx, {
      goal: composer.goalEnabled ? () => ({ enabled: true }) : undefined,
      plan: composer.planEnabled && planAllowed ? () => ({ enabled: true }) : undefined,
    });
    if (!route.ok) {
      if (route.notice) notify(route.notice);
      return route;
    }
    const allowsModel = route.kind === 'start' || route.kind === 'continue';
    // Validation passed: only now clear the composer (the draft keeps the raw text on failure).
    drafts.patch(key, { text: '' });
    return performSend({
      kind: route.kind,
      session: { sessionId: route.session.sessionId, backed: !route.session.ephemeral },
      rawText: route.rawText,
      goalDraft: route.goalDraft,
      planDraft: route.planDraft,
      provider: allowsModel ? composer.provider : '',
      model: allowsModel ? composer.model : '',
      interrupt: route.kind === 'steer' && composer.interrupt,
      draftKey: key,
    });
  }

  /** startRun({ prompt, goal, planMode, provider, model, workdir }) -> { ok, sessionId } */
  function startRun(payload = {}) {
    const flag = (value) => (value && typeof value === 'object' ? value : value ? { enabled: true } : null);
    return performSend({
      kind: 'start',
      session: { sessionId: drafts.newSessionIdentity(), backed: false },
      rawText: payload.prompt,
      goalDraft: flag(payload.goal),
      planDraft: flag(payload.planMode),
      provider: payload.provider,
      model: payload.model,
      workdir: payload.workdir,
    });
  }

  /** continueRun(sessionId, { message, provider, model, planMode }) */
  function continueRun(sessionId = currentId(), payload = {}) {
    return performSend({
      kind: 'continue',
      session: { sessionId, backed: true },
      rawText: payload.message,
      provider: payload.provider,
      model: payload.model,
      planDraft: payload.planMode ? (typeof payload.planMode === 'object' ? payload.planMode : { enabled: true }) : null,
    });
  }

  /** steerRun(sessionId, { message, interrupt }) */
  function steerRun(sessionId = currentId(), payload = {}) {
    return performSend({ kind: 'steer', session: { sessionId, backed: true }, rawText: payload.message, interrupt: payload.interrupt });
  }

  // ---- stop / interrupt -------------------------------------------------------------

  /** stopRun(sessionId = current): stop through the owned handle; not-owned explains the backend detail. */
  async function stopRun(sessionId = currentId()) {
    if (!sessionId) return { ok: false, reason: 'noSession' };
    if (stopping.has(sessionId)) {
      notify({ key: 'composer.stop.already', tone: 'info' });
      return { ok: false, reason: 'busy' };
    }
    stopping.add(sessionId);
    setPending(sessionId, 'stop');
    try {
      const response = await api.stopSession(sessionId);
      notify({ key: response?.reconciled ? 'composer.stop.recovered' : 'composer.stop.requested', tone: 'success' });
      if (isCurrent(sessionId)) await refresh({ silent: true });
      refreshList({ silent: true });
      return { ok: true, response };
    } catch (error) {
      if (error?.code === 'ACTIVE_HANDLE_NOT_OWNED') {
        toast({ tone: 'warning', message: t('composer.stop.notOwned'), detail: str(error.detail || error.message), action: str(error.action) || t('composer.stop.notOwnedAction') });
      } else {
        toastErr(error, 'composer.stop.failed');
      }
      return { ok: false, reason: 'failed', error };
    } finally {
      stopping.delete(sessionId);
      clearPending(sessionId, 'stop');
    }
  }

  /** interruptRun(sessionId = current) */
  async function interruptRun(sessionId = currentId()) {
    if (!sessionId) return { ok: false, reason: 'noSession' };
    setPending(sessionId, 'interrupt');
    try {
      const response = await api.interruptSession(sessionId);
      notify({ key: 'composer.interrupt.requested', tone: 'success' });
      if (isCurrent(sessionId)) await refresh({ silent: true });
      return { ok: true, response };
    } catch (error) {
      toastErr(error, 'composer.interrupt.failed');
      return { ok: false, reason: 'failed', error };
    } finally {
      clearPending(sessionId, 'interrupt');
    }
  }

  // ---- approvals ----------------------------------------------------------------------

  function approvals() {
    if (!controller) {
      const storage = (typeof deps.storage === 'function' ? deps.storage() : deps.storage) || browserStorage() || memoryStorage();
      controller = new ApprovalOperationController({
        storage,
        requestID: () => requestID(),
        submit: (sessionID, entrypoint, payload) => (entrypoint === 'mission' ? api.approveMissionPlan(sessionID, payload) : api.approvePlanMode(sessionID, payload)),
        query: (sessionID, id) => api.getApprovalReceipt(sessionID, id),
        onChange: (sessionID) => {
          if (isCurrent(sessionID)) syncApproval();
        },
      });
    }
    return controller;
  }

  function approvalSnapshot(id) {
    if (!id) return null;
    try {
      const operations = approvals();
      const record = operations.get(id);
      const busy = operations.busy(id);
      if (!record && !busy) return null;
      return { record, response: record ? operations.response(id) : null, busy };
    } catch (error) {
      return { record: null, response: null, busy: false, error: approvalActionError(error) };
    }
  }

  function syncApproval() {
    const id = currentId();
    const next = approvalSnapshot(id);
    if (sameJSON(getState().session.approval, next)) return;
    update('session', (session) => (session.id === id ? { ...session, approval: next } : session));
  }

  function currentViewToken(sessionId) {
    return approvalViewToken({ sessionID: sessionId, epoch: approvalEpoch, state: getState().session.detail?.state });
  }

  const isViewCurrent = (token) => isCurrentApprovalViewToken(token, { sessionID: currentId(), epoch: approvalEpoch });

  function notifyApprovalError(error, fallbackKey = 'approval.error.unverified') {
    const slot = approvalActionError(error, fallbackKey);
    const stale = slot.key === 'approval.planChanged';
    toast({
      tone: stale ? 'warning' : 'danger',
      message: slot.raw !== undefined ? slot.raw : tx(slot),
      detail: stale || slot.raw === undefined ? '' : str(error?.detail),
      action: stale ? '' : str(error?.action),
    });
  }

  function confirmCoverageOverride() {
    const spec = coverageOverrideConfirmation();
    return confirm({ title: tx(spec.title), body: tx(spec.message), confirmLabel: tx(spec.confirm), danger: spec.tone === 'danger' });
  }

  function presentApproval(response, viewToken, sessionId, { quietAdmitted = false } = {}) {
    let savedRequestID = '';
    try {
      savedRequestID = approvals().get(sessionId)?.approval_request_id || '';
    } catch {
      savedRequestID = '';
    }
    const presentation = approvalPresentation(response, {
      viewCurrent: isViewCurrent(viewToken),
      sessionID: sessionId,
      goalID: getState().session.detail?.goal?.goal_id,
      savedRequestID,
      loadedState: getState().session.detail?.state,
      viewToken,
    });
    if (presentation.toast && !(quietAdmitted && presentation.kind === 'already_admitted')) notify(presentation.toast);
    if (presentation.generating) update('session', (session) => (session.id === sessionId ? { ...session, generating: true } : session));
    return presentation;
  }

  /**
   * approvePlan(sessionId = current, { entrypoint: 'planmode'|'mission' })
   * Approves the displayed target; coverage blocks ask for an explicit override
   * (new request id); stale targets ask for a re-review and refresh.
   */
  async function approvePlan(sessionId = currentId(), options = {}) {
    const state = getState();
    if (!sessionId || state.session.id !== sessionId || !state.session.detail) {
      notify({ key: 'composer.noSession', tone: 'info' });
      return { outcome: 'no_session' };
    }
    const detail = state.session.detail;
    const planMode = detail.plan_mode || null;
    const entrypoint = options.entrypoint === 'mission' ? 'mission' : 'planmode';
    const goalIdentity = entrypoint === 'mission' ? goalActionIdentity(detail.goal) : '';
    const goalCurrent = () => goalActionIdentity(getState().session.detail?.goal) === goalIdentity;
    setPending(sessionId, 'approve');
    try {
      if (entrypoint === 'mission' && !planMode?.linked_goal_id) {
        // Unlinked mission plan: facts-only approval without a receipt.
        const result = await approveUnlinkedMission({
          approve: (extra) => api.approveMissionPlan(sessionId, extra),
          isCurrent: () => isCurrent(sessionId) && goalCurrent(),
          confirmCoverageOverride,
        });
        const notice = approvalOutcomeNotice(result.outcome);
        if (notice) notify(notice);
        if (isCurrent(sessionId)) await refresh({ silent: true });
        return result;
      }
      let target;
      try {
        target = displayedApprovalTarget(planMode);
      } catch (error) {
        notifyApprovalError(error);
        refresh({ silent: true });
        return { outcome: 'reload' };
      }
      const viewToken = currentViewToken(sessionId);
      const result = await executeReviewedApproval({
        controller: approvals(),
        sessionID: sessionId,
        entrypoint,
        target,
        isViewCurrent: () => isViewCurrent(viewToken),
        isTargetDisplayed: (reviewed) => isDisplayedApprovalTarget(getState().session.detail?.plan_mode, reviewed),
        isGoalIdentityCurrent: goalIdentity ? goalCurrent : undefined,
        confirmCoverageOverride,
      });
      const notice = approvalOutcomeNotice(result.outcome);
      if (notice && isViewCurrent(viewToken)) notify(notice);
      if (result.outcome !== 'busy') presentApproval(result.response, viewToken, sessionId);
      if (isViewCurrent(viewToken)) {
        if (getState().composer.revising) update('composer', { revising: false });
        await refresh({ silent: true });
        refreshList({ silent: true });
      }
      return result;
    } catch (error) {
      const stale = isStaleApprovalTarget(error);
      if (isCurrent(sessionId) || stale) notifyApprovalError(error);
      if (stale && isCurrent(sessionId)) refresh({ silent: true });
      return { outcome: 'error', error };
    } finally {
      clearPending(sessionId, 'approve');
      syncApproval();
    }
  }

  async function receiptAction(action, sessionId = currentId()) {
    if (!sessionId) return null;
    const viewToken = currentViewToken(sessionId);
    setPending(sessionId, 'approval-check');
    try {
      const operations = approvals();
      const response = action === 'retry' ? await operations.retry(sessionId) : await operations.check(sessionId);
      presentApproval(response, viewToken, sessionId);
      if (isViewCurrent(viewToken)) await refresh({ silent: true });
      return response;
    } catch (error) {
      if (isViewCurrent(viewToken)) notifyApprovalError(error);
      return null;
    } finally {
      clearPending(sessionId, 'approval-check');
      syncApproval();
    }
  }

  /** Query the saved approval receipt (never resends). */
  const checkApproval = (sessionId) => receiptAction('check', sessionId);
  /** Retry the saved request with its original id/parameters (only after a check allows it). */
  const retryApproval = (sessionId) => receiptAction('retry', sessionId);

  /** recovery_required: continue the session normally from the composer. */
  function continueAfterApprovalRecovery() {
    const { ui } = getState();
    if (ui.inspectorOpen && !ui.viewport.wide) setInspector(false);
    update('composer', (composer) => ({ ...composer, revising: false, focusToken: composer.focusToken + 1 }));
    notify(approvalOutcomeNotice('continue'));
  }

  function maybeRestoreApproval(sessionId) {
    if (!sessionId || restoredApprovals.has(sessionId)) return;
    restoredApprovals.add(sessionId);
    let pending = null;
    try {
      const record = approvals().get(sessionId);
      if (!record || !RESTORE_APPROVAL_PHASES.includes(record.phase)) {
        syncApproval();
        return;
      }
      const viewToken = currentViewToken(sessionId);
      pending = approvals().restore(sessionId);
      if (!pending) return;
      pending.then((response) => {
        if (isViewCurrent(viewToken)) presentApproval(response, viewToken, sessionId, { quietAdmitted: true });
      }).catch(() => {}).finally(() => {
        syncApproval();
        if (isViewCurrent(viewToken)) refresh({ silent: true });
      });
    } catch {
      syncApproval();
    }
  }

  // ---- plan revision / cancel / input -------------------------------------------------

  /**
   * revisePlan(sessionId, message?): with a message, sends it as a plan
   * revision; otherwise switches the composer into revising mode and focuses
   * it (closing an overlay inspector first so focus is not left behind inert content).
   */
  function revisePlan(sessionId = currentId(), message) {
    if (typeof message === 'string' && message.trim()) {
      return performSend({ kind: 'revise', session: { sessionId, backed: true }, rawText: message });
    }
    const state = getState();
    if (!state.session.id || (sessionId && sessionId !== state.session.id)) return Promise.resolve({ ok: false, reason: 'noSession' });
    if (state.ui.inspectorOpen && !state.ui.viewport.wide) setInspector(false);
    update('composer', (composer) => ({ ...composer, revising: true, goalEnabled: false, planEnabled: false, focusToken: composer.focusToken + 1 }));
    return Promise.resolve({ ok: true, revising: true });
  }

  /** Leaves revising mode (the next send still revises while the plan awaits approval). */
  function cancelRevision() {
    update('composer', (composer) => (composer.revising ? { ...composer, revising: false } : composer));
  }

  async function cancelPlan(sessionId = currentId()) {
    if (!sessionId) return { ok: false, reason: 'noSession' };
    const ok = await confirm({ title: t('composer.plan.cancelTitle'), body: t('composer.plan.cancelBody'), confirmLabel: t('composer.plan.cancelConfirm'), danger: true });
    if (!ok) return { ok: false, reason: 'declined' };
    if (!isCurrent(sessionId)) return { ok: false, reason: 'stale' };
    setPending(sessionId, 'cancel-plan');
    try {
      const response = await api.cancelPlanMode(sessionId);
      if (isCurrent(sessionId)) {
        notify({ key: 'composer.plan.cancelled', tone: 'success' });
        if (getState().composer.revising) update('composer', { revising: false });
        await refresh({ silent: true });
      }
      refreshList({ silent: true });
      return { ok: true, response };
    } catch (error) {
      if (isCurrent(sessionId)) toastErr(error, 'composer.plan.cancelFailed');
      return { ok: false, reason: 'failed', error };
    } finally {
      clearPending(sessionId, 'cancel-plan');
    }
  }

  /** answerPlanInput(sessionId, { requestID, selections } | { requestID, answers }) */
  async function answerPlanInput(sessionId = currentId(), payload = {}) {
    const planMode = isCurrent(sessionId) ? getState().session.detail?.plan_mode : null;
    const request = planMode?.pending_request;
    const requestID = str(payload.requestID).trim();
    if (!request || !requestID || request.request_id !== requestID) {
      notify({ key: 'composer.planInput.stale', tone: 'warning' });
      if (isCurrent(sessionId)) refresh({ silent: true });
      return { ok: false, reason: 'stale' };
    }
    const questions = Array.isArray(request.questions) ? request.questions : [];
    const answers = Array.isArray(payload.answers) ? payload.answers : collectPlanInputAnswers(request, payload.selections);
    if (!questions.length || answers.length !== questions.length) {
      notify({ key: 'composer.planInput.incomplete', tone: 'warning' });
      return { ok: false, reason: 'incomplete' };
    }
    setPending(sessionId, 'answer');
    try {
      const response = await api.answerPlanModeInput(sessionId, { requestID, answers });
      if (isCurrent(sessionId)) {
        notify({ key: 'composer.planInput.sent', tone: 'success' });
        await refresh({ silent: true });
      }
      refreshList({ silent: true });
      return { ok: true, response, answers };
    } catch (error) {
      if (isCurrent(sessionId)) toastErr(error, 'composer.planInput.failed');
      return { ok: false, reason: 'failed', error };
    } finally {
      clearPending(sessionId, 'answer');
    }
  }

  // ---- goal ---------------------------------------------------------------------------

  async function goalAction(sessionId, call, successKey, confirmSpec = null) {
    const id = sessionId || currentId();
    if (!id) return { ok: false, reason: 'noSession' };
    if (confirmSpec && !await confirm(confirmSpec)) return { ok: false, reason: 'declined' };
    if (confirmSpec && !isCurrent(id)) return { ok: false, reason: 'stale' };
    setPending(id, 'goal');
    try {
      const response = await call(id);
      if (isCurrent(id)) {
        notify({ key: successKey, tone: 'success' });
        await refresh({ silent: true });
      }
      refreshList({ silent: true });
      return { ok: true, response };
    } catch (error) {
      if (isCurrent(id)) toastErr(error, 'composer.goal.failed');
      return { ok: false, reason: 'failed', error };
    } finally {
      clearPending(id, 'goal');
    }
  }

  const createGoal = (sessionId, payload = {}) => goalAction(sessionId, (id) => api.createGoal(id, payload), 'composer.goal.created');
  const updateGoal = (sessionId, payload = {}) => goalAction(sessionId, (id) => api.updateGoal(id, payload), 'composer.goal.updated');
  const pauseGoal = (sessionId) => goalAction(sessionId, (id) => api.pauseGoal(id), 'composer.goal.paused');
  const resumeGoal = (sessionId) => goalAction(sessionId, (id) => api.resumeGoal(id), 'composer.goal.resumed');
  const completeGoal = (sessionId) => goalAction(sessionId, (id) => api.completeGoal(id), 'composer.goal.completed', {
    title: t('composer.goal.completeTitle'), body: t('composer.goal.completeBody'), confirmLabel: t('composer.goal.completeConfirm'), danger: false,
  });
  const clearGoal = (sessionId) => goalAction(sessionId, (id) => api.deleteGoal(id), 'composer.goal.cleared', {
    title: t('composer.goal.clearTitle'), body: t('composer.goal.clearBody'), confirmLabel: t('composer.goal.clearConfirm'), danger: true,
  });

  // ---- failed drafts ------------------------------------------------------------------

  /** Restores a failed draft into the composer (only when the composer is empty; never resends). */
  function restoreFailedDraft(draftId) {
    const result = recovery.restore(draftId, { composerValue: getState().composer.text });
    if (!result.ok) {
      if (result.notice) notify(result.notice);
      return result;
    }
    drafts.patch(drafts.currentKey(), { text: result.rawText });
    update('composer', (composer) => ({ ...composer, focusToken: composer.focusToken + 1 }));
    if (result.notice) notify(result.notice);
    return result;
  }

  function discardFailedDraft(draftId) {
    return recovery.dismiss(draftId);
  }

  // ---- store wiring ---------------------------------------------------------------------

  /** Keeps session.optimistic / session.approval in sync with the open session. */
  function attach(storeApi) {
    const offs = [
      storeApi.select((state) => state.session.id, () => {
        approvalEpoch += 1;
        syncOptimistic();
        syncApproval();
      }),
      storeApi.select((state) => [state.session.id, state.session.messages, state.session.detail], () => reconcileCurrent(), shallowEqual),
      storeApi.select((state) => (state.session.detail ? state.session.id : ''), (id) => {
        if (id) maybeRestoreApproval(id);
      }),
    ];
    return () => offs.forEach((off) => off());
  }

  return {
    submitComposer,
    startRun,
    continueRun,
    steerRun,
    stopRun,
    interruptRun,
    approvePlan,
    checkApproval,
    retryApproval,
    continueAfterApprovalRecovery,
    revisePlan,
    cancelRevision,
    cancelPlan,
    answerPlanInput,
    createGoal,
    updateGoal,
    pauseGoal,
    resumeGoal,
    completeGoal,
    clearGoal,
    restoreFailedDraft,
    discardFailedDraft,
    attach,
    approvals,
    syncApproval,
    reconcileCurrent,
    recovery,
  };
}

// ---- app instance -----------------------------------------------------------------------

const app = createSessionActions({
  api: apiModule,
  getState: store.getState,
  update: store.update,
  navigate,
  sessionPath,
  refreshSession,
  refreshOverview,
  toggleInspector,
  pushToast,
  toastError,
  confirmDialog,
  drafts: composerDrafts,
});
app.attach(store);

/** Sends the composer text: start (/), continue, steer or plan revision by state. */
export const submitComposer = () => app.submitComposer();
/** startRun({ prompt, goal, planMode, provider, model, workdir }) -> { ok, sessionId } */
export const startRun = (payload) => app.startRun(payload);
/** continueRun(sessionId, { message, provider, model, planMode }) */
export const continueRun = (sessionId, payload) => app.continueRun(sessionId, payload);
/** steerRun(sessionId, { message, interrupt }) */
export const steerRun = (sessionId, payload) => app.steerRun(sessionId, payload);
/** stopRun(sessionId = current) */
export const stopRun = (sessionId) => app.stopRun(sessionId || undefined);
/** interruptRun(sessionId = current) */
export const interruptRun = (sessionId) => app.interruptRun(sessionId || undefined);

export const approvePlan = (sessionId, options) => app.approvePlan(sessionId || undefined, options);
export const checkApproval = (sessionId) => app.checkApproval(sessionId || undefined);
export const retryApproval = (sessionId) => app.retryApproval(sessionId || undefined);
export const continueAfterApprovalRecovery = () => app.continueAfterApprovalRecovery();
export const revisePlan = (sessionId, message) => app.revisePlan(sessionId || undefined, message);
export const cancelRevision = () => app.cancelRevision();
export const cancelPlan = (sessionId) => app.cancelPlan(sessionId || undefined);
export const answerPlanInput = (sessionId, payload) => app.answerPlanInput(sessionId || undefined, payload);

export const createGoal = (sessionId, payload) => app.createGoal(sessionId, payload);
export const updateGoal = (sessionId, payload) => app.updateGoal(sessionId, payload);
export const pauseGoal = (sessionId) => app.pauseGoal(sessionId);
export const resumeGoal = (sessionId) => app.resumeGoal(sessionId);
export const completeGoal = (sessionId) => app.completeGoal(sessionId);
export const clearGoal = (sessionId) => app.clearGoal(sessionId);

/** Restores a failed draft into the composer (only when the composer is empty). */
export const restoreFailedDraft = (draftId) => app.restoreFailedDraft(draftId);
export const discardFailedDraft = (draftId) => app.discardFailedDraft(draftId);
