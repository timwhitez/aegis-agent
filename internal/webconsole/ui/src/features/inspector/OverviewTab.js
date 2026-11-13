// Overview tab: run status, session facts and links, current turn / steer
// queue / skills, last reply and error, child budget, provider attempts and
// options, required artifacts, recovery facts and a background notification
// preview. Data: session.detail (read-only); raw values in translate="no".
import { html } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { agentLabel, maybeArray, phaseLabel, shortId, truncateText } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { setInspectorTab } from '../../state/actions.js';
import { currentDisplayStatus } from '../../state/selectors.js';
import { Badge } from '../../ui/Badge.js';
import { Button } from '../../ui/Button.js';
import { Disclosure } from '../../ui/Disclosure.js';
import { Skeleton } from '../../ui/Skeleton.js';
import {
  activeHandleView, checkpointView, parentCoordinationView, pendingSteers, providerAttemptStats,
  providerFailureClass, providerOptionRows, providerRetryCount, recentProviderAttempts, relatedSessions, requiredArtifactRows,
} from './inspector-model.js';
import {
  CopyValue, Fact, Facts, Metrics, Notice, Raw, RawChips, Section, SessionLink, StatusPill, TimeText, num, statusText, text,
} from './parts.js';
import { BudgetInspector, CheckpointCard, CoordinationCard, NotificationCard } from './cards.js';

const FAILURE_TITLES = {
  authRouting: () => t('inspector.failure.authRouting'),
  credential: () => t('inspector.failure.credential'),
  rateLimit: () => t('inspector.failure.rateLimit'),
  requestContract: () => t('inspector.failure.requestContract'),
  transport: () => t('inspector.failure.transport'),
  serverError: () => t('inspector.failure.serverError'),
};

const FAILURE_HINTS = {
  authRouting: () => t('inspector.failure.authRouting.hint'),
  credential: () => t('inspector.failure.credential.hint'),
  rateLimit: () => t('inspector.failure.rateLimit.hint'),
  requestContract: () => t('inspector.failure.requestContract.hint'),
  transport: () => t('inspector.failure.transport.hint'),
  serverError: () => t('inspector.failure.serverError.hint'),
};

const HANDLE_LABELS = {
  current_process: () => t('inspector.handle.currentProcess'),
  running_not_owned: () => t('inspector.handle.runningNotOwned'),
  settled: () => t('inspector.handle.settled'),
};

const ARTIFACT_LABELS = {
  completed: () => t('inspector.artifact.updated'),
  ready: () => t('inspector.artifact.present'),
  pending: () => t('inspector.artifact.missing'),
  failed: () => t('inspector.artifact.invalid'),
};

const OUTCOME_LABELS = {
  success: () => t('inspector.attempt.outcome.success'),
  failure: () => t('inspector.attempt.outcome.failure'),
  retry: () => t('inspector.attempt.outcome.retry'),
  auto_resume: () => t('inspector.attempt.outcome.autoResume'),
};

function optionValue(value) {
  if (value === true) return t('inspector.option.on');
  if (value === false) return t('inspector.option.off');
  return html`<${Raw} class="insp-mono">${String(value)}<//>`;
}

function LoadingState({ error }) {
  if (error) {
    return html`<${Notice} tone="danger" role="alert" title=${t('inspector.loadFailed')}>
      <p><${Raw}>${error.message}<//></p>
      ${error.detail ? html`<p class="insp-hint"><span class="insp-meta-label">${t('inspector.overview.errorDetail')}</span> <${Raw}>${error.detail}<//></p>` : null}
      ${error.action ? html`<p class="insp-hint"><span class="insp-meta-label">${t('inspector.overview.errorAction')}</span> <${Raw}>${error.action}<//></p>` : null}
    <//>`;
  }
  return html`<div class="insp-loading" aria-live="polite">
    <span class="sr-only">${t('inspector.loading')}</span>
    <${Skeleton} lines=${3} /><${Skeleton} lines=${4} />
  </div>`;
}

