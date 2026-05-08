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

    container.innerHTML = `
      <div class="view-header">
        <h2 class="view-title">Settings</h2>
        <p class="view-subtitle">Configure provider defaults, local API credentials, and guardrails mode. API keys are persisted to the local env file for future restarts.</p>
      </div>
      <div class="skill-card settings-card">
        <div class="settings-form">
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
            <label class="field-label">API Provider</label>
            <select id="settings-provider" class="settings-input">
              ${options}
            </select>
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
            <label class="field-label">API Key</label>
            <input id="settings-apikey" class="settings-input" type="password" placeholder="Leave blank to keep existing persisted key...">
          </div>
          <button id="settings-save-btn" class="skill-btn install settings-save-btn">Save Changes</button>
        </div>
      </div>
    `;
    if (window.lucide && lucide.createIcons) {
      lucide.createIcons({ root: container });
    }

    const providerSelect = document.getElementById('settings-provider');
    const guardrailsSelect = document.getElementById('settings-guardrails');
    const maxTurnsHardInput = document.getElementById('settings-max-turns-hard');
    const disableHardTurnLimitInput = document.getElementById('settings-disable-hard-turn-limit');
    const baseURLInput = document.getElementById('settings-baseurl');
    const modelInput = document.getElementById('settings-model');
    const apiKeyInput = document.getElementById('settings-apikey');
    const saveButton = document.getElementById('settings-save-btn');

    const syncProviderFields = () => {
      const provider = providers[providerSelect.value];
      if (!provider) {
        return;
      }
      baseURLInput.value = provider.base_url || '';
      modelInput.value = provider.model || '';
      apiKeyInput.value = provider.has_key ? '••••••••••••••••' : '';
      apiKeyInput.dataset.originalHasKey = provider.has_key ? 'true' : 'false';
    };

    providerSelect.addEventListener('change', syncProviderFields);
    syncProviderFields();
    maxTurnsHardInput.value = maxTurnsHard > 0 ? String(maxTurnsHard) : '40';
    disableHardTurnLimitInput.checked = disableHardTurnLimit;
    maxTurnsHardInput.disabled = disableHardTurnLimit;
    disableHardTurnLimitInput.addEventListener('change', () => {
      maxTurnsHardInput.disabled = disableHardTurnLimitInput.checked;
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
        await saveConfig({
          guardrailsMode: guardrailsSelect.value,
          maxTurnsHard: Number.parseInt(maxTurnsHardInput.value || '0', 10),
          disableHardTurnLimit: disableHardTurnLimitInput.checked,
          provider: providerSelect.value,
          baseURL: baseURLInput.value,
          model: modelInput.value,
          apiKey: apiKeyInput.value === '••••••••••••••••' && apiKeyInput.dataset.originalHasKey === 'true' ? '' : apiKeyInput.value
        });
        showToast('Settings saved.', 'success');
        saveButton.innerText = 'Saved';
        saveButton.disabled = false;
        if (apiKeyInput.value !== '••••••••••••••••') {
          apiKeyInput.value = '••••••••••••••••';
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
