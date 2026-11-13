// Component bindings for the store.
import { useLayoutEffect, useReducer, useRef } from '../lib/html.js';
import { shallowEqual } from '../lib/store.js';
import { store } from './store.js';

/**
 * useStore(selector, equality = shallowEqual) -> selected value.
 * Re-renders only when the selected value changes under equality.
 */
export function useStore(selector, equality = shallowEqual) {
  const [, forceRender] = useReducer((count) => count + 1, 0);
  const selectorRef = useRef(selector);
  const equalityRef = useRef(equality);
  selectorRef.current = selector;
  equalityRef.current = equality;
  const value = selector(store.getState());
  const valueRef = useRef(value);
  valueRef.current = value;
  useLayoutEffect(() => {
    const check = (state) => {
      const next = selectorRef.current(state);
      if (!equalityRef.current(next, valueRef.current)) forceRender();
    };
    const unsubscribe = store.subscribe(check);
    check(store.getState());
    return unsubscribe;
  }, []);
  return value;
}

/** Current locale; components that call t() re-render when it changes. */
export function useLocale() {
  return useStore((state) => state.ui.locale, Object.is);
}
