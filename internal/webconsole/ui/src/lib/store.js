// Minimal subscribable store. Pure: no DOM, safe to import from node:test.

/** Shallow equality for plain objects and arrays (one level deep). */
export function shallowEqual(a, b) {
  if (Object.is(a, b)) return true;
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a)) {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i += 1) {
      if (!Object.is(a[i], b[i])) return false;
    }
    return true;
  }
  const keysA = Object.keys(a);
  const keysB = Object.keys(b);
  if (keysA.length !== keysB.length) return false;
  for (const key of keysA) {
    if (!Object.prototype.hasOwnProperty.call(b, key) || !Object.is(a[key], b[key])) return false;
  }
  return true;
}

/**
 * createStore(initialState) -> { getState, setState, update, subscribe, select }
 *
 * - setState(partial | (state) => nextState): a partial object is shallow-merged
 *   at the top level; an updater must return the complete next state. Returning
 *   the same reference is a no-op (no notification).
 * - update(slice, patch | (sliceState) => nextSliceState): shallow-merges a patch
 *   into one top-level slice.
 * - subscribe(listener(state, prevState)) -> unsubscribe.
 * - select(selector, onChange(next, prev), equality = Object.is) -> unsubscribe;
 *   onChange fires only when the selected value changes under equality.
 */
export function createStore(initialState = {}) {
  let state = initialState;
  const listeners = new Set();

  function getState() {
    return state;
  }

  function setState(next) {
    const prev = state;
    const computed = typeof next === 'function' ? next(prev) : { ...prev, ...next };
    if (computed === prev || computed === undefined) return;
    state = computed;
    for (const listener of Array.from(listeners)) {
      listener(state, prev);
    }
  }

  function update(slice, patch) {
    setState((prev) => {
      const current = prev[slice];
      const nextSlice = typeof patch === 'function' ? patch(current) : { ...current, ...patch };
      if (nextSlice === current) return prev;
      return { ...prev, [slice]: nextSlice };
    });
  }

  function subscribe(listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
  }

  function select(selector, onChange, equality = Object.is) {
    let selected = selector(state);
    return subscribe((nextState) => {
      const next = selector(nextState);
      if (equality(next, selected)) return;
      const prev = selected;
      selected = next;
      onChange(next, prev);
    });
  }

  return { getState, setState, update, subscribe, select };
}
