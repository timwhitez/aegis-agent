import test, { beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import * as api from '../../internal/webconsole/ui/src/lib/api.js';

let calls = [];
let nextResponse = null;

function respond(status, body, { json = true, statusText = '' } = {}) {
  nextResponse = { status, body, json, statusText };
}

beforeEach(() => {
  calls = [];
  respond(200, { ok: true });
  globalThis.fetch = async (url, options = {}) => {
    calls.push({ url, options });
    const { status, body, json, statusText } = nextResponse;
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText,
      async json() {
        if (!json) throw new SyntaxError('not json');
        return body;
      },
    };
  };
});

const last = () => calls[calls.length - 1];
const bodyOf = (call) => JSON.parse(call.options.body);

test('GET requests carry no mutation header; unsafe methods always do', async () => {
  await api.getMeta();
  assert.equal(last().url, '/api/meta');
  assert.equal(last().options.method, 'GET');
  assert.equal(last().options.headers['X-Aegis-Agent-Web'], undefined);
  for (const call of [() => api.stopSession('s1'), () => api.deleteSession('s1'), () => api.updateGoal('s1', {}), () => api.clearSessions()]) {
    await call();
    assert.equal(last().options.headers['X-Aegis-Agent-Web'], '1', last().url);
  }
});

test('error envelope becomes APIError with code/detail/action/status/payload', async () => {
  const payload = { error: 'plan changed', code: 'approval_target_stale', detail: 'version 3 != 2', action: 'review again' };
  respond(409, payload);
  await assert.rejects(api.approvePlanMode('s1', { approval_target: {} }), (error) => {
    assert.ok(error instanceof api.APIError);
    assert.equal(error.message, 'plan changed');
    assert.equal(error.code, 'approval_target_stale');
    assert.equal(error.detail, 'version 3 != 2');
    assert.equal(error.action, 'review again');
    assert.equal(error.status, 409);
    assert.deepEqual(error.payload, payload);
    return true;
  });
  respond(502, null, { json: false, statusText: 'Bad Gateway' });
  await assert.rejects(api.getOverview(), (error) => error.status === 502 && error.message === 'Bad Gateway' && error.payload === null);
  respond(500, null, { json: false });
  await assert.rejects(api.getOverview(), (error) => error.message === 'Request failed: 500');
});

test('session reads use encoded ids, limit 40 and before_id paging', async () => {
  await api.getSession('a/b c');
  assert.equal(last().url, '/api/sessions/a%2Fb%20c?limit=40');
  await api.getSessionMessages('s1', { beforeId: 'msg 9' });
  assert.equal(last().url, '/api/sessions/s1/messages?before_id=msg%209&limit=40');
  await api.getSessionMessages('s1');
  assert.equal(last().url, '/api/sessions/s1/messages?limit=40');
  await api.getSessionContext('s1');
  assert.equal(last().url, '/api/sessions/s1/context');
  await api.getSessionFileChanges('s1');
  assert.equal(last().url, '/api/sessions/s1/file-changes');
  await api.getSessionChildren('s1', { limit: 20 });
  assert.equal(last().url, '/api/sessions/s1/children?limit=20');
  await api.getSessionTasks('s1');
  assert.equal(last().url, '/api/sessions/s1/tasks');
  await api.getHistory({ page: 2, pageSize: 25 });
  assert.equal(last().url, '/api/history?page=2&page_size=25');
  await api.getOverview();
  assert.equal(last().url, '/api/overview');
  await api.listSessions({ limit: 10 });
  assert.equal(last().url, '/api/sessions?limit=10');
  await api.deleteSession('s1');
  assert.deepEqual([last().url, last().options.method], ['/api/sessions/s1', 'DELETE']);
  await api.clearSessions();
  assert.deepEqual([last().url, last().options.method], ['/api/sessions/clear', 'POST']);
});

test('run control payloads keep the server field names and drop undefined fields', async () => {
  await api.startSession({ prompt: 'do it', provider: 'openai', model: 'gpt', goal: { objective: 'x' }, planMode: true });
  assert.equal(last().url, '/api/sessions/start');
  assert.equal(last().options.headers['Content-Type'], 'application/json');
  assert.deepEqual(bodyOf(last()), { prompt: 'do it', provider: 'openai', model: 'gpt', goal: { objective: 'x' }, plan_mode: true });
  await api.continueSession('s1', { message: 'more', planMode: false });
  assert.deepEqual([last().url, bodyOf(last())], ['/api/sessions/s1/continue', { message: 'more', plan_mode: false }]);
  await api.steerSession('s1', { message: 'turn left', interrupt: 1 });
  assert.deepEqual([last().url, bodyOf(last())], ['/api/sessions/s1/steer', { message: 'turn left', interrupt: true }]);
  await api.interruptSession('s1');
  assert.deepEqual([last().url, last().options.method, last().options.body], ['/api/sessions/s1/interrupt', 'POST', undefined]);
  await api.stopSession('s1');
  assert.equal(last().url, '/api/sessions/s1/stop');
});

