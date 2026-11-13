// Sanitized Markdown block. The only place in the session stream that uses
// dangerouslySetInnerHTML, and only with lib/markdown.js output. Renders are
// cached per cache key + content, so polling never re-parses old messages.
import { html } from '../../../lib/html.js';
import { t } from '../../../lib/i18n.js';
import { renderMarkdownCached } from '../../../lib/markdown.js';

const translate = (key) => t(key);

function openExternal(event) {
  const anchor = event.target?.closest?.('a[href]');
  if (!anchor) return;
  const href = anchor.getAttribute('href') || '';
  if (!/^https?:/i.test(href)) return;
  let external = true;
  try {
    external = new URL(href, globalThis.location?.href).origin !== globalThis.location?.origin;
  } catch {
    external = true;
  }
  if (!external) return;
  event.preventDefault();
  globalThis.open?.(href, '_blank', 'noopener,noreferrer');
}

/** <Markdown cacheKey text class? /> — model/user text, always translate="no". */
export function Markdown({ cacheKey, text, class: className = '' }) {
  const markup = renderMarkdownCached(cacheKey || 'anon', String(text ?? ''), { t: translate });
  return html`<div class=${`prose ${className}`.trim()} translate="no" onClick=${openExternal} dangerouslySetInnerHTML=${{ __html: markup }}></div>`;
}
