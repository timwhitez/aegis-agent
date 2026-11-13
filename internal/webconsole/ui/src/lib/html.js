// Rendering bridge: the only module that imports the vendored Preact, hooks
// and htm builds. Components import from here, never from ../vendor directly.
import { h, render, Fragment, createContext, cloneElement, toChildArray, createRef } from '../../vendor/preact.module.js';
import {
  useState,
  useEffect,
  useLayoutEffect,
  useMemo,
  useCallback,
  useRef,
  useReducer,
  useContext,
  useId,
} from '../../vendor/hooks.module.js';
import htm from '../../vendor/htm.module.js';

/**
 * Element factory used by html``. Preact assigns `translate` as the boolean
 * DOM property, so the string "no" would become translate="yes". Normalize
 * string values on DOM elements so translate="no" (and translate=${false})
 * both render translate="no".
 */
export function hx(type, props, ...children) {
  if (props && typeof type === 'string' && typeof props.translate === 'string') {
    props = { ...props, translate: props.translate.trim().toLowerCase() !== 'no' };
  }
  return h(type, props, ...children);
}

/** Tagged template bound to hx(): html`<div class=${cls}>${child}</div>`. */
export const html = htm.bind(hx);

export {
  h,
  render,
  Fragment,
  createContext,
  cloneElement,
  toChildArray,
  createRef,
  useState,
  useEffect,
  useLayoutEffect,
  useMemo,
  useCallback,
  useRef,
  useReducer,
  useContext,
  useId,
};
