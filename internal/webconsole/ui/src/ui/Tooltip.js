import { cloneElement, toChildArray } from '../lib/html.js';

/**
 * <Tooltip text="...">${singleElement}</Tooltip> — deliberately simple: adds a
 * native title (and keeps an existing aria-label). Rich tooltips are out of scope.
 */
export function Tooltip({ text, children }) {
  const [child] = toChildArray(children);
  if (!child || typeof child !== 'object') return child ?? null;
  return cloneElement(child, { title: text });
}
