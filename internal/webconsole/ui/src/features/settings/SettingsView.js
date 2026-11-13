// Settings page (route /settings, data-testid page-settings): provider
// profile, adapter family, base URL, model, context window and write-only API
// key; reasoning; guardrails and the global turn guard; sub-agent budget;
// role provider overrides; read-only browser runtime. Saving writes config and
// .env, so it needs a confirmation; "测试连接" probes the current form values
// without persisting (spec/17-web-console.md §7 Settings API). Form state is
// page-local; all payloads come from lib/settings-model.js.
import { html, useEffect, useRef, useState } from '../../lib/html.js';
import { t, tx } from '../../lib/i18n.js';
import * as api from '../../lib/api.js';
import {
  API_PROVIDERS,
  GUARDRAILS_MODES,
  MASKED_API_KEY,
  SETTINGS_SAVE_NOTICES,
  apiKeyAfterSave,
  apiProviderLabel,
  buildConfigSavePayload,
  buildConfigTestPayload,
  coerceReasoningSelection,
  effectiveAPIProvider,
  interpretProbeError,
  interpretProbeResult,
  providerFormFields,
  reasoningControls,
  settingsFormFromConfig,
  settingsSaveConfirmation,
  toggleStateLabel,
} from '../../lib/settings-model.js';
import { useStore } from '../../state/hooks.js';
import { navigate, refreshMeta, setSidebarOpen } from '../../state/actions.js';
import { pushToast, toastError } from '../../state/toasts.js';
import { confirmDialog } from '../../state/dialogs.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { Button } from '../../ui/Button.js';
import { Badge } from '../../ui/Badge.js';
import { Icon } from '../../ui/Icon.js';
import { Disclosure } from '../../ui/Disclosure.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { Field, Select, TextInput, Toggle } from './SettingsFields.js';
import { ProbeResult } from './ProbeResult.js';
import { RoleOverrides } from './RoleOverrides.js';
import {
  collectFieldErrors,
  fieldDomId,
  firstErrorField,
  formSnapshot,
  isGuardedNavigationHref,
  toSaveConfigInput,
  toTestConfigInput,
} from './settings-helpers.js';
import { ErrorLines, Raw, errorInfo, richText, validationText } from './rich.js';

function Section({ id, title, description, children, testId, flush = false }) {
  return html`<section class="st-section" aria-labelledby=${id} data-testid=${testId}>
    <header class="st-section-head">
      <h2 class="st-section-title" id=${id}>${title}</h2>
      ${description ? html`<p class="st-section-desc">${description}</p>` : null}
    </header>
    <div class=${`st-card ${flush ? 'st-card-flush' : ''}`}>${children}</div>
  </section>`;
}

function StateBadge({ enabled, testId }) {
  return html`<${Badge} tone=${enabled ? 'success' : 'neutral'} data-testid=${testId}>${tx(toggleStateLabel(enabled))}<//>`;
}

function BrowserRuntime({ browser }) {
  if (!browser) return null;
  const enabled = Boolean(browser.enabled);
  const rows = [
    ['settings.browser.mode', browser.mode === 'local' ? t('settings.browser.modeLocal') : html`<${Raw} value=${browser.mode || ''} />`],
    ['settings.browser.installRoot', browser.install_root ? html`<${Raw} value=${browser.install_root} class="st-mono" />` : t('settings.browser.notSet')],
    ['settings.browser.executable', browser.browser_executable ? html`<${Raw} value=${browser.browser_executable} class="st-mono" />` : t('settings.browser.auto')],
    ['settings.browser.timeout', Number(browser.timeout_sec) > 0 ? t('settings.browser.seconds', { count: Number(browser.timeout_sec) }) : t('settings.browser.notSet')],
  ];
  return html`<${Disclosure}
    class="st-browser"
    data-testid="settings-browser"
    summary=${html`<span class="st-role-summary">
      <span class="st-role-text">
        <span class="st-role-name">${t('settings.section.browser.title')}</span>
        <span class="st-role-desc">${t('settings.section.browser.description')}</span>
      </span>
      <${StateBadge} enabled=${enabled} />
    </span>`}
  >
    <dl class="st-facts">
      ${rows.map(([key, value]) => html`<div><dt>${t(key)}</dt><dd>${value}</dd></div>`)}
    </dl>
    <ul class="st-notes" role="list">
      <li>${t('settings.browser.hostSandbox')}</li>
      <li>${t('settings.browser.chromiumSandbox')}</li>
      <li>${t('settings.browser.images')}</li>
    </ul>
  <//>`;
}

