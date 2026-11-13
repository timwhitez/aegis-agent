// Per-session composer drafts (memory only, never persisted).
//
// The composer slice always shows the draft of one "owner": the open session
// ('s:<id>') or the new-session composer ('new:<ephemeral id>'). Switching to
// another session stashes the owner's text + toggles and restores the target's
// draft, so returning to a session brings its unsent text back. The
// new-session draft is separate and is reset by actions.newSession(), which
// also issues a new ephemeral identity (send-recovery.js binds new-session
// sends and failed drafts to it). Pages (/sessions, /settings …) keep the
// current owner. composer.submitting reflects in-flight sends of the owner.
import { nextEphemeralSessionId } from '../lib/send-recovery.js';
import { getState, update } from './store.js';

export const DRAFT_FIELDS = Object.freeze(['text', 'goalEnabled', 'planEnabled', 'interrupt', 'provider', 'model']);

export function emptyDraft() {
  return { text: '', goalEnabled: false, planEnabled: false, interrupt: false, provider: '', model: '' };
}

/** The draft fields of a composer slice. */
export function pickDraft(composer = {}) {
  const draft = emptyDraft();
  for (const field of DRAFT_FIELDS) {
    if (composer[field] !== undefined) draft[field] = composer[field];
  }
  return draft;
}

export function isEmptyDraft(draft) {
  const empty = emptyDraft();
  return DRAFT_FIELDS.every((field) => draft?.[field] === empty[field] || (field === 'text' && !draft?.text));
}

export const sessionDraftKey = (id) => `s:${id}`;
export const isNewSessionKey = (key) => String(key || '').startsWith('new:');

/**
 * createComposerDrafts({ getState, update, random? }) -> drafts controller.
 * keyForRoute(route) returns '' for page routes (the owner is kept).
 */
export function createComposerDrafts({ getState, update, random = Math.random }) {
  const stash = new Map();
  const submitting = new Set();
  let ephemeralId = nextEphemeralSessionId(random);
  let owner = `new:${ephemeralId}`;

  const newKey = () => `new:${ephemeralId}`;

  function keyForRoute(route) {
    if (route?.name === 'session' && route.params?.id) return sessionDraftKey(route.params.id);
    if (route?.name === 'new') return newKey();
    return '';
  }

  function stashOwner() {
    const draft = pickDraft(getState().composer);
    if (isEmptyDraft(draft)) stash.delete(owner);
    else stash.set(owner, draft);
  }

  /** Makes key the composer owner (stash current, restore target). Returns true when it switched. */
  function switchTo(key) {
    if (!key || key === owner) return false;
    stashOwner();
    const restored = stash.get(key) || emptyDraft();
    stash.delete(key);
    owner = key;
    update('composer', (composer) => ({ ...composer, ...restored, revising: false, submitting: submitting.has(key) }));
    return true;
  }

  function syncRoute(route) {
    return switchTo(keyForRoute(route));
  }

  /** Called by newSession() before it clears the composer: keeps the old owner's draft, drops the new-session draft. */
  function resetNewSession() {
    if (isNewSessionKey(owner)) {
      stash.delete(owner);
    } else {
      stashOwner();
      stash.delete(newKey());
    }
    ephemeralId = nextEphemeralSessionId(random);
    owner = newKey();
    return owner;
  }

  /** Patches the live composer when key owns it, otherwise the stashed draft. */
  function patch(key, values) {
    if (!key) return;
    if (key === owner) {
      update('composer', (composer) => ({ ...composer, ...values }));
      return;
    }
    const draftValues = Object.fromEntries(Object.entries(values).filter(([field]) => DRAFT_FIELDS.includes(field)));
    if (!Object.keys(draftValues).length) return;
    const next = { ...(stash.get(key) || emptyDraft()), ...draftValues };
    if (isEmptyDraft(next)) stash.delete(key);
    else stash.set(key, next);
  }

  function setSubmitting(key, value) {
    if (value) submitting.add(key);
    else submitting.delete(key);
    if (key === owner && getState().composer.submitting !== Boolean(value)) update('composer', { submitting: Boolean(value) });
  }

  return {
    keyForRoute,
    syncRoute,
    switchTo,
    resetNewSession,
    patch,
    setSubmitting,
    isSubmitting: (key) => submitting.has(key),
    currentKey: () => owner,
    newSessionIdentity: () => ephemeralId,
    draftFor: (key) => (key === owner ? pickDraft(getState().composer) : (stash.get(key) || null)),
  };
}

/** App instance bound to the store (actions.newSession / syncSessionForRoute and session-actions use it). */
export const composerDrafts = createComposerDrafts({ getState, update });
