// REST client: the single fetch entry point of the Web console frontend.
// Pure module (uses the global fetch only when a request is made).

export class APIError extends Error {
  constructor(message, { code = '', detail = '', action = '', status = 0, payload = null } = {}) {
    super(message);
    this.name = 'APIError';
    this.code = code;
    this.detail = detail;
    this.action = action;
    this.status = status;
    this.payload = payload;
  }
}

/** Header the server requires on unsafe methods (CSRF guard). */
export const WEB_MUTATION_HEADER = 'X-Aegis-Agent-Web';

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/** Drops undefined top-level fields so the server sees "absent", not null. */
export function compactPayload(payload) {
  return Object.fromEntries(Object.entries(payload || {}).filter(([, value]) => value !== undefined));
}

export function jsonRequest(payload, options = {}) {
  return {
    ...options,
    method: options.method || 'POST',
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    body: JSON.stringify(compactPayload(payload)),
  };
}

export async function requestJSON(url, options = {}) {
  const method = String(options.method || 'GET').toUpperCase();
  const headers = { ...(options.headers || {}) };
  if (!SAFE_METHODS.has(method)) {
    headers[WEB_MUTATION_HEADER] = '1';
  }
  const response = await fetch(url, { ...options, method, headers });
  let payload = null;
  try {
    payload = await response.json();
  } catch {
    payload = null;
  }
  if (!response.ok) {
    const message = payload?.error || payload?.message || response.statusText || `Request failed: ${response.status}`;
    throw new APIError(message, {
      code: payload?.code || '',
      detail: payload?.detail || '',
      action: payload?.action || '',
      status: response.status,
      payload,
    });
  }
  return payload;
}

export function requestFormJSON(url, formData, options = {}) {
  return requestJSON(url, { method: 'POST', ...options, body: formData });
}

const enc = encodeURIComponent;
const sessionURL = (sessionID, suffix = '') => `/api/sessions/${enc(sessionID)}${suffix}`;
function query(params) {
  const parts = Object.entries(params)
    .filter(([, value]) => value !== undefined && value !== null && value !== '')
    .map(([key, value]) => `${enc(key)}=${enc(String(value))}`);
  return parts.length ? `?${parts.join('&')}` : '';
}

// ---- meta / overview / history ------------------------------------------
export const getMeta = () => requestJSON('/api/meta');
export const getOverview = () => requestJSON('/api/overview');
export const getHistory = ({ page = 1, pageSize = 10 } = {}) =>
  requestJSON(`/api/history${query({ page, page_size: pageSize })}`);
export const listSessions = ({ limit } = {}) => requestJSON(`/api/sessions${query({ limit })}`);
export const clearSessions = () => requestJSON('/api/sessions/clear', { method: 'POST' });

// ---- session detail and sub-resources -------------------------------------
export const getSession = (sessionID, { limit = 40 } = {}) => requestJSON(`${sessionURL(sessionID)}${query({ limit })}`);
export const deleteSession = (sessionID) => requestJSON(sessionURL(sessionID), { method: 'DELETE' });
export const getSessionMessages = (sessionID, { beforeId, limit = 40 } = {}) =>
  requestJSON(`${sessionURL(sessionID, '/messages')}${query({ before_id: beforeId, limit })}`);
export const getSessionContext = (sessionID) => requestJSON(sessionURL(sessionID, '/context'));
export const getSessionFileChanges = (sessionID) => requestJSON(sessionURL(sessionID, '/file-changes'));
export const getSessionChildren = (sessionID, { limit } = {}) => requestJSON(`${sessionURL(sessionID, '/children')}${query({ limit })}`);
export const getSessionTasks = (sessionID) => requestJSON(sessionURL(sessionID, '/tasks'));

// ---- run control ------------------------------------------------------------
export function startSession(payload = {}) {
  return requestJSON('/api/sessions/start', jsonRequest({
    prompt: payload.prompt,
    agent_name: payload.agentName,
    agent_role: payload.agentRole,
    provider: payload.provider,
    model: payload.model,
    workdir: payload.workdir,
    mode: payload.mode,
    system: payload.system,
    isolation_mode: payload.isolationMode,
    isolation_root: payload.isolationRoot,
    goal: payload.goal,
    plan_mode: payload.planMode,
  }));
}

export function continueSession(sessionID, payload = {}) {
  return requestJSON(sessionURL(sessionID, '/continue'), jsonRequest({
    message: payload.message || '',
    provider: payload.provider,
    model: payload.model,
    system: payload.system,
    plan_mode: payload.planMode,
  }));
}

export function steerSession(sessionID, payload = {}) {
  return requestJSON(sessionURL(sessionID, '/steer'), jsonRequest({
    message: payload.message || '',
    interrupt: Boolean(payload.interrupt),
  }));
}

export const interruptSession = (sessionID) => requestJSON(sessionURL(sessionID, '/interrupt'), { method: 'POST' });
export const stopSession = (sessionID) => requestJSON(sessionURL(sessionID, '/stop'), { method: 'POST' });

// ---- plan mode and approval receipts ----------------------------------------
export const getPlanMode = (sessionID) => requestJSON(sessionURL(sessionID, '/planmode'));
export const approvePlanMode = (sessionID, payload = {}) => requestJSON(sessionURL(sessionID, '/planmode/approve'), jsonRequest(payload));
export const revisePlanMode = (sessionID, message) => requestJSON(sessionURL(sessionID, '/planmode/revise'), jsonRequest({ message }));
export const cancelPlanMode = (sessionID) => requestJSON(sessionURL(sessionID, '/planmode/cancel'), jsonRequest({}));
export function answerPlanModeInput(sessionID, payload = {}) {
  return requestJSON(sessionURL(sessionID, '/planmode/input'), jsonRequest({
    request_id: payload.requestID,
    answers: payload.answers || [],
  }));
}
export const getApprovalReceipt = (sessionID, requestID) => requestJSON(sessionURL(sessionID, `/approval-receipts/${enc(requestID)}`));

