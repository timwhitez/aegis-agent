// PLACEHOLDER — owned by the settings feature worker.
// Contract: export function SettingsView(); route /settings; root element
// data-testid="page-settings" with the shared PageHeader. Data: getConfig / saveConfig / testConfig with lib/settings-model.js; risky saves use confirmDialog.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function SettingsView() {
  useStore((state) => state.ui.locale, Object.is);
  return html`<div class="page" data-testid="page-settings">
    <div class="page-inner">
      <${PageHeader} title=${t('shell.page.settings.title')} description=${t('shell.page.settings.description')} />
      <${EmptyState} icon="settings" title=${t('shell.placeholder.title')} body=${t('shell.placeholder.body')} />
    </div>
  </div>`;
}
