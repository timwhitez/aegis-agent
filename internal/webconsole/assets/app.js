const state = {
  meta: null,
  overview: null,
  sessions: [],
  sessionDetail: null,
  workers: null,
  queueJobs: [],
  currentView: 'overview',
  selectedSessionId: '',
  sessionTab: 'timeline',
  providers: [],
  refreshing: false,
  toastCounter: 0,
  sessionFilterQuery: '',
  sessionFilterStatus: 'all',
  queueFilterQuery: '',
  queueFilterStatus: 'all',
  timelineFilterKind: 'all',
  timelineFilterQuery: '',
};

const elements = {
  topbarMeta: document.getElementById('topbar-meta'),
  overviewView: document.getElementById('overview-view'),
  queueView: document.getElementById('queue-view'),
  sessionView: document.getElementById('session-view'),
  sessionList: document.getElementById('session-list'),
  sessionCountBadge: document.getElementById('session-count-badge'),
  startCard: document.getElementById('start-card'),
  sessionActionCard: document.getElementById('session-action-card'),
  queueJobCard: document.getElementById('queue-job-card'),
  workerCard: document.getElementById('worker-card'),
  toastRoot: document.getElementById('toast-root'),
  refreshButton: document.getElementById('manual-refresh-button'),
  openStartButton: document.getElementById('open-start-button'),
};

const POLL_MS = 2000;

async function fetchJSON(path, options = {}) {
  const response = await fetch(path, {
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.error || `${response.status} ${response.statusText}`);
  }
  return payload;
}

