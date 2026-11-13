// Composer (spec/17-web-console-frontend.md §7.2): auto-growing input with
// state-aware placeholder, Goal / Plan / interrupt toggles, provider/model
// override and a primary button that morphs send ↔ steer ↔ stop.
// Sending, drafts and recovery live in state/session-actions.js; this
// component only renders the composer slice. Enter sends, Shift+Enter adds a
// line, and nothing is sent while an IME composition is active (composition
// listeners use lowercase names: Preact keeps unknown on* names case-sensitive).
// Test ids: composer, composer-input, send-button, stop-button, goal-toggle,
// plan-toggle, interrupt-toggle, model-picker, composer-revising,
// composer-plan-hint, composer-revise-cancel.
import { html, useEffect, useId, useLayoutEffect, useRef } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { setComposerText, toggleGoalMode, togglePlanMode, toggleInterruptMode } from '../../state/actions.js';
import { cancelRevision, stopRun, submitComposer } from '../../state/session-actions.js';
import { canStopSession } from '../../state/selectors.js';
import { Icon } from '../../ui/Icon.js';
import { Spinner } from '../../ui/Spinner.js';
import { Kbd } from '../../ui/Kbd.js';
import { ModeChip } from './controls/ModeChip.js';
import { ModelPicker } from './controls/ModelPicker.js';
import { revealCard } from './controls/scroll.js';
import { canSubmit, composerMode, effectiveToggles, placeholderKey, primaryAction, primaryLabelKey } from './controls/composer-model.js';

function selectView(state) {
  const mode = composerMode(state);
  const composer = state.composer;
  return {
    locale: state.ui.locale,
    sessionId: state.session.id,
    text: composer.text,
    focusToken: composer.focusToken,
    goalEnabled: composer.goalEnabled,
    planEnabled: composer.planEnabled,
    interrupt: composer.interrupt,
    provider: composer.provider,
    model: composer.model,
    revising: composer.revising,
    submitting: composer.submitting,
    kind: mode.kind,
    canStop: canStopSession(state),
    stopping: state.session.pendingAction === 'stop',
    providers: state.meta.data?.providers || null,
    defaultProvider: state.meta.data?.default_provider || '',
  };
}

const sameView = (a, b) => Object.keys(a).every((key) => Object.is(a[key], b[key]));

