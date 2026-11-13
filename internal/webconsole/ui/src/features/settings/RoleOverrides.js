// Role provider overrides: one collapsible row per role from
// lib/settings-model.js ROLE_PROVIDER_ROLES; empty fields inherit the
// selected provider defaults.
import { html } from '../../lib/html.js';
import { t, tx } from '../../lib/i18n.js';
import { API_PROVIDERS, apiProviderLabel, roleOverrideIsCustom, roleReasoningOptions } from '../../lib/settings-model.js';
import { Disclosure } from '../../ui/Disclosure.js';
import { Badge } from '../../ui/Badge.js';
import { Field, Select, TextInput } from './SettingsFields.js';
import { fieldDomId } from './settings-helpers.js';
import { validationText } from './rich.js';

/**
 * Props: rows, providers (names), errors (visible field errors), openRoles
 * (Set), onToggle(role, open), onChange(role, field, value), onBlur(fieldId).
 */
export function RoleOverrides({ rows, providers, errors, openRoles, onToggle, onChange, onBlur }) {
  const providerOptions = [{ value: '', label: t('settings.roleField.inheritProvider') }, ...providers.map((name) => ({ value: name, label: name, raw: true }))];
  const apiOptions = [{ value: '', label: tx(apiProviderLabel('')) }, ...API_PROVIDERS.map((value) => ({ value, label: tx(apiProviderLabel(value)) }))];
  const effortOptions = roleReasoningOptions().map((option) => ({ value: option.value, label: tx(option.label) }));
  return html`<div class="st-roles">
    ${rows.map((row) => {
      const custom = roleOverrideIsCustom(row);
      const base = `role.${row.role}`;
      const id = (field) => fieldDomId(`${base}.${field}`);
      const tokensError = errors[`${base}.max_output_tokens`];
      return html`<${Disclosure}
        key=${row.role}
        class="st-role"
        open=${openRoles.has(row.role)}
        onToggle=${(open) => onToggle(row.role, open)}
        data-testid=${`settings-role-${row.role}`}
        summary=${html`<span class="st-role-summary">
          <span class="st-role-text">
            <span class="st-role-name">${tx({ key: 'settings.role.' + row.role })}</span>
            <span class="st-role-desc">${tx({ key: 'settings.roleDescription.' + row.role })}</span>
          </span>
          <${Badge} tone=${custom ? 'brand' : 'neutral'}>${tx({ key: custom ? 'settings.roleState.custom' : 'settings.roleState.default' })}<//>
        </span>`}
      >
        <div class="st-grid st-role-grid">
          <${Field} id=${id('provider')} label=${t('settings.field.provider')}>
            <${Select} id=${id('provider')} value=${row.provider} options=${providerOptions} onChange=${(value) => onChange(row.role, 'provider', value)} testId=${`settings-role-${row.role}-provider`} />
          <//>
          <${Field} id=${id('api_provider')} label=${t('settings.field.apiProvider')}>
            <${Select} id=${id('api_provider')} value=${row.api_provider} options=${apiOptions} onChange=${(value) => onChange(row.role, 'api_provider', value)} testId=${`settings-role-${row.role}-api-provider`} />
          <//>
          <${Field} id=${id('base_url')} label=${t('settings.field.baseURL')} class="is-wide">
            <${TextInput} id=${id('base_url')} raw value=${row.base_url} placeholder=${t('settings.roleField.inheritBaseURL')} onInput=${(value) => onChange(row.role, 'base_url', value)} testId=${`settings-role-${row.role}-base-url`} />
          <//>
          <${Field} id=${id('model')} label=${t('settings.field.model')}>
            <${TextInput} id=${id('model')} raw value=${row.model} placeholder=${t('settings.roleField.inheritModel')} onInput=${(value) => onChange(row.role, 'model', value)} testId=${`settings-role-${row.role}-model`} />
          <//>
          <${Field} id=${id('reasoning_effort')} label=${t('settings.roleField.reasoningEffort')}>
            <${Select} id=${id('reasoning_effort')} value=${row.reasoning_effort} options=${effortOptions} onChange=${(value) => onChange(row.role, 'reasoning_effort', value)} testId=${`settings-role-${row.role}-reasoning`} />
          <//>
          <${Field} id=${id('max_output_tokens')} label=${tx({ key: 'settings.field.roleMaxOutputTokens' })} error=${tokensError ? validationText(tokensError) : ''}>
            <${TextInput}
              id=${id('max_output_tokens')}
              value=${row.max_output_tokens}
              inputMode="numeric"
              placeholder=${t('settings.roleField.inheritTokens')}
              error=${tokensError}
              onInput=${(value) => onChange(row.role, 'max_output_tokens', value)}
              onBlur=${() => onBlur(`${base}.max_output_tokens`)}
              testId=${`settings-role-${row.role}-max-output-tokens`}
            />
          <//>
        </div>
      <//>`;
    })}
  </div>`;
}