function escapeHtml(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function formatDate(value) {
  if (!value) return 'N/A';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function shortId(value) {
  if (!value) return 'N/A';
  return value.length > 18 ? `${value.slice(0, 10)}...${value.slice(-6)}` : value;
}

function truncateText(value, limit = 140) {
  const text = String(value ?? '').trim();
  if (!text) return '';
  return text.length > limit ? `${text.slice(0, limit - 1)}...` : text;
}

function sessionRoleLabel(item) {
  const name = String(item?.agent_name || '').trim();
  const role = String(item?.agent_role || '').trim();
  if (name && role) return `${name} · ${role}`;
  return name || role || item.mode || 'session';
}

function statusClass(status) {
  return `status-badge status-${String(status || 'unknown').replaceAll(':', '_')}`;
}

function firstNonEmpty(...values) {
  for (const value of values) {
    const text = String(value ?? '').trim();
    if (text) return text;
  }
  return '';
}

function compactPath(value, tailSegments = 4) {
  const text = String(value ?? '').trim();
  if (!text) return 'N/A';
  const normalized = text.replaceAll('\\', '/');
  const parts = normalized.split('/').filter(Boolean);
  if (parts.length <= tailSegments) return normalized;
  const prefix = normalized.startsWith('/') ? '/' : '';
  return `${prefix}.../${parts.slice(-tailSegments).join('/')}`;
}

function normalizeTokenValues(values) {
  return (values || [])
    .map((value) => String(value ?? '').trim())
    .filter(Boolean);
}

function renderTokenRow(values, extraClass = '') {
  const items = normalizeTokenValues(values);
  if (!items.length) return '';
  return `
    <div class="token-row ${escapeHtml(extraClass)}">
      ${items.map((item) => `<span class="token-pill">${escapeHtml(item)}</span>`).join('')}
    </div>
  `;
}

function summarizeDataFields(data, limit = 4) {
  if (!data || typeof data !== 'object') return [];
  const summary = [];
  for (const [key, value] of Object.entries(data)) {
    if (value === null || value === undefined || value === '') continue;
    let text = '';
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
      text = String(value);
    } else if (Array.isArray(value)) {
      text = `${value.length} item${value.length === 1 ? '' : 's'}`;
    } else {
      continue;
    }
    summary.push(`${key}: ${truncateText(text, 48)}`);
    if (summary.length >= limit) break;
  }
  return summary;
}

function summarizeProviderOptions(options) {
  if (!options || typeof options !== 'object') return [];
  const chips = [];
  if (options.reasoning_effort) chips.push(`reasoning=${options.reasoning_effort}`);
  if (options.text_verbosity) chips.push(`verbosity=${options.text_verbosity}`);
  if (options.max_output_tokens) chips.push(`max_tokens=${options.max_output_tokens}`);
  if (options.thinking_budget) chips.push(`thinking=${options.thinking_budget}`);
  if (options.retry_policy?.max_attempts) chips.push(`retry=${options.retry_policy.max_attempts} attempts`);
  if (options.store !== undefined && options.store !== null) chips.push(`store=${String(Boolean(options.store))}`);
  return chips;
}

function renderMiniActions(actions) {
  const items = (actions || []).filter((item) => item && item.sessionId && item.label);
  if (!items.length) return '';
  return `
    <div class="mini-actions">
      ${items.map((item) => `
        <button class="mini-button" type="button" data-open-session-id="${escapeHtml(item.sessionId)}">
          ${escapeHtml(item.label)}
        </button>
      `).join('')}
    </div>
  `;
}

function renderJsonDetails(summary, payload) {
  if (!payload || typeof payload !== 'object' || Object.keys(payload).length === 0) return '';
  return `
    <details class="details-block">
      <summary>${escapeHtml(summary)}</summary>
      <pre class="details-code">${escapeHtml(JSON.stringify(payload, null, 2))}</pre>
    </details>
  `;
}

function normalizedQuery(value) {
  return String(value ?? '').trim().toLowerCase();
}

function matchesQuery(parts, query) {
  const needle = normalizedQuery(query);
  if (!needle) return true;
  return parts
    .map((part) => String(part ?? '').toLowerCase())
    .join(' ')
    .includes(needle);
}

function matchesStatus(value, filterValue) {
  return !filterValue || filterValue === 'all' || String(value || '') === filterValue;
}

function uniqueStatuses(items, pick) {
  return Array.from(new Set((items || []).map((item) => String(pick(item) || '').trim()).filter(Boolean))).sort();
}

function statusCounts(items, pick) {
  const counts = {};
  for (const item of items || []) {
    const key = String(pick(item) || '').trim();
    if (!key) continue;
    counts[key] = (counts[key] || 0) + 1;
  }
  return counts;
}

function filteredSessions() {
  return (state.sessions || []).filter((item) => matchesStatus(item.status, state.sessionFilterStatus)
    && matchesQuery([
      item.id,
      item.status,
      item.provider,
      item.model,
      item.phase,
      item.workdir,
      item.agent_name,
      item.agent_role,
      item.parent_session_id,
      item.queue_job_id,
    ], state.sessionFilterQuery));
}

function filteredQueueJobs() {
  return (state.queueJobs || []).filter((job) => matchesStatus(job.status, state.queueFilterStatus)
    && matchesQuery([
      job.id,
      job.status,
      job.prompt,
      job.final_text,
      job.last_error,
      job.agent_name,
      job.agent_role,
      job.provider,
      job.model,
      job.session_id,
      job.parent_session_id,
      job.requested_workdir,
      job.effective_workdir,
      ...(job.visible_paths || []),
    ], state.queueFilterQuery));
}

function filteredTimeline(detail) {
  const items = detail?.timeline || [];
  return items.filter((item) => {
    const kindMatches = state.timelineFilterKind === 'all' || item.kind === state.timelineFilterKind;
    const queryMatches = matchesQuery([
      item.kind,
      item.role,
      item.event_type,
      item.phase,
      item.message_id,
      item.event_id,
      item.text,
      JSON.stringify(item.data || {}),
    ], state.timelineFilterQuery);
    return kindMatches && queryMatches;
  });
}

function renderFilterToolbar({
  searchId,
  searchValue,
  searchPlaceholder,
  statusId,
  statusValue,
  statusOptions,
  clearTarget,
  summary,
  chipTarget = '',
  chipCounts = {},
  allCount = 0,
  activeStatusLabel = 'All statuses',
}) {
  const hasActiveFilters = normalizedQuery(searchValue) || (statusValue && statusValue !== 'all');
  return `
    <div class="filter-toolbar">
      <div class="filter-grid">
        <label class="filter-field" for="${escapeHtml(searchId)}">
          <span>Search</span>
          <input id="${escapeHtml(searchId)}" class="filter-input" type="search" value="${escapeHtml(searchValue)}" placeholder="${escapeHtml(searchPlaceholder)}" />
        </label>
        <label class="filter-field" for="${escapeHtml(statusId)}">
          <span>Status</span>
          <select id="${escapeHtml(statusId)}" class="filter-select">
            <option value="all">All statuses</option>
            ${statusOptions.map((status) => `<option value="${escapeHtml(status)}"${statusValue === status ? ' selected' : ''}>${escapeHtml(status)}</option>`).join('')}
          </select>
        </label>
      </div>
      ${statusOptions.length
        ? `
          <div class="filter-chip-row">
            <button class="filter-chip ${statusValue === 'all' ? 'is-active' : ''}" type="button" data-filter-chip-target="${escapeHtml(chipTarget)}" data-filter-chip-value="all">
              ${escapeHtml(activeStatusLabel)} · ${escapeHtml(String(allCount))}
            </button>
            ${statusOptions.map((status) => `
              <button class="filter-chip ${statusValue === status ? 'is-active' : ''}" type="button" data-filter-chip-target="${escapeHtml(chipTarget)}" data-filter-chip-value="${escapeHtml(status)}">
                ${escapeHtml(status)} · ${escapeHtml(String(chipCounts[status] || 0))}
              </button>
            `).join('')}
          </div>
        `
        : ''}
      <div class="filter-toolbar-foot">
        <span class="helper-text">${escapeHtml(summary)}</span>
        ${hasActiveFilters ? `<button class="mini-button" type="button" data-clear-filter="${escapeHtml(clearTarget)}">Clear filters</button>` : ''}
      </div>
    </div>
  `;
}

function renderTimelineToolbar(detail) {
  const items = detail?.timeline || [];
  const filtered = filteredTimeline(detail);
  const messageCount = items.filter((item) => item.kind === 'message').length;
  const eventCount = items.filter((item) => item.kind === 'event').length;
  return `
    <div class="filter-toolbar">
      <div class="filter-grid filter-grid-timeline">
        <label class="filter-field" for="timeline-filter-query">
          <span>Search</span>
          <input id="timeline-filter-query" class="filter-input" type="search" value="${escapeHtml(state.timelineFilterQuery)}" placeholder="Search text, ids, phases, or metadata." />
        </label>
        <label class="filter-field" for="timeline-filter-kind">
          <span>Kind</span>
          <select id="timeline-filter-kind" class="filter-select">
            <option value="all"${state.timelineFilterKind === 'all' ? ' selected' : ''}>All entries</option>
            <option value="message"${state.timelineFilterKind === 'message' ? ' selected' : ''}>Messages</option>
            <option value="event"${state.timelineFilterKind === 'event' ? ' selected' : ''}>Events</option>
          </select>
        </label>
      </div>
      <div class="filter-chip-row">
        <button class="filter-chip ${state.timelineFilterKind === 'all' ? 'is-active' : ''}" type="button" data-filter-chip-target="timeline-kind" data-filter-chip-value="all">All · ${escapeHtml(String(items.length))}</button>
        <button class="filter-chip ${state.timelineFilterKind === 'message' ? 'is-active' : ''}" type="button" data-filter-chip-target="timeline-kind" data-filter-chip-value="message">Messages · ${escapeHtml(String(messageCount))}</button>
        <button class="filter-chip ${state.timelineFilterKind === 'event' ? 'is-active' : ''}" type="button" data-filter-chip-target="timeline-kind" data-filter-chip-value="event">Events · ${escapeHtml(String(eventCount))}</button>
      </div>
      <div class="filter-toolbar-foot">
        <span class="helper-text">${escapeHtml(`${filtered.length} visible of ${items.length} timeline entries · ${messageCount} messages · ${eventCount} events`)}</span>
        ${(normalizedQuery(state.timelineFilterQuery) || state.timelineFilterKind !== 'all')
          ? '<button class="mini-button" type="button" data-clear-filter="timeline">Clear filters</button>'
          : ''}
      </div>
    </div>
  `;
}

function showToast(message, type = 'info') {
  const id = `toast-${++state.toastCounter}`;
  const node = document.createElement('div');
  node.className = `toast ${type === 'error' ? 'is-error' : ''}`;
  node.id = id;
  node.setAttribute('role', type === 'error' ? 'alert' : 'status');
  node.setAttribute('aria-live', type === 'error' ? 'assertive' : 'polite');
  node.textContent = message;
  elements.toastRoot.appendChild(node);
  window.setTimeout(() => {
    document.getElementById(id)?.remove();
  }, 3600);
}

function renderChrome() {
  const workerSummary = `${String(state.workers?.desired_count ?? 0)} desired / ${String(state.workers?.active_count ?? 0)} active`;
  const selectedSummary = state.selectedSessionId
    ? [
      shortId(state.selectedSessionId),
      state.sessionDetail?.state?.status || '',
      state.sessionDetail ? sessionRoleLabel(state.sessionDetail.metadata || {}) : '',
    ].filter(Boolean).join(' · ')
    : 'None';
  elements.topbarMeta.innerHTML = [
    { label: 'Workspace Root', value: state.meta?.session_root || 'Loading...' },
    { label: 'Worker Pool', value: workerSummary },
    { label: 'Default Provider', value: state.meta?.default_provider || 'Loading...' },
    { label: 'Active Context', value: selectedSummary },
  ].map((item) => `
      <div class="topbar-meta-item">
        <span class="topbar-meta-label">${escapeHtml(item.label)}</span>
        <span class="topbar-meta-value">${escapeHtml(item.value)}</span>
      </div>
    `).join('');
  elements.refreshButton.disabled = state.refreshing;
  elements.refreshButton.textContent = state.refreshing ? 'Refreshing...' : 'Refresh';
}

function setFormPending(form, pending, pendingLabel) {
  if (!form) return;
  form.dataset.pending = pending ? 'true' : 'false';
  form.setAttribute('aria-busy', pending ? 'true' : 'false');
  form.querySelectorAll('button, input, select, textarea').forEach((field) => {
    field.disabled = pending;
  });
  const submitButton = form.querySelector('button[type="submit"]');
  if (!submitButton) return;
  if (!submitButton.dataset.idleLabel) {
    submitButton.dataset.idleLabel = submitButton.textContent || '';
  }
  submitButton.textContent = pending ? pendingLabel : submitButton.dataset.idleLabel;
}

function setButtonPending(button, pending, pendingLabel) {
  if (!button) return;
  if (!button.dataset.idleLabel) {
    button.dataset.idleLabel = button.textContent || '';
  }
  button.disabled = pending;
  button.textContent = pending ? pendingLabel : button.dataset.idleLabel;
}

function setView(view) {
  state.currentView = view;
  if (view !== 'session') {
    state.sessionTab = 'timeline';
  }
  render();
}

function setSession(sessionId) {
  state.selectedSessionId = sessionId;
  state.currentView = 'session';
  render();
}

async function refreshAll() {
  if (state.refreshing) return;
  state.refreshing = true;
  renderChrome();
  try {
    const [meta, overview, sessions, workers, queueJobs] = await Promise.all([
      fetchJSON('/api/meta'),
      fetchJSON('/api/overview'),
      fetchJSON('/api/sessions?limit=80'),
      fetchJSON('/api/workers'),
      fetchJSON('/api/queue/jobs?limit=80'),
    ]);

    state.meta = meta;
    state.overview = overview;
    state.sessions = Array.isArray(sessions) ? sessions : [];
    state.workers = workers;
    state.queueJobs = Array.isArray(queueJobs) ? queueJobs : [];
    state.providers = meta.providers || [];

    if (state.selectedSessionId) {
      try {
        state.sessionDetail = await fetchJSON(`/api/sessions/${encodeURIComponent(state.selectedSessionId)}?limit=48`);
      } catch (error) {
        state.sessionDetail = null;
        showToast(error.message, 'error');
      }
    }

    render();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    state.refreshing = false;
    renderChrome();
  }
}

async function refreshSelectedSession() {
  if (!state.selectedSessionId) return;
  try {
    state.sessionDetail = await fetchJSON(`/api/sessions/${encodeURIComponent(state.selectedSessionId)}?limit=48`);
    render();
  } catch (error) {
    showToast(error.message, 'error');
  }
}

function providerOptions() {
  return (state.providers || [])
    .map((provider) => `<option value="${escapeHtml(provider.name)}">${escapeHtml(provider.name)} · ${escapeHtml(provider.model)}</option>`)
    .join('');
}

function roleOptions(selected = '') {
  return [
    { value: '', label: 'No role hint' },
    { value: 'planner', label: 'planner' },
    { value: 'generator', label: 'generator' },
    { value: 'evaluator', label: 'evaluator' },
  ].map((item) => `
      <option value="${escapeHtml(item.value)}"${selected === item.value ? ' selected' : ''}>${escapeHtml(item.label)}</option>
    `).join('');
}

function startFormTemplate() {
  return `
    <div class="section-header-copy">
      <h2>Start Session</h2>
      <p>Create a new <span class="mono">run</span> or <span class="mono">exec</span> session without leaving the browser. Use this for the main task you want the agent to own.</p>
    </div>
    <form id="start-form" class="form-grid">
      <div class="field">
        <label for="start-prompt">Prompt</label>
        <textarea id="start-prompt" name="prompt" placeholder="Audit the repo and fix the smallest safe issue first." required></textarea>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="start-provider">Provider</label>
          <select id="start-provider" name="provider">
            <option value="">Default provider</option>
            ${providerOptions()}
          </select>
        </div>
        <div class="field">
          <label for="start-model">Model override</label>
          <input id="start-model" name="model" placeholder="gpt-5.4" />
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="start-mode">Mode</label>
          <select id="start-mode" name="mode">
            <option value="run">run</option>
            <option value="exec">exec</option>
          </select>
        </div>
        <div class="field">
          <label for="start-isolation">Isolation</label>
          <select id="start-isolation" name="isolation_mode">
            <option value="">Default</option>
            <option value="off">off</option>
            <option value="auto">auto</option>
            <option value="copy">copy</option>
            <option value="git">git</option>
          </select>
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="start-agent-role">Agent Role</label>
          <select id="start-agent-role" name="agent_role">
            ${roleOptions()}
          </select>
        </div>
        <div class="field">
          <label for="start-agent-name">Agent Name</label>
          <input id="start-agent-name" name="agent_name" placeholder="reviewer" />
        </div>
      </div>
      <div class="field">
        <label for="start-workdir">Workdir</label>
        <input id="start-workdir" name="workdir" placeholder="Leave empty to use the current workspace." />
      </div>
      <div class="field">
        <label for="start-system">System override</label>
        <textarea id="start-system" name="system" placeholder="Optional system override for this session."></textarea>
      </div>
      <button class="button button-primary" type="submit">Start Session</button>
    </form>
  `;
}

function renderOverview() {
  const overview = state.overview || {
    session_counters: {},
    queue_counters: {},
    recent_sessions: [],
    recent_jobs: [],
    recent_failures: [],
    feed: [],
    workers: { desired_count: 0, active_count: 0 },
  };
  const sessionCounters = overview.session_counters || {};
  const queueCounters = overview.queue_counters || {};
  const running = sessionCounters.running || 0;
  const awaiting = sessionCounters.awaiting_input || 0;
  const queued = queueCounters.queued || 0;
  const processing = queueCounters.running || 0;

  elements.overviewView.innerHTML = `
    <div class="hero-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <p class="eyebrow">Overview</p>
            <h2>Operational control without leaving the durable session model.</h2>
            <p>Use <span class="mono">steer</span> while a session is still running, and switch to <span class="mono">continue</span> once the state is paused, failed, or awaiting input.</p>
          </div>
          <span class="${statusClass('running')}">CLI-first runtime</span>
        </div>
        <div class="detail-pairs">
          <div class="detail-pair">
            <strong>Session Root</strong>
            <span class="mono">${escapeHtml(state.meta?.session_root || 'N/A')}</span>
          </div>
          <div class="detail-pair">
            <strong>Default Provider</strong>
            <span class="mono">${escapeHtml(state.meta?.default_provider || 'N/A')}</span>
          </div>
          <div class="detail-pair">
            <strong>Queue Workers</strong>
            <span>${escapeHtml(String(state.workers?.desired_count ?? 0))} desired / ${escapeHtml(String(state.workers?.active_count ?? 0))} active</span>
          </div>
          <div class="detail-pair">
            <strong>Parallel Queue Execution</strong>
            <span>Submit background jobs, scale workers, and keep the queue durable instead of inventing browser-only state.</span>
          </div>
        </div>
        ${renderTokenRow((state.meta?.capabilities || []).map((capability) => capability.replaceAll('_', ' ')), 'capability-row')}
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <p class="eyebrow">Control Map</p>
            <h2>Start on the right rail, inspect in the center, keep the CLI contract intact.</h2>
            <p>The browser shell stays subordinate to the same local files, runtime handles, and queue workers that back the CLI.</p>
          </div>
        </div>
        <div class="timeline-list">
          <div class="timeline-item">
            <div class="timeline-header">
              <span class="${statusClass('running')}">1. Start</span>
              <span class="muted">New work</span>
            </div>
            <div class="timeline-text">Launch a main session with the task prompt, provider choice, and optional isolation mode.</div>
          </div>
          <div class="timeline-item">
            <div class="timeline-header">
              <span class="${statusClass('awaiting_input')}">2. Steer / Continue</span>
              <span class="muted">Mid-run control</span>
            </div>
            <div class="timeline-text">Use steer for running sessions and continue for sessions that are waiting on you.</div>
          </div>
          <div class="timeline-item">
            <div class="timeline-header">
              <span class="${statusClass('queued')}">3. Queue</span>
              <span class="muted">Parallel backlog</span>
            </div>
            <div class="timeline-text">Push child work or unattended jobs into the background queue and scale workers when throughput matters.</div>
          </div>
        </div>
        <div class="context-panel">
          <div class="context-panel-head">
            <strong>Current Focus</strong>
            <span class="muted">${state.sessionDetail ? 'Selection-aware shell' : 'Choose a session to inspect details'}</span>
          </div>
          ${state.sessionDetail ? `
            <div class="context-grid">
              <div class="context-stat">
                <span class="context-label">Session</span>
                <span class="mono">${escapeHtml(shortId(state.sessionDetail.metadata?.id || state.selectedSessionId))}</span>
              </div>
              <div class="context-stat">
                <span class="context-label">Status</span>
                <span>${escapeHtml(state.sessionDetail.state?.status || 'unknown')}</span>
              </div>
              <div class="context-stat">
                <span class="context-label">Phase</span>
                <span>${escapeHtml(state.sessionDetail.state?.phase || 'no-phase')}</span>
              </div>
              <div class="context-stat">
                <span class="context-label">Workdir</span>
                <span title="${escapeHtml(state.sessionDetail.metadata?.workdir || '')}">${escapeHtml(compactPath(state.sessionDetail.metadata?.workdir || ''))}</span>
              </div>
            </div>
          ` : `
            <div class="timeline-text">The left rail remains the fastest way to jump between durable sessions while keeping the same runtime contract underneath.</div>
          `}
        </div>
      </section>
    </div>

    <div class="stats-grid">
      ${renderStatCard('Running Sessions', running, 'Sessions currently executing in the runtime')}
      ${renderStatCard('Awaiting Input', awaiting, 'Sessions paused at a durable handoff boundary')}
      ${renderStatCard('Queued Jobs', queued, 'Background jobs waiting for worker capacity')}
      ${renderStatCard('Running Jobs', processing, 'Queue jobs actively being consumed')}
    </div>

    <div class="split-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Recent Sessions</h2>
            <p>Latest session summaries across the local root. Choose one in the left rail to open the full detail view.</p>
          </div>
          <span class="badge">${escapeHtml(String((overview.recent_sessions || []).length))}</span>
        </div>
        <div class="table-list">
          ${(overview.recent_sessions || []).length
            ? (overview.recent_sessions || []).slice(0, 6).map(renderSessionSnapshot).join('')
            : renderEmpty('No sessions have been created yet.')}
        </div>
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Recent Queue Jobs</h2>
            <p>Fresh background executions with role, provider, and completion clues surfaced together.</p>
          </div>
          <span class="badge">${escapeHtml(String((overview.recent_jobs || []).length))}</span>
        </div>
        <div class="table-list">
          ${(overview.recent_jobs || []).length
            ? (overview.recent_jobs || []).slice(0, 6).map(renderOverviewJob).join('')
            : renderEmpty('No queue jobs have been submitted yet.')}
        </div>
      </section>
    </div>

    <div class="split-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Recent Activity</h2>
            <p>Mixed feed of session summaries and queue transitions to help you orient quickly.</p>
          </div>
        </div>
        <div class="feed-list">
          ${(overview.feed || []).length
            ? (overview.feed || []).map(renderFeedItem).join('')
            : renderEmpty('No recent activity yet.')}
        </div>
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Recent Failures</h2>
            <p>Failures stay visible so you can resume or inspect them instead of losing context.</p>
          </div>
        </div>
        <div class="failure-list">
          ${(overview.recent_failures || []).length
            ? (overview.recent_failures || []).map(renderFailureItem).join('')
            : renderEmpty('No recent failures.')}
        </div>
      </section>
    </div>
  `;
}

function renderStatCard(label, value, subvalue) {
  return `
    <section class="stat-card">
      <div class="stat-label">${escapeHtml(label)}</div>
      <div class="stat-value">${escapeHtml(String(value))}</div>
      <div class="stat-subvalue">${escapeHtml(subvalue)}</div>
    </section>
  `;
}

function renderSessionSnapshot(item) {
  return `
    <button class="table-item table-item-actionable" type="button" data-open-session-id="${escapeHtml(item.id)}">
      <div class="table-headline">
        <div>
          <div class="mono">${escapeHtml(shortId(item.id))}</div>
          <div class="table-meta">${escapeHtml(item.provider)} · ${escapeHtml(item.model)}</div>
        </div>
        <span class="${statusClass(item.status)}">${escapeHtml(item.status)}</span>
      </div>
      <div class="detail-pairs">
        <div class="detail-pair">
          <strong>Role / Agent</strong>
          <span>${escapeHtml(sessionRoleLabel(item))}</span>
        </div>
        <div class="detail-pair">
          <strong>Phase</strong>
          <span>${escapeHtml(item.phase || 'N/A')}</span>
        </div>
      </div>
      <div class="table-footer">
        <span class="table-meta" title="${escapeHtml(item.workdir || '')}">${escapeHtml(compactPath(item.workdir || ''))}</span>
        <span class="table-link">Open session</span>
      </div>
    </button>
  `;
}

function renderOverviewJob(job) {
  return `
    <article class="table-item">
      <div class="table-headline">
        <div>
          <div class="mono">${escapeHtml(shortId(job.id))}</div>
          <div class="table-meta">${escapeHtml(job.agent_name || 'default-agent')} · ${escapeHtml(job.agent_role || 'unspecified-role')}</div>
        </div>
        <span class="${statusClass(job.status)}">${escapeHtml(job.status)}</span>
      </div>
      <div class="feed-text">${escapeHtml(truncateText(job.final_text || job.prompt || 'No summary', 160))}</div>
      <div class="table-meta">${escapeHtml(job.provider || 'default')} · ${escapeHtml(job.model || 'default')} · ${escapeHtml(formatDate(job.updated_at))}</div>
      ${renderMiniActions([
        { sessionId: job.session_id, label: 'Open child session' },
        { sessionId: job.parent_session_id, label: 'Open parent' },
      ])}
    </article>
  `;
}

function renderFeedItem(item) {
  return `
    <article class="feed-item">
      <div class="feed-header">
        <span class="${statusClass(item.event_type || item.kind)}">${escapeHtml(item.kind)}</span>
        <span class="muted">${escapeHtml(formatDate(item.time))}</span>
      </div>
      <div class="feed-text mono">${escapeHtml(item.text || item.event_type || 'event')}</div>
      ${renderTokenRow(summarizeDataFields(item.data), 'metadata-row')}
      <div class="table-meta">${escapeHtml(truncateText(JSON.stringify(item.data || {}, null, 2), 240) || 'No extra metadata')}</div>
    </article>
  `;
}

function renderFailureItem(item) {
  const openActions = item.kind === 'session'
    ? [{ sessionId: item.id, label: 'Open failed session' }]
    : [];
  return `
    <article class="failure-item">
      <div class="feed-header">
        <span class="${statusClass('failed')}">${escapeHtml(item.kind)}</span>
        <span class="muted">${escapeHtml(formatDate(item.updated_at))}</span>
      </div>
      <div class="mono">${escapeHtml(item.id)}</div>
      <div class="feed-text">${escapeHtml(item.message || 'No error summary')}</div>
      ${renderMiniActions(openActions)}
    </article>
  `;
}

function renderQueueView() {
  const visibleJobs = filteredQueueJobs();
  const statusOptions = uniqueStatuses(state.queueJobs, (job) => job.status);
  const counts = statusCounts(state.queueJobs, (job) => job.status);
  elements.queueView.innerHTML = `
    <div class="stats-grid">
      ${renderStatCard('Queued', state.overview?.queue_counters?.queued || 0, 'Jobs waiting for a worker')}
      ${renderStatCard('Running', state.overview?.queue_counters?.running || 0, 'Jobs in the worker pipeline')}
      ${renderStatCard('Completed', state.overview?.queue_counters?.completed || 0, 'Jobs finished successfully')}
      ${renderStatCard('Failed', state.overview?.queue_counters?.failed || 0, 'Jobs that need inspection or rerun')}
    </div>

    <div class="hero-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <p class="eyebrow">Queue Overview</p>
            <h2>Background jobs stay durable, role-aware, and cross-linked to their child sessions.</h2>
            <p>Use the right rail to submit unattended work, then inspect queue state, worker capacity, and child-session outcomes from one place.</p>
          </div>
          <span class="${statusClass('queued')}">${escapeHtml(String(state.queueJobs.length))} jobs</span>
        </div>
        <div class="context-grid">
          <div class="context-stat">
            <span class="context-label">Desired Workers</span>
            <span>${escapeHtml(String(state.workers?.desired_count ?? 0))}</span>
          </div>
          <div class="context-stat">
            <span class="context-label">Active Workers</span>
            <span>${escapeHtml(String(state.workers?.active_count ?? 0))}</span>
          </div>
          <div class="context-stat">
            <span class="context-label">Poll Interval</span>
            <span>${escapeHtml(String(state.workers?.poll_interval_ms ?? 0))} ms</span>
          </div>
          <div class="context-stat">
            <span class="context-label">Recent Child Sessions</span>
            <span>${escapeHtml(String((state.queueJobs || []).filter((job) => job.session_id).length))}</span>
          </div>
        </div>
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <p class="eyebrow">Job Contract</p>
            <h2>Worker count affects throughput only.</h2>
            <p>The queue remains file-backed: prompt, parent linkage, requested workdir, role hints, and final outcome all stay durable.</p>
          </div>
        </div>
        ${renderTokenRow([
          'queue submit',
          'parent linkage',
          'child session',
          'worker pool',
          'durable notification',
        ], 'capability-row')}
      </section>
    </div>

    <section class="section-card">
      <div class="section-header">
        <div class="section-header-copy">
          <h2>Queue Jobs</h2>
          <p>Background jobs are durable file-backed records. Worker concurrency changes throughput, not the job contract.</p>
        </div>
        <span class="${statusClass('queued')}">${escapeHtml(String(visibleJobs.length))} visible</span>
      </div>
      ${renderFilterToolbar({
        searchId: 'queue-filter-query',
        searchValue: state.queueFilterQuery,
        searchPlaceholder: 'Search ids, prompts, roles, errors, or workdirs.',
        statusId: 'queue-filter-status',
        statusValue: state.queueFilterStatus,
        statusOptions,
        clearTarget: 'queue',
        chipTarget: 'queue-status',
        chipCounts: counts,
        allCount: state.queueJobs.length,
        activeStatusLabel: 'All jobs',
        summary: `${visibleJobs.length} visible of ${state.queueJobs.length} queue jobs`,
      })}
      <div class="table-list">
        ${visibleJobs.length ? visibleJobs.map(renderJobItem).join('') : renderEmpty('No queue jobs match the current filters.')}
      </div>
    </section>

    <section class="section-card">
      <div class="section-header">
        <div class="section-header-copy">
          <h2>Worker Pool</h2>
          <p>Workers share the same queue root and rely on atomic claim semantics to avoid duplicate consumption.</p>
        </div>
      </div>
      <div class="worker-list">
        ${(state.workers?.workers || []).length
          ? (state.workers.workers || []).map(renderWorkerItem).join('')
          : renderEmpty('No active workers. Scale the pool from the action rail.')}
      </div>
    </section>
  `;
}

function renderJobItem(job) {
  const summary = firstNonEmpty(job.final_text, job.last_error, job.prompt, 'No summary');
  const workdir = firstNonEmpty(job.effective_workdir, job.requested_workdir, '');
  const pathTokens = (job.visible_paths || []).slice(0, 4).map((path) => compactPath(path, 3));
  return `
    <article class="table-item">
      <div class="table-headline">
        <div>
          <div class="mono">${escapeHtml(shortId(job.id))}</div>
          <div class="table-meta">${escapeHtml(job.agent_name || 'default-agent')} · ${escapeHtml(job.agent_role || 'unspecified-role')} · ${escapeHtml(job.mode || 'exec')}</div>
        </div>
        <span class="${statusClass(job.status)}">${escapeHtml(job.status)}</span>
      </div>
      <div class="detail-pairs">
        <div class="detail-pair">
          <strong>Prompt</strong>
          <span>${escapeHtml(job.prompt || '')}</span>
        </div>
        <div class="detail-pair">
          <strong>Session</strong>
          <span class="mono">${escapeHtml(job.session_id || 'pending')}</span>
        </div>
        <div class="detail-pair">
          <strong>Provider / Model</strong>
          <span class="mono">${escapeHtml(job.provider || 'default')} · ${escapeHtml(job.model || 'default')}</span>
        </div>
        <div class="detail-pair">
          <strong>Updated</strong>
          <span>${escapeHtml(formatDate(job.updated_at))}</span>
        </div>
      </div>
      <div class="feed-text">${escapeHtml(truncateText(summary, 240))}</div>
      ${workdir ? `<div class="table-meta" title="${escapeHtml(workdir)}">workdir: ${escapeHtml(compactPath(workdir))}</div>` : ''}
      ${renderTokenRow(pathTokens, 'metadata-row')}
      ${renderMiniActions([
        { sessionId: job.session_id, label: 'Open child session' },
        { sessionId: job.parent_session_id, label: 'Open parent session' },
      ])}
    </article>
  `;
}

function renderWorkerItem(worker) {
  return `
    <article class="worker-item">
      <div class="worker-header">
        <div class="mono">worker-${escapeHtml(String(worker.id))}</div>
        <span class="${statusClass(worker.state)}">${escapeHtml(worker.state)}</span>
      </div>
      <div class="detail-pairs">
        <div class="detail-pair">
          <strong>Last Job</strong>
          <span class="mono">${escapeHtml(worker.last_job_id || 'none')}</span>
        </div>
        <div class="detail-pair">
          <strong>Last Status</strong>
          <span>${escapeHtml(worker.last_job_status || 'none')}</span>
        </div>
        <div class="detail-pair">
          <strong>Updated</strong>
          <span>${escapeHtml(formatDate(worker.updated_at))}</span>
        </div>
        <div class="detail-pair">
          <strong>Error</strong>
          <span>${escapeHtml(worker.last_error || 'none')}</span>
        </div>
      </div>
    </article>
  `;
}

function renderSpotlightCard(eyebrow, title, copy, tokens = []) {
  return `
    <article class="spotlight-card">
      <span class="spotlight-eyebrow">${escapeHtml(eyebrow)}</span>
      <strong class="spotlight-title">${escapeHtml(title)}</strong>
      <p class="spotlight-copy">${escapeHtml(copy)}</p>
      ${renderTokenRow(tokens)}
    </article>
  `;
}

function renderSessionView() {
  const detail = state.sessionDetail;
  if (!detail) {
    elements.sessionView.innerHTML = renderEmpty('Choose a session from the left rail to inspect its full state.');
    return;
  }

  const meta = detail.metadata;
  const runtimeOptions = summarizeProviderOptions(meta.provider_options);
  const lastExcerpt = firstNonEmpty(detail.state.last_assistant_excerpt, detail.state.last_error, detail.state.incomplete_reason, 'No recent assistant excerpt captured yet.');
  const sessionTab = state.sessionTab;
  let body = '';

  if (sessionTab === 'timeline') {
    const visibleTimeline = filteredTimeline(detail);
    body = `
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Timeline</h2>
            <p>Messages and runtime events stay in one stream so you can see the agent output and control-plane behavior together.</p>
          </div>
        </div>
        ${renderTimelineToolbar(detail)}
        <div class="timeline-list">
          ${visibleTimeline.length
            ? visibleTimeline.map(renderTimelineItem).join('')
            : renderEmpty('No timeline entries match the current filters.')}
        </div>
      </section>
    `;
  } else if (sessionTab === 'tasks') {
    body = renderTasksTab(detail.task_board || { todo: [], counters: {}, groups: {} });
  } else if (sessionTab === 'children') {
    body = renderChildrenTab(detail.children || { sessions: [], jobs: [] });
  } else {
    body = renderQueueLinksTab(detail);
  }

  elements.sessionView.innerHTML = `
    <section class="session-header">
      <div class="session-header-top">
        <div class="session-header-copy">
          <p class="eyebrow">Selected Session</p>
          <h2>${escapeHtml(shortId(meta.id))}</h2>
          <p>${escapeHtml(meta.provider)} · ${escapeHtml(meta.model)} · ${escapeHtml(meta.mode)}</p>
        </div>
        <div class="card-row">
          <span class="${statusClass(detail.state.status)}">${escapeHtml(detail.state.status)}</span>
          ${detail.active_handle ? `<span class="${statusClass('running')}">owned by web console</span>` : ''}
        </div>
      </div>
      <div class="spotlight-grid">
        ${renderSpotlightCard(
          'Execution',
          `Turn ${detail.state.turn || 0} · ${detail.state.phase || 'no-phase'}`,
          firstNonEmpty(detail.state.current_task, 'No current task recorded yet.'),
          [
            meta.mode || 'run',
            meta.completion_policy || 'interactive',
            detail.active_handle ? 'owned by web console' : 'durable session only',
          ],
        )}
        ${renderSpotlightCard(
          'Recovery',
          `${detail.state.status || 'unknown'} · pending steer ${detail.state.pending_steer_count || 0}`,
          firstNonEmpty(detail.state.pause_reason, detail.state.incomplete_reason, detail.state.last_error, 'No recovery warnings are currently recorded.'),
          [
            meta.agent_role || 'no-role',
            meta.agent_name || 'anonymous-agent',
            meta.queue_job_id ? `queue=${shortId(meta.queue_job_id)}` : '',
          ],
        )}
        ${renderSpotlightCard(
          'Output',
          'Last assistant excerpt',
          truncateText(lastExcerpt, 200) || 'No assistant output yet.',
          (detail.state.loaded_skills || []).slice(0, 4).map((skill) => `skill:${skill}`),
        )}
        ${renderSpotlightCard(
          'Provider Options',
          `${meta.provider || 'default'} · ${meta.model || 'default'}`,
          firstNonEmpty(meta.requested_workdir, meta.workdir, 'No workdir recorded.'),
          runtimeOptions.length ? runtimeOptions : [meta.isolation?.mode || 'isolation=off'],
        )}
      </div>
      <div class="meta-grid">
        ${renderMetaItem('Workdir', meta.workdir)}
        ${renderMetaItem('Requested Workdir', meta.requested_workdir || meta.workdir || 'N/A')}
        ${renderMetaItem('Created', formatDate(meta.created_at))}
        ${renderMetaItem('Updated', formatDate(detail.state.updated_at))}
        ${renderMetaItem('Agent Name', meta.agent_name || 'none')}
        ${renderMetaItem('Completion Policy', meta.completion_policy || 'interactive')}
        ${renderMetaItem('Turn', String(detail.state.turn || 0))}
        ${detail.state.pause_reason ? renderMetaItem('Pause Reason', detail.state.pause_reason) : ''}
        ${detail.state.last_error ? renderMetaItem('Last Error', detail.state.last_error) : ''}
        ${renderMetaItem('Parent Session', meta.parent_session_id || 'none')}
        ${renderMetaItem('Root Session', meta.root_session_id || meta.id)}
        ${renderMetaItem('Queue Job', meta.queue_job_id || 'none')}
        ${renderMetaItem('Agent Role', meta.agent_role || 'none')}
        ${renderMetaItem('Isolation', meta.isolation?.mode || 'off')}
      </div>
      <div class="tabs">
        ${renderTabButton('timeline', 'Timeline')}
        ${renderTabButton('tasks', 'Tasks')}
        ${renderTabButton('children', 'Children')}
        ${renderTabButton('links', 'Queue Links')}
      </div>
    </section>
    ${body}
  `;
}

function renderMetaItem(label, value) {
  return `
    <div class="meta-item">
      <span class="meta-item-label">${escapeHtml(label)}</span>
      <span class="meta-item-value">${escapeHtml(value)}</span>
    </div>
  `;
}

function renderTabButton(value, label) {
  return `<button class="tab-button ${state.sessionTab === value ? 'is-active' : ''}" type="button" data-session-tab="${escapeHtml(value)}">${escapeHtml(label)}</button>`;
}

function renderTimelineItem(item) {
  const summaryTokens = [
    item.phase ? `phase:${item.phase}` : '',
    ...summarizeDataFields(item.data),
  ];
  return `
    <article class="timeline-item" data-timeline-kind="${escapeHtml(item.kind)}">
      <div class="timeline-header">
        <div class="card-row">
          <span class="${statusClass(item.role || item.event_type || item.kind)}">${escapeHtml(item.kind === 'message' ? item.role : item.event_type || item.kind)}</span>
          <span class="mono">${escapeHtml(item.kind === 'message' ? (item.message_id || '') : (item.event_id || ''))}</span>
        </div>
        <span class="muted">${escapeHtml(formatDate(item.time))}</span>
      </div>
      <div class="timeline-text">${escapeHtml(item.text || JSON.stringify(item.data || {}, null, 2))}</div>
      ${renderTokenRow(summaryTokens, 'metadata-row')}
      ${renderJsonDetails('View raw metadata', item.data)}
    </article>
  `;
}

function renderTasksTab(board) {
  const counters = board.counters || {};
  const groups = board.groups || {};
  return `
    <div class="stats-grid">
      ${renderStatCard('Todo Items', (board.todo || []).length, 'High-frequency execution rhythm')}
      ${renderStatCard('Ready Tasks', counters.ready || 0, 'Pending work with no blockers')}
      ${renderStatCard('Blocked Tasks', counters.blocked || 0, 'Tasks waiting on dependencies')}
      ${renderStatCard('Completed Tasks', counters.completed || 0, 'Durable task graph progress')}
    </div>
    <div class="split-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Todo Rhythm</h2>
            <p>Short-cycle execution plan inside the current session.</p>
          </div>
        </div>
        <div class="table-list">
          ${(board.todo || []).length
            ? (board.todo || []).map((item) => `
                <article class="table-item">
                  <div class="table-headline">
                    <div>${escapeHtml(item.content)}</div>
                    <span class="${statusClass(item.status)}">${escapeHtml(item.status)}</span>
                  </div>
                  ${renderTokenRow([item.priority || 'normal'])}
                  <div class="table-meta">${escapeHtml(item.priority || 'normal')} · ${escapeHtml(formatDate(item.updated_at))}</div>
                </article>
              `).join('')
            : renderEmpty('No todo items for this session.')}
        </div>
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Task Board</h2>
            <p>Durable graph grouped by runtime state.</p>
          </div>
        </div>
        <div class="table-list">
          ${['ready', 'blocked', 'completed', 'cancelled']
            .filter((group) => (groups[group] || []).length)
            .map((group) => `
              <article class="table-item">
                <div class="table-headline">
                  <strong>${escapeHtml(group)}</strong>
                  <span class="badge">${escapeHtml(String(groups[group].length))}</span>
                </div>
                <div class="timeline-list">
                  ${groups[group].map((task) => `
                    <article class="detail-pair">
                      <strong>${escapeHtml(task.id)} · ${escapeHtml(task.subject)}</strong>
                      <span>${escapeHtml(task.description || 'No description')}</span>
                      ${renderTokenRow([
                        task.priority || 'normal',
                        task.owner ? `owner:${task.owner}` : '',
                        task.blocked_by?.length ? `blocked_by:${task.blocked_by.length}` : '',
                        task.blocks?.length ? `blocks:${task.blocks.length}` : '',
                        ...(task.labels || []).map((label) => `label:${label}`),
                      ])}
                    </article>
                  `).join('')}
                </div>
              </article>
            `).join('') || renderEmpty('No task graph items for this session.')}
        </div>
      </section>
    </div>
  `;
}

function renderChildrenTab(children) {
  return `
    <div class="split-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Child Sessions</h2>
            <p>Synchronous or background child execution linked to the current parent.</p>
          </div>
        </div>
        <div class="table-list">
          ${(children.sessions || []).length
            ? children.sessions.map((item) => `
                <article class="table-item">
                  <div class="table-headline">
                    <div class="mono">${escapeHtml(shortId(item.id))}</div>
                    <span class="${statusClass(item.status)}">${escapeHtml(item.status)}</span>
                  </div>
                  <div class="table-meta">${escapeHtml(item.agent_role || 'no-role')} · ${escapeHtml(item.provider)} · ${escapeHtml(item.model)}</div>
                  <div class="feed-text" title="${escapeHtml(item.workdir || '')}">${escapeHtml(compactPath(item.workdir || 'no-workdir'))}</div>
                  ${renderTokenRow([
                    item.phase || 'no-phase',
                    item.depth ? `depth:${item.depth}` : '',
                    item.queue_job_id ? `queue:${shortId(item.queue_job_id)}` : '',
                  ])}
                  ${renderMiniActions([{ sessionId: item.id, label: 'Open child session' }])}
                </article>
              `).join('')
            : renderEmpty('No child sessions for this parent session.')}
        </div>
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Child Queue Jobs</h2>
            <p>Background child jobs created under the current parent session.</p>
          </div>
        </div>
        <div class="table-list">
          ${(children.jobs || []).length
            ? children.jobs.map(renderJobItem).join('')
            : renderEmpty('No child queue jobs for this parent session.')}
        </div>
      </section>
    </div>
  `;
}

function renderQueueLinksTab(detail) {
  return `
    <div class="split-grid">
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Background Notifications</h2>
            <p>Notifications that flowed back into this session from background child work.</p>
          </div>
        </div>
        <div class="notification-list">
          ${(detail.background_notifications || []).length
            ? detail.background_notifications.map((item) => `
                <article class="notification-item">
                  <div class="table-headline">
                    <div class="mono">${escapeHtml(shortId(item.queue_job_id || item.id))}</div>
                    <span class="${statusClass(item.status)}">${escapeHtml(item.status)}</span>
                  </div>
                  <div class="timeline-text">${escapeHtml(item.final_text || item.last_error || 'No summary')}</div>
                  <div class="table-meta">${escapeHtml(item.agent_role || 'no-role')} · ${escapeHtml(formatDate(item.created_at))}</div>
                  ${renderTokenRow([
                    item.agent_name || '',
                    item.delivery_status ? `delivery:${item.delivery_status}` : '',
                    item.session_status ? `session:${item.session_status}` : '',
                  ])}
                  ${renderTokenRow((item.visible_paths || []).slice(0, 4).map((path) => compactPath(path, 3)), 'metadata-row')}
                  ${renderMiniActions([
                    { sessionId: item.session_id, label: 'Open child session' },
                  ])}
                </article>
              `).join('')
            : renderEmpty('No background notifications have been written yet.')}
        </div>
      </section>
      <section class="section-card">
        <div class="section-header">
          <div class="section-header-copy">
            <h2>Steer Requests</h2>
            <p>Control queue written to <span class="mono">control/steer.jsonl</span>.</p>
          </div>
        </div>
        <div class="notification-list">
          ${(detail.steer_requests || []).length
            ? detail.steer_requests.map((item) => `
                <article class="notification-item">
                  <div class="table-headline">
                    <div class="mono">${escapeHtml(shortId(item.id))}</div>
                    <span class="${statusClass(item.status)}">${escapeHtml(item.status)}</span>
                  </div>
                  <div class="timeline-text">${escapeHtml(item.text || '')}</div>
                  ${renderTokenRow([
                    item.source || 'unknown',
                    `interrupt=${String(Boolean(item.interrupt))}`,
                    formatDate(item.created_at),
                  ])}
                </article>
              `).join('')
            : renderEmpty('No steer requests have been queued for this session.')}
        </div>
      </section>
    </div>
  `;
}

function renderEmpty(message) {
  return `
    <div class="empty-state">
      <strong>Nothing visible yet</strong>
      <span>${escapeHtml(message)}</span>
    </div>
  `;
}

function renderSidebar() {
  const visibleSessions = filteredSessions();
  const statusOptions = uniqueStatuses(state.sessions, (item) => item.status);
  const counts = statusCounts(state.sessions, (item) => item.status);
  const hasActiveSessionFilters = normalizedQuery(state.sessionFilterQuery) || (state.sessionFilterStatus && state.sessionFilterStatus !== 'all');
  const selectedSessionHidden = Boolean(
    state.selectedSessionId
      && !visibleSessions.some((item) => item.id === state.selectedSessionId)
      && (state.sessions || []).some((item) => item.id === state.selectedSessionId),
  );
  elements.sessionCountBadge.textContent = hasActiveSessionFilters
    ? `${visibleSessions.length}/${state.sessions.length}`
    : String(state.sessions.length);
  elements.sessionList.innerHTML = `
    ${renderFilterToolbar({
      searchId: 'session-filter-query',
      searchValue: state.sessionFilterQuery,
      searchPlaceholder: 'Search ids, roles, providers, or workdirs.',
      statusId: 'session-filter-status',
      statusValue: state.sessionFilterStatus,
      statusOptions,
      clearTarget: 'sessions',
      chipTarget: 'session-status',
      chipCounts: counts,
      allCount: state.sessions.length,
      activeStatusLabel: 'All sessions',
      summary: `${visibleSessions.length} visible of ${state.sessions.length} sessions`,
    })}
    ${selectedSessionHidden ? `
      <div class="hidden-selection-note">
        <span class="helper-text">The selected session is currently hidden by the sidebar filters.</span>
        <button class="mini-button" type="button" data-reveal-selected-session="true">Reveal selected</button>
      </div>
    ` : ''}
    ${visibleSessions.length
      ? visibleSessions.map((item) => `
        <button class="session-list-item ${state.selectedSessionId === item.id ? 'is-active' : ''}" type="button" data-session-id="${escapeHtml(item.id)}">
          <div class="card-row">
            <span class="session-id">${escapeHtml(shortId(item.id))}</span>
            <span class="${statusClass(item.status)}">${escapeHtml(item.status)}</span>
          </div>
          <div class="session-meta">${escapeHtml(item.provider)} · ${escapeHtml(item.model)} · ${escapeHtml(sessionRoleLabel(item))}</div>
          <div class="session-meta">${escapeHtml(item.phase || 'no-phase')} · ${escapeHtml(formatDate(item.updated_at))}</div>
          <div class="session-meta" title="${escapeHtml(item.workdir || '')}">${escapeHtml(compactPath(item.workdir || ''))}</div>
        </button>
      `).join('')
      : renderEmpty(state.sessions.length ? 'No sessions match the current filters.' : 'No sessions yet.')}
  `;

  document.querySelectorAll('[data-view-button]').forEach((button) => {
    button.classList.toggle('is-active', button.dataset.viewButton === state.currentView);
  });
}

function renderActions() {
  elements.startCard.innerHTML = startFormTemplate();
  elements.queueJobCard.innerHTML = queueJobTemplate();
  elements.workerCard.innerHTML = workerControlTemplate();
  elements.sessionActionCard.innerHTML = sessionActionTemplate();
  bindActionForms();
}

function sessionActionTemplate() {
  if (!state.sessionDetail) {
    return `
      <div class="section-header-copy">
        <h2>Session Actions</h2>
        <p>Select a session to unlock steer, continue, and direct interrupt controls.</p>
      </div>
      ${renderEmpty('No session selected.')}
    `;
  }

  const status = state.sessionDetail.state.status;
  const canContinue = ['awaiting_input', 'paused', 'failed'].includes(status);
  const canSteer = status === 'running';

  return `
    <div class="section-header-copy">
      <h2>Session Actions</h2>
      <p>${canSteer
        ? 'Use steer to refine a running session without resetting the task.'
        : canContinue
          ? 'Use continue to resume from the durable session state.'
          : 'This session is not currently controllable from the browser.'}</p>
    </div>
    <div class="context-panel">
      <div class="context-panel-head">
        <strong>${escapeHtml(shortId(state.sessionDetail.metadata?.id || state.selectedSessionId))}</strong>
        <span class="${statusClass(status)}">${escapeHtml(status)}</span>
      </div>
      <div class="context-grid">
        <div class="context-stat">
          <span class="context-label">Phase</span>
          <span>${escapeHtml(state.sessionDetail.state?.phase || 'no-phase')}</span>
        </div>
        <div class="context-stat">
          <span class="context-label">Pending Steer</span>
          <span>${escapeHtml(String(state.sessionDetail.state?.pending_steer_count || 0))}</span>
        </div>
        <div class="context-stat">
          <span class="context-label">Agent</span>
          <span>${escapeHtml(sessionRoleLabel(state.sessionDetail.metadata || {}))}</span>
        </div>
        <div class="context-stat">
          <span class="context-label">Workdir</span>
          <span title="${escapeHtml(state.sessionDetail.metadata?.workdir || '')}">${escapeHtml(compactPath(state.sessionDetail.metadata?.workdir || ''))}</span>
        </div>
      </div>
    </div>
    ${canSteer ? `
      <form id="steer-form" class="form-grid">
        <div class="field">
          <label for="steer-message">Steer Message</label>
          <textarea id="steer-message" name="message" placeholder="Focus on failing tests first." required></textarea>
        </div>
        <label class="checkbox-inline">
          <input type="checkbox" name="interrupt" />
          <span>Request interrupt at the next safe point</span>
        </label>
        <button class="button button-primary" type="submit">Send Steer</button>
      </form>
      ${state.sessionDetail.active_handle ? `<button id="interrupt-button" class="button button-danger" type="button">Interrupt This Session</button>` : ''}
    ` : ''}
    ${canContinue ? `
      <form id="continue-form" class="form-grid">
        <div class="field">
          <label for="continue-message">Continue Message</label>
          <textarea id="continue-message" name="message" placeholder="Proceed with the next step." required></textarea>
        </div>
        <div class="form-row">
          <div class="field">
            <label for="continue-provider">Provider</label>
            <select id="continue-provider" name="provider">
              <option value="">Keep current provider</option>
              ${providerOptions()}
            </select>
          </div>
          <div class="field">
            <label for="continue-model">Model override</label>
            <input id="continue-model" name="model" placeholder="Leave empty to keep the current model." />
          </div>
        </div>
        <button class="button button-primary" type="submit">Continue Session</button>
      </form>
    ` : ''}
  `;
}

function queueJobTemplate() {
  const selected = state.selectedSessionId || '';
  return `
    <div class="section-header-copy">
      <h2>Queue Job</h2>
      <p>Use the durable background queue for child work, unattended tasks, or parallel backlog execution.</p>
    </div>
    <form id="queue-form" class="form-grid">
      <div class="field">
        <label for="queue-prompt">Prompt</label>
        <textarea id="queue-prompt" name="prompt" placeholder="Summarize the current task scope and call finish when done." required></textarea>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="queue-parent">Parent Session</label>
          <input id="queue-parent" name="parent_session_id" value="${escapeHtml(selected)}" placeholder="Optional parent session id" />
        </div>
        <div class="field">
          <label for="queue-role">Agent Role</label>
          <select id="queue-role" name="agent_role">
            ${roleOptions()}
          </select>
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="queue-provider">Provider</label>
          <select id="queue-provider" name="provider">
            <option value="">Default provider</option>
            ${providerOptions()}
          </select>
        </div>
        <div class="field">
          <label for="queue-model">Model override</label>
          <input id="queue-model" name="model" placeholder="Use the provider default if empty." />
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="queue-agent-name">Agent Name</label>
          <input id="queue-agent-name" name="agent_name" placeholder="reviewer" />
        </div>
        <div class="field">
          <label for="queue-mode">Mode</label>
          <select id="queue-mode" name="mode">
            <option value="exec">exec</option>
            <option value="run">run</option>
          </select>
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label for="queue-workdir">Workdir</label>
          <input id="queue-workdir" name="workdir" placeholder="Optional isolated workdir root." />
        </div>
        <div class="field">
          <label for="queue-isolation">Isolation</label>
          <select id="queue-isolation" name="isolation_mode">
            <option value="">auto</option>
            <option value="off">off</option>
            <option value="auto">auto</option>
            <option value="copy">copy</option>
            <option value="git">git</option>
          </select>
        </div>
      </div>
      <button class="button button-secondary" type="submit">Submit Queue Job</button>
    </form>
  `;
}

function workerControlTemplate() {
  return `
    <div class="section-header-copy">
      <h2>Worker Pool</h2>
      <p>Scale background workers when queue throughput matters. Worker count changes execution capacity, not the queue contract.</p>
    </div>
    <form id="worker-form" class="form-grid">
      <div class="field">
        <label for="worker-count">Desired Worker Count</label>
        <input id="worker-count" type="number" min="0" name="desired_count" value="${escapeHtml(String(state.workers?.desired_count ?? 0))}" />
      </div>
      <button class="button button-secondary" type="submit">Apply Worker Count</button>
    </form>
    <div class="helper-text">Current snapshot: ${escapeHtml(String(state.workers?.active_count ?? 0))} active workers, poll interval ${escapeHtml(String(state.workers?.poll_interval_ms ?? 0))} ms.</div>
  `;
}

function bindActionForms() {
  document.getElementById('start-form')?.addEventListener('submit', onStartSubmit);
  document.getElementById('steer-form')?.addEventListener('submit', onSteerSubmit);
  document.getElementById('continue-form')?.addEventListener('submit', onContinueSubmit);
  document.getElementById('queue-form')?.addEventListener('submit', onQueueSubmit);
  document.getElementById('worker-form')?.addEventListener('submit', onWorkerSubmit);
  document.getElementById('interrupt-button')?.addEventListener('click', onInterruptClick);
}

async function onStartSubmit(event) {
  event.preventDefault();
  if (event.currentTarget.dataset.pending === 'true') return;
  const form = new FormData(event.currentTarget);
  const payload = Object.fromEntries(form.entries());
  setFormPending(event.currentTarget, true, 'Starting...');
  try {
    const result = await fetchJSON('/api/sessions/start', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    showToast(`Started session ${shortId(result.session_id)}`);
    state.selectedSessionId = result.session_id;
    state.currentView = 'session';
    await refreshAll();
    await refreshSelectedSession();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setFormPending(event.currentTarget, false, 'Starting...');
  }
}

async function onSteerSubmit(event) {
  event.preventDefault();
  if (!state.selectedSessionId) return;
  if (event.currentTarget.dataset.pending === 'true') return;
  const form = new FormData(event.currentTarget);
  const payload = {
    message: form.get('message'),
    interrupt: form.get('interrupt') === 'on',
  };
  setFormPending(event.currentTarget, true, 'Sending...');
  try {
    await fetchJSON(`/api/sessions/${encodeURIComponent(state.selectedSessionId)}/steer`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    showToast('Steer request queued.');
    event.currentTarget.reset();
    await refreshSelectedSession();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setFormPending(event.currentTarget, false, 'Sending...');
  }
}

async function onContinueSubmit(event) {
  event.preventDefault();
  if (!state.selectedSessionId) return;
  if (event.currentTarget.dataset.pending === 'true') return;
  const form = new FormData(event.currentTarget);
  const payload = Object.fromEntries(form.entries());
  setFormPending(event.currentTarget, true, 'Continuing...');
  try {
    await fetchJSON(`/api/sessions/${encodeURIComponent(state.selectedSessionId)}/continue`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    showToast('Continue request accepted.');
    await refreshAll();
    await refreshSelectedSession();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setFormPending(event.currentTarget, false, 'Continuing...');
  }
}

async function onQueueSubmit(event) {
  event.preventDefault();
  if (event.currentTarget.dataset.pending === 'true') return;
  const form = new FormData(event.currentTarget);
  const payload = Object.fromEntries(form.entries());
  setFormPending(event.currentTarget, true, 'Submitting...');
  try {
    await fetchJSON('/api/queue/jobs', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    showToast('Queue job submitted.');
    event.currentTarget.reset();
    if (state.selectedSessionId) {
      const parent = document.getElementById('queue-parent');
      if (parent) parent.value = state.selectedSessionId;
    }
    await refreshAll();
    await refreshSelectedSession();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setFormPending(event.currentTarget, false, 'Submitting...');
  }
}

async function onWorkerSubmit(event) {
  event.preventDefault();
  if (event.currentTarget.dataset.pending === 'true') return;
  const form = new FormData(event.currentTarget);
  const payload = { desired_count: Number(form.get('desired_count') || 0) };
  setFormPending(event.currentTarget, true, 'Applying...');
  try {
    state.workers = await fetchJSON('/api/workers', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    showToast('Worker pool updated.');
    render();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setFormPending(event.currentTarget, false, 'Applying...');
  }
}

async function onInterruptClick() {
  if (!state.selectedSessionId) return;
  const button = document.getElementById('interrupt-button');
  if (button?.disabled) return;
  setButtonPending(button, true, 'Interrupting...');
  try {
    await fetchJSON(`/api/sessions/${encodeURIComponent(state.selectedSessionId)}/interrupt`, {
      method: 'POST',
      body: JSON.stringify({}),
    });
    showToast('Interrupt requested.');
    await refreshSelectedSession();
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setButtonPending(button, false, 'Interrupting...');
  }
}

function render() {
  renderChrome();
  renderSidebar();
  renderOverview();
  renderQueueView();
  renderSessionView();
  renderActions();

  elements.overviewView.classList.toggle('is-hidden', state.currentView !== 'overview');
  elements.queueView.classList.toggle('is-hidden', state.currentView !== 'queue');
  elements.sessionView.classList.toggle('is-hidden', state.currentView !== 'session');
}

function clearFilters(target) {
  switch (target) {
    case 'sessions':
      state.sessionFilterQuery = '';
      state.sessionFilterStatus = 'all';
      renderSidebar();
      return;
    case 'queue':
      state.queueFilterQuery = '';
      state.queueFilterStatus = 'all';
      renderQueueView();
      return;
    case 'timeline':
      state.timelineFilterQuery = '';
      state.timelineFilterKind = 'all';
      renderSessionView();
      return;
    default:
  }
}

document.addEventListener('click', (event) => {
  const revealSelectedButton = event.target.closest('[data-reveal-selected-session]');
  if (revealSelectedButton) {
    state.sessionFilterQuery = '';
    state.sessionFilterStatus = 'all';
    renderSidebar();
    return;
  }

  const filterChip = event.target.closest('[data-filter-chip-target]');
  if (filterChip) {
    const target = filterChip.dataset.filterChipTarget;
    const value = filterChip.dataset.filterChipValue || 'all';
    switch (target) {
      case 'session-status':
        state.sessionFilterStatus = value;
        renderSidebar();
        return;
      case 'queue-status':
        state.queueFilterStatus = value;
        renderQueueView();
        return;
      case 'timeline-kind':
        state.timelineFilterKind = value;
        renderSessionView();
        return;
      default:
    }
  }

  const clearFilterButton = event.target.closest('[data-clear-filter]');
  if (clearFilterButton) {
    clearFilters(clearFilterButton.dataset.clearFilter);
    return;
  }

  const openSessionButton = event.target.closest('[data-open-session-id]');
  if (openSessionButton) {
    setSession(openSessionButton.dataset.openSessionId);
    refreshSelectedSession();
    return;
  }

  const sessionButton = event.target.closest('[data-session-id]');
  if (sessionButton) {
    setSession(sessionButton.dataset.sessionId);
    refreshSelectedSession();
    return;
  }

  const viewButton = event.target.closest('[data-view-button]');
  if (viewButton) {
    setView(viewButton.dataset.viewButton);
    return;
  }

  const tabButton = event.target.closest('[data-session-tab]');
  if (tabButton) {
    state.sessionTab = tabButton.dataset.sessionTab;
    renderSessionView();
    renderActions();
  }
});

document.addEventListener('input', (event) => {
  switch (event.target.id) {
    case 'session-filter-query':
      state.sessionFilterQuery = event.target.value;
      renderSidebar();
      break;
    case 'queue-filter-query':
      state.queueFilterQuery = event.target.value;
      renderQueueView();
      break;
    case 'timeline-filter-query':
      state.timelineFilterQuery = event.target.value;
      renderSessionView();
      break;
    default:
  }
});

document.addEventListener('change', (event) => {
  switch (event.target.id) {
    case 'session-filter-status':
      state.sessionFilterStatus = event.target.value;
      renderSidebar();
      break;
    case 'queue-filter-status':
      state.queueFilterStatus = event.target.value;
      renderQueueView();
      break;
    case 'timeline-filter-kind':
      state.timelineFilterKind = event.target.value;
      renderSessionView();
      break;
    default:
  }
});

elements.refreshButton.addEventListener('click', () => refreshAll());
elements.openStartButton.addEventListener('click', () => {
  setView('overview');
  window.scrollTo({ top: 0, behavior: 'smooth' });
  document.getElementById('start-prompt')?.focus();
});

window.setInterval(() => {
  refreshAll();
}, POLL_MS);

refreshAll();
