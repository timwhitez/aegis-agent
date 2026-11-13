// Labelled form controls for the Settings page. Every control has a <label>,
// help and error text are linked with aria-describedby, and invalid fields
// set aria-invalid.
import { html } from '../../lib/html.js';
import { Icon } from '../../ui/Icon.js';

export function describedBy(id, { help, error } = {}) {
  return [help ? id + '-help' : '', error ? id + '-error' : ''].filter(Boolean).join(' ') || undefined;
}

/** Label + control + help + error. */
export function Field({ id, label, help, error, labelExtra = null, class: className = '', children }) {
  return html`<div class=${`st-field ${className}`.trim()}>
    <div class="st-label-row">
      <label class="st-label" for=${id}>${label}</label>
      ${labelExtra}
    </div>
    ${children}
    ${help ? html`<p class="st-help" id=${id + '-help'}>${help}</p>` : null}
    ${error ? html`<p class="st-field-error" id=${id + '-error'}><${Icon} name="alert-circle" size=${14} /><span>${error}</span></p>` : null}
  </div>`;
}

/** Text input; raw=true keeps durable values out of page translation. */
export function TextInput({ id, value, onInput, onBlur, help, error, placeholder = '', disabled = false, raw = false, type = 'text', inputMode, autoComplete = 'off', testId }) {
  return html`<input
    id=${id}
    class=${`input st-input ${raw ? 'is-raw' : ''}`}
    type=${type}
    value=${value ?? ''}
    placeholder=${placeholder}
    disabled=${disabled}
    inputmode=${inputMode}
    autocomplete=${autoComplete}
    spellcheck=${false}
    translate=${raw ? false : undefined}
    aria-invalid=${error ? 'true' : undefined}
    aria-describedby=${describedBy(id, { help, error })}
    data-testid=${testId}
    onInput=${(event) => onInput(event.currentTarget.value)}
    onBlur=${onBlur}
  />`;
}

/** Native select. options: [{ value, label, raw? }] (raw labels are not translated). */
export function Select({ id, value, onChange, options = [], help, error, disabled = false, testId, onBlur }) {
  return html`<div class="st-select-wrap">
    <select
      id=${id}
      class="input st-select"
      value=${value}
      disabled=${disabled}
      aria-invalid=${error ? 'true' : undefined}
      aria-describedby=${describedBy(id, { help, error })}
      data-testid=${testId}
      onChange=${(event) => onChange(event.currentTarget.value)}
      onBlur=${onBlur}
    >
      ${options.map((option) => html`<option value=${option.value} selected=${option.value === value} translate=${option.raw ? false : undefined}>${option.label}</option>`)}
    </select>
    <${Icon} name="chevron-down" size=${14} class="st-select-chevron" />
  </div>`;
}

/** Switch-style checkbox with a visible label and optional description. */
export function Toggle({ id, checked, onChange, label, description = '', disabled = false, testId, state = null }) {
  return html`<div class="st-toggle-row">
    <input
      id=${id}
      type="checkbox"
      role="switch"
      class="st-switch"
      checked=${Boolean(checked)}
      disabled=${disabled}
      aria-describedby=${description ? id + '-desc' : undefined}
      data-testid=${testId}
      onChange=${(event) => onChange(event.currentTarget.checked)}
    />
    <div class="st-toggle-text">
      <label class="st-toggle-label" for=${id}>${label}</label>
      ${description ? html`<p class="st-help" id=${id + '-desc'}>${description}</p>` : null}
    </div>
    ${state}
  </div>`;
}
