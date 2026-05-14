class APIError extends Error {
  constructor(message, { code = '', detail = '', action = '', status = 0 } = {}) {
    super(message);
    this.name = 'APIError';
    this.code = code;
    this.detail = detail;
    this.action = action;
    this.status = status;
  }
}

function compactPayload(payload) {
  return Object.fromEntries(
    Object.entries(payload || {}).filter(([, value]) => value !== undefined)
  );
}

function jsonRequest(payload, options = {}) {
  return {
    ...options,
    method: options.method || 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {})
    },
    body: JSON.stringify(compactPayload(payload))
  };
}

async function requestJSON(url, options = {}) {
  const method = (options.method || 'GET').toUpperCase();
  const headers = { ...(options.headers || {}) };
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    headers['X-Go-Cli-Agent-Web'] = '1';
  }
  const response = await fetch(url, { ...options, headers });
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
      status: response.status
    });
  }
  return payload;
}

async function requestFormJSON(url, formData, options = {}) {
  return requestJSON(url, {
    ...options,
    body: formData
  });
}

function startSession(payload) {
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
    plan_mode: payload.planMode
  }));
}

function continueSession(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/continue`, jsonRequest({
    message: payload.message || '',
    provider: payload.provider,
    model: payload.model,
    system: payload.system,
    plan_mode: payload.planMode
  }));
}

function getPlanMode(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/planmode`);
}

function approvePlanMode(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/planmode/approve`, jsonRequest(payload));
}

function revisePlanMode(sessionID, message) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/planmode/revise`, jsonRequest({ message }));
}

function cancelPlanMode(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/planmode/cancel`, jsonRequest({}));
}

function answerPlanModeInput(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/planmode/input`, jsonRequest({
    request_id: payload.requestID,
    answers: payload.answers || []
  }));
}

function steerSession(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/steer`, jsonRequest({
    message: payload.message || '',
    interrupt: Boolean(payload.interrupt)
  }));
}

function interruptSession(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/interrupt`, { method: 'POST' });
}

function stopSession(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/stop`, { method: 'POST' });
}

function getGoal(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal`);
}

function createGoal(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal`, jsonRequest(payload));
}

function updateGoal(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal`, jsonRequest(payload, { method: 'PATCH' }));
}

function deleteGoal(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal`, jsonRequest({}, { method: 'DELETE' }));
}

function completeGoal(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal/complete`, { method: 'POST' });
}

function pauseGoal(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal/pause`, { method: 'POST' });
}

function resumeGoal(sessionID) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/goal/resume`, { method: 'POST' });
}

function patchMissionPlan(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/mission/plan`, jsonRequest(payload, { method: 'PATCH' }));
}

function approveMissionPlan(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/mission/plan/approve`, jsonRequest(payload, { method: 'POST' }));
}

function patchMissionValidation(sessionID, payload = {}) {
  return requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/mission/validation`, jsonRequest(payload, { method: 'PATCH' }));
}

function submitQueueJob(payload) {
  return requestJSON('/api/queue/jobs', jsonRequest({
    prompt: payload.prompt,
    parent_session_id: payload.parentSessionID,
    agent_name: payload.agentName,
    agent_role: payload.agentRole,
    provider: payload.provider,
    model: payload.model,
    workdir: payload.workdir,
    system: payload.system,
    mode: payload.mode,
    wait_mode: payload.waitMode,
    isolation_mode: payload.isolationMode,
    isolation_root: payload.isolationRoot
  }));
}

function saveConfig(payload) {
  return requestJSON('/api/config', jsonRequest({
    guardrails_mode: payload.guardrailsMode,
    max_turns_hard: payload.maxTurnsHard,
    disable_hard_turn_limit: payload.disableHardTurnLimit,
    provider: payload.provider,
    api_provider: payload.apiProvider,
    base_url: payload.baseURL,
    model: payload.model,
    reasoning_mode: payload.reasoningMode,
    reasoning_summary: payload.reasoningSummary,
    role_providers: payload.roleProviders,
    api_key: payload.apiKey
  }));
}

function testConfig(payload) {
  return requestJSON('/api/config/test', jsonRequest({
    provider: payload.provider,
    api_provider: payload.apiProvider,
    base_url: payload.baseURL,
    model: payload.model,
    reasoning_mode: payload.reasoningMode,
    reasoning_summary: payload.reasoningSummary,
    api_key: payload.apiKey
  }));
}
