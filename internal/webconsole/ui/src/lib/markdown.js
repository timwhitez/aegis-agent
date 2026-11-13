/**
 * Safe Markdown renderer for the Web console (pure ES module, no DOM).
 *
 * The output is an HTML string that the UI injects with
 * dangerouslySetInnerHTML. Security rules (ported from the v1 utils.js +
 * markdown-security.js pair and never weakened):
 *   - Raw HTML in the source is always escaped; only tags generated here are
 *     emitted (p, br, h1-h6, ul/ol/li, blockquote, hr, pre/code, table parts,
 *     strong/em/del/code, a, span, disabled task-list checkboxes).
 *   - Every href/src passes sanitizeHref(): http(s), mailto and explicit
 *     relative/fragment paths only; protocol-relative (//host) and any value
 *     containing characters outside the relative-path allowlist (for example
 *     backslashes that browsers normalise into network paths) are rejected.
 *   - Images never auto-load by default. http(s) and explicit same-origin
 *     images render as an inert "open image" link (user click required);
 *     other sanitized sources render as a blocked-image label; unsafe sources
 *     stay literal text. options.inlineSameOriginImages=true restores the v1
 *     behaviour of inlining explicit same-origin images as lazy <img>.
 *   - No inline style attributes are generated (CSP friendly); table alignment
 *     uses md-align-* classes.
 *
 * i18n keys (only requested through the optional options.t translator; the
 * renderer never hardcodes UI sentences):
 *   markdown.openImage     label in front of an inert remote/same-origin image link
 *   markdown.blockedImage  label in front of a blocked image placeholder
 */

/** Maximum number of cached renders kept in the LRU cache. */
export const MARKDOWN_CACHE_LIMIT = 200;

const TOKEN_MARK = '\u0001';
const MAX_QUOTE_DEPTH = 8;
const markdownCache = new Map();

/**
 * Escape a value for HTML text content.
 * @param {unknown} value
 * @returns {string}
 */
