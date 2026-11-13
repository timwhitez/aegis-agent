// Inline Plan Mode question card (spec/17-web-console-frontend.md §7.3):
// shown while plan_mode.status === 'awaiting_user_input'. Every question is
// a single choice among the offered options or a free-form "Other" answer;
// answers are built with session-model.js collectPlanInputAnswers() and
// submitted through session-actions answerPlanInput(). Selections are local
// UI state keyed by request id. Test ids: plan-input-card, plan-input-option,
// plan-input-other, plan-input-other-text, plan-input-submit, plan-input-cancel.
import { html, useId, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { PLAN_INPUT_OTHER_LABEL, collectPlanInputAnswers, prunePlanInputSelections } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { answerPlanInput, cancelPlan } from '../../state/session-actions.js';
import { Button } from '../../ui/Button.js';
import { Icon } from '../../ui/Icon.js';

const isOtherOption = (option) => String(option?.label || '').trim().toLowerCase() === PLAN_INPUT_OTHER_LABEL.toLowerCase();

function Question({ request, question, index, selection, otherText, onSelect, onOther }) {
  const id = useId();
  const options = (Array.isArray(question.options) ? question.options : []).filter((option) => option?.label && !isOtherOption(option));
  const name = `${id}-choice`;
  const otherSelected = Boolean(selection?.is_other);
  return html`<fieldset class="plan-question" data-question-id=${question.id}>
    <legend class="plan-question-legend">
      <span class="plan-question-index">${t('composer.planInput.questionIndex', { index: index + 1 })}</span>
      ${question.header ? html`<span class="plan-question-header" translate="no">${question.header}</span>` : null}
      <span class="plan-question-text" translate="no">${question.question}</span>
    </legend>
    <div class="plan-options">
      ${options.map((option) => {
        const checked = !otherSelected && selection?.label === option.label;
        return html`<label class=${`plan-option ${checked ? 'is-checked' : ''}`} key=${option.label}>
          <input type="radio" name=${name} checked=${checked} data-testid="plan-input-option" data-label=${option.label}
            onChange=${() => onSelect(question.id, { label: option.label, value: option.label, is_other: false })} />
          <span class="plan-option-text">
            <span class="plan-option-label" translate="no">${option.label}</span>
            ${option.description ? html`<span class="plan-option-desc" translate="no">${option.description}</span>` : null}
          </span>
        </label>`;
      })}
      <label class=${`plan-option is-other ${otherSelected ? 'is-checked' : ''}`}>
        <input type="radio" name=${name} checked=${otherSelected} data-testid="plan-input-other"
          onChange=${() => onSelect(question.id, { label: PLAN_INPUT_OTHER_LABEL, value: otherText, is_other: true })} />
        <span class="plan-option-text">
          <span class="plan-option-label">${t('composer.planInput.other')}</span>
          <span class="plan-option-desc">${t('composer.planInput.otherHint')}</span>
        </span>
      </label>
      ${otherSelected ? html`<label class="plan-other-field">
        <span class="sr-only">${t('composer.planInput.otherLabel')}</span>
        <textarea
          class="input plan-other-input"
          rows="2"
          value=${otherText}
          placeholder=${t('composer.planInput.otherPlaceholder')}
          data-testid="plan-input-other-text"
          onInput=${(event) => onOther(question.id, event.currentTarget.value)}
        ></textarea>
      </label>` : null}
    </div>
  </fieldset>`;
}

export function PlanInputCard() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    sessionId: state.session.id,
    plan: state.session.detail?.plan_mode || null,
    pendingAction: state.session.pendingAction,
  }));
  const [selections, setSelections] = useState({});
  const [otherTexts, setOtherTexts] = useState({});
  const headingId = useId();
  const request = view.plan?.status === 'awaiting_user_input' ? view.plan.pending_request : null;
  if (!request?.request_id) return null;
  const requestID = request.request_id;
  const questions = Array.isArray(request.questions) ? request.questions : [];
  const current = prunePlanInputSelections(request, selections)[requestID] || {};
  const others = otherTexts[requestID] || {};
  const answers = collectPlanInputAnswers(request, current);
  const complete = questions.length > 0 && answers.length === questions.length;
  const answered = questions.filter((question) => {
    const selection = current[question.id];
    return Boolean(selection) && (!selection.is_other || String(selection.value || '').trim() !== '');
  }).length;
  const busy = view.pendingAction === 'answer';

  const select = (questionID, value) => setSelections((prev) => ({ [requestID]: { ...(prev[requestID] || {}), [questionID]: value } }));
  const setOther = (questionID, text) => {
    setOtherTexts((prev) => ({ [requestID]: { ...(prev[requestID] || {}), [questionID]: text } }));
    select(questionID, { label: PLAN_INPUT_OTHER_LABEL, value: text, is_other: true });
  };
  const submit = (event) => {
    event.preventDefault();
    if (!complete || busy) return;
    answerPlanInput(view.sessionId, { requestID, answers });
  };

  return html`<section class="plan-input-card" data-testid="plan-input-card" data-request-id=${requestID} aria-labelledby=${headingId}>
    <form onSubmit=${submit}>
      <header class="plan-input-head">
        <p class="approval-eyebrow"><${Icon} name="message-square" size=${14} /><span>${t('composer.planInput.eyebrow')}</span></p>
        <h3 class="plan-input-title" id=${headingId} tabindex="-1" data-card-focus>${t('composer.planInput.title', { count: questions.length })}</h3>
      </header>
      ${questions.map((question, index) => html`<${Question}
        key=${question.id}
        request=${request}
        question=${question}
        index=${index}
        selection=${current[question.id]}
        otherText=${others[question.id] || ''}
        onSelect=${select}
        onOther=${setOther}
      />`)}
      <footer class="approval-actions">
        <${Button} type="submit" variant="primary" icon="check" pending=${busy} disabled=${!complete} data-testid="plan-input-submit">
          ${t('composer.planInput.submit')}
        <//>
        <span class="plan-input-progress" aria-live="polite">${t('composer.planInput.progress', { done: answered, total: questions.length })}</span>
        <span class="approval-actions-spacer"></span>
        <${Button} variant="ghost" class="is-danger-text" icon="x" disabled=${busy} pending=${view.pendingAction === 'cancel-plan'} data-testid="plan-input-cancel" onClick=${() => cancelPlan(view.sessionId)}>
          ${t('composer.planInput.cancel')}
        <//>
      </footer>
    </form>
  </section>`;
}