export function SettingsView() {
  useStore((state) => state.ui.locale, Object.is);
  const [config, setConfig] = useState(null);
  const [form, setForm] = useState(null);
  const [baseline, setBaseline] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(null);
  const [touched, setTouched] = useState(() => new Set());
  const [showAllErrors, setShowAllErrors] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(null);
  const [testing, setTesting] = useState(false);
  const [probe, setProbe] = useState(null);
  const [replacingKey, setReplacingKey] = useState(false);
  const [openRoles, setOpenRoles] = useState(() => new Set());
  const mounted = useRef(true);
  const loadSeq = useRef(0);
  const testSeq = useRef(0);
  const dirtyRef = useRef(false);
  const apiKeyInputRef = useRef(null);

  const dirty = Boolean(form) && formSnapshot(form) !== baseline;
  dirtyRef.current = dirty;
  const allErrors = form ? collectFieldErrors(form) : {};
  const errors = {};
  for (const [field, error] of Object.entries(allErrors)) {
    if (showAllErrors || touched.has(field)) errors[field] = error;
  }
  const errorText = (field) => (errors[field] ? validationText(errors[field]) : '');

  useEffect(() => () => { mounted.current = false; }, []);

  function applyConfig(next) {
    const nextForm = settingsFormFromConfig(next);
    setConfig(next);
    setForm(nextForm);
    setBaseline(formSnapshot(nextForm));
    setTouched(new Set());
    setShowAllErrors(false);
    setReplacingKey(false);
    setOpenRoles(new Set(nextForm.roleProviders.filter((row) => row.custom).map((row) => row.role)));
  }

  async function load({ silent = false } = {}) {
    const seq = ++loadSeq.current;
    if (!silent) setLoading(true);
    setLoadError(null);
    try {
      const next = await api.getConfig();
      if (!mounted.current || seq !== loadSeq.current) return false;
      applyConfig(next);
      return true;
    } catch (error) {
      if (!mounted.current || seq !== loadSeq.current) return false;
      if (!silent) setLoadError(errorInfo(error, t('settings.loadFailed')));
      return false;
    } finally {
      if (mounted.current && seq === loadSeq.current) setLoading(false);
    }
  }

  useEffect(() => { load(); }, []);

  // Unsaved-changes guard: browser unload + in-app links (sidebar, history rows).
  useEffect(() => {
    if (!dirty) return undefined;
    const onBeforeUnload = (event) => {
      event.preventDefault();
      event.returnValue = '';
    };
    const onClick = (event) => {
      if (!dirtyRef.current || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const anchor = event.target?.closest?.('a[href]');
      if (!anchor || anchor.target || anchor.hasAttribute('download')) return;
      const href = anchor.getAttribute('href');
      if (!isGuardedNavigationHref(href, '/settings')) return;
      event.preventDefault();
      event.stopPropagation();
      confirmDialog({
        title: t('settings.leave.title'),
        body: t('settings.leave.body'),
        confirmLabel: t('settings.leave.confirm'),
        danger: true,
      }).then((ok) => {
        if (!ok) return;
        dirtyRef.current = false;
        setSidebarOpen(false);
        navigate(href);
      });
    };
    globalThis.addEventListener('beforeunload', onBeforeUnload);
    document.addEventListener('click', onClick, true);
    return () => {
      globalThis.removeEventListener('beforeunload', onBeforeUnload);
      document.removeEventListener('click', onClick, true);
    };
  }, [dirty]);

  // ---- form updates -------------------------------------------------------------

  const update = (patch) => setForm((current) => ({ ...current, ...patch }));
  const touch = (field) => setTouched((current) => (current.has(field) ? current : new Set([...current, field])));
  const providerConfig = config?.providers?.[form?.provider] || null;
  const adapter = form ? effectiveAPIProvider(form.apiProvider, providerConfig) : '';
  const controls = form ? reasoningControls(adapter, form.reasoningMode, form.reasoningSummary) : null;

  function changeProvider(name) {
    setForm((current) => ({ ...current, ...providerFormFields(config, name) }));
    setReplacingKey(false);
    setProbe(null);
  }

  function changeAPIProvider(value) {
    setForm((current) => {
      const selection = coerceReasoningSelection(effectiveAPIProvider(value, config?.providers?.[current.provider]), current.reasoningMode, current.reasoningSummary);
      return { ...current, apiProvider: value, reasoningMode: selection.mode, reasoningSummary: selection.summary };
    });
  }

  function updateBudget(patch) {
    setForm((current) => ({ ...current, childBudget: { ...current.childBudget, ...patch } }));
  }

  function updateRole(role, field, value) {
    setForm((current) => ({
      ...current,
      roleProviders: current.roleProviders.map((row) => (row.role === role ? { ...row, [field]: value } : row)),
    }));
  }

  function startReplaceKey() {
    setReplacingKey(true);
    update({ apiKey: '' });
    requestAnimationFrame(() => apiKeyInputRef.current?.focus());
  }

  function cancelReplaceKey() {
    setReplacingKey(false);
    update({ apiKey: MASKED_API_KEY });
  }

  function discard() {
    if (!config) return;
    applyConfig(config);
    setSaveError(null);
  }

  function focusField(field) {
    if (field.startsWith('role.')) {
      const role = field.split('.')[1];
      setOpenRoles((current) => new Set([...current, role]));
    }
    requestAnimationFrame(() => {
      const target = field === 'childBudget'
        ? document.getElementById(fieldDomId('childActiveRuntime'))
        : document.getElementById(fieldDomId(field));
      target?.focus();
      target?.scrollIntoView?.({ block: 'center', behavior: 'smooth' });
    });
  }

  // ---- actions --------------------------------------------------------------------

  async function save() {
    if (!form || saving) return;
    setShowAllErrors(true);
    const invalid = collectFieldErrors(form);
    if (Object.keys(invalid).length) {
      setSaveError({ message: t('settings.validation.summary'), local: true });
      focusField(firstErrorField(invalid));
      return;
    }
    let dto;
    try {
      dto = buildConfigSavePayload(form);
    } catch (error) {
      setSaveError({ message: validationText(error), local: true });
      return;
    }
    const confirmation = settingsSaveConfirmation(form);
    const ok = await confirmDialog({
      title: tx(confirmation.title),
      body: tx(confirmation.message),
      confirmLabel: tx(confirmation.confirm),
      danger: confirmation.tone === 'danger',
    });
    if (!mounted.current) return;
    if (!ok) {
      pushToast({ tone: SETTINGS_SAVE_NOTICES.cancelled.tone, message: tx(SETTINGS_SAVE_NOTICES.cancelled) });
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      await api.saveConfig(toSaveConfigInput(dto));
      if (!mounted.current) return;
      pushToast({ tone: SETTINGS_SAVE_NOTICES.saved.tone, message: tx(SETTINGS_SAVE_NOTICES.saved) });
      refreshMeta();
      const reloaded = await load({ silent: true });
      if (!reloaded && mounted.current) {
        const key = apiKeyAfterSave(form.apiKey, dto.api_key, form.originalHasKey);
        const savedForm = { ...form, apiKey: key.value, originalHasKey: key.originalHasKey };
        setForm(savedForm);
        setBaseline(formSnapshot(savedForm));
        setReplacingKey(false);
      }
    } catch (error) {
      if (!mounted.current) return;
      setSaveError(errorInfo(error, t('settings.save.failed')));
      toastError(error, 'settings.save.failed');
    } finally {
      if (mounted.current) setSaving(false);
    }
  }

  async function testConnection() {
    if (!form || testing) return;
    let payload;
    try {
      payload = buildConfigTestPayload(form);
    } catch {
      touch('contextWindowTokens');
      focusField('contextWindowTokens');
      return;
    }
    const seq = ++testSeq.current;
    setTesting(true);
    setProbe(null);
    try {
      const result = await api.testConfig(toTestConfigInput(payload));
      if (!mounted.current || seq !== testSeq.current) return;
      setProbe({ result: interpretProbeResult(result), raw: result });
    } catch (error) {
      if (!mounted.current || seq !== testSeq.current) return;
      setProbe({ result: interpretProbeError(error), raw: null });
    } finally {
      if (mounted.current && seq === testSeq.current) setTesting(false);
    }
  }

  // ---- render ---------------------------------------------------------------------

  const header = html`<${PageHeader} title=${t('shell.page.settings.title')} description=${t('settings.page.description')} />`;

  if (loading && !form) {
    return html`<div class="page st-page" data-testid="page-settings">
      <div class="page-inner st-inner">
        ${header}
        <div class="st-loading" aria-busy="true">
          <${Skeleton} lines=${4} />
          <${Skeleton} lines=${5} />
          <span class="sr-only" role="status">${t('settings.loading')}</span>
        </div>
      </div>
    </div>`;
  }

  if (!form) {
    return html`<div class="page st-page" data-testid="page-settings">
      <div class="page-inner st-inner">
        ${header}
        <div class="st-load-error" role="alert" data-testid="settings-load-error">
          <span class="st-load-error-icon"><${Icon} name="alert-circle" size=${18} /></span>
          <p class="st-load-error-title">${t('settings.loadFailed')}</p>
          <${ErrorLines} error=${loadError} />
          <${Button} icon="refresh" onClick=${() => load()}>${t('settings.retry')}<//>
        </div>
      </div>
    </div>`;
  }

  const providerOptions = form.providers.map((name) => ({ value: name, label: name, raw: true }));
  const apiOptions = [{ value: '', label: tx(apiProviderLabel('')) }, ...API_PROVIDERS.map((value) => ({ value, label: tx(apiProviderLabel(value)) }))];
  const modeOptions = controls.modes.map((option) => ({ value: option.value, label: tx(option.label) }));
  const summaryOptions = controls.summaries.map((option) => ({ value: option.value, label: tx(option.label) }));
  const guardOptions = GUARDRAILS_MODES.map((mode) => ({ value: mode, label: tx({ key: 'settings.guardrails.' + mode }) }));
  const budget = form.childBudget;
  const showKeyInput = !form.originalHasKey || replacingKey;

  return html`<div class="page st-page" data-testid="page-settings">
    <div class="page-inner st-inner">
      ${header}
      <form
        class="st-form"
        novalidate
        data-testid="settings-form"
        onSubmit=${(event) => { event.preventDefault(); if (dirty) save(); }}
        onKeyDown=${(event) => {
          if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 's' && !event.isComposing) {
            event.preventDefault();
            if (dirty) save();
          }
        }}
      >
        <${Section} id="st-sec-provider" title=${t('settings.section.provider.title')} description=${t('settings.section.provider.description')} testId="settings-section-provider">
          <div class="st-grid">
            <${Field} id="st-provider" label=${t('settings.field.provider')} error=${errorText('provider')}>
              <${Select} id="st-provider" value=${form.provider} options=${providerOptions} error=${errors.provider} onChange=${changeProvider} testId="settings-provider" />
            <//>
            <${Field}
              id="st-api-provider"
              label=${t('settings.field.apiProvider')}
              help=${adapter ? richText('settings.apiProvider.effective', { adapter: tx(controls.adapter.adapterLabel) }) : t('settings.apiProvider.unknown')}
            >
              <${Select} id="st-api-provider" value=${form.apiProvider} options=${apiOptions} help onChange=${changeAPIProvider} testId="settings-api-provider" />
            <//>
            <${Field} id="st-base-url" label=${t('settings.field.baseURL')} class="is-wide">
              <${TextInput} id="st-base-url" raw value=${form.baseURL} placeholder="https://" inputMode="url" onInput=${(value) => update({ baseURL: value })} testId="settings-base-url" />
            <//>
            <${Field} id="st-model" label=${t('settings.field.model')}>
              <${TextInput} id="st-model" raw value=${form.model} onInput=${(value) => update({ model: value })} testId="settings-model" />
            <//>
            <${Field} id=${fieldDomId('contextWindowTokens')} label=${t('settings.field.contextWindow')} help=${t('settings.help.contextWindow')} error=${errorText('contextWindowTokens')}>
              <${TextInput}
                id=${fieldDomId('contextWindowTokens')}
                value=${form.contextWindowTokens}
                inputMode="numeric"
                placeholder=${t('settings.placeholder.contextWindow')}
                help
                error=${errors.contextWindowTokens}
                onInput=${(value) => update({ contextWindowTokens: value })}
                onBlur=${() => touch('contextWindowTokens')}
                testId="settings-context-window"
              />
            <//>
            <div class="st-field is-wide st-key-field">
              <div class="st-label-row">
                ${showKeyInput
                  ? html`<label class="st-label" for="st-api-key">${t('settings.field.apiKey')}</label>`
                  : html`<span class="st-label" id="st-api-key-label">${t('settings.field.apiKey')}</span>`}
                <${Badge}
                  tone=${form.originalHasKey ? 'success' : 'neutral'}
                  dot
                  data-testid="settings-api-key-state"
                  data-state=${form.originalHasKey ? 'configured' : 'missing'}
                >${form.originalHasKey ? t('settings.apiKey.configured') : t('settings.apiKey.notConfigured')}<//>
              </div>
              ${showKeyInput ? html`<div class="st-key-row">
                <input
                  id="st-api-key"
                  ref=${apiKeyInputRef}
                  class="input st-input"
                  type="password"
                  value=${form.apiKey === MASKED_API_KEY ? '' : form.apiKey}
                  placeholder=${t('settings.apiKey.placeholder')}
                  autocomplete="new-password"
                  spellcheck=${false}
                  aria-describedby="st-api-key-help"
                  data-testid="settings-api-key"
                  onInput=${(event) => update({ apiKey: event.currentTarget.value })}
                />
                ${form.originalHasKey ? html`<${Button} variant="ghost" onClick=${cancelReplaceKey} data-testid="settings-api-key-keep">${t('settings.apiKey.keep')}<//>` : null}
              </div>` : html`<div class="st-key-row" role="group" aria-labelledby="st-api-key-label">
                <span class="st-key-mask" aria-hidden="true">${MASKED_API_KEY.slice(0, 12)}</span>
                <${Button} size="sm" icon="pencil" onClick=${startReplaceKey} aria-describedby="st-api-key-help" data-testid="settings-api-key-replace">${t('settings.apiKey.replace')}<//>
              </div>`}
              <p class="st-help" id="st-api-key-help">${replacingKey ? t('settings.apiKey.replaceHelp') : t('settings.apiKey.help')}</p>
            </div>
          </div>
          <div class="st-card-foot">
            <div class="st-test-row">
              <${Button} icon="sparkles" pending=${testing} onClick=${testConnection} aria-describedby="st-test-help" data-testid="settings-test">
                ${testing ? t('settings.test.testing') : t('settings.test.button')}
              <//>
              <p class="st-help" id="st-test-help">${t('settings.test.help')}</p>
            </div>
            <div class="st-probe-region" role="status" aria-live="polite" aria-label=${t('settings.probe.region')}>
              ${probe ? html`<${ProbeResult} result=${probe.result} raw=${probe.raw} onDismiss=${() => setProbe(null)} />` : null}
            </div>
          </div>
        <//>

        <${Section} id="st-sec-reasoning" title=${t('settings.section.reasoning.title')} description=${t('settings.section.reasoning.description')} testId="settings-section-reasoning">
          <div class="st-grid">
            <${Field} id="st-reasoning-mode" label=${t('settings.field.reasoningMode')} help=${tx(controls.modeHelp)}>
              <${Select} id="st-reasoning-mode" value=${controls.mode} options=${modeOptions} help disabled=${modeOptions.length < 2} onChange=${(value) => update({ reasoningMode: value })} testId="settings-reasoning-mode" />
            <//>
            <${Field} id="st-reasoning-summary" label=${t('settings.field.reasoningSummary')} help=${tx(controls.summaryHelp)}>
              <${Select} id="st-reasoning-summary" value=${controls.summary} options=${summaryOptions} help disabled=${summaryOptions.length < 2} onChange=${(value) => update({ reasoningSummary: value })} testId="settings-reasoning-summary" />
            <//>
          </div>
        <//>

        <${Section} id="st-sec-guard" title=${t('settings.section.guard.title')} description=${t('settings.section.guard.description')} testId="settings-section-guard">
          <div class="st-grid">
            <${Field} id="st-guardrails" label=${t('settings.field.guardrails')} help=${t('settings.guardrails.help')} class="is-wide">
              <${Select} id="st-guardrails" value=${form.guardrailsMode} options=${guardOptions} help onChange=${(value) => update({ guardrailsMode: value })} testId="settings-guardrails" />
            <//>
          </div>
          <div class="st-subsection">
            <div class="st-subhead">
              <h3 class="st-subtitle">${t('settings.turns.title')}</h3>
              <${StateBadge} enabled=${!form.disableHardTurnLimit} testId="settings-hard-limit-state" />
            </div>
            <p class="st-help st-subdesc">${t('settings.turns.help')}</p>
            <div class="st-grid">
              <${Field} id=${fieldDomId('maxTurnsSoft')} label=${t('settings.field.softTurns')} help=${t('settings.help.softTurns')} error=${errorText('maxTurnsSoft')}>
                <${TextInput} id=${fieldDomId('maxTurnsSoft')} value=${form.maxTurnsSoft} inputMode="numeric" help error=${errors.maxTurnsSoft}
                  onInput=${(value) => update({ maxTurnsSoft: value })} onBlur=${() => touch('maxTurnsSoft')} testId="settings-max-turns-soft" />
              <//>
              <${Field} id=${fieldDomId('maxTurnsHard')} label=${t('settings.field.hardTurns')} help=${t('settings.help.hardTurns')} error=${form.disableHardTurnLimit ? '' : errorText('maxTurnsHard')}>
                <${TextInput} id=${fieldDomId('maxTurnsHard')} value=${form.maxTurnsHard} inputMode="numeric" help disabled=${form.disableHardTurnLimit}
                  error=${form.disableHardTurnLimit ? null : errors.maxTurnsHard}
                  onInput=${(value) => update({ maxTurnsHard: value })} onBlur=${() => touch('maxTurnsHard')} testId="settings-max-turns-hard" />
              <//>
            </div>
            <${Toggle}
              id="st-disable-hard"
              checked=${form.disableHardTurnLimit}
              label=${t('settings.field.disableHardLimit')}
              description=${t('settings.help.disableHardLimit')}
              onChange=${(checked) => update({ disableHardTurnLimit: checked })}
              testId="settings-disable-hard-limit"
            />
          </div>
        <//>

        <${Section} id="st-sec-budget" title=${t('settings.section.budget.title')} description=${t('settings.section.budget.description')} testId="settings-section-budget">
          <${Toggle}
            id="st-budget-enabled"
            checked=${budget.enabled}
            label=${t('settings.budget.enable')}
            description=${t('settings.budget.note')}
            onChange=${(checked) => updateBudget({ enabled: checked })}
            testId="settings-child-budget-enabled"
            state=${html`<${StateBadge} enabled=${budget.enabled} testId="settings-child-budget-state" />`}
          />
          <div class=${`st-grid st-grid-3 ${budget.enabled ? '' : 'is-disabled'}`}>
            <${Field} id=${fieldDomId('childActiveRuntime')} label=${tx({ key: 'settings.field.childActiveRuntime' })} help=${t('settings.help.activeRuntime')} error=${errorText('childActiveRuntime')}>
              <${TextInput} id=${fieldDomId('childActiveRuntime')} value=${budget.activeRuntime} placeholder=${t('settings.placeholder.activeRuntime')} help disabled=${!budget.enabled}
                error=${errors.childActiveRuntime} onInput=${(value) => updateBudget({ activeRuntime: value })} onBlur=${() => touch('childActiveRuntime')} testId="settings-child-active-runtime" />
            <//>
            <${Field} id=${fieldDomId('childElapsed')} label=${tx({ key: 'settings.field.childElapsed' })} help=${t('settings.help.elapsed')} error=${errorText('childElapsed')}>
              <${TextInput} id=${fieldDomId('childElapsed')} value=${budget.elapsed} placeholder=${t('settings.placeholder.elapsed')} help disabled=${!budget.enabled}
                error=${errors.childElapsed} onInput=${(value) => updateBudget({ elapsed: value })} onBlur=${() => touch('childElapsed')} testId="settings-child-elapsed" />
            <//>
            <${Field} id=${fieldDomId('childMaxTurns')} label=${tx({ key: 'settings.field.childMaxTurns' })} help=${t('settings.help.maxTurns')} error=${errorText('childMaxTurns')}>
              <${TextInput} id=${fieldDomId('childMaxTurns')} value=${budget.maxTurns} inputMode="numeric" placeholder=${t('settings.placeholder.maxTurns')} help disabled=${!budget.enabled}
                error=${errors.childMaxTurns} onInput=${(value) => updateBudget({ maxTurns: value })} onBlur=${() => touch('childMaxTurns')} testId="settings-child-max-turns" />
            <//>
          </div>
          ${errors.childBudget ? html`<p class="st-field-error st-group-error" id="st-child-budget-error" data-testid="settings-child-budget-error"><${Icon} name="alert-circle" size=${14} /><span>${validationText(errors.childBudget)}</span></p>` : null}
        <//>

        <${Section} id="st-sec-roles" flush title=${t('settings.section.roles.title')} description=${t('settings.section.roles.description')} testId="settings-section-roles">
          <${RoleOverrides}
            rows=${form.roleProviders}
            providers=${form.providers}
            errors=${errors}
            openRoles=${openRoles}
            onToggle=${(role, open) => setOpenRoles((current) => {
              const next = new Set(current);
              if (open) next.add(role);
              else next.delete(role);
              return next;
            })}
            onChange=${updateRole}
            onBlur=${touch}
          />
        <//>

        ${form.browser ? html`<section class="st-section" aria-label=${t('settings.section.browser.title')}>
          <div class="st-card st-card-flush"><${BrowserRuntime} browser=${form.browser} /></div>
        </section>` : null}

        ${dirty || saving ? html`<div class="st-savebar" role="region" aria-label=${t('settings.saveBar.label')} data-testid="settings-save-bar">
          <div class="st-savebar-text">
            <span class="st-savebar-dot" aria-hidden="true"></span>
            <span>${t('settings.dirty.message')}</span>
          </div>
          ${saveError && (!saveError.local || Object.keys(allErrors).length) ? html`<div class="st-savebar-error" role="alert" data-testid="settings-save-error">
            ${saveError.local ? html`<span>${saveError.message}</span>` : html`<${ErrorLines} error=${saveError} />`}
          </div>` : null}
          <div class="st-savebar-actions">
            <${Button} variant="ghost" disabled=${saving} onClick=${discard} data-testid="settings-discard">${t('settings.dirty.discard')}<//>
            <${Button} variant="primary" type="submit" pending=${saving} data-testid="settings-save">${saving ? t('settings.dirty.saving') : t('settings.dirty.save')}<//>
          </div>
        </div>` : null}
      </form>
    </div>
  </div>`;
}
