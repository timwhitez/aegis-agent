// Toasts keep backend text raw (spec/17-web-console-frontend.md §9): the
// message/detail/action of an API error render inside translate="no" nodes,
// separate from the translatable operator copy and labels.
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';
import '../../internal/webconsole/ui/src/i18n/index.js';
import { setLocale, t } from '../../internal/webconsole/ui/src/lib/i18n.js';
import { APIError } from '../../internal/webconsole/ui/src/lib/api.js';
import { getState, setState } from '../../internal/webconsole/ui/src/state/store.js';
import { dismissToast, pushToast, toastError, toastRawParts } from '../../internal/webconsole/ui/src/state/toasts.js';
import { ToastBody } from '../../internal/webconsole/ui/src/ui/Toasts.js';

afterEach(() => {
  for (const toast of getState().toasts) dismissToast(toast.id);
  setState({ toasts: [] });
  setLocale('zh-CN', { persist: false });
});

/** Flattens a vnode tree into text runs: { text, raw } where raw = inside translate="no". */
function textRuns(node, raw = false, out = []) {
  if (node === null || node === undefined || typeof node === 'boolean') return out;
  if (typeof node === 'string' || typeof node === 'number') {
    if (String(node).trim()) out.push({ text: String(node), raw });
    return out;
  }
  if (Array.isArray(node)) {
    node.forEach((child) => textRuns(child, raw, out));
    return out;
  }
  const inside = raw || node.props?.translate === false;
  textRuns(node.props?.children, inside, out);
  return out;
}

const latest = () => getState().toasts.at(-1);

test('toastRawParts normalizes true / per-part objects / nothing', () => {
  assert.deepEqual(toastRawParts(true), { message: true, detail: true, action: true });
  assert.deepEqual(toastRawParts({ detail: true }), { message: false, detail: true, action: false });
  assert.deepEqual(toastRawParts(undefined), { message: false, detail: false, action: false });
});

test('toastError marks the backend message/detail/action raw; fallback copy is not raw', () => {
  toastError(new APIError('session is not resumable', { status: 409, detail: 'session s1 is already running', action: 'Send steer to the active run.' }), 'composer.plan.cancelFailed');
  assert.deepEqual(latest().raw, { message: true, detail: true, action: true });
  assert.equal(latest().message, 'session is not resumable');
  toastError(new APIError('', { status: 0 }));
  assert.equal(latest().message, t('common.error.network'));
  assert.equal(latest().raw.message, false);
  toastError(new APIError('', { status: 500 }), 'composer.plan.cancelFailed');
  assert.equal(latest().message, t('composer.plan.cancelFailed'));
  assert.equal(latest().raw.message, false);
  pushToast({ tone: 'success', message: t('composer.plan.cancelled'), timeout: 0 });
  assert.deepEqual(latest().raw, { message: false, detail: false, action: false }, 'plain copy toasts stay translatable');
});

test('ToastBody renders raw parts in translate="no" nodes, labels and copy outside', () => {
  for (const locale of ['zh-CN', 'en']) {
    setLocale(locale, { persist: false });
    const toast = {
      message: 'session is not resumable',
      detail: 'session s1 is already running',
      action: 'Send steer to the active run.',
      raw: toastRawParts(true),
    };
    const runs = textRuns(ToastBody({ toast }));
    const rawTexts = runs.filter((run) => run.raw).map((run) => run.text);
    const copyTexts = runs.filter((run) => !run.raw).map((run) => run.text.trim());
    assert.deepEqual(rawTexts, [toast.message, toast.detail, toast.action], locale);
    assert.deepEqual(copyTexts, [t('common.error.detail', { detail: '' }).trim(), t('common.error.action', { action: '' }).trim()], locale + ': labels stay translatable');
  }
});

test('ToastBody keeps operator copy translatable and mixes raw detail with a copy action', () => {
  setLocale('en', { persist: false });
  const toast = {
    message: t('composer.stop.notOwned'),
    detail: 'owned by another process',
    action: t('composer.stop.notOwnedAction'),
    raw: toastRawParts({ detail: true }),
  };
  const runs = textRuns(ToastBody({ toast }));
  assert.deepEqual(runs.filter((run) => run.raw).map((run) => run.text), ['owned by another process']);
  assert.ok(runs.some((run) => !run.raw && run.text === t('composer.stop.notOwned')), 'copy message is translatable');
  assert.ok(runs.some((run) => !run.raw && run.text === t('common.error.action', { action: t('composer.stop.notOwnedAction') })), 'copy action is interpolated into its label');
  const plain = textRuns(ToastBody({ toast: { message: 'Saved', detail: '', action: '' } }));
  assert.deepEqual(plain, [{ text: 'Saved', raw: false }], 'toasts without a raw marker render as copy');
});
