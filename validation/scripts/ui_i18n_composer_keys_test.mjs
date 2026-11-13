// Composer area i18n coverage: every send./kind./approval. key declared in
// the lib module headers, every key the lib helpers can emit, and every
// literal 'composer.' key in the composer-owned sources exists in both
// locales of src/i18n/composer.js.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import composer from '../../internal/webconsole/ui/src/i18n/composer.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';
import { SEND_KINDS, failedDraftView, sendSuccessFeedback, resolveSendRoute, SendRecoveryStore } from '../../internal/webconsole/ui/src/lib/send-recovery.js';
import { approvalNoticeState, approvalOutcomeNotice, coverageOverrideConfirmation } from '../../internal/webconsole/ui/src/lib/approval-operations.js';
import { KNOWN_STATUSES } from '../../internal/webconsole/ui/src/lib/status.js';

const UI = fileURLToPath(new URL('../../internal/webconsole/ui/src/', import.meta.url));
const OWN_PREFIXES = ['composer.', 'send.', 'kind.', 'approval.'];
const LOCALES = ['zh-CN', 'en'];

/** Keys under prefixes listed after "i18n keys emitted" in a lib header ('.x' continues the previous key's parent). */
export function headerKeys(source, prefixes) {
  const end = source.indexOf('*/');
  const header = source.slice(0, end > 0 ? end : 0);
  const start = header.indexOf('i18n keys emitted');
  const keys = new Set();
  if (start < 0) return keys;
  let last = '';
  for (const raw of header.slice(start).split('\n').slice(1)) {
    const line = raw.replace(/^\s*\*\s?/, '');
    for (const token of line.split(/[\s|,;]+/)) {
      let match = /^([a-z]+(?:\.[A-Za-z_]+)+)/.exec(token);
      if (match) {
        last = match[1];
        if (prefixes.some((prefix) => last.startsWith(prefix))) keys.add(last);
        continue;
      }
      match = /^\.([A-Za-z_]+)/.exec(token);
      if (match && last) {
        last = last.slice(0, last.lastIndexOf('.')) + '.' + match[1];
        if (prefixes.some((prefix) => last.startsWith(prefix))) keys.add(last);
      }
    }
  }
  return keys;
}

function assertKeys(keys, label) {
  const missing = [];
  for (const key of keys) for (const locale of LOCALES) if (!Object.hasOwn(composer[locale], key)) missing.push(locale + ' ' + key);
  assert.deepEqual(missing, [], label + ' missing:\n' + missing.join('\n'));
}

test('composer area claims the lib key families it renders', () => {
  assert.deepEqual([...composer.prefixes].sort(), ['approval.', 'kind.', 'send.']);
});

test('every send./kind./approval. key declared in lib headers exists', () => {
  const lib = UI + 'lib/';
  const declared = new Set();
  for (const name of readdirSync(lib).filter((file) => file.endsWith('.js'))) {
    for (const key of headerKeys(readFileSync(lib + name, 'utf8'), OWN_PREFIXES)) declared.add(key);
  }
  for (const expected of ['send.draft.unconfirmed', 'kind.revise', 'approval.notice.recovery_required', 'approval.notice.failed', 'approval.planChanged', 'approval.continueHint']) {
    assert.ok(declared.has(expected), 'header scan found ' + expected);
  }
  assert.ok(declared.size >= 50, 'header scan found ' + declared.size + ' keys');
  assertKeys(declared, 'lib header keys');
});

test('keys emitted by the lib helpers exist', () => {
  const keys = new Set(SEND_KINDS.map((kind) => 'kind.' + kind));
  for (const kind of SEND_KINDS) {
    for (const interrupt of [false, true]) {
      for (const planDraft of [false, true]) {
        const toast = sendSuccessFeedback(kind, { interrupt, planDraft }).toast;
        if (toast) keys.add(toast.key);
      }
    }
  }
  for (const status of ['failed', 'unconfirmed']) {
    for (const ephemeral of [false, true]) {
      const view = failedDraftView({ id: 'd', kind: 'start', status, ephemeral, sessionId: 'x', rawText: 'y' });
      [view.title, view.kind, view.session, view.notice, view.restore].forEach((slot) => keys.add(slot.key));
    }
  }
  keys.add(resolveSendRoute({ rawText: 'x', sessionId: '', launchInFlight: true }).notice.key);
  keys.add(resolveSendRoute({ rawText: 'x', sessionId: 's', backed: true, status: 'queued' }).notice.key);
  const store = new SendRecoveryStore();
  const intent = store.begin({ session: { sessionId: 's', backed: true }, kind: 'continue', rawText: 'x' });
  store.fail(intent.id, { status: 0 });
  keys.add(store.restore(intent.id, { composerValue: 'busy' }).notice.key);
  keys.add(store.restore(intent.id).notice.key);
  for (const phase of ['pending', 'unknown', 'not_found', 'prepared', 'admitted', 'rejected', 'recovery_required', 'failed', 'weird']) {
    const notice = approvalNoticeState({ record: { phase, approval_request_id: 'r' }, loadedStatus: 'running' });
    keys.add(notice.title.key);
    keys.add(notice.message.key);
    keys.add(notice.currentStatus.key);
    notice.actions.forEach((action) => keys.add(action.label.key));
  }
  for (const outcome of ['busy', 'override_declined', 'approved', 'continue']) keys.add(approvalOutcomeNotice(outcome).key);
  const coverage = coverageOverrideConfirmation();
  [coverage.title, coverage.message, coverage.confirm].forEach((slot) => keys.add(slot.key));
  assertKeys(keys, 'lib helper keys');
});

test('approval receipt status labels resolve through common.status.*', () => {
  for (const status of KNOWN_STATUSES) {
    for (const locale of LOCALES) assert.ok(Object.hasOwn(dictionaries[locale], 'common.status.' + status), locale + ' common.status.' + status);
  }
});

test('every literal composer key in composer-owned sources exists (incl. non-t() call sites)', () => {
  const files = [
    'state/session-actions.js',
    ...['Composer.js', 'ApprovalCard.js', 'PlanInputCard.js', 'GoalBanner.js', 'FailedDrafts.js', 'TodoStrip.js'].map((name) => 'features/session/' + name),
    ...readdirSync(UI + 'features/session/controls').filter((name) => name.endsWith('.js')).map((name) => 'features/session/controls/' + name),
  ];
  const keys = new Set();
  for (const file of files) {
    for (const match of readFileSync(UI + file, 'utf8').matchAll(/'((?:composer|send|kind|approval)\.[A-Za-z0-9_.]+)'/g)) {
      if (!match[1].endsWith('.')) keys.add(match[1]);
    }
  }
  assert.ok(keys.size > 100, 'scan found ' + keys.size + ' keys');
  assertKeys(keys, 'source keys');
});

test('approval.planChanged keeps the spec wording in both locales', () => {
  assert.equal(composer['zh-CN']['approval.planChanged'], '计划已更新，请重新审阅');
  assert.equal(composer.en['approval.planChanged'], 'The plan has changed. Please review it again.');
});
