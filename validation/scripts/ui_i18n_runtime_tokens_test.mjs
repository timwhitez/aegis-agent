// Runtime token coverage (spec/17-web-console-frontend.md §9, spec/17-web-console.md
// §4.5): every event type and session phase the Go runtime can emit has an
// inspector-owned eventType.<type> / phase.<phase> entry in both locales, so the
// zh-CN Timeline / Overview never fall back to humanized English ("Task created",
// "Plan approval"). The tokens are scanned from the non-test Go sources under
// internal/, not listed by hand.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import inspector from '../../internal/webconsole/ui/src/i18n/inspector.js';

const INTERNAL = fileURLToPath(new URL('../../internal/', import.meta.url));
const TYPE = '([a-z][a-z0-9_]*(?:\\.[a-z0-9_]+)+)';
const PHASE = '([a-z][a-z0-9_]*)';
const CALL = '(?:\\bevents\\.New|\\b[\\w.]*(?:[Ee]vent|[Ee]mit)\\w*)\\(';

// Event types appear as the first/second literal argument of an event/emit
// helper (events.New, appendEvent, emit, emitToolEvent, recordLaunchHandleEvent,
// appendAuditEvent …), as history/event Type: fields, eventType variables or
// fields, Event* constants, and Type comparisons in readers.
const EVENT_RULES = [
  new RegExp(CALL + '\\s*(?:[^,()"]+,\\s*)?"' + TYPE + '"', 'g'),
  new RegExp('\\bType:\\s*"' + TYPE + '"', 'g'),
  new RegExp('\\b\\w*[Ee]ventType\\s*(?::=|=|:)\\s*"' + TYPE + '"', 'g'),
  new RegExp('\\b\\w*Event\\w*\\s*=\\s*"' + TYPE + '"', 'g'),
  new RegExp('Type\\s*[!=]=\\s*"' + TYPE + '"', 'g'),
];
// Some types reach those helpers through parameters (mutateGoalStatus(…,
// "goal.paused"), handleGoalStatus(…)), so every dotted literal in a namespace
// that the rules above proved to be an event namespace counts too, except file
// names and these non-event identifiers.
const NOT_EVENT_TYPES = new Set([
  // hook points (hooks/manager.go, doctor) — the events are hook.*
  'session.start', 'session.pause', 'session.fail', 'session.complete', 'session.cancel',
  // doctor / config check ids
  'provider.admission', 'provider.api_key_env', 'provider.config', 'provider.probe',
  'session.dir', 'session.dir.mode', 'session.partial_state', 'session.root.strategy',
  // completion controller error stage label (eventErrorDecision), not an event
  'artifact.gate',
]);
const FILE_NAME = /\.(?:json|jsonl|lock|md|ya?ml|sock|html|exe|conf|xml|properties)$/;
// Session phases: state.Phase assignments (approval receipt/preparation record
// phases are a different field and excluded), State literals and the phase
// argument of event helpers.
const RECORD_RECEIVER = /receipt|record|candidate/i;

function goFiles(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const file = path.join(dir, entry.name);
    if (entry.isDirectory()) goFiles(file, out);
    else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) out.push(file);
  }
  return out;
}

const sources = goFiles(INTERNAL).map((file) => readFileSync(file, 'utf8'));

function scanEventTypes() {
  const direct = new Set();
  const literals = new Set();
  for (const source of sources) {
    for (const rule of EVENT_RULES) for (const match of source.matchAll(rule)) direct.add(match[1]);
    for (const match of source.matchAll(new RegExp('"' + TYPE + '"', 'g'))) literals.add(match[1]);
  }
  const namespaces = new Set([...direct].map((type) => type.split('.')[0]));
  const all = new Set([...direct].filter((type) => !NOT_EVENT_TYPES.has(type)));
  for (const literal of literals) {
    if (namespaces.has(literal.split('.')[0]) && !FILE_NAME.test(literal) && !NOT_EVENT_TYPES.has(literal)) all.add(literal);
  }
  return { types: [...all].sort(), literals };
}

function scanPhases() {
  const phases = new Set();
  for (const source of sources) {
    for (const match of source.matchAll(new RegExp('\\b(\\w+)\\.Phase\\s*=\\s*"' + PHASE + '"', 'g'))) {
      if (!RECORD_RECEIVER.test(match[1])) phases.add(match[2]);
    }
    for (const match of source.matchAll(new RegExp('\\b(\\w+)\\.Status,\\s*\\1\\.Phase\\s*=\\s*[^,\\n]+,\\s*"' + PHASE + '"', 'g'))) phases.add(match[2]);
    for (const match of source.matchAll(new RegExp(CALL + '\\s*[^,()]+,\\s*(?:"[^"]*"|[\\w.]+),\\s*"' + PHASE + '"', 'g'))) phases.add(match[1]);
    for (const line of source.split('\n')) {
      const match = line.match(new RegExp('\\bPhase:\\s*"' + PHASE + '"'));
      if (match && !/Receipt|Preparation|approval/i.test(line)) phases.add(match[1]);
    }
  }
  return [...phases].sort();
}

const has = (locale, key) => Object.prototype.hasOwnProperty.call(inspector[locale], key) && String(inspector[locale][key]).trim() !== '';

test('the Go scan finds the runtime event types and phases', () => {
  const { types, literals } = scanEventTypes();
  for (const anchor of ['session.created', 'task.created', 'task.updated', 'todo.updated', 'goal.paused', 'webconsole.handle.acquired',
    'planmode.plan_submitted', 'completion.gate.blocked', 'hook.triggered', 'tool.before']) {
    assert.ok(types.includes(anchor), 'scan found ' + anchor);
  }
  for (const excluded of NOT_EVENT_TYPES) {
    assert.ok(literals.has(excluded), 'stale NOT_EVENT_TYPES entry ' + excluded);
    assert.ok(!types.includes(excluded), excluded + ' is not an event type');
  }
  const phases = scanPhases();
  for (const anchor of ['prepare', 'turn_decide', 'provider_call', 'tool_execute', 'plan_approval', 'plan_input', 'background_wait', 'interrupt']) {
    assert.ok(phases.includes(anchor), 'scan found phase ' + anchor);
  }
  for (const receiptPhase of ['validated', 'admitted', 'claim_pending']) assert.ok(!phases.includes(receiptPhase), receiptPhase + ' is an approval record phase');
});

test('every Go-emitted event type has eventType.<type> in zh-CN and en', () => {
  const { types } = scanEventTypes();
  for (const locale of ['zh-CN', 'en']) {
    assert.deepEqual(types.filter((type) => !has(locale, 'eventType.' + type)), [], locale + ' missing eventType.* entries');
  }
  for (const type of types) assert.notEqual(inspector['zh-CN']['eventType.' + type], inspector.en['eventType.' + type], 'zh-CN eventType.' + type + ' is translated');
});

test('every Go-set session phase has phase.<phase> in zh-CN and en', () => {
  const phases = scanPhases();
  for (const locale of ['zh-CN', 'en']) {
    assert.deepEqual(phases.filter((phase) => !has(locale, 'phase.' + phase)), [], locale + ' missing phase.* entries');
  }
  for (const phase of phases) assert.notEqual(inspector['zh-CN']['phase.' + phase], inspector.en['phase.' + phase], 'zh-CN phase.' + phase + ' is translated');
});
