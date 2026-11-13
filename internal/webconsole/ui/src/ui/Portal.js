import { render, useLayoutEffect, useState } from '../lib/html.js';

/**
 * <Portal>children</Portal> renders children into a container appended to
 * document.body (outside #app-layout, so the layout can be made inert).
 * Children form a separate render root: use module state (store/hooks), not context.
 */
export function Portal({ children }) {
  const [host] = useState(() => {
    const element = document.createElement('div');
    element.className = 'portal-root';
    return element;
  });
  useLayoutEffect(() => {
    document.body.appendChild(host);
    return () => {
      render(null, host);
      host.remove();
    };
  }, [host]);
  useLayoutEffect(() => {
    render(children, host);
  });
  return null;
}
