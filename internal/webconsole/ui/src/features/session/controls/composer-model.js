// Pure composer view model (no DOM): what a send would do for the current
// state, which toolbar controls apply, and the placeholder / primary action.
// Mirrors send-recovery.js resolveSendRoute() so the UI never offers a mode
// the send pipeline would reject. Tested by validation/scripts/ui_composer_model_test.mjs.
import { CONTINUABLE_STATUSES, detailHasLiveRunHandle, steerReadiness } from '../../../lib/send-recovery.js';

export const PENDING_PLAN_STATUSES = Object.freeze(['planning', 'awaiting_user_input', 'awaiting_approval']);

const lower = (value) => String(value || '').trim().toLowerCase();

/**
 * composerMode(state) -> { kind: 'start'|'continue'|'steer'|'revise'|'starting'|'loading'|'blocked', … }
 * kind follows resolveSendRoute (send-recovery.js steerReadiness): steer while
 * the loaded state is 'running' (the server's steer rule), then the launch
 * window, then continuable statuses. 'starting' is the launch window: an
 * accepted start whose detail has not loaded yet, or a live run handle/marker
 * whose state has not reached 'running' (also on paused/awaiting_input/
 * completed or a plan awaiting approval right after an accepted continue,
 * approve or revise); the composer is disabled instead of offering a send the
 * server refuses.
 */
export function composerMode(state) {
  const isNew = state.route.name !== 'session';
  const detail = isNew ? null : state.session.detail;
  const status = lower(detail?.state?.status);
  const planStatus = lower(detail?.plan_mode?.status);
  const generating = !isNew && Boolean(state.session.generating);
  const launching = !isNew && !detail && state.session.activity?.kind === 'start';
  let kind;
  if (isNew) kind = 'start';
  else if (!detail) kind = launching ? 'starting' : 'loading';
  else {
    const readiness = steerReadiness({ status, generating, liveHandle: detailHasLiveRunHandle(detail) });
    if (readiness !== 'none') kind = readiness;
    else if (CONTINUABLE_STATUSES.includes(status)) kind = planStatus === 'awaiting_approval' ? 'revise' : 'continue';
    else kind = 'blocked';
  }
  const planPending = PENDING_PLAN_STATUSES.includes(planStatus);
  return {
    kind,
    status,
    completed: status === 'completed',
    canGoal: kind === 'start',
    canPlan: (kind === 'start' || kind === 'continue') && !planPending,
    canInterrupt: kind === 'steer',
    canPickModel: kind === 'start' || kind === 'continue',
    planAwaitingApproval: planStatus === 'awaiting_approval',
    planInputPending: planStatus === 'awaiting_user_input',
    planVersion: Number(detail?.plan_mode?.plan_version || 0),
  };
}

/** Effective toggles: a toggle that does not apply to the mode is off. */
export function effectiveToggles(mode, composer) {
  return {
    goal: mode.canGoal && Boolean(composer.goalEnabled),
    plan: mode.canPlan && Boolean(composer.planEnabled),
    interrupt: mode.canInterrupt && Boolean(composer.interrupt),
  };
}

/** i18n key of the textarea placeholder. */
export function placeholderKey(mode, composer) {
  const toggles = effectiveToggles(mode, composer);
  switch (mode.kind) {
    case 'start':
      if (toggles.goal) return 'composer.placeholder.goal';
      if (toggles.plan) return 'composer.placeholder.plan';
      return 'composer.placeholder.new';
    case 'continue':
      if (toggles.plan) return 'composer.placeholder.plan';
      if (mode.planInputPending) return 'composer.placeholder.planInput';
      return mode.completed ? 'composer.placeholder.followUp' : 'composer.placeholder.continue';
    case 'steer':
      return toggles.interrupt ? 'composer.placeholder.interrupt' : 'composer.placeholder.steer';
    case 'revise':
      return 'composer.placeholder.revise';
    case 'loading':
      return 'composer.placeholder.loading';
    case 'starting':
      return 'composer.placeholder.starting';
    default:
      // A run that is settling (handle release, plan submission) accepts input again shortly.
      return ['running', 'queued', 'pending'].includes(mode.status) || mode.planAwaitingApproval ? 'composer.placeholder.settling' : 'composer.placeholder.blocked';
  }
}

/** Primary button: 'stop' while a run is live and the input is empty, otherwise 'steer' | 'revise' | 'send'. */
export function primaryAction(mode, text) {
  const hasText = String(text || '').trim() !== '';
  if (mode.kind === 'steer') return hasText ? 'steer' : 'stop';
  if (mode.kind === 'revise') return 'revise';
  return 'send';
}

/** i18n key of the primary button label for an action. */
export function primaryLabelKey(action, mode) {
  switch (action) {
    case 'stop':
      return 'composer.action.stop';
    case 'steer':
      return 'composer.action.steer';
    case 'revise':
      return 'composer.action.revise';
    default:
      return mode.kind === 'start' ? 'composer.action.start' : 'composer.action.send';
  }
}

/** True when the send button may submit now. */
export function canSubmit(mode, composer) {
  return String(composer.text || '').trim() !== '' && !composer.submitting && !['loading', 'starting', 'blocked'].includes(mode.kind);
}