test('plan mode, approval receipt, goal and mission endpoints', async () => {
  const cases = [
    [() => api.getPlanMode('s1'), '/api/sessions/s1/planmode', 'GET'],
    [() => api.approvePlanMode('s1', { approval_request_id: 'r1' }), '/api/sessions/s1/planmode/approve', 'POST', { approval_request_id: 'r1' }],
    [() => api.revisePlanMode('s1', 'change it'), '/api/sessions/s1/planmode/revise', 'POST', { message: 'change it' }],
    [() => api.cancelPlanMode('s1'), '/api/sessions/s1/planmode/cancel', 'POST', {}],
    [() => api.answerPlanModeInput('s1', { requestID: 'q1', answers: [{ id: 'a' }] }), '/api/sessions/s1/planmode/input', 'POST', { request_id: 'q1', answers: [{ id: 'a' }] }],
    [() => api.getApprovalReceipt('s1', 'req/1'), '/api/sessions/s1/approval-receipts/req%2F1', 'GET'],
    [() => api.getGoal('s1'), '/api/sessions/s1/goal', 'GET'],
    [() => api.createGoal('s1', { objective: 'ship' }), '/api/sessions/s1/goal', 'POST', { objective: 'ship' }],
    [() => api.updateGoal('s1', { objective: 'ship' }), '/api/sessions/s1/goal', 'PATCH', { objective: 'ship' }],
    [() => api.deleteGoal('s1'), '/api/sessions/s1/goal', 'DELETE', {}],
    [() => api.completeGoal('s1'), '/api/sessions/s1/goal/complete', 'POST'],
    [() => api.pauseGoal('s1'), '/api/sessions/s1/goal/pause', 'POST'],
    [() => api.resumeGoal('s1'), '/api/sessions/s1/goal/resume', 'POST'],
    [() => api.patchMissionPlan('s1', { items: [] }), '/api/sessions/s1/mission/plan', 'PATCH', { items: [] }],
    [() => api.approveMissionPlan('s1', {}), '/api/sessions/s1/mission/plan/approve', 'POST', {}],
    [() => api.patchMissionValidation('s1', { status: 'ok' }), '/api/sessions/s1/mission/validation', 'PATCH', { status: 'ok' }],
    [() => api.getQueueJob('job 1'), '/api/queue/jobs/job%201', 'GET'],
    [() => api.listQueueJobs({ limit: 5 }), '/api/queue/jobs?limit=5', 'GET'],
  ];
  for (const [call, url, method, body] of cases) {
    await call();
    assert.equal(last().url, url);
    assert.equal(last().options.method, method, url);
    if (body !== undefined) assert.deepEqual(bodyOf(last()), body, url);
  }
});

test('skills, workspace and settings endpoints', async () => {
  await api.listSkills();
  assert.equal(last().url, '/api/skills');
  const file = new Blob(['zip'], { type: 'application/zip' });
  await api.uploadSkill(file);
  assert.equal(last().url, '/api/skills/upload');
  assert.ok(last().options.body instanceof FormData);
  assert.equal(last().options.headers['Content-Type'], undefined, 'browser sets multipart boundary');
  await api.installSkill('my skill');
  assert.deepEqual([last().url, last().options.method], ['/api/skills/my%20skill/install', 'POST']);
  await api.uninstallSkill('s');
  assert.deepEqual([last().url, last().options.method], ['/api/skills/s/uninstall', 'POST']);

  await api.listWorkspaceFiles('');
  assert.equal(last().url, '/api/files?path=.');
  await api.readWorkspaceFile('a b.txt', 10, 20);
  assert.equal(last().url, '/api/file/read?path=a%20b.txt&offset=10&limit=20');
  await api.createWorkspaceDirectory('', 'new');
  assert.deepEqual(bodyOf(last()), { path: '.', name: 'new' });
  await api.uploadWorkspaceFile('docs', file);
  assert.equal(last().options.body.get('path'), 'docs');
  await api.renameWorkspaceFile('a.txt', 'b.txt');
  assert.deepEqual([last().url, last().options.method, bodyOf(last())], ['/api/files/rename', 'PATCH', { path: 'a.txt', name: 'b.txt' }]);
  await api.deleteWorkspacePath('x/y');
  assert.deepEqual([last().url, last().options.method], ['/api/files?path=x%2Fy', 'DELETE']);
  await api.deleteWorkspacePaths(['a', 'b']);
  assert.deepEqual(bodyOf(last()), { paths: ['a', 'b'] });
  assert.equal(api.workspaceDownloadURL('a b'), '/api/file/download?path=a%20b');

  await api.getConfig();
  assert.equal(last().url, '/api/config');
  await api.saveConfig({ provider: 'openai', guardrailsMode: 'strict', apiKey: 'k', legacyUIEnabled: true });
  assert.deepEqual(bodyOf(last()), { provider: 'openai', guardrails_mode: 'strict', api_key: 'k' });
  await api.saveConfig({ childBudget: { disabled: false, maxActiveRuntimeSec: 60, maxElapsedSec: 0, maxTurnsPerAttempt: 3 } });
  assert.deepEqual(bodyOf(last()).child_budget, { disabled: false, max_active_runtime_sec: 60, max_elapsed_sec: 0, max_turns_per_attempt: 3 });
  await api.testConfig({ provider: 'openai', baseURL: 'http://x', model: 'm' });
  assert.deepEqual([last().url, bodyOf(last())], ['/api/config/test', { provider: 'openai', base_url: 'http://x', model: 'm' }]);
});

test('the retired legacy UI setting is never sent', async () => {
  await api.saveConfig({ legacyUIEnabled: true, legacy_ui_enabled: true });
  assert.ok(!('legacy_ui_enabled' in bodyOf(last())));
});