function StatusHero({ detail, status, generating }) {
  const state = detail.state || {};
  const handle = activeHandleView(detail);
  const running = generating || status === 'running';
  return html`<div class="insp-hero" data-testid="inspector-overview-status">
    ${running ? html`<p class="insp-hero-row" aria-live="polite">
      <span class="insp-dot tone-info is-live" aria-hidden="true"></span>
      <span class="insp-hero-phase">${state.phase ? text(phaseLabel(state.phase)) : statusText('running')}</span>
    </p>` : null}
    <p class="insp-hero-meta">
      ${state.turn ? html`<span>${t('inspector.overview.turn', { turn: num(state.turn) })}</span>` : null}
      ${state.turn && state.updated_at ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}
      ${state.updated_at ? html`<span>${t('inspector.overview.updated')} <${TimeText} at=${state.updated_at} relative /></span>` : null}
    </p>
    ${state.current_task ? html`<p class="insp-hero-task"><span class="insp-meta-label">${t('inspector.overview.currentTask')}</span> <${Raw}>${state.current_task}<//></p>` : null}
    ${handle ? html`<p class="insp-handle" data-testid="inspector-handle" data-handle-state=${handle.state}>
      <span class=${`insp-dot tone-${handle.tone}`} aria-hidden="true"></span>
      <span>${HANDLE_LABELS[handle.state] ? HANDLE_LABELS[handle.state]() : html`<${Raw}>${handle.state}<//>`}</span>
      ${handle.pid ? html`<span class="insp-sep" aria-hidden="true">·</span><span>PID <${Raw} class="insp-mono">${String(handle.pid)}<//></span>` : null}
      ${handle.startedAt && handle.state !== 'settled' ? html`<span class="insp-sep" aria-hidden="true">·</span><span>${t('inspector.handle.started')} <${TimeText} at=${handle.startedAt} /></span>` : null}
      ${handle.releasedAt && handle.state === 'settled' ? html`<span class="insp-sep" aria-hidden="true">·</span><span>${t('inspector.handle.released')} <${TimeText} at=${handle.releasedAt} /></span>` : null}
    </p>` : null}
    ${handle?.action && handle.state === 'running_not_owned' ? html`<p class="insp-hint"><${Raw}>${handle.action}<//></p>` : null}
  </div>`;
}

function ErrorNotices({ detail, refreshError }) {
  const state = detail.state || {};
  const lastError = String(state.last_error || '').trim();
  const failure = providerFailureClass(lastError);
  const retries = failure === 'transport' ? providerRetryCount(detail) : 0;
  return html`
    ${lastError ? html`<${Notice} tone="danger" testId="inspector-last-error" title=${failure ? FAILURE_TITLES[failure]() : t('inspector.overview.lastError')}>
      <p class="insp-error-text"><${Raw}>${lastError}<//></p>
      ${failure ? html`<p class="insp-hint">${FAILURE_HINTS[failure]()}${retries ? ' ' + t('inspector.failure.retries', { count: retries }) : ''}</p>` : null}
    <//>` : null}
    ${refreshError ? html`<${Notice} tone="warning" testId="inspector-refresh-error" title=${t('inspector.overview.refreshFailed')}>
      <p><${Raw}>${refreshError.message}<//></p>
      ${refreshError.detail ? html`<p class="insp-hint"><span class="insp-meta-label">${t('inspector.overview.errorDetail')}</span> <${Raw}>${refreshError.detail}<//></p>` : null}
      ${refreshError.action ? html`<p class="insp-hint"><span class="insp-meta-label">${t('inspector.overview.errorAction')}</span> <${Raw}>${refreshError.action}<//></p>` : null}
    <//>` : null}
  `;
}

function SessionFacts({ detail, mode }) {
  const meta = detail.metadata || {};
  const state = detail.state || {};
  const links = relatedSessions(detail);
  const agent = agentLabel(meta.agent_name, meta.agent_role);
  const model = [meta.provider, meta.model].filter(Boolean).join(' / ');
  const isolation = meta.isolation?.mode ? meta.isolation.mode + (meta.isolation.requested_mode && meta.isolation.requested_mode !== meta.isolation.mode ? ' (' + meta.isolation.requested_mode + ')' : '') : '';
  return html`<${Section} title=${t('inspector.overview.facts')} testId="inspector-overview-facts">
    <${Facts}>
      <${Fact} label=${t('inspector.fact.agent')}>${agent ? html`<${Raw}>${agent}<//>` : t('inspector.fact.masterSession')}<//>
      <${Fact} label=${t('inspector.fact.model')}>${model ? html`<${Raw}>${model}<//>` : null}<//>
      <${Fact} label=${t('inspector.fact.mode')}>${meta.mode ? html`<${Raw}>${meta.mode}${meta.completion_policy ? ' · ' + meta.completion_policy : ''}<//>` : null}<//>
      <${Fact} label=${t('inspector.fact.workdir')} testId="inspector-fact-workdir">${meta.workdir ? html`<${CopyValue} value=${meta.workdir} label=${t('inspector.copyPath')} />` : null}<//>
      <${Fact} label=${t('inspector.fact.requestedWorkdir')}>${meta.requested_workdir && meta.requested_workdir !== meta.workdir ? html`<${Raw} class="insp-mono">${meta.requested_workdir}<//>` : null}<//>
      <${Fact} label=${t('inspector.fact.isolation')}>${isolation ? html`<${Raw}>${isolation}<//>` : null}<//>
      <${Fact} label=${t('inspector.fact.toolProfile')}>${meta.tool_profile ? html`<${Raw}>${meta.tool_profile}<//>` : null}<//>
      <${Fact} label=${t('inspector.fact.created')}>${meta.created_at ? html`<${TimeText} at=${meta.created_at} />` : null}<//>
      <${Fact} label=${t('inspector.fact.updated')}>${state.updated_at ? html`<${TimeText} at=${state.updated_at} />` : null}<//>
      <${Fact} label=${t('inspector.fact.sessionId')} testId="inspector-fact-session-id">${meta.id ? html`<${CopyValue} value=${meta.id} label=${t('inspector.copyId')} />` : null}<//>
      <${Fact} label=${t('inspector.fact.parent')}>${links.parent ? html`<${SessionLink} id=${links.parent} mode=${mode} testId="inspector-link-parent"><span translate="no">${shortId(links.parent)}</span><//>` : null}<//>
      <${Fact} label=${t('inspector.fact.root')}>${links.root ? html`<${SessionLink} id=${links.root} mode=${mode} testId="inspector-link-root"><span translate="no">${shortId(links.root)}</span><//>` : null}<//>
      <${Fact} label=${t('inspector.fact.queueJob')}>${links.queueJob ? html`<${CopyValue} value=${links.queueJob} display=${shortId(links.queueJob)} label=${t('inspector.copyId')} />` : null}<//>
      <${Fact} label=${t('inspector.fact.pauseReason')}>${state.pause_reason ? html`<${Raw}>${state.pause_reason}<//>` : null}<//>
      <${Fact} label=${t('inspector.fact.incompleteReason')}>${state.incomplete_reason ? html`<${Raw}>${state.incomplete_reason}<//>` : null}<//>
    <//>
  <//>`;
}

function RunSection({ detail }) {
  const state = detail.state || {};
  const steers = pendingSteers(detail);
  const skills = maybeArray(state.loaded_skills);
  const pendingCount = Math.max(Number(state.pending_steer_count || 0), steers.length);
  if (!state.phase && !steers.length && !skills.length && !pendingCount) return null;
  return html`<${Section} title=${t('inspector.overview.run')} testId="inspector-overview-run">
    <${Facts}>
      <${Fact} label=${t('inspector.fact.phase')}>${state.phase ? text(phaseLabel(state.phase)) : null}<//>
      <${Fact} label=${t('inspector.fact.turn')}>${state.turn ? num(state.turn) : null}<//>
      <${Fact} label=${t('inspector.fact.steerQueue')} testId="inspector-steer-count">${pendingCount ? t('inspector.overview.steerPending', { count: pendingCount }) : null}<//>
      <${Fact} label=${t('inspector.fact.skills')} testId="inspector-loaded-skills">${skills.length ? html`<${RawChips} values=${skills} />` : null}<//>
    <//>
    ${steers.length ? html`<ul class="insp-list" role="list" data-testid="inspector-steer-queue">
      ${steers.slice(0, 4).map((item) => html`<li class="insp-list-item">
        <div class="insp-list-head">
          <${StatusPill} status=${item.status} />
          ${item.interrupt ? html`<${Badge} tone="warning">${t('inspector.overview.interrupt')}<//>` : null}
          <${TimeText} at=${item.created_at} />
        </div>
        <p class="insp-card-copy"><${Raw}>${truncateText(item.text, 220)}<//></p>
      </li>`)}
    </ul>` : null}
  <//>`;
}

function AttemptsSection({ detail }) {
  const attempts = maybeArray(detail.provider_attempts);
  const state = detail.state || {};
  const autoResumes = Number(state.provider_auto_resume_count || 0);
  const maxTokenResumes = Number(state.provider_max_tokens_resume_count || 0);
  if (!attempts.length && !autoResumes && !maxTokenResumes) return null;
  const stats = providerAttemptStats(attempts);
  const recent = recentProviderAttempts(detail, 4);
  return html`<${Section} title=${t('inspector.attempts.title')} testId="inspector-attempts">
    <${Metrics} items=${[
      { label: t('inspector.attempts.total'), value: num(stats.total), sub: t('inspector.attempts.outcomes', { success: stats.success, failure: stats.failure }), testId: 'inspector-attempts-total' },
      { label: t('inspector.attempts.recovery'), value: num(stats.retry + stats.autoResume + maxTokenResumes), sub: t('inspector.attempts.recoveryDetail', { retry: stats.retry, resume: Math.max(stats.autoResume, autoResumes) + maxTokenResumes }) },
      { label: t('inspector.attempts.cacheRead'), value: num(stats.cacheRead), sub: t('inspector.attempts.cacheCreation', { count: num(stats.cacheCreation) }) },
    ]} />
    ${recent.length ? html`<ul class="insp-list" role="list">
      ${recent.map((attempt) => html`<li class="insp-list-item" data-testid="inspector-attempt" data-outcome=${attempt.outcome}>
        <div class="insp-list-head">
          <span class="insp-list-title">${t('inspector.attempts.label', { turn: attempt.turn || '–', attempt: attempt.attempt || '–' })}</span>
          <${StatusPill} status=${attempt.outcome} label=${OUTCOME_LABELS[attempt.outcome] ? OUTCOME_LABELS[attempt.outcome]() : undefined} />
        </div>
        ${attempt.error ? html`<p class="insp-card-copy is-error"><${Raw}>${truncateText(attempt.error, 200)}<//></p>` : null}
        <p class="insp-card-meta">
          ${attempt.model ? html`<${Raw}>${attempt.model}<//>` : null}
          ${attempt.errorClass ? html`<span class="insp-sep" aria-hidden="true">·</span><${Raw} class="insp-mono">${attempt.errorClass}<//>` : null}
          ${attempt.timeoutKind ? html`<span class="insp-sep" aria-hidden="true">·</span><${Raw} class="insp-mono">${attempt.timeoutKind}<//>` : null}
          ${attempt.statusCode ? html`<span class="insp-sep" aria-hidden="true">·</span><span>HTTP <${Raw}>${String(attempt.statusCode)}<//></span>` : null}
          ${attempt.backoffMs ? html`<span class="insp-sep" aria-hidden="true">·</span><span>${t('inspector.attempts.backoff', { ms: num(attempt.backoffMs) })}</span>` : null}
          ${attempt.createdAt ? html`<span class="insp-sep" aria-hidden="true">·</span><${TimeText} at=${attempt.createdAt} />` : null}
        </p>
      </li>`)}
    </ul>` : null}
  <//>`;
}

function OptionsSection({ detail }) {
  const rows = providerOptionRows(detail.metadata?.provider_options);
  return html`<${Section} testId="inspector-provider-options">
    <${Disclosure} summary=${t('inspector.options.title')} class="insp-disclosure">
      ${rows.length ? html`<${Facts}>${rows.map((row) => html`<${Fact} label=${t(row.key)}>${optionValue(row.value)}<//>`)}<//>`
        : html`<p class="insp-muted">${t('inspector.options.defaults')}</p>`}
    <//>
  <//>`;
}

function ArtifactsSection({ detail }) {
  const rows = requiredArtifactRows(detail);
  if (!rows.length) return null;
  return html`<${Section} title=${t('inspector.artifacts.title')} testId="inspector-artifacts">
    <ul class="insp-list" role="list">${rows.map((row) => html`<li class="insp-list-item insp-list-row">
      <${Raw} class="insp-mono insp-path" title=${row.path}>${row.path}<//>
      <${StatusPill} status=${row.state} label=${ARTIFACT_LABELS[row.state]()} />
    </li>`)}</ul>
  <//>`;
}

export function OverviewTab({ mode = 'panel' }) {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    detail: state.session.detail,
    error: state.session.error,
    status: currentDisplayStatus(state),
    generating: state.session.generating,
  }));
  const detail = view.detail;
  if (!detail) return html`<${LoadingState} error=${view.error} />`;
  const meta = detail.metadata || {};
  const state = detail.state || {};
  const coordination = parentCoordinationView(detail);
  const checkpoint = checkpointView(detail);
  const notifications = maybeArray(detail.background_notifications).slice(-3).reverse();
  const notificationTotal = maybeArray(detail.background_notifications).length;
  return html`<div class="insp-tab insp-overview" data-testid="inspector-overview">
    <${StatusHero} detail=${detail} status=${view.status} generating=${view.generating} />
    <${ErrorNotices} detail=${detail} refreshError=${view.error} />
    ${state.last_assistant_excerpt ? html`<${Section} title=${t('inspector.overview.lastReply')} testId="inspector-last-reply">
      <blockquote class="insp-quote"><${Raw}>${truncateText(state.last_assistant_excerpt, 600)}<//></blockquote>
    <//>` : null}
    <${SessionFacts} detail=${detail} mode=${mode} />
    <${RunSection} detail=${detail} />
    ${meta.effective_budget ? html`<${Section} title=${t('inspector.budget.title')} testId="inspector-budget-section">
      <${BudgetInspector} budget=${meta.effective_budget} pauseReason=${state.pause_reason || ''} />
    <//>` : null}
    <${AttemptsSection} detail=${detail} />
    <${ArtifactsSection} detail=${detail} />
    ${coordination || checkpoint ? html`<${Section} title=${t('inspector.recovery.title')} testId="inspector-recovery">
      <div class="insp-stack">
        <${CoordinationCard} view=${coordination} mode=${mode} />
        <${CheckpointCard} view=${checkpoint} mode=${mode} />
      </div>
    <//>` : null}
    ${notifications.length ? html`<${Section}
      title=${t('inspector.notifications.title')}
      testId="inspector-notifications-preview"
      aside=${notificationTotal > notifications.length ? html`<${Button} size="sm" variant="ghost" onClick=${() => setInspectorTab('agents')}>${t('inspector.notifications.viewAll', { count: notificationTotal })}<//>` : null}
    >
      <div class="insp-stack">${notifications.map((item) => html`<${NotificationCard} key=${item.id} item=${item} mode=${mode} />`)}</div>
    <//>` : null}
    <${OptionsSection} detail=${detail} />
  </div>`;
}
