// Session control actions — STUBS.
//
// TODO(session/composer area): implement start / continue / steer / stop /
// interrupt with send-recovery.js (failed drafts, intent recovery) and pending
// state in composer.submitting. Contract for the implementer:
//   - every call goes through lib/api.js; never call fetch directly;
//   - set composer.submitting while a request is in flight (prevents double submit);
//   - on success refresh via actions.refreshSession()/refreshOverview() and, for
//     a new run, openSession(newId) — the router owns the URL;
//   - failures become failed drafts only when send-recovery.js says so;
//   - keep the exported names/signatures below (the shell, command palette and
//     placeholders already import them).
// TODO(approvals area): approvePlan / revisePlan / cancelPlan / answerPlanInput
// via approval-operations.js (ApprovalOperationController contract, spec 17 §7.13).
// TODO(goal area): goal actions via lib/api.js goal endpoints.
import { t } from '../lib/i18n.js';
import { pushToast } from './toasts.js';

function notImplemented(name) {
  pushToast({ tone: 'info', message: t('common.notImplemented') });
  return Promise.resolve({ ok: false, notImplemented: name });
}

/** Sends the composer text: start (/), continue, steer or plan revision by state. */
export function submitComposer() { return notImplemented('submitComposer'); }
/** startRun({ prompt, goal, planMode, provider, model }) -> new session id */
export function startRun(_payload) { return notImplemented('startRun'); }
/** continueRun(sessionId, { message, provider, model, planMode }) */
export function continueRun(_sessionId, _payload) { return notImplemented('continueRun'); }
/** steerRun(sessionId, { message, interrupt }) */
export function steerRun(_sessionId, _payload) { return notImplemented('steerRun'); }
/** stopRun(sessionId = current) */
export function stopRun(_sessionId) { return notImplemented('stopRun'); }
/** interruptRun(sessionId = current) */
export function interruptRun(_sessionId) { return notImplemented('interruptRun'); }

export function approvePlan(_sessionId, _options) { return notImplemented('approvePlan'); }
export function revisePlan(_sessionId, _message) { return notImplemented('revisePlan'); }
export function cancelPlan(_sessionId) { return notImplemented('cancelPlan'); }
export function answerPlanInput(_sessionId, _payload) { return notImplemented('answerPlanInput'); }

export function createGoal(_sessionId, _payload) { return notImplemented('createGoal'); }
export function updateGoal(_sessionId, _payload) { return notImplemented('updateGoal'); }
export function pauseGoal(_sessionId) { return notImplemented('pauseGoal'); }
export function resumeGoal(_sessionId) { return notImplemented('resumeGoal'); }
export function completeGoal(_sessionId) { return notImplemented('completeGoal'); }
export function clearGoal(_sessionId) { return notImplemented('clearGoal'); }

/** Restores a failed draft into the composer (only when the composer is empty). */
export function restoreFailedDraft(_draftId) { return notImplemented('restoreFailedDraft'); }
export function discardFailedDraft(_draftId) { return notImplemented('discardFailedDraft'); }
