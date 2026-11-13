// Skills page (/skills, data-testid="page-skills"): local skill catalog from
// GET /api/skills, .zip upload through the picker or drag and drop (the page
// is the drop target), uninstall of managed skills with confirmation, and an
// empty state explaining where skills come from. List data, upload progress
// and pending flags are page-local.
import { html, useEffect, useRef, useState } from '../../lib/html.js';
import { t, formatNumber } from '../../lib/i18n.js';
import * as api from '../../lib/api.js';
import { compactPath } from '../../lib/format.js';
import { useStore } from '../../state/hooks.js';
import { confirmDialog } from '../../state/dialogs.js';
import { pushToast } from '../../state/toasts.js';
import { PageHeader } from '../../ui/PageHeader.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Button } from '../../ui/Button.js';
import { IconButton } from '../../ui/IconButton.js';
import { Icon } from '../../ui/Icon.js';
import { Badge } from '../../ui/Badge.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { Spinner } from '../../ui/Spinner.js';
import {
  canInstallViaUpload,
  canUninstall,
  dragHasFiles,
  groupSkills,
  reasonKey,
  skillCounts,
  skillKind,
  skillLocation,
  validateSkillFile,
} from './skills-model.js';

const GROUP_COPY = {
  installed: { titleKey: 'skills.group.installed', hintKey: '' },
  discovered: { titleKey: 'skills.group.discovered', hintKey: 'skills.group.discoveredHint' },
};

const KIND_BADGE = {
  managed: { tone: 'success', key: 'skills.state.installed' },
  readonly: { tone: 'neutral', key: 'skills.state.readOnly' },
  discovered: { tone: 'neutral', key: 'skills.state.discovered' },
};

function errorInfo(error) {
  return { message: error?.message || '', detail: error?.detail || '', action: error?.action || '', status: error?.status || 0 };
}

