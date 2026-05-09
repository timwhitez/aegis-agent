async function renderSettings() {
  const container = nodes.views.settings;
  container.innerHTML = '<div class="view-loading">Loading backend settings…</div>';
  try {
    const configData = await requestJSON('/api/config');
    const providers = configData.providers || {};
    const defaultProvider = configData.default_provider || '';
    const guardrailsMode = configData.guardrails_mode || 'yolo';
    const disableHardTurnLimit = Boolean(configData.disable_hard_turn_limit);
    const maxTurnsHard = Number(configData.max_turns_hard || 0);
    const options = Object.keys(providers).map((providerName) => `
      <option value="${escapeAttr(providerName)}" ${providerName === defaultProvider ? 'selected' : ''}>${escapeHTML(providerName)}</option>
    `).join('');
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
        <p class="view-subtitle">Configure provider defaults, local API credentials, and guardrails mode. API keys are persisted to the local env file for future restarts.</p>
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
              YOLO disables runtime retrieval, project-memory, and review-artifact guardrails for new or resumed turns.
            </p>
          </div>
          <div class="field">
            <label class="field-label">Hard Max Turns</label>
            <input id="settings-max-turns-hard" class="settings-input" type="number" min="1" step="1">
            <label class="check-row">
              <input id="settings-disable-hard-turn-limit" type="checkbox">
              <span>Disable hard turn limit</span>
            </label>
            <p class="view-subtitle settings-help">
              When disabled, the runtime will no longer fail a session with <code>max_turns_hard_exceeded</code>.
            </p>
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
    const maxTurnsHardInput = document.getElementById('settings-max-turns-hard');
    const disableHardTurnLimitInput = document.getElementById('settings-disable-hard-turn-limit');
    const baseURLInput = document.getElementById('settings-baseurl');
    const modelInput = document.getElementById('settings-model');
    const reasoningModeSelect = document.getElementById('settings-reasoning-mode');
    const reasoningHelp = document.getElementById('settings-reasoning-help');
    const reasoningSummarySelect = document.getElementById('settings-reasoning-summary');
    const reasoningSummaryHelp = document.getElementById('settings-reasoning-summary-help');
    const apiKeyInput = document.getElementById('settings-apikey');
    const testButton = document.getElementById('settings-test-btn');
    const saveButton = document.getElementById('settings-save-btn');

    const syncProviderFields = () => {
      const provider = providers[providerSelect.value];
      if (!provider) {
        return;
      }
      baseURLInput.value = provider.base_url || '';
      modelInput.value = provider.model || '';
      apiProviderSelect.value = provider.api_provider || '';
      apiProviderHelp.textContent = `Effective adapter: ${apiProviderLabel(provider.effective_api_provider || provider.api_provider || '')}.`;
      apiKeyInput.value = provider.has_key ? maskedKey : '';
      apiKeyInput.dataset.originalHasKey = provider.has_key ? 'true' : 'false';
      const modes = Array.isArray(provider.reasoning_modes) && provider.reasoning_modes.length > 0 ? provider.reasoning_modes : ['default'];
      reasoningModeSelect.innerHTML = modes.map((mode) => `
        <option value="${escapeAttr(mode)}" ${mode === provider.reasoning_mode ? 'selected' : ''}>${escapeHTML(modeLabel(mode))}</option>
      `).join('');
      if (!modes.includes(reasoningModeSelect.value)) {
        reasoningModeSelect.value = modes[0] || 'default';
      }
      if (modes.includes('xhigh')) {
        reasoningHelp.textContent = 'GPT-compatible providers send reasoning_effort with the selected level.';
      } else if (modes.includes('max')) {
        reasoningHelp.textContent = 'Thinking providers set include_thoughts and a budget profile.';
      } else {
        reasoningHelp.textContent = 'This provider only exposes its default reasoning behavior.';
      }
      const summaryModes = Array.isArray(provider.reasoning_summary_modes) && provider.reasoning_summary_modes.length > 0 ? provider.reasoning_summary_modes : ['default'];
      reasoningSummarySelect.innerHTML = summaryModes.map((mode) => `
        <option value="${escapeAttr(mode)}" ${mode === provider.reasoning_summary ? 'selected' : ''}>${escapeHTML(modeLabel(mode))}</option>
      `).join('');
      if (!summaryModes.includes(reasoningSummarySelect.value)) {
        reasoningSummarySelect.value = summaryModes[0] || 'default';
      }
      if (summaryModes.includes('auto')) {
        reasoningSummaryHelp.textContent = 'Visible thinking requires a summary setting and an upstream response that actually returns readable summary text.';
      } else {
        reasoningSummaryHelp.textContent = 'Reasoning summaries are not exposed for this adapter.';
      }
    };

    providerSelect.addEventListener('change', syncProviderFields);
    syncProviderFields();
    maxTurnsHardInput.value = maxTurnsHard > 0 ? String(maxTurnsHard) : '40';
    disableHardTurnLimitInput.checked = disableHardTurnLimit;
    maxTurnsHardInput.disabled = disableHardTurnLimit;
    disableHardTurnLimitInput.addEventListener('change', () => {
      maxTurnsHardInput.disabled = disableHardTurnLimitInput.checked;
    });

    const currentAPIKeyValue = () => (
      apiKeyInput.value === maskedKey && apiKeyInput.dataset.originalHasKey === 'true' ? '' : apiKeyInput.value
    );
    const buildConfigPayload = () => ({
      guardrailsMode: guardrailsSelect.value,
      maxTurnsHard: Number.parseInt(maxTurnsHardInput.value || '0', 10),
      disableHardTurnLimit: disableHardTurnLimitInput.checked,
      provider: providerSelect.value,
      apiProvider: apiProviderSelect.value,
      baseURL: baseURLInput.value,
      model: modelInput.value,
      reasoningMode: reasoningModeSelect.value,
      reasoningSummary: reasoningSummarySelect.value,
      apiKey: currentAPIKeyValue()
    });

    testButton.addEventListener('click', async () => {
      testButton.innerText = 'Testing...';
      testButton.disabled = true;
      try {
        const result = await testConfig(buildConfigPayload());
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
        showToast(err.message || 'Provider test failed.', 'error');
        testButton.innerHTML = '<i data-lucide="activity"></i><span>Test Settings</span>';
        testButton.disabled = false;
        if (window.lucide && lucide.createIcons) {
          lucide.createIcons({ root: testButton });
        }
      }
    });

    saveButton.addEventListener('click', async () => {
      saveButton.innerText = 'Saving...';
      saveButton.disabled = true;
      try {
        if (!disableHardTurnLimitInput.checked) {
          const parsed = Number.parseInt(maxTurnsHardInput.value, 10);
          if (!Number.isFinite(parsed) || parsed <= 0) {
            throw new Error('Hard max turns must be a positive integer, or disable the hard limit.');
          }
        }
        await saveConfig(buildConfigPayload());
        showToast('Settings saved.', 'success');
        saveButton.innerText = 'Saved';
        saveButton.disabled = false;
        if (apiKeyInput.value !== maskedKey) {
          apiKeyInput.value = maskedKey;
          apiKeyInput.dataset.originalHasKey = 'true';
        }
        setTimeout(() => {
          if (document.body.contains(saveButton)) {
            saveButton.innerText = 'Save Changes';
          }
        }, 1500);
      } catch (err) {
        showToast(err.message || 'Failed to save configuration.', 'error');
        saveButton.innerText = 'Save Changes';
        saveButton.disabled = false;
      }
    });
  } catch (err) {
    container.innerHTML = '<div class="empty-panel">Failed to load backend settings.</div>';
    showToast('Failed to load backend settings.', 'error');
  }
}
