const settingsViewState = {
  requestSeq: 0
};

function formatSettingsDuration(seconds) {
  const value = Number(seconds || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return '';
  }
  if (value % 86400 === 0) return `${value / 86400}d`;
  if (value % 3600 === 0) return `${value / 3600}h`;
  if (value % 60 === 0) return `${value / 60}m`;
  return `${value}s`;
}

function parseSettingsDuration(rawValue, label) {
  const raw = String(rawValue || '').trim().toLowerCase();
  if (raw === '' || raw === '0') {
    return 0;
  }
  if (/^\d+$/.test(raw)) {
    return Number.parseInt(raw, 10);
  }
  const unitSeconds = { d: 86400, h: 3600, m: 60, s: 1 };
  let total = 0;
  let consumed = '';
  for (const match of raw.matchAll(/(\d+)\s*([dhms])/g)) {
    total += Number.parseInt(match[1], 10) * unitSeconds[match[2]];
    consumed += match[0].replace(/\s+/g, '');
  }
  if (total <= 0 || consumed !== raw.replace(/\s+/g, '')) {
    throw new Error(`${label} must be a duration such as 30m, 2h, 1h30m, or seconds.`);
  }
  return total;
}

async function renderSettings() {
  const requestSeq = ++settingsViewState.requestSeq;
  const container = nodes.views.settings;
  container.innerHTML = '<div class="view-loading">Loading backend settings…</div>';
  try {
    const configData = await requestJSON('/api/config');
    if (settingsViewState.requestSeq !== requestSeq) {
      return;
    }
    const providers = configData.providers || {};
    const defaultProvider = configData.default_provider || '';
	    const guardrailsMode = configData.guardrails_mode || 'yolo';
	    const maxTurnsSoft = Number(configData.max_turns_soft || 24);
	    const disableHardTurnLimit = Boolean(configData.disable_hard_turn_limit);
	    const maxTurnsHard = Number(configData.max_turns_hard || 0);
	    const childBudget = configData.child_budget || {};
	    const childBudgetMaxActiveRuntimeSec = Number(childBudget.max_active_runtime_sec || childBudget.max_wall_clock_sec || 0);
	    const childBudgetMaxElapsedSec = Number(childBudget.max_elapsed_sec || 0);
	    const childBudgetMaxTurnsPerAttempt = Number(childBudget.max_turns_per_attempt || childBudget.max_turns || 0);
	    const childBudgetDisabled = Object.prototype.hasOwnProperty.call(childBudget, 'disabled')
	      ? Boolean(childBudget.disabled)
	      : childBudgetMaxActiveRuntimeSec <= 0 && childBudgetMaxElapsedSec <= 0 && childBudgetMaxTurnsPerAttempt <= 0;
    const roleProviders = configData.role_providers || {};
    const options = Object.keys(providers).map((providerName) => `
      <option value="${escapeAttr(providerName)}" ${providerName === defaultProvider ? 'selected' : ''}>${escapeHTML(providerName)}</option>
    `).join('');
    const inheritedProviderOptions = `<option value="">Inherit default</option>${Object.keys(providers).map((providerName) => `
      <option value="${escapeAttr(providerName)}">${escapeHTML(providerName)}</option>
    `).join('')}`;
    const apiProviderOptions = `
      <option value="">Inherit adapter</option>
      <option value="openai-compatible">OpenAI-compatible Responses</option>
      <option value="anthropic-compatible">Anthropic-compatible Messages</option>
      <option value="google">Google Gemini</option>
    `;
    const roleLabels = {
      planner: 'Planner',
      generator: 'Generator',
      evaluator: 'Evaluator',
      explorer: 'Explorer'
    };
    const roleDescriptions = {
      planner: 'Decomposition, plans, and handoff artifacts.',
      generator: 'Bounded implementation and drafting slices.',
      evaluator: 'Independent review, audit, and validation passes.',
      explorer: 'Read-only repository exploration with a bounded evidence handoff.'
    };
    const roleProviderRows = ['planner', 'generator', 'evaluator', 'explorer'].map((role) => {
      const override = roleProviders[role] || {};
      const hasOverride = Boolean(override.provider || override.api_provider || override.base_url || override.model || override.reasoning_effort || Number(override.max_output_tokens || 0) > 0);
      return `
        <details class="role-provider-panel" data-role-provider="${escapeAttr(role)}" ${hasOverride ? 'open' : ''}>
          <summary class="role-provider-summary">
            <span>
              <strong>${escapeHTML(roleLabels[role])}</strong>
              <small>${escapeHTML(roleDescriptions[role])}</small>
            </span>
            <span class="role-provider-state">${hasOverride ? 'Custom' : 'Default'}</span>
          </summary>
          <div class="role-provider-grid">
            <label class="field">
              <span class="field-label">Provider Profile</span>
              <select class="settings-input" data-role-field="provider">
                ${inheritedProviderOptions}
              </select>
            </label>
            <label class="field">
              <span class="field-label">API Provider</span>
              <select class="settings-input" data-role-field="api_provider">
                ${apiProviderOptions}
              </select>
            </label>
            <label class="field">
              <span class="field-label">Base URL</span>
              <input class="settings-input" data-role-field="base_url" type="text" placeholder="Inherit provider base URL">
            </label>
            <label class="field">
              <span class="field-label">Model Name</span>
              <input class="settings-input" data-role-field="model" type="text" placeholder="Inherit provider model">
            </label>
            <label class="field">
              <span class="field-label">Reasoning Effort</span>
              <select class="settings-input" data-role-field="reasoning_effort">
                <option value="">Inherit provider effort</option>
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
                <option value="xhigh">XHigh</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">Max Output Tokens</span>
              <input class="settings-input" data-role-field="max_output_tokens" type="number" min="0" step="1" placeholder="Inherit provider limit">
            </label>
          </div>
        </details>
      `;
    }).join('');
    const maskedKey = '••••••••••••••••';
    const modeLabel = (mode) => ({
      default: 'Provider default',
      low: 'Low',
      medium: 'Medium',
      high: 'High',
      xhigh: 'XHigh',
      standard: 'Standard',
      max: 'Max',
      off: 'Off'
    }[mode] || mode);
    const apiProviderLabel = (value) => ({
      'openai-compatible': 'OpenAI-compatible Responses',
      'anthropic-compatible': 'Anthropic-compatible Messages',
      google: 'Google Gemini'
    }[value] || value || 'Provider default');

    container.innerHTML = `
      <div class="view-header">
        <h2 class="view-title">Settings</h2>
        <p class="view-subtitle">Configure runtime limits, provider defaults, local API credentials, and guardrails mode. API keys are persisted to the local env file for future restarts.</p>
      </div>
      <div class="skill-card settings-card">
        <form class="settings-form">
          <div class="field">
            <label class="field-label">Guardrails Mode</label>
            <select id="settings-guardrails" class="settings-input">
              <option value="yolo" ${guardrailsMode === 'yolo' ? 'selected' : ''}>YOLO (default)</option>
              <option value="standard" ${guardrailsMode === 'standard' ? 'selected' : ''}>Standard</option>
            </select>
            <p class="view-subtitle settings-help">
              YOLO disables non-essential runtime reminders and checks for new or resumed turns; tool safety boundaries still apply.
            </p>
          </div>
          <div class="field settings-runtime-section">
            <div class="settings-section-heading">
              <div>
                <span class="field-label">Runtime Limits</span>
                <p class="view-subtitle settings-help">
                  The global turn guard applies to every session run. Child budgets are an additional delegated-work policy and are off by default.
                </p>
              </div>
            </div>
            <div class="settings-runtime-grid">
              <section class="settings-limit-card">
                <div class="settings-limit-header">
                  <div>
                    <strong>Global turn guard</strong>
                    <small>Applies per run to master, foreground child, and background/queue child sessions.</small>
                  </div>
                  <span id="settings-global-turn-limit-state" class="settings-limit-state">${disableHardTurnLimit ? 'Off' : 'Enabled'}</span>
                </div>
                <label class="field">
                  <span class="field-label">Soft Checkpoint Turns</span>
                  <input id="settings-max-turns-soft" class="settings-input" type="number" min="1" step="1">
                </label>
                <label class="field">
                  <span class="field-label">Hard Max Turns</span>
                  <input id="settings-max-turns-hard" class="settings-input" type="number" min="1" step="1">
                </label>
                <label class="check-row">
                  <input id="settings-disable-hard-turn-limit" type="checkbox">
                  <span>Disable global hard turn limit</span>
                </label>
                <p class="view-subtitle settings-help">
                  Soft is a one-time checkpoint reminder and never stops execution. Hard is disabled by default; when enabled it fails the current run with <code>max_turns_hard_exceeded</code>.
                </p>
              </section>
              <section class="settings-limit-card settings-child-budget-card">
                <div class="settings-limit-header">
                  <div>
                    <strong>Sub-agent budget</strong>
                    <small>Only delegated child/background sessions are affected; the effective policy is snapshotted when work is created.</small>
                  </div>
                  <span id="settings-child-budget-state" class="settings-limit-state">Off</span>
                </div>
                <label class="check-row settings-budget-toggle">
                  <input id="settings-enable-child-budget" type="checkbox">
                  <span>Enable sub-agent budget</span>
                </label>
                <div class="settings-limit-fields">
                  <label class="field">
                    <span class="field-label">Active Runtime</span>
                    <input id="settings-child-budget-active-runtime" class="settings-input" type="text" placeholder="Off, or 30m / 2h">
                  </label>
                  <label class="field">
                    <span class="field-label">Absolute Elapsed Deadline</span>
                    <input id="settings-child-budget-elapsed" class="settings-input" type="text" placeholder="Off, or 2h / 1d">
                  </label>
                  <label class="field">
                    <span class="field-label">Turns per Attempt</span>
                    <input id="settings-child-budget-max-turns" class="settings-input" type="number" min="0" step="1" placeholder="No turn limit">
                  </label>
                </div>
                <p class="view-subtitle settings-help">
                  Active runtime excludes paused/offline time. Absolute elapsed time includes queueing and pauses. Changes affect newly created child/job work only; existing work keeps its durable snapshot. A parent can extend/resume or cancel/settle a budget-paused child.
                </p>
              </section>
            </div>
          </div>
          <div class="field">
            <label class="field-label">Provider Profile</label>
            <select id="settings-provider" class="settings-input">
              ${options}
            </select>
          </div>
          <div class="field">
            <label class="field-label">API Provider</label>
            <select id="settings-api-provider" class="settings-input">
              <option value="">Provider default</option>
              <option value="openai-compatible">OpenAI-compatible Responses</option>
              <option value="anthropic-compatible">Anthropic-compatible Messages</option>
              <option value="google">Google Gemini</option>
            </select>
            <p id="settings-api-provider-help" class="view-subtitle settings-help"></p>
          </div>
          <div class="field">
            <label class="field-label">Base URL</label>
            <input id="settings-baseurl" class="settings-input" type="text">
          </div>
          <div class="field">
            <label class="field-label">Model Name</label>
            <input id="settings-model" class="settings-input" type="text">
          </div>
          <div class="field">
            <label class="field-label">Context Window Tokens</label>
            <input id="settings-context-window" class="settings-input" type="number" min="0" step="1" placeholder="Auto">
            <p class="view-subtitle settings-help">0 uses the known-model table or the 200000-token default.</p>
          </div>
          <div class="field">
            <label class="field-label">Reasoning Mode</label>
            <select id="settings-reasoning-mode" class="settings-input"></select>
            <p id="settings-reasoning-help" class="view-subtitle settings-help"></p>
          </div>
          <div class="field">
            <label class="field-label">Reasoning Summary</label>
            <select id="settings-reasoning-summary" class="settings-input"></select>
            <p id="settings-reasoning-summary-help" class="view-subtitle settings-help"></p>
          </div>
          <div class="field">
            <label class="field-label">API Key</label>
            <input id="settings-apikey" class="settings-input" type="password" placeholder="Leave blank to keep existing persisted key...">
          </div>
          <div class="field role-provider-section">
            <div class="settings-section-heading">
              <div>
                <span class="field-label">Role Provider Overrides</span>
                <p class="view-subtitle settings-help">
                  Optional provider settings for planner, generator, evaluator, and explorer sessions. Blank fields inherit the selected provider defaults.
                </p>
              </div>
            </div>
            <div class="role-provider-list">
              ${roleProviderRows}
            </div>
          </div>
          <div class="settings-action-row">
            <button id="settings-test-btn" class="skill-btn settings-test-btn" type="button">
              <i data-lucide="activity"></i>
              <span>Test Settings</span>
            </button>
            <button id="settings-save-btn" class="skill-btn install settings-save-btn" type="button">Save Changes</button>
          </div>
        </form>
      </div>
    `;
    if (window.lucide && lucide.createIcons) {
      lucide.createIcons({ root: container });
    }

    const providerSelect = document.getElementById('settings-provider');
    const apiProviderSelect = document.getElementById('settings-api-provider');
    const apiProviderHelp = document.getElementById('settings-api-provider-help');
    const guardrailsSelect = document.getElementById('settings-guardrails');
    const maxTurnsSoftInput = document.getElementById('settings-max-turns-soft');
    const maxTurnsHardInput = document.getElementById('settings-max-turns-hard');
    const disableHardTurnLimitInput = document.getElementById('settings-disable-hard-turn-limit');
    const globalTurnLimitState = document.getElementById('settings-global-turn-limit-state');
    const enableChildBudgetInput = document.getElementById('settings-enable-child-budget');
    const childBudgetActiveRuntimeInput = document.getElementById('settings-child-budget-active-runtime');
    const childBudgetElapsedInput = document.getElementById('settings-child-budget-elapsed');
    const childBudgetMaxTurnsInput = document.getElementById('settings-child-budget-max-turns');
    const childBudgetState = document.getElementById('settings-child-budget-state');
    const baseURLInput = document.getElementById('settings-baseurl');
    const modelInput = document.getElementById('settings-model');
    const contextWindowInput = document.getElementById('settings-context-window');
    const reasoningModeSelect = document.getElementById('settings-reasoning-mode');
    const reasoningHelp = document.getElementById('settings-reasoning-help');
    const reasoningSummarySelect = document.getElementById('settings-reasoning-summary');
    const reasoningSummaryHelp = document.getElementById('settings-reasoning-summary-help');
    const apiKeyInput = document.getElementById('settings-apikey');
    const testButton = document.getElementById('settings-test-btn');
    const saveButton = document.getElementById('settings-save-btn');
    const roleProviderPanels = Array.from(container.querySelectorAll('[data-role-provider]'));
    const isCurrentSettingsRender = () => settingsViewState.requestSeq === requestSeq;

    const selectedAPIProvider = (provider) => apiProviderSelect.value || provider?.effective_api_provider || provider?.api_provider || '';
    const reasoningFamilyForAPIProvider = (value) => {
      switch (value) {
        case 'openai-compatible':
          return 'openai';
        case 'anthropic-compatible':
        case 'google':
          return 'thinking';
        default:
          return '';
      }
    };
    const reasoningModesForAPIProvider = (value) => {
      switch (reasoningFamilyForAPIProvider(value)) {
        case 'openai':
          return ['default', 'low', 'medium', 'high', 'xhigh'];
        case 'thinking':
          return ['default', 'standard', 'max', 'off'];
        default:
          return ['default'];
      }
    };
    const reasoningSummaryModesForAPIProvider = (value) => (
      reasoningFamilyForAPIProvider(value) === 'openai'
        ? ['default', 'auto', 'concise', 'detailed', 'off']
        : ['default']
    );
    const syncReasoningControls = (provider, preferredMode, preferredSummary) => {
      const apiProvider = selectedAPIProvider(provider);
      apiProviderHelp.textContent = `Effective adapter: ${apiProviderLabel(apiProvider)}.`;

      const modes = reasoningModesForAPIProvider(apiProvider);
      reasoningModeSelect.innerHTML = modes.map((mode) => `
        <option value="${escapeAttr(mode)}">${escapeHTML(modeLabel(mode))}</option>
      `).join('');
      reasoningModeSelect.value = modes.includes(preferredMode) ? preferredMode : 'default';
      if (modes.includes('xhigh')) {
        reasoningHelp.textContent = 'GPT-compatible providers send reasoning_effort with the selected level.';
      } else if (modes.includes('max')) {
        reasoningHelp.textContent = 'Thinking providers set include_thoughts and a budget profile.';
      } else {
        reasoningHelp.textContent = 'This provider only exposes its default reasoning behavior.';
      }

      const summaryModes = reasoningSummaryModesForAPIProvider(apiProvider);
      reasoningSummarySelect.innerHTML = summaryModes.map((mode) => `
        <option value="${escapeAttr(mode)}">${escapeHTML(modeLabel(mode))}</option>
      `).join('');
      reasoningSummarySelect.value = summaryModes.includes(preferredSummary) ? preferredSummary : 'default';
      if (summaryModes.includes('auto')) {
        reasoningSummaryHelp.textContent = 'Visible thinking requires a summary setting and an upstream response that actually returns readable summary text.';
      } else {
        reasoningSummaryHelp.textContent = 'Reasoning summaries are not exposed for this adapter.';
      }
    };

    const syncProviderFields = () => {
      const provider = providers[providerSelect.value];
      if (!provider) {
        return;
      }
      baseURLInput.value = provider.base_url || '';
      modelInput.value = provider.model || '';
      contextWindowInput.value = provider.context_window_tokens ? String(provider.context_window_tokens) : '';
      apiProviderSelect.value = provider.api_provider || '';
      apiKeyInput.value = provider.has_key ? maskedKey : '';
      apiKeyInput.dataset.originalHasKey = provider.has_key ? 'true' : 'false';
      syncReasoningControls(provider, provider.reasoning_mode || 'default', provider.reasoning_summary || 'default');
    };

    providerSelect.addEventListener('change', syncProviderFields);
    apiProviderSelect.addEventListener('change', () => {
      syncReasoningControls(providers[providerSelect.value], reasoningModeSelect.value, reasoningSummarySelect.value);
    });
    syncProviderFields();
    maxTurnsSoftInput.value = maxTurnsSoft > 0 ? String(maxTurnsSoft) : '24';
    maxTurnsHardInput.value = maxTurnsHard > 0 ? String(maxTurnsHard) : '40';
    disableHardTurnLimitInput.checked = disableHardTurnLimit;
    const syncMasterLimitControls = () => {
      const disabled = disableHardTurnLimitInput.checked;
      maxTurnsHardInput.disabled = disabled;
      globalTurnLimitState.textContent = disabled ? 'Off' : 'Enabled';
    };
    disableHardTurnLimitInput.addEventListener('change', syncMasterLimitControls);
    syncMasterLimitControls();
    enableChildBudgetInput.checked = !childBudgetDisabled;
    childBudgetActiveRuntimeInput.value = formatSettingsDuration(childBudgetMaxActiveRuntimeSec);
    childBudgetElapsedInput.value = formatSettingsDuration(childBudgetMaxElapsedSec);
    childBudgetMaxTurnsInput.value = childBudgetMaxTurnsPerAttempt > 0 ? String(childBudgetMaxTurnsPerAttempt) : '';
    const syncChildBudgetControls = () => {
      const enabled = enableChildBudgetInput.checked;
      childBudgetActiveRuntimeInput.disabled = !enabled;
      childBudgetElapsedInput.disabled = !enabled;
      childBudgetMaxTurnsInput.disabled = !enabled;
      childBudgetState.textContent = enabled ? 'Enabled' : 'Off';
    };
    enableChildBudgetInput.addEventListener('change', syncChildBudgetControls);
    syncChildBudgetControls();

    const currentAPIKeyValue = () => (
      apiKeyInput.value === maskedKey && apiKeyInput.dataset.originalHasKey === 'true' ? '' : apiKeyInput.value
    );
    const syncRoleProviderState = (panel) => {
      const hasValue = Array.from(panel.querySelectorAll('[data-role-field]')).some((node) => String(node.value || '').trim() !== '');
      const stateNode = panel.querySelector('.role-provider-state');
      if (stateNode) {
        stateNode.textContent = hasValue ? 'Custom' : 'Default';
      }
    };
    const setRoleProviderValues = () => {
      roleProviderPanels.forEach((panel) => {
        const role = panel.dataset.roleProvider;
        const override = roleProviders[role] || {};
        panel.querySelector('[data-role-field="provider"]').value = override.provider || '';
        panel.querySelector('[data-role-field="api_provider"]').value = override.api_provider || '';
        panel.querySelector('[data-role-field="base_url"]').value = override.base_url || '';
        panel.querySelector('[data-role-field="model"]').value = override.model || '';
        panel.querySelector('[data-role-field="reasoning_effort"]').value = override.reasoning_effort || '';
        panel.querySelector('[data-role-field="max_output_tokens"]').value = Number(override.max_output_tokens || 0) > 0 ? String(override.max_output_tokens) : '';
        syncRoleProviderState(panel);
        panel.querySelectorAll('[data-role-field]').forEach((node) => {
          node.addEventListener('input', () => syncRoleProviderState(panel));
          node.addEventListener('change', () => syncRoleProviderState(panel));
        });
      });
    };
    setRoleProviderValues();
    const collectRoleProviders = () => {
      const out = {};
      roleProviderPanels.forEach((panel) => {
        const role = panel.dataset.roleProvider;
        out[role] = {
          provider: panel.querySelector('[data-role-field="provider"]').value.trim(),
          api_provider: panel.querySelector('[data-role-field="api_provider"]').value.trim(),
          base_url: panel.querySelector('[data-role-field="base_url"]').value.trim(),
          model: panel.querySelector('[data-role-field="model"]').value.trim(),
          reasoning_effort: panel.querySelector('[data-role-field="reasoning_effort"]').value.trim(),
          max_output_tokens: Number.parseInt(panel.querySelector('[data-role-field="max_output_tokens"]').value || '0', 10)
        };
      });
      return out;
    };
    const currentContextWindowTokens = () => {
      const raw = contextWindowInput.value.trim();
      if (raw === '') {
        return 0;
      }
      const parsed = Number.parseInt(raw, 10);
      if (!Number.isFinite(parsed) || parsed < 0) {
        throw new Error('Context window tokens must be a non-negative integer.');
      }
      return parsed;
    };
    const parseOptionalNonNegativeInteger = (input, label) => {
      const raw = input.value.trim();
      if (raw === '') {
        return 0;
      }
      const parsed = Number.parseInt(raw, 10);
      if (!Number.isFinite(parsed) || parsed < 0) {
        throw new Error(`${label} must be a non-negative integer.`);
      }
      return parsed;
    };
    const currentChildBudget = () => {
      if (!enableChildBudgetInput.checked) {
        return {
          disabled: true,
          maxActiveRuntimeSec: 0,
          maxElapsedSec: 0,
          maxTurnsPerAttempt: 0
        };
      }
      const maxActiveRuntimeSec = parseSettingsDuration(childBudgetActiveRuntimeInput.value, 'Sub-agent active runtime');
      const maxElapsedSec = parseSettingsDuration(childBudgetElapsedInput.value, 'Sub-agent absolute elapsed deadline');
      const maxTurnsPerAttempt = parseOptionalNonNegativeInteger(childBudgetMaxTurnsInput, 'Sub-agent turns per attempt');
      if (maxActiveRuntimeSec === 0 && maxElapsedSec === 0 && maxTurnsPerAttempt === 0) {
        throw new Error('Enable at least one positive sub-agent budget limit.');
      }
      return {
        disabled: false,
        maxActiveRuntimeSec,
        maxElapsedSec,
        maxTurnsPerAttempt
      };
    };
    const buildProviderPayload = () => ({
      provider: providerSelect.value,
      apiProvider: apiProviderSelect.value,
      baseURL: baseURLInput.value,
      model: modelInput.value,
      contextWindowTokens: currentContextWindowTokens(),
      reasoningMode: reasoningModeSelect.value,
      reasoningSummary: reasoningSummarySelect.value,
      roleProviders: collectRoleProviders(),
      apiKey: currentAPIKeyValue()
    });
    const buildConfigPayload = () => ({
      guardrailsMode: guardrailsSelect.value,
      maxTurnsSoft: Number.parseInt(maxTurnsSoftInput.value || '0', 10),
      maxTurnsHard: Number.parseInt(maxTurnsHardInput.value || '0', 10),
      disableHardTurnLimit: disableHardTurnLimitInput.checked,
      childBudget: currentChildBudget(),
      ...buildProviderPayload()
    });

    testButton.addEventListener('click', async () => {
      testButton.innerText = 'Testing...';
      testButton.disabled = true;
      try {
        const result = await testConfig(buildProviderPayload());
        if (!isCurrentSettingsRender()) {
          return;
        }
        const selectedMode = modeLabel(result.reasoning_mode || reasoningModeSelect.value || 'default');
        const thinkingDetail = result.thinking_detail ? ` ${result.thinking_detail}.` : '';
        const thinkingStrategy = result.thinking_strategy ? ` Strategy: ${result.thinking_strategy}.` : '';
        showToast(`Provider test passed: ${result.provider} / ${result.model} / ${selectedMode}.${thinkingDetail}${thinkingStrategy}`, 'success');
        testButton.innerHTML = '<i data-lucide="activity"></i><span>Test Settings</span>';
        testButton.disabled = false;
        if (window.lucide && lucide.createIcons) {
          lucide.createIcons({ root: testButton });
        }
      } catch (err) {
        if (!isCurrentSettingsRender()) {
          return;
        }
        showToast(err.message || 'Provider test failed.', 'error');
        testButton.innerHTML = '<i data-lucide="activity"></i><span>Test Settings</span>';
        testButton.disabled = false;
        if (window.lucide && lucide.createIcons) {
          lucide.createIcons({ root: testButton });
        }
      }
    });

    saveButton.addEventListener('click', async () => {
      try {
        const softTurns = Number.parseInt(maxTurnsSoftInput.value, 10);
        if (!Number.isFinite(softTurns) || softTurns <= 0) {
          throw new Error('Soft checkpoint turns must be a positive integer.');
        }
        if (!disableHardTurnLimitInput.checked) {
          const parsed = Number.parseInt(maxTurnsHardInput.value, 10);
          if (!Number.isFinite(parsed) || parsed <= 0) {
            throw new Error('Hard max turns must be a positive integer, or disable the hard limit.');
          }
        }
        currentChildBudget();
        const confirmed = await confirmSettingsSave(apiKeyInput, maskedKey);
        if (!isCurrentSettingsRender()) {
          return;
        }
        if (!confirmed) {
          showToast('Settings save cancelled.', 'info');
          return;
        }
        saveButton.innerText = 'Saving...';
        saveButton.disabled = true;
        const submittedAPIKey = currentAPIKeyValue();
        await saveConfig(buildConfigPayload());
        if (!isCurrentSettingsRender()) {
          return;
        }
        showToast('Settings saved.', 'success');
        saveButton.innerText = 'Saved';
        saveButton.disabled = false;
        if (submittedAPIKey !== '') {
          apiKeyInput.value = maskedKey;
          apiKeyInput.dataset.originalHasKey = 'true';
        } else if (apiKeyInput.dataset.originalHasKey === 'true') {
          apiKeyInput.value = maskedKey;
        } else if (apiKeyInput.value !== maskedKey) {
          apiKeyInput.value = '';
          apiKeyInput.dataset.originalHasKey = 'false';
        }
        setTimeout(() => {
          if (document.body.contains(saveButton)) {
            saveButton.innerText = 'Save Changes';
          }
        }, 1500);
      } catch (err) {
        if (!isCurrentSettingsRender()) {
          return;
        }
        showToast(err.message || 'Failed to save configuration.', 'error');
        saveButton.innerText = 'Save Changes';
        saveButton.disabled = false;
      }
    });
  } catch (err) {
    if (settingsViewState.requestSeq !== requestSeq) {
      return;
    }
    const message = settingsErrorMessage(err);
    const panel = document.createElement('div');
    panel.className = 'empty-panel';
    panel.textContent = message;
    container.replaceChildren(panel);
    showToast(message, 'error');
  }
}

function settingsErrorMessage(err, fallback = 'Failed to load backend settings.') {
  return err?.message || fallback;
}

async function confirmSettingsSave(apiKeyInput, maskedKey) {
  const value = String(apiKeyInput?.value || '').trim();
  const writesAPIKey = value !== '' && value !== maskedKey;
  const message = writesAPIKey
    ? 'Save settings and write the entered API key to the local env file?'
    : 'Save settings to the local configuration files?';
  return confirmLocalAction({
    title: 'Save settings',
    message,
    confirmLabel: 'Save',
    tone: writesAPIKey ? 'danger' : 'default'
  });
}