export function SkillsView() {
  useStore((state) => state.ui.locale, Object.is);
  const [skills, setSkills] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [upload, setUpload] = useState({ pending: false, name: '', error: null });
  const [dragActive, setDragActive] = useState(false);
  const [pending, setPending] = useState(() => new Set());
  const inputRef = useRef(null);
  const dragDepth = useRef(0);
  const requestSeq = useRef(0);
  const uploadingRef = useRef(false);

  async function load({ silent = false } = {}) {
    const seq = ++requestSeq.current;
    if (!silent) setLoading(true);
    try {
      const data = await api.listSkills();
      if (seq !== requestSeq.current) return;
      setSkills(Array.isArray(data) ? data : []);
      setError(null);
    } catch (err) {
      if (seq !== requestSeq.current) return;
      setError(errorInfo(err));
    } finally {
      if (seq === requestSeq.current) setLoading(false);
    }
  }
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => { loadRef.current(); }, []);

  const openPicker = () => {
    if (uploadingRef.current) {
      pushToast({ tone: 'info', message: t('skills.upload.busy') });
      return;
    }
    inputRef.current?.click();
  };

  async function uploadFile(file) {
    if (uploadingRef.current) {
      pushToast({ tone: 'info', message: t('skills.upload.busy') });
      return;
    }
    const problem = validateSkillFile(file);
    if (problem) {
      setUpload({ pending: false, name: file?.name || '', error: { message: t(problem), detail: '', action: '', local: true } });
      return;
    }
    uploadingRef.current = true;
    setUpload({ pending: true, name: file.name, error: null });
    try {
      const result = await api.uploadSkill(file);
      const count = Number(result?.installed_count) || 0;
      pushToast({ tone: 'success', message: count > 1 ? t('skills.upload.successCount', { count: formatNumber(count) }) : t('skills.upload.success') });
      setUpload({ pending: false, name: '', error: null });
      await loadRef.current({ silent: true });
    } catch (err) {
      setUpload({ pending: false, name: file.name, error: errorInfo(err) });
    } finally {
      uploadingRef.current = false;
    }
  }

  const onInputChange = (event) => {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = '';
    if (file) uploadFile(file);
  };

  const onDragEnter = (event) => {
    if (!dragHasFiles(event.dataTransfer)) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragActive(true);
  };
  const onDragOver = (event) => {
    if (!dragHasFiles(event.dataTransfer)) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = upload.pending ? 'none' : 'copy';
  };
  const onDragLeave = (event) => {
    if (!dragHasFiles(event.dataTransfer)) return;
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragActive(false);
  };
  const onDrop = (event) => {
    if (!dragHasFiles(event.dataTransfer)) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragActive(false);
    const files = Array.from(event.dataTransfer?.files || []);
    if (!files.length) return;
    if (files.length > 1) pushToast({ tone: 'info', message: t('skills.upload.onlyFirst') });
    uploadFile(files[0]);
  };

  async function uninstall(skill, nameId) {
    const ok = await confirmDialog({
      title: t('skills.uninstall.title'),
      body: html`<span class="skill-confirm-target" translate="no">${skill.name || skill.id}</span><span class="skill-confirm-text">${t('skills.uninstall.body')}</span>`,
      confirmLabel: t('skills.uninstall.confirm'),
      danger: true,
    });
    if (!ok) return;
    setPending((current) => new Set(current).add(skill.id));
    try {
      await api.uninstallSkill(skill.id);
      pushToast({ tone: 'success', message: t('skills.uninstall.success') });
      await loadRef.current({ silent: true });
    } catch (err) {
      pushToast({ tone: 'danger', message: t('skills.uninstall.failed'), detail: err?.detail || err?.message || '', action: err?.action || '' });
    } finally {
      setPending((current) => {
        const next = new Set(current);
        next.delete(skill.id);
        return next;
      });
      globalThis.document?.getElementById(nameId)?.closest('.skill-card')?.querySelector('button')?.focus?.();
    }
  }

  const groups = groupSkills(skills);
  const counts = skillCounts(skills);
  const ready = Array.isArray(skills);

  return html`<div
    class=${`page page-skills ${dragActive ? 'is-drag-active' : ''}`}
    data-testid="page-skills"
    onDragEnter=${onDragEnter}
    onDragOver=${onDragOver}
    onDragLeave=${onDragLeave}
    onDrop=${onDrop}
  >
    <div class="page-inner">
      <${PageHeader}
        title=${t('shell.page.skills.title')}
        description=${t('shell.page.skills.description')}
        actions=${html`
          <${IconButton}
            icon="refresh"
            label=${t('skills.refresh')}
            variant="secondary"
            pending=${loading && ready}
            onClick=${() => loadRef.current()}
            data-testid="skills-refresh"
          />
          <${Button} variant="primary" icon="upload" pending=${upload.pending} onClick=${openPicker} data-testid="skills-upload-button">
            ${t('skills.upload.button')}
          <//>`}
      />
      <input
        ref=${inputRef}
        type="file"
        accept=".zip,application/zip,application/x-zip-compressed"
        class="sr-only"
        tabindex="-1"
        aria-hidden="true"
        data-testid="skills-upload-input"
        onChange=${onInputChange}
      />

      <div
        class=${`skills-dropzone ${dragActive ? 'is-active' : ''} ${upload.pending ? 'is-pending' : ''} ${upload.error ? 'has-error' : ''}`}
        data-testid="skills-dropzone"
      >
        <span class="skills-dropzone-icon">${upload.pending ? html`<${Spinner} size=${18} />` : html`<${Icon} name="upload" size=${18} />`}</span>
        <div class="skills-dropzone-text" aria-live="polite">
          ${upload.pending ? html`<p class="skills-dropzone-title">${t('skills.upload.pending')}</p>
            <p class="skills-dropzone-hint" translate="no">${upload.name}</p>`
            : dragActive ? html`<p class="skills-dropzone-title">${t('skills.dropzone.active')}</p>`
            : html`<p class="skills-dropzone-title">${t('skills.dropzone.title')}</p>
              <p class="skills-dropzone-hint">
                <span>${t('skills.dropzone.or')} </span>
                <button type="button" class="link-btn" onClick=${openPicker} data-testid="skills-dropzone-choose">${t('skills.dropzone.choose')}</button>
                <span> · ${t('skills.dropzone.limits')}</span>
              </p>`}
        </div>
        ${upload.error && !upload.pending ? html`<div class="skills-upload-error" role="alert" data-testid="skills-upload-error">
          <p class="skills-upload-error-title">${upload.error.local ? upload.error.message : t('skills.upload.failed')}</p>
          ${upload.name ? html`<p class="skills-upload-error-text" translate="no">${upload.name}</p>` : null}
          ${!upload.error.local && upload.error.message ? html`<p class="skills-upload-error-text" translate="no">${upload.error.message}</p>` : null}
          ${upload.error.detail ? html`<p class="skills-upload-error-text"><span>${t('skills.error.detailLabel')}</span> <span translate="no">${upload.error.detail}</span></p>` : null}
          ${upload.error.action ? html`<p class="skills-upload-error-text"><span>${t('skills.error.actionLabel')}</span> <span translate="no">${upload.error.action}</span></p>` : null}
          <${IconButton} icon="x" size="sm" label=${t('skills.upload.dismissError')} class="skills-upload-error-close" onClick=${() => setUpload({ pending: false, name: '', error: null })} />
        </div>` : null}
      </div>

      ${loading && !ready && !error ? html`<div class="skills-grid is-loading" aria-busy="true">
        <span class="sr-only" role="status">${t('skills.loading')}</span>
        ${[0, 1, 2].map((index) => html`<div class="skill-card skill-card-skeleton" key=${index}><${Skeleton} lines=${3} /></div>`)}
      </div>` : null}

      ${error && !ready ? html`<div class="page-alert" role="alert" data-testid="skills-error">
        <span class="page-alert-icon"><${Icon} name="alert-circle" size=${18} /></span>
        <div class="page-alert-body">
          <p class="page-alert-title">${t('skills.loadFailed')}</p>
          ${error.message ? html`<p class="page-alert-text" translate="no">${error.message}</p>` : null}
          ${error.detail ? html`<p class="page-alert-text"><span>${t('skills.error.detailLabel')}</span> <span translate="no">${error.detail}</span></p>` : null}
          ${error.action ? html`<p class="page-alert-text"><span>${t('skills.error.actionLabel')}</span> <span translate="no">${error.action}</span></p>` : null}
        </div>
        <${Button} size="sm" variant="secondary" icon="refresh" onClick=${() => loadRef.current()}>${t('common.retry')}<//>
      </div>` : null}

      ${ready && counts.total === 0 ? html`<${EmptyState}
        icon="box"
        title=${t('skills.empty.title')}
        body=${t('skills.empty.body')}
        class="page-empty"
        data-testid="skills-empty"
      ><${Button} variant="secondary" icon="upload" onClick=${openPicker}>${t('skills.empty.action')}<//><//>` : null}

      ${groups.map((group) => html`<section class="skills-section" key=${group.key} data-group=${group.key} aria-labelledby=${`skills-group-${group.key}`}>
        <header class="skills-section-head">
          <h2 class="skills-section-title" id=${`skills-group-${group.key}`}>
            ${t(GROUP_COPY[group.key].titleKey)}
            <span class="skills-section-count">${formatNumber(group.items.length)}</span>
          </h2>
          ${GROUP_COPY[group.key].hintKey ? html`<p class="skills-section-hint">${t(GROUP_COPY[group.key].hintKey)}</p>` : null}
        </header>
        <ul class="skills-grid" role="list">
          ${group.items.map((skill, index) => html`<li key=${skill.id || index}>
            <${SkillCard}
              skill=${skill}
              pending=${pending.has(skill.id)}
              uploadPending=${upload.pending}
              onUninstall=${uninstall}
              onUpload=${openPicker}
            />
          </li>`)}
        </ul>
      </section>`)}
    </div>
  </div>`;
}

let cardSeq = 0;

function SkillCard({ skill, pending, uploadPending, onUninstall, onUpload }) {
  const idRef = useRef('');
  if (!idRef.current) idRef.current = `skill-name-${(cardSeq += 1)}`;
  const nameId = idRef.current;
  const kind = skillKind(skill);
  const badge = KIND_BADGE[kind];
  const location = skillLocation(skill);
  const reason = String(skill.disabled_reason || '').trim();
  const knownReason = reasonKey(reason);
  const showId = skill.id && skill.name && skill.id !== skill.name;
  return html`<article
    class=${`skill-card kind-${kind} ${pending ? 'is-pending' : ''}`}
    data-testid="skill-card"
    data-skill-id=${skill.id}
    data-installed=${String(Boolean(skill.installed))}
    aria-busy=${pending ? 'true' : undefined}
  >
    <div class="skill-card-head">
      <span class="skill-icon" aria-hidden="true"><${Icon} name=${kind === 'discovered' ? 'folder' : 'box'} size=${18} /></span>
      <div class="skill-card-titles">
        <h3 class="skill-name" id=${nameId} translate="no">${skill.name || skill.id}</h3>
        ${showId ? html`<p class="skill-id" translate="no">${skill.id}</p>` : null}
      </div>
      <${Badge} tone=${badge.tone} class="skill-state" data-testid="skill-state">${t(badge.key)}<//>
    </div>
    ${skill.description ? html`<p class="skill-desc" translate="no">${skill.description}</p>` : null}
    <dl class="skill-facts">
      ${skill.author ? html`<div class="skill-fact"><dt>${t('skills.card.source')}</dt><dd translate="no" data-testid="skill-author">${skill.author}</dd></div>` : null}
      ${skill.trust ? html`<div class="skill-fact"><dt>${t('skills.card.trust')}</dt><dd translate="no">${skill.trust}</dd></div>` : null}
      ${location ? html`<div class="skill-fact"><dt>${t('skills.card.location')}</dt><dd><code translate="no" title=${location}>${compactPath(location, 44)}</code></dd></div>` : null}
    </dl>
    <div class="skill-card-foot">
      ${canUninstall(skill) ? html`<${Button}
        size="sm"
        variant="secondary"
        icon="trash"
        class="skill-uninstall-btn"
        pending=${pending}
        aria-describedby=${nameId}
        onClick=${() => onUninstall(skill, nameId)}
        data-testid="skill-uninstall"
      >${t('skills.uninstall')}<//>` : null}
      ${canInstallViaUpload(skill) ? html`<${Button} size="sm" variant="secondary" icon="upload" disabled=${uploadPending} aria-describedby=${nameId} onClick=${onUpload} data-testid="skill-install">${t('skills.install.viaUpload')}<//>` : null}
      ${!canUninstall(skill) && !canInstallViaUpload(skill) ? html`<p class="skill-note">
        ${kind === 'discovered' && !reason ? t('skills.note.discovered') : null}
        ${knownReason ? t(knownReason) : null}
        ${reason && !knownReason ? html`<span translate="no">${reason}</span>` : null}
        ${kind === 'readonly' && !reason ? t('skills.state.readOnly') : null}
      </p>` : null}
    </div>
  </article>`;
}
