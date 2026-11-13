// Inspector i18n coverage: every key family the lib modules emit under the
// prefixes owned by src/i18n/inspector.js, the inspector's own key maps, and
// translated (not copied) zh-CN text.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import inspector from '../../internal/webconsole/ui/src/i18n/inspector.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';
import { TASK_GROUP_ORDER } from '../../internal/webconsole/ui/src/lib/session-model.js';
import { INSPECTOR_TABS } from '../../internal/webconsole/ui/src/state/prefs.js';
import {
  CONTEXT_LINEAGE_METRICS, CONTEXT_PEAK_METRICS, CONTEXT_USAGE_METRICS, HANDLE_STATE_KEYS, TAB_LABEL_KEYS, providerOptionRows,
} from '../../internal/webconsole/ui/src/features/inspector/inspector-model.js';

const OWNED_PREFIXES = ['inspector.', ...inspector.prefixes];

// Keys declared in the lib/session-model.js header under the inspector-owned prefixes.
const LIB_KEYS = [
  ...['providerCall', 'requestPrepared', 'assistantMessage', 'toolStarted', 'toolFinished', 'toolBlocked', 'toolInterrupted', 'childQueued', 'childSpawned', 'queueClaimed', 'queueNotified', 'providerCancelled', 'runStopped', 'runPaused', 'runCancelled', 'sessionCompleted', 'sessionFailed', 'sessionAwaitingInput']
    .flatMap((name) => ['event.' + name, 'event.' + name + '.copy']),
  'event.toolFallback', 'event.queueCompleted', 'event.queueCancelled', 'event.queueFailed', 'event.queueRole', 'event.queueChanged',
  'event.goal.copy', 'event.mission.planUpdated', 'event.mission.planApproved', 'event.mission.validationUpdated', 'event.mission.changed', 'event.mission.planStatus',
  ...['created', 'inputRequested', 'inputAnswered', 'inputCancelled', 'planSubmitted', 'planApproved', 'planRevised', 'cancelled', 'executionStarted', 'awaitingApproval', 'changed'].map((name) => 'event.planmode.' + name),
  'event.planmode.executionStarted.copy', 'event.planmode.questions', 'event.planVersion',
  'event.steerRequested', 'event.steerQueued', 'event.steerAccepted', 'event.steerDeferred', 'event.steerInterruptRequested', 'event.steer.copy', 'event.recorded',
  'timeline.backgroundResults', 'timeline.backgroundResults.copy', 'timeline.toolResults', 'timeline.toolResults.copy', 'timeline.emptyMessage',
  ...['background_results', 'harness_reminder', 'steer', 'user', 'system', 'tool', 'assistant'].map((variant) => 'timeline.message.' + variant),
  'tasks.untitledTask', 'tasks.untitledTodo', 'tasks.noDescription', 'tasks.blockedBy', 'tasks.blocks',
  ...TASK_GROUP_ORDER.map((group) => 'tasks.group.' + group),
  'files.partial', 'files.disclosure',
  'budget.unknown', 'budget.offSnapshotted', 'budget.attempt', 'budget.turns', 'budget.active', 'budget.checkpointLeaseOpen', 'budget.checkpointLeaseClosed', 'budget.crashRecovery', 'budget.crashRecoveryAt', 'budget.deadline',
  ...['active', 'disabled', 'exhausted', 'cancelled'].map((status) => 'budgetStatus.' + status),
  'subagent.childAgent',
  'notification.fallbackTitle', 'notification.noFinalText', 'notification.pendingDelivery', 'notification.pendingHint',
  ...['extend_resume', 'cancel_settle', 'inspect'].map((action) => 'notification.action.' + action),
  ...['active', 'paused', 'budget_limited', 'complete', 'cleared'].map((status) => 'goalStatus.' + status),
  ...['draft', 'needs_approval', 'approved', 'planning', 'awaiting_user_input', 'awaiting_approval', 'rejected', 'cancelled', 'executing'].map((status) => 'planStatus.' + status),
  ...['prepare', 'turn_decide', 'provider_call', 'tool_execute', 'assistant_output', 'control_drain', 'interrupt', 'compact', 'delegate', 'queue', 'background_wait', 'plan_input'].map((phase) => 'phase.' + phase),
  ...['goal.created', 'goal.updated', 'goal.paused', 'goal.resumed', 'goal.budget_limited', 'goal.budget_wrapup_required', 'goal.progress.recorded', 'goal.completed', 'goal.cleared', 'event']
    .map((type) => 'eventType.' + type),
];