export function escapeHTML(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

/**
 * Escape a value for a double-quoted HTML attribute.
 * @param {unknown} value
 * @returns {string}
 */
export function escapeAttr(value) {
  return escapeHTML(value);
}

/**
 * Return the href when it is safe to emit, otherwise ''.
 * Allowed: http:, https:, mailto: and explicit relative/fragment paths made of
 * URL-safe characters. Protocol-relative URLs are rejected.
 * @param {unknown} value
 * @returns {string}
 */
export function sanitizeHref(value) {
  const href = String(value ?? '').trim();
  if (!href) return '';
  if (href.startsWith('//')) return '';
  if (/^(https?:|mailto:)/i.test(href)) return href;
  if (/^[./#][A-Za-z0-9._~:/?#[\]@!$&'()*+,;=%\-]*$/.test(href)) return href;
  return '';
}

/**
 * True when an image source is an explicit same-origin path (/, ./ or ../)
 * without a scheme, protocol-relative prefix or backslash.
 * @param {unknown} src
 * @returns {boolean}
 */
export function isExplicitSameOriginImageSource(src) {
  const value = String(src ?? '').trim();
  if (!value || value.includes('\\')) return false;
  if (/^[a-z][a-z0-9+.-]*:/i.test(value) || value.startsWith('//')) return false;
  return value.startsWith('/') || value.startsWith('./') || value.startsWith('../');
}

function stripTokenMarks(value) {
  return String(value ?? '').replaceAll(TOKEN_MARK, '\uFFFD');
}

function resolveLabels(options = {}) {
  const translate = typeof options.t === 'function' ? options.t : null;
  const read = (key) => {
    if (!translate) return '';
    try {
      const value = translate(key);
      return typeof value === 'string' && value !== key ? value : '';
    } catch {
      return '';
    }
  };
  return {
    openImage: read('markdown.openImage'),
    blockedImage: read('markdown.blockedImage'),
    inlineSameOriginImages: options.inlineSameOriginImages === true
  };
}

function labelPrefix(label) {
  return label ? `<span class="md-img-label">${escapeHTML(label)}</span> ` : '';
}

function renderImageToken(alt, src, ctx) {
  const safeSrc = sanitizeHref(src);
  if (!safeSrc) {
    return escapeHTML(`![${alt}](${src})`);
  }
  const sameOrigin = isExplicitSameOriginImageSource(safeSrc);
  if (sameOrigin && ctx.inlineSameOriginImages) {
    return `<img class="md-img" src="${escapeAttr(safeSrc)}" alt="${escapeAttr(alt)}" loading="lazy" />`;
  }
  const altText = `<span class="md-img-alt" translate="no">${escapeHTML(alt || safeSrc)}</span>`;
  if (sameOrigin || /^https?:/i.test(safeSrc)) {
    return `<a class="md-img-link" href="${escapeAttr(safeSrc)}" target="_blank" rel="noopener noreferrer">${labelPrefix(ctx.openImage)}${altText}</a>`;
  }
  return `<span class="md-img-blocked">${labelPrefix(ctx.blockedImage)}${altText}</span>`;
}

function trimAutolink(url) {
  let value = url;
  while (/[.,;:!?)\]]$/.test(value)) {
    if (value.endsWith(')') && (value.match(/\(/g) || []).length >= (value.match(/\)/g) || []).length) break;
    value = value.slice(0, -1);
  }
  return value;
}

/**
 * Render one line of inline Markdown (code spans, images, links, autolinks,
 * bold/italic/strikethrough) to safe HTML.
 * @param {unknown} value
 * @param {object} [options] same options as renderMarkdown
 * @returns {string}
 */
export function inlineMarkdown(value, options = {}) {
  const ctx = options.__ctx || resolveLabels(options);
  const tokens = [];
  const rawTokens = [];
  const push = (html, raw) => {
    const placeholder = `${TOKEN_MARK}T${tokens.length}${TOKEN_MARK}`;
    tokens.push(html);
    rawTokens.push(raw);
    return placeholder;
  };
  const toRaw = (text) => String(text).replace(/\u0001T(\d+)\u0001/g, (_, index) => rawTokens[Number(index)] ?? '');
  let remaining = stripTokenMarks(value);

  // Code spans first so their content is never interpreted as Markdown.
  remaining = remaining.replace(/(`{1,3})([^`]|[^`][\s\S]*?[^`])\1(?!`)/g, (match, fence, code) =>
    push(`<code>${escapeHTML(code)}</code>`, match));

  // Images before links so ![alt](src) is not parsed as [alt](src).
  remaining = remaining.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (match, alt, src) =>
    push(renderImageToken(toRaw(alt), toRaw(src), ctx), toRaw(match)));

  remaining = remaining.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (match, label, href) => {
    const safeHref = sanitizeHref(toRaw(href));
    if (!safeHref) {
      return push(escapeHTML(label), toRaw(label));
    }
    return push(`<a href="${escapeAttr(safeHref)}" target="_blank" rel="noopener noreferrer">${escapeHTML(label)}</a>`, toRaw(match));
  });

  remaining = remaining.replace(/\bhttps?:\/\/[^\s<>"'`\u0001]+/g, (match) => {
    const url = trimAutolink(match);
    const rest = match.slice(url.length);
    const safe = sanitizeHref(url);
    if (!safe) return match;
    return push(`<a href="${escapeAttr(safe)}" target="_blank" rel="noopener noreferrer">${escapeHTML(url)}</a>`, url) + rest;
  });

  let html = escapeHTML(remaining);
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  html = html.replace(/(^|[^\w])__([^_]+)__(?!\w)/g, '$1<strong>$2</strong>');
  html = html.replace(/(^|[^*])\*([^*\s](?:[^*]*[^*\s])?)\*(?!\*)/g, '$1<em>$2</em>');
  html = html.replace(/(^|[^\w])_([^_\s](?:[^_]*[^_\s])?)_(?!\w)/g, '$1<em>$2</em>');
  html = html.replace(/~~([^~]+)~~/g, '<del>$1</del>');

  for (let i = tokens.length - 1; i >= 0; i -= 1) {
    html = html.split(`${TOKEN_MARK}T${i}${TOKEN_MARK}`).join(tokens[i]);
  }
  return html;
}

const FENCE_OPEN = /^\s{0,3}(`{3,}|~{3,})(.*)$/;
const FENCE_CLOSE = /^\s{0,3}(`{3,}|~{3,})\s*$/;
const HEADING = /^\s{0,3}(#{1,6})\s+(.+?)\s*#*\s*$/;
const RULE = /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^\s{0,3}>\s?(.*)$/;
const BULLET = /^(\s*)[-*+]\s+(.*)$/;
const ORDERED = /^(\s*)(\d{1,9})[.)]\s+(.*)$/;
const TASK = /^\[( |x|X)\]\s+(.*)$/;
const TABLE_DELIMITER = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

