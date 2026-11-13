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

/** Tagged template bound to Preact's h(): html`<div class=${cls}>${child}</div>`. */
export const html = htm.bind(h);

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