export function Composer() {
  const view = useStore(selectView, sameView);
  const mode = useStore((state) => composerMode(state), (a, b) => JSON.stringify(a) === JSON.stringify(b));
  const inputRef = useRef(null);
  const composingRef = useRef(false);
  const compositionEndRef = useRef(0);
  const noteId = useId();
  const hintId = useId();

  const resize = () => {
    const element = inputRef.current;
    if (!element) return;
    element.style.height = 'auto';
    element.style.height = `${element.scrollHeight}px`;
  };
  useLayoutEffect(resize, [view.text]);

  useEffect(() => {
    if (!view.focusToken) return undefined;
    // After the current commit: an overlay inspector closing in the same update restores its own focus first.
    const frame = requestAnimationFrame(() => {
      const element = inputRef.current;
      if (!element) return;
      element.focus({ preventScroll: true });
      const end = element.value.length;
      element.setSelectionRange(end, end);
    });
    return () => cancelAnimationFrame(frame);
  }, [view.focusToken]);

  const toggles = effectiveToggles(mode, view);
  const action = primaryAction(mode, view.text);
  const submittable = canSubmit(mode, view);

  const onKeyDown = (event) => {
    if (event.key !== 'Enter' || event.shiftKey || event.altKey) return;
    if (event.isComposing || composingRef.current || event.keyCode === 229 || Date.now() - compositionEndRef.current < 50) return;
    event.preventDefault();
    if (submittable) submitComposer();
  };

  const onSubmit = (event) => {
    event.preventDefault();
    if (action === 'stop') return;
    if (submittable) submitComposer();
  };

  const note = toggles.goal ? t('composer.goal.note')
    : toggles.plan ? t('composer.plan.note')
      : toggles.interrupt ? t('composer.interrupt.note') : '';

  const showHint = mode.planAwaitingApproval || mode.planInputPending;
  const primaryLabel = t(primaryLabelKey(action, mode));

  return html`<div class="composer-shell" data-kind=${mode.kind}>
    ${view.revising && mode.planAwaitingApproval ? html`<div class="composer-context is-revising" data-testid="composer-revising">
      <${Icon} name="pencil" size=${14} />
      <span class="composer-context-text">${t('composer.revising.title', { version: mode.planVersion || 1 })}</span>
      <button type="button" class="composer-context-action" data-testid="composer-revise-cancel" onClick=${cancelRevision}>${t('composer.revising.cancel')}</button>
    </div>` : showHint ? html`<div class="composer-context" data-testid="composer-plan-hint">
      <${Icon} name="clipboard-list" size=${14} />
      <span class="composer-context-text">${mode.planAwaitingApproval ? t('composer.hint.planAwaiting') : t('composer.hint.planInput')}</span>
      <button type="button" class="composer-context-action" onClick=${() => revealCard(mode.planAwaitingApproval ? 'approval-card' : 'plan-input-card')}>${t('composer.hint.view')}</button>
    </div>` : null}
    <form class="composer" data-testid="composer" data-kind=${mode.kind} aria-busy=${view.submitting ? 'true' : undefined} onSubmit=${onSubmit}>
      <label class="sr-only" for="composer-input">${t('composer.label')}</label>
      <textarea
        id="composer-input"
        ref=${inputRef}
        class="composer-input"
        rows="1"
        placeholder=${t(placeholderKey(mode, view))}
        value=${view.text}
        aria-describedby=${[note ? noteId : '', hintId].filter(Boolean).join(' ')}
        aria-keyshortcuts="Enter"
        data-testid="composer-input"
        onInput=${(event) => { setComposerText(event.currentTarget.value); resize(); }}
        onKeyDown=${onKeyDown}
        oncompositionstart=${() => { composingRef.current = true; }}
        oncompositionend=${() => { composingRef.current = false; compositionEndRef.current = Date.now(); }}
      ></textarea>
      ${note ? html`<p class="composer-note" id=${noteId} aria-live="polite">${note}</p>` : null}
      <div class="composer-toolbar">
        <div class="composer-tools" role="group" aria-label=${t('composer.tools')}>
          ${mode.canGoal ? html`<${ModeChip}
            icon="target"
            label=${t('composer.goal.toggle')}
            hint=${t('composer.goal.hint')}
            pressed=${toggles.goal}
            testId="goal-toggle"
            onToggle=${(next) => toggleGoalMode(next)}
          />` : null}
          ${mode.canPlan ? html`<${ModeChip}
            icon="clipboard-list"
            label=${t('composer.plan.toggle')}
            hint=${t('composer.plan.hint')}
            pressed=${toggles.plan}
            testId="plan-toggle"
            onToggle=${(next) => togglePlanMode(next)}
          />` : null}
          ${mode.canInterrupt ? html`<${ModeChip}
            icon="hand"
            tone="warning"
            label=${t('composer.interrupt.toggle')}
            hint=${t('composer.interrupt.hint')}
            pressed=${toggles.interrupt}
            testId="interrupt-toggle"
            onToggle=${(next) => toggleInterruptMode(next)}
          />` : null}
          ${mode.canPickModel ? html`<${ModelPicker}
            provider=${view.provider}
            model=${view.model}
            providers=${view.providers}
            defaultProvider=${view.defaultProvider}
          />` : null}
        </div>
        <span class="composer-keyhint" id=${hintId}>
          <${Kbd} keys=${['Enter']} /><span>${t('composer.key.send')}</span>
          <${Kbd} keys=${['shift', 'Enter']} /><span>${t('composer.key.newline')}</span>
        </span>
        ${action === 'stop' ? html`<button
          type="button"
          class="composer-primary is-stop"
          aria-label=${primaryLabel}
          title=${view.canStop ? primaryLabel : t('composer.action.stopUnavailable')}
          disabled=${!view.canStop || view.stopping}
          aria-busy=${view.stopping ? 'true' : undefined}
          data-testid="stop-button"
          onClick=${() => stopRun(view.sessionId)}
        >${view.stopping ? html`<${Spinner} size=${16} />` : html`<${Icon} name="square" size=${16} />`}</button>` : html`<button
          type="submit"
          class=${`composer-primary is-${action}`}
          aria-label=${primaryLabel}
          title=${primaryLabel}
          disabled=${!submittable}
          aria-busy=${view.submitting ? 'true' : undefined}
          data-testid="send-button"
          data-action=${action}
        >${view.submitting ? html`<${Spinner} size=${16} />` : html`<${Icon} name="arrow-up" size=${16} />`}</button>`}
      </div>
    </form>
  </div>`;
}
