import test from 'node:test';
import assert from 'node:assert/strict';
import {
  isAttentionStatus, planModeActionDisplayStatus, sessionDetailDisplayStatus, sessionDetailHasActiveDescendants,
  sessionHasCurrentProcessHandle, sessionHasDirectControl, sessionSummaryDisplayStatus, statusLabelKey, statusTone,
} from '../../internal/webconsole/ui/src/lib/status.js';

test('plan-mode actions outrank the runtime status', () => {
  assert.equal(planModeActionDisplayStatus('awaiting_approval'), 'awaiting_plan_approval');
  assert.equal(planModeActionDisplayStatus('AWAITING_USER_INPUT'), 'awaiting_plan_input');
  assert.equal(planModeActionDisplayStatus('executing'), '');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'running' }, plan_mode: { status: 'awaiting_approval' }, active_handle: true }), 'awaiting_plan_approval');
  assert.equal(sessionSummaryDisplayStatus({ status: 'paused', plan_mode_status: 'awaiting_user_input' }), 'awaiting_plan_input');
  assert.equal(sessionSummaryDisplayStatus({ status: 'failed' }), 'failed');
  assert.equal(sessionSummaryDisplayStatus({}), 'unknown');
});

test('a current-process handle means running unless completed', () => {
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'paused' }, active_handle: true }), 'running');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'awaiting_input' }, active_handle_owner: { owned_by_current_process: true } }), 'running');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'completed' }, active_handle: true }), 'completed');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'failed' } }), 'failed');
  assert.equal(sessionDetailDisplayStatus(null), 'unknown');
  assert.equal(sessionHasCurrentProcessHandle({ active_handle: false, active_handle_owner: { owned_by_current_process: false } }), false);
  assert.equal(sessionHasDirectControl({ active_handle: true, active_handle_owner: { owned_by_current_process: false } }), false);
  assert.equal(sessionHasDirectControl({ active_handle: true, active_handle_owner: { owned_by_current_process: true } }), true);
});

test('active descendants are queued/pending/running children or jobs', () => {
  assert.equal(sessionDetailHasActiveDescendants({ children: { sessions: [{ status: 'completed' }], jobs: [] } }), false);
  assert.equal(sessionDetailHasActiveDescendants({ children: { sessions: [{ status: 'running' }] } }), true);
  assert.equal(sessionDetailHasActiveDescendants({ children: { jobs: [{ status: 'done', session_status: 'queued' }] } }), true);
  assert.equal(sessionDetailHasActiveDescendants({}), false);
});

test('tone mapping follows the design contract', () => {
  assert.equal(statusTone('running'), 'info');
  for (const status of ['awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input', 'paused']) assert.equal(statusTone(status), 'warning', status);
  assert.equal(statusTone('completed'), 'success');
  assert.equal(statusTone('failed'), 'danger');
  for (const status of ['cancelled', 'queued', 'unknown', '', undefined, 'weird']) assert.equal(statusTone(status), 'neutral', String(status));
});

test('attention group and label keys', () => {
  for (const status of ['running', 'awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input']) assert.ok(isAttentionStatus(status), status);
  for (const status of ['paused', 'completed', 'failed', 'queued']) assert.ok(!isAttentionStatus(status), status);
  assert.equal(statusLabelKey('Running'), 'common.status.running');
  assert.equal(statusLabelKey('something_new'), '');
});
