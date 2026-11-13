import test from 'node:test';
import assert from 'node:assert/strict';
import { initialState } from '../../internal/webconsole/ui/src/state/store.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';
import {
  canSubmit, composerMode, effectiveToggles, placeholderKey, primaryAction, primaryLabelKey,
} from '../../internal/webconsole/ui/src/features/session/controls/composer-model.js';

function state({ route = 'session', status = '', planStatus = '', generating = false, detail = true, composer = {} } = {}) {
  const base = initialState();
  return {
    ...base,
    route: route === 'new' ? { name: 'new', path: '/', params: {} } : { name: 'session', path: '/s/a', params: { id: 'a' } },
    session: { ...base.session, id: route === 'new' ? '' : 'a', generating, detail: detail ? { state: { status }, plan_mode: planStatus ? { status: planStatus, plan_version: 3 } : null } : null },
    composer: { ...base.composer, ...composer },
  };
}

test('composer mode follows the send route', () => {
  assert.equal(composerMode(state({ route: 'new' })).kind, 'start');
  assert.equal(composerMode(state({ detail: false })).kind, 'loading');
  for (const status of ['paused', 'awaiting_input', 'failed', 'completed']) assert.equal(composerMode(state({ status })).kind, 'continue');
  assert.equal(composerMode(state({ status: 'running', generating: true })).kind, 'steer');
  assert.equal(composerMode(state({ status: 'queued' })).kind, 'blocked');
  const revise = composerMode(state({ status: 'awaiting_input', planStatus: 'awaiting_approval' }));
  assert.equal(revise.kind, 'revise');
  assert.equal(revise.planVersion, 3);
  assert.equal(revise.planAwaitingApproval, true);
});

test('toolbar controls only where they apply', () => {
  const start = composerMode(state({ route: 'new' }));
  assert.deepEqual([start.canGoal, start.canPlan, start.canInterrupt, start.canPickModel], [true, true, false, true]);
  const cont = composerMode(state({ status: 'paused' }));
  assert.deepEqual([cont.canGoal, cont.canPlan, cont.canInterrupt, cont.canPickModel], [false, true, false, true]);
  const pending = composerMode(state({ status: 'awaiting_input', planStatus: 'awaiting_user_input' }));
  assert.equal(pending.canPlan, false);
  assert.equal(pending.planInputPending, true);
  const steer = composerMode(state({ status: 'running', generating: true }));
  assert.deepEqual([steer.canGoal, steer.canPlan, steer.canInterrupt, steer.canPickModel], [false, false, true, false]);
  assert.deepEqual(effectiveToggles(steer, { goalEnabled: true, planEnabled: true, interrupt: true }), { goal: false, plan: false, interrupt: true });
});

test('placeholder and primary action', () => {
  const start = composerMode(state({ route: 'new' }));
  assert.equal(placeholderKey(start, { goalEnabled: true }), 'composer.placeholder.goal');
  assert.equal(placeholderKey(start, { planEnabled: true }), 'composer.placeholder.plan');
  assert.equal(placeholderKey(start, {}), 'composer.placeholder.new');
  assert.equal(placeholderKey(composerMode(state({ status: 'completed' })), {}), 'composer.placeholder.followUp');
  const steer = composerMode(state({ status: 'running', generating: true }));
  assert.equal(placeholderKey(steer, { interrupt: true }), 'composer.placeholder.interrupt');
  assert.equal(primaryAction(steer, ''), 'stop');
  assert.equal(primaryAction(steer, '  '), 'stop');
  assert.equal(primaryAction(steer, 'more'), 'steer');
  assert.equal(primaryAction(start, ''), 'send');
  assert.equal(primaryLabelKey('send', start), 'composer.action.start');
  assert.equal(canSubmit(start, { text: 'x', submitting: false }), true);
  assert.equal(canSubmit(start, { text: 'x', submitting: true }), false);
  assert.equal(canSubmit(composerMode(state({ status: 'queued' })), { text: 'x' }), false);
});

test('every key the composer model can return exists in both locales', () => {
  const statuses = ['', 'paused', 'awaiting_input', 'failed', 'completed', 'running', 'queued'];
  const keys = new Set();
  for (const route of ['new', 'session']) {
    for (const status of statuses) {
      for (const planStatus of ['', 'awaiting_approval', 'awaiting_user_input']) {
        for (const generating of [false, true]) {
          for (const detail of [true, false]) {
            const mode = composerMode(state({ route, status, planStatus, generating, detail }));
            for (const composer of [{}, { goalEnabled: true }, { planEnabled: true }, { interrupt: true }]) keys.add(placeholderKey(mode, composer));
            for (const text of ['', 'x']) keys.add(primaryLabelKey(primaryAction(mode, text), mode));
          }
        }
      }
    }
  }
  for (const key of keys) {
    for (const locale of ['zh-CN', 'en']) assert.ok(Object.hasOwn(dictionaries[locale], key), locale + ' missing ' + key);
  }
});