// Params each lib descriptor passes (placeholders must be a subset).
const LIB_PARAMS = {
  'event.mission.planStatus': ['status', 'statusLabel'],
  'event.planmode.questions': ['count'],
  'event.planVersion': ['version'],
  'tasks.blockedBy': ['count'],
  'tasks.blocks': ['count'],
  'budget.attempt': ['attempt'],
  'budget.turns': ['used', 'limit', 'remaining'],
  'budget.active': ['usedSec', 'limitSec', 'remainingSec'],
  'budget.checkpointLeaseOpen': ['at'],
  'budget.checkpointLeaseClosed': ['at'],
  'budget.crashRecovery': ['value', 'at'],
  'budget.crashRecoveryAt': ['value', 'at'],
  'budget.deadline': ['at'],
};

const placeholders = (text) => [...String(text).matchAll(/\{(\w+)\}/g)].map((match) => match[1]);

test('inspector dictionary covers every lib key family it owns', () => {
  for (const locale of ['zh-CN', 'en']) {
    const missing = LIB_KEYS.filter((key) => !Object.prototype.hasOwnProperty.call(inspector[locale], key));
    assert.deepEqual(missing, [], locale + ' missing lib keys');
  }
  for (const key of LIB_KEYS) {
    assert.ok(OWNED_PREFIXES.some((prefix) => key.startsWith(prefix)), key + ' must belong to an inspector prefix');
  }
});

test('lib descriptor params cover every placeholder', () => {
  for (const [key, params] of Object.entries(LIB_PARAMS)) {
    for (const locale of ['zh-CN', 'en']) {
      const unknown = placeholders(inspector[locale][key]).filter((name) => !params.includes(name));
      assert.deepEqual(unknown, [], locale + ' ' + key + ' uses placeholders the lib never passes');
    }
  }
});

test('inspector key maps resolve in both locales', () => {
  const keys = [
    ...INSPECTOR_TABS.map((id) => TAB_LABEL_KEYS[id]),
    ...Object.values(HANDLE_STATE_KEYS),
    ...CONTEXT_PEAK_METRICS.map((metric) => metric.key),
    ...CONTEXT_USAGE_METRICS.map((metric) => metric.key),
    ...CONTEXT_LINEAGE_METRICS.map((metric) => metric.key),
    ...providerOptionRows({
      api_provider: 'x', reasoning_effort: 'x', reasoning_summary: 'x', text_verbosity: 'x', max_output_tokens: 1, context_window_tokens: 1,
      thinking_budget: 1, temperature: 1, top_p: 1, include_thoughts: true, prompt_cache: true, store: true, base_url: 'x',
      retry_policy: { max_attempts: 1 }, timeout_policy: { request_timeout_sec: 1, stream_idle_timeout_ms: 1 },
    }).map((row) => row.key),
  ];
  assert.equal(keys.length, INSPECTOR_TABS.length + 3 + 6 + 4 + 6 + 16);
  for (const key of keys) {
    assert.equal(typeof key, 'string');
    for (const locale of ['zh-CN', 'en']) assert.ok(Object.prototype.hasOwnProperty.call(dictionaries[locale], key), locale + ' missing ' + key);
  }
});

test('inspector feature sources only use literal t() keys that exist', () => {
  const dir = fileURLToPath(new URL('../../internal/webconsole/ui/src/features/inspector/', import.meta.url));
  const used = new Set();
  for (const name of readdirSync(dir).filter((file) => file.endsWith('.js'))) {
    const source = readFileSync(dir + name, 'utf8');
    assert.equal(/(?<![\w$.])t\(\s*`[^`]*\$\{/.test(source), false, name + ' must not build t() keys from templates');
    for (const match of source.matchAll(/'(inspector\.[A-Za-z0-9_.]+)'/g)) used.add(match[1]);
  }
  assert.ok(used.size > 100, 'scan found inspector keys');
  for (const key of used) {
    for (const locale of ['zh-CN', 'en']) assert.ok(Object.prototype.hasOwnProperty.call(dictionaries[locale], key), locale + ' missing ' + key);
  }
});

test('zh-CN inspector copy is translated, not copied from English', () => {
  const identical = Object.keys(inspector['zh-CN'])
    .filter((key) => inspector['zh-CN'][key] === inspector.en[key])
    .filter((key) => !['inspector.option.topP'].includes(key));
  assert.deepEqual(identical, []);
  assert.equal(inspector['zh-CN']['phase.turn_decide'], '等待模型决策');
  assert.equal(inspector.en['phase.turn_decide'], 'Awaiting model decision');
  assert.equal(inspector['zh-CN']['tasks.noDescription'], '无描述。');
  assert.equal(inspector.en['tasks.noDescription'], 'No description.');
});