function openFence(line) {
  const match = FENCE_OPEN.exec(line);
  if (!match) return null;
  const fence = match[1];
  const info = match[2].trim();
  if (fence[0] === '`' && info.includes('`')) return null;
  const lang = info.split(/\s+/)[0] || '';
  return { fence, lang: /^[A-Za-z0-9_+#.-]+$/.test(lang) ? lang : '' };
}

function splitTableRow(line) {
  let text = line.trim();
  if (text.startsWith('|')) text = text.slice(1);
  if (text.endsWith('|') && !text.endsWith('\\|')) text = text.slice(0, -1);
  const cells = [];
  let current = '';
  for (let i = 0; i < text.length; i += 1) {
    if (text[i] === '\\' && text[i + 1] === '|') {
      current += '|';
      i += 1;
    } else if (text[i] === '|') {
      cells.push(current.trim());
      current = '';
    } else {
      current += text[i];
    }
  }
  cells.push(current.trim());
  return cells;
}

function tableAlignments(line) {
  return splitTableRow(line).map((cell) => {
    const left = cell.startsWith(':');
    const right = cell.endsWith(':');
    if (left && right) return 'center';
    if (right) return 'right';
    if (left) return 'left';
    return '';
  });
}

function isTableStart(lines, index) {
  const header = lines[index];
  const delimiter = lines[index + 1];
  return typeof header === 'string' && typeof delimiter === 'string' &&
    header.includes('|') && delimiter.includes('|') === true && TABLE_DELIMITER.test(delimiter) &&
    delimiter.includes('-');
}

function renderTable(rows, alignments, options) {
  const cellHTML = (tag, text, index) => {
    const align = alignments[index] ? ` class="md-align-${alignments[index]}"` : '';
    return `<${tag}${align}>${inlineMarkdown(text, options)}</${tag}>`;
  };
  const width = rows[0].length;
  const normalise = (row) => Array.from({ length: width }, (_, i) => row[i] ?? '');
  const head = `<thead><tr>${normalise(rows[0]).map((cell, i) => cellHTML('th', cell, i)).join('')}</tr></thead>`;
  const bodyRows = rows.slice(1).map((row) => `<tr>${normalise(row).map((cell, i) => cellHTML('td', cell, i)).join('')}</tr>`);
  const body = bodyRows.length ? `<tbody>${bodyRows.join('')}</tbody>` : '';
  return `<div class="md-table-wrap"><table>${head}${body}</table></div>`;
}

function renderListItem(content, options) {
  const task = TASK.exec(content);
  if (task) {
    const checked = task[1].toLowerCase() === 'x' ? ' checked' : '';
    return `<li class="task-list-item"><input type="checkbox" disabled${checked} /> ${inlineMarkdown(task[2], options)}</li>`;
  }
  return `<li>${inlineMarkdown(content, options)}</li>`;
}

/**
 * Render Markdown to safe HTML without the try/catch fallback or cache.
 * @param {unknown} text
 * @param {object} [options] see renderMarkdown
 * @param {number} [depth] blockquote nesting depth (internal)
 * @returns {string}
 */
export function safeMarkdownInner(text, options = {}, depth = 0) {
  const ctxOptions = options.__ctx ? options : { ...options, __ctx: resolveLabels(options) };
  const source = stripTokenMarks(text).replace(/\r\n?/g, '\n');
  if (!source.trim()) {
    return '<p></p>';
  }
  const lines = source.split('\n');
  const blocks = [];
  let fence = null;
  let codeLines = [];
  let list = null;
  let paragraph = [];
  let quote = [];

  const flushParagraph = () => {
    if (!paragraph.length) return;
    blocks.push(`<p>${paragraph.map((line) => inlineMarkdown(line, ctxOptions)).join('<br>')}</p>`);
    paragraph = [];
  };
  const flushList = () => {
    if (!list) return;
    const items = list.items.map((item) => renderListItem(item, ctxOptions)).join('');
    if (list.type === 'ol') {
      const start = list.start !== 1 ? ` start="${list.start}"` : '';
      blocks.push(`<ol${start}>${items}</ol>`);
    } else {
      blocks.push(`<ul>${items}</ul>`);
    }
    list = null;
  };
  const flushQuote = () => {
    if (!quote.length) return;
    const inner = depth >= MAX_QUOTE_DEPTH
      ? `<p>${quote.map((line) => inlineMarkdown(line, ctxOptions)).join('<br>')}</p>`
      : safeMarkdownInner(quote.join('\n'), ctxOptions, depth + 1);
    blocks.push(`<blockquote>${inner}</blockquote>`);
    quote = [];
  };
  const flushAll = () => {
    flushParagraph();
    flushList();
    flushQuote();
  };
  const flushCode = () => {
    const langClass = fence?.lang ? ` class="language-${escapeAttr(fence.lang)}"` : '';
    blocks.push(`<pre><code${langClass}>${escapeHTML(codeLines.join('\n'))}</code></pre>`);
    codeLines = [];
  };

  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    if (fence) {
      const close = FENCE_CLOSE.exec(line);
      if (close && close[1][0] === fence.fence[0] && close[1].length >= fence.fence.length) {
        flushCode();
        fence = null;
      } else {
        codeLines.push(line);
      }
      continue;
    }
    const opened = openFence(line);
    if (opened) {
      flushAll();
      fence = opened;
      continue;
    }
    const quoted = QUOTE.exec(line);
    if (quoted) {
      flushParagraph();
      flushList();
      quote.push(quoted[1]);
      continue;
    }
    flushQuote();
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    if (RULE.test(line)) {
      flushAll();
      blocks.push('<hr>');
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      flushAll();
      const level = heading[1].length;
      blocks.push(`<h${level}>${inlineMarkdown(heading[2], ctxOptions)}</h${level}>`);
      continue;
    }
    const bullet = BULLET.exec(line);
    const ordered = bullet ? null : ORDERED.exec(line);
    if (bullet || ordered) {
      flushParagraph();
      const type = bullet ? 'ul' : 'ol';
      if (!list || list.type !== type) {
        flushList();
        list = { type, start: ordered ? Number.parseInt(ordered[2], 10) : 1, items: [] };
      }
      list.items.push(bullet ? bullet[2] : ordered[3]);
      continue;
    }
    if (list && /^\s{2,}\S/.test(line)) {
      list.items[list.items.length - 1] += '\n' + line.trim();
      continue;
    }
    if (isTableStart(lines, index)) {
      flushAll();
      const alignments = tableAlignments(lines[index + 1]);
      const rows = [splitTableRow(line)];
      index += 2;
      while (index < lines.length && lines[index].trim() && lines[index].includes('|')) {
        rows.push(splitTableRow(lines[index]));
        index += 1;
      }
      index -= 1;
      blocks.push(renderTable(rows, alignments, ctxOptions));
      continue;
    }
    flushList();
    paragraph.push(line.trim());
  }
  if (fence) {
    // Unclosed fence: keep the captured code instead of dropping it.
    flushCode();
    fence = null;
  }
  flushAll();
  return blocks.join('') || `<p>${inlineMarkdown(source.trim(), ctxOptions)}</p>`;
}

function fallbackHTML(text) {
  return `<pre class="markdown-fallback">${escapeHTML(String(text ?? ''))}</pre>`;
}

/**
 * Render Markdown to safe HTML; malformed input falls back to escaped text.
 * @param {unknown} text
 * @param {object} [options] see renderMarkdown
 * @returns {string}
 */
export function safeMarkdown(text, options = {}) {
  try {
    return safeMarkdownInner(text, options) || fallbackHTML(text);
  } catch {
    return fallbackHTML(text);
  }
}

function djb2Hash(value) {
  let hash = 5381;
  for (let i = 0; i < value.length; i += 1) {
    hash = ((hash << 5) + hash + value.charCodeAt(i)) | 0;
  }
  return (hash >>> 0).toString(36);
}

/**
 * Cached render keyed by a caller namespace plus content hash. The cache
 * verifies the full source on hit, so hash collisions never return another
 * message's HTML. Policy labels are part of the key, so locale switches
 * re-render.
 * @param {string} key caller namespace, e.g. 'msg:<id>'
 * @param {unknown} text
 * @param {object} [options] see renderMarkdown
 * @returns {string}
 */
export function renderMarkdownCached(key, text, options = {}) {
  const source = String(text ?? '');
  const ctx = resolveLabels(options);
  if (!source) {
    return safeMarkdown(source, { ...options, __ctx: ctx });
  }
  const signature = [ctx.openImage, ctx.blockedImage, ctx.inlineSameOriginImages ? '1' : '0'].join('\u0000');
  const cacheKey = `${key || 'anon'}::${source.length}::${djb2Hash(source)}::${djb2Hash(signature)}`;
  const cached = markdownCache.get(cacheKey);
  if (cached && cached.source === source && cached.signature === signature) {
    markdownCache.delete(cacheKey);
    markdownCache.set(cacheKey, cached);
    return cached.html;
  }
  const html = safeMarkdown(source, { ...options, __ctx: ctx });
  markdownCache.set(cacheKey, { source, signature, html });
  while (markdownCache.size > MARKDOWN_CACHE_LIMIT) {
    markdownCache.delete(markdownCache.keys().next().value);
  }
  return html;
}

/**
 * Render Markdown to a sanitized HTML string (cached by content).
 * @param {unknown} text Markdown source (model/user/tool text)
 * @param {{t?: (key: string) => string, inlineSameOriginImages?: boolean}} [options]
 *   t: translator used only for markdown.openImage / markdown.blockedImage labels.
 *   inlineSameOriginImages: opt-in v1 behaviour (default false: images are inert).
 * @returns {string}
 */
export function renderMarkdown(text, options = {}) {
  return renderMarkdownCached('md', text, options);
}

/** Drop every cached render (tests and locale resets). */
export function clearMarkdownCache() {
  markdownCache.clear();
}

/** @returns {number} current cache entry count (diagnostics/tests). */
export function markdownCacheSize() {
  return markdownCache.size;
}
