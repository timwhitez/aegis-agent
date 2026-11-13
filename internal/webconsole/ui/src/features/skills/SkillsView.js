// PLACEHOLDER — owned by the skills feature worker.
// Contract: export function SkillsView(); route /skills; root element
// data-testid="page-skills" with the shared PageHeader. Data: listSkills / uploadSkill / installSkill / uninstallSkill.
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { EmptyState } from '../../ui/EmptyState.js';

export function SkillsView() {
  useStore((state) => state.ui.locale, Object.is);
  return html`<div class="page" data-testid="page-skills">
    <div class="page-inner">
      <${PageHeader} title=${t('shell.page.skills.title')} description=${t('shell.page.skills.description')} />
      <${EmptyState} icon="box" title=${t('shell.placeholder.title')} body=${t('shell.placeholder.body')} />
    </div>
  </div>`;
}