// ---- goal ---------------------------------------------------------------------
export const getGoal = (sessionID) => requestJSON(sessionURL(sessionID, '/goal'));
export const createGoal = (sessionID, payload = {}) => requestJSON(sessionURL(sessionID, '/goal'), jsonRequest(payload));
export const updateGoal = (sessionID, payload = {}) => requestJSON(sessionURL(sessionID, '/goal'), jsonRequest(payload, { method: 'PATCH' }));
export const deleteGoal = (sessionID) => requestJSON(sessionURL(sessionID, '/goal'), jsonRequest({}, { method: 'DELETE' }));
export const completeGoal = (sessionID) => requestJSON(sessionURL(sessionID, '/goal/complete'), { method: 'POST' });
export const pauseGoal = (sessionID) => requestJSON(sessionURL(sessionID, '/goal/pause'), { method: 'POST' });
export const resumeGoal = (sessionID) => requestJSON(sessionURL(sessionID, '/goal/resume'), { method: 'POST' });

// ---- mission ------------------------------------------------------------------
export const patchMissionPlan = (sessionID, payload = {}) => requestJSON(sessionURL(sessionID, '/mission/plan'), jsonRequest(payload, { method: 'PATCH' }));
export const approveMissionPlan = (sessionID, payload = {}) => requestJSON(sessionURL(sessionID, '/mission/plan/approve'), jsonRequest(payload, { method: 'POST' }));
export const patchMissionValidation = (sessionID, payload = {}) => requestJSON(sessionURL(sessionID, '/mission/validation'), jsonRequest(payload, { method: 'PATCH' }));

// ---- queue (read-only observation) ------------------------------------------------
export const listQueueJobs = ({ limit } = {}) => requestJSON(`/api/queue/jobs${query({ limit })}`);
export const getQueueJob = (jobID) => requestJSON(`/api/queue/jobs/${enc(jobID)}`);

// ---- skills -------------------------------------------------------------------
export const listSkills = () => requestJSON('/api/skills');
export function uploadSkill(file) {
  const form = new FormData();
  form.append('file', file);
  return requestFormJSON('/api/skills/upload', form);
}
export const installSkill = (skillID) => requestJSON(`/api/skills/${enc(skillID)}/install`, { method: 'POST' });
export const uninstallSkill = (skillID) => requestJSON(`/api/skills/${enc(skillID)}/uninstall`, { method: 'POST' });

// ---- settings -------------------------------------------------------------------
export const getConfig = () => requestJSON('/api/config');

export function saveConfig(payload = {}) {
  const childBudget = payload.childBudget;
  return requestJSON('/api/config', jsonRequest({
    guardrails_mode: payload.guardrailsMode,
    max_turns_soft: payload.maxTurnsSoft,
    max_turns_hard: payload.maxTurnsHard,
    disable_hard_turn_limit: payload.disableHardTurnLimit,
    child_budget: childBudget ? {
      disabled: childBudget.disabled,
      max_active_runtime_sec: childBudget.maxActiveRuntimeSec,
      max_elapsed_sec: childBudget.maxElapsedSec,
      max_turns_per_attempt: childBudget.maxTurnsPerAttempt,
      active_runtime_checkpoint_ms: childBudget.activeRuntimeCheckpointMS,
    } : undefined,
    provider: payload.provider,
    api_provider: payload.apiProvider,
    base_url: payload.baseURL,
    model: payload.model,
    context_window_tokens: payload.contextWindowTokens,
    reasoning_mode: payload.reasoningMode,
    reasoning_summary: payload.reasoningSummary,
    role_providers: payload.roleProviders,
    api_key: payload.apiKey,
  }));
}

export function testConfig(payload = {}) {
  return requestJSON('/api/config/test', jsonRequest({
    provider: payload.provider,
    api_provider: payload.apiProvider,
    base_url: payload.baseURL,
    model: payload.model,
    context_window_tokens: payload.contextWindowTokens,
    reasoning_mode: payload.reasoningMode,
    reasoning_summary: payload.reasoningSummary,
    api_key: payload.apiKey,
  }));
}

// ---- workspace files ---------------------------------------------------------------
export const listWorkspaceFiles = (path = '.') => requestJSON(`/api/files${query({ path: path || '.' })}`);
export const readWorkspaceFile = (path, offset = 0, limit = 256 * 1024) =>
  requestJSON(`/api/file/read${query({ path, offset: String(offset || 0), limit: String(limit) })}`);
export const createWorkspaceDirectory = (path, name) => requestJSON('/api/files/mkdir', jsonRequest({ path: path || '.', name }));
export function uploadWorkspaceFile(path, file) {
  const form = new FormData();
  form.append('path', path || '.');
  form.append('file', file);
  return requestFormJSON('/api/files/upload', form);
}
export const renameWorkspaceFile = (path, name) => requestJSON('/api/files/rename', jsonRequest({ path, name }, { method: 'PATCH' }));
export const deleteWorkspacePath = (path) => requestJSON(`/api/files${query({ path })}`, { method: 'DELETE' });
export const deleteWorkspacePaths = (paths = []) => requestJSON('/api/files/delete', jsonRequest({ paths }));
export const workspaceDownloadURL = (path) => `/api/file/download${query({ path })}`;
