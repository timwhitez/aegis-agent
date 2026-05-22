function maybeArray(value) {
  return Array.isArray(value) ? value : [];
}

function normalizeText(value) {
  return String(value || '')
    .trim()
    .replace(/\s+/g, ' ');
}

function safeMarkdown(text) {
  const source = String(text || '').replace(/\r\n/g, '\n');
  const lines = source.split('\n');
  const blocks = [];
  let inCode = false;
  let codeLines = [];
  let listLines = [];
  const flushList = () => {
    if (!listLines.length) return;
    blocks.push(`<ul>${listLines.map((line) => `<li>${inlineMarkdown(line)}</li>`).join('')}</ul>`);
    listLines = [];
  };
  const flushCode = () => {
    blocks.push(`<pre><code>${escapeHTML(codeLines.join('\n'))}</code></pre>`);
    codeLines = [];
  };
  lines.forEach((line) => {
    if (line.trim().startsWith('```')) {
      if (inCode) {
        flushCode();
      } else {
        flushList();
      }
      inCode = !inCode;
      return;
    }
    if (inCode) {
      codeLines.push(line);
      return;
    }
    if (/^\s*[-*]\s+/.test(line)) {
      listLines.push(line.replace(/^\s*[-*]\s+/, ''));
      return;
    }
    flushList();
    const trimmed = line.trim();
    if (!trimmed) {
      return;
    }
    const heading = /^(#{1,3})\s+(.+)$/.exec(trimmed);
    if (heading) {
      const level = heading[1].length;
      blocks.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`);
      return;
    }
    blocks.push(`<p>${inlineMarkdown(trimmed)}</p>`);
  });
  if (inCode) {
    flushCode();
  }
  flushList();
  return blocks.join('') || '<p></p>';
}

function inlineMarkdown(value) {
  // Process markdown tokens on the raw value FIRST, then escape the remaining text.
  // This avoids double-escaping and ensures sanitizeHref receives unescaped URLs.
  const tokens = [];
  let remaining = String(value || '');

  // Extract images ![alt](src) before links so they aren't matched as [text](href)
  remaining = remaining.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (match, alt, src) => {
    const placeholder = `\x01T${tokens.length}\x01`;
    const safeSrc = sanitizeHref(src);
    if (!safeSrc) {
      tokens.push(`![${escapeHTML(alt)}](${escapeHTML(src)})`);
    } else {
      tokens.push(`<img src="${escapeAttr(safeSrc)}" alt="${escapeAttr(alt)}" style="max-width: 100%; height: auto;" />`);
    }
    return placeholder;
  });

  // Extract links [label](href)
  remaining = remaining.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (match, label, href) => {
    const placeholder = `\x01T${tokens.length}\x01`;
    const safeHref = sanitizeHref(href);
    if (!safeHref) {
      tokens.push(escapeHTML(label));
    } else {
      tokens.push(`<a href="${escapeAttr(safeHref)}" target="_blank" rel="noreferrer">${escapeHTML(label)}</a>`);
    }
    return placeholder;
  });

  // Now escape the remaining (non-markdown) text
  let html = escapeHTML(remaining);

  // Inline code
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');

  // Restore tokens (\x01 is not touched by escapeHTML, and the index is deterministic)
  for (let i = tokens.length - 1; i >= 0; i--) {
    const placeholderEscaped = `\x01T${i}\x01`;
    html = html.split(placeholderEscaped).join(tokens[i]);
  }

  return html;
}

function sanitizeHref(value) {
  const href = String(value || '').trim();
  if (!href) return '';
  if (/^(https?:|mailto:)/i.test(href)) return href;
  if (/^[./#][A-Za-z0-9._~:/?#[\]@!$&'()*+,;=%\-]*$/.test(href)) return href;
  return '';
}

function escapeHTML(value) {
  return String(value || '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function escapeAttr(value) {
  return escapeHTML(value);
}

function shortId(value) {
  const text = String(value || '');
  if (!text) {
    return 'n/a';
  }
  if (text.length <= 14) {
    return text;
  }
  return `${text.slice(0, 6)}…${text.slice(-4)}`;
}

function truncateText(value, limit = 160) {
  const text = String(value || '').trim();
  if (text.length <= limit) {
    return text;
  }
  return `${text.slice(0, Math.max(limit - 1, 0))}…`;
}

function prettyJSON(value) {
  if (value === undefined || value === null || value === '') {
    return '{}';
  }
  try {
    if (typeof value === 'string') {
      const trimmed = value.trim();
      if (!trimmed) {
        return '{}';
      }
      return JSON.stringify(JSON.parse(trimmed), null, 2);
    }
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function parseMaybeJSON(value) {
  if (typeof value !== 'string') {
    return value && typeof value === 'object' ? value : null;
  }
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

function collectShellRedirectPaths(command) {
  const tokens = tokenizeShellCommand(command);
  const paths = [];
  for (let i = 0; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (!isShellOutputRedirect(token)) {
      continue;
    }
    const target = cleanShellRedirectTarget(tokens[i + 1]);
    if (!target) {
      continue;
    }
    paths.push({
      path: target,
      mode: token.includes('>>') ? 'append' : 'write'
    });
    i += 1;
  }
  return paths;
}

function tokenizeShellCommand(command) {
  const source = String(command || '');
  const tokens = [];
  let current = '';
  let quote = '';
  let escaping = false;

  const flush = () => {
    if (current) {
      tokens.push(current);
      current = '';
    }
  };

  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i];

    if (escaping) {
      current += ch;
      escaping = false;
      continue;
    }

    if (ch === '\\' && quote !== "'") {
      escaping = true;
      continue;
    }

    if (quote) {
      if (ch === quote) {
        quote = '';
      } else {
        current += ch;
      }
      continue;
    }

    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }

    if (/\s/.test(ch)) {
      flush();
      continue;
    }

    const redirect = readShellOutputRedirect(source, i);
    if (redirect) {
      flush();
      tokens.push(redirect.token);
      i = redirect.end;
      continue;
    }

    if (ch === ';' || ch === '|' || ch === '&') {
      flush();
      continue;
    }

    current += ch;
  }

  flush();
  return tokens;
}

function readShellOutputRedirect(source, index) {
  const first = source[index];
  let prefix = '';
  let cursor = index;
  if ((first >= '0' && first <= '9') || first === '&') {
    if (source[index + 1] !== '>') {
      return null;
    }
    prefix = first;
    cursor += 1;
  } else if (first !== '>') {
    return null;
  }

  if (source[cursor] !== '>') {
    return null;
  }

  if (source[cursor + 1] === '>') {
    return { token: `${prefix}>>`, end: cursor + 1 };
  }
  if (source[cursor + 1] === '|') {
    return { token: `${prefix}>|`, end: cursor + 1 };
  }
  return { token: `${prefix}>`, end: cursor };
}

function isShellOutputRedirect(token) {
  return /^(?:\d+|&)?(?:>|>>|>\|)$/.test(String(token || ''));
}

function cleanShellRedirectTarget(target) {
  const value = String(target || '').trim();
  if (!value || value === '-' || value.startsWith('&') || value.startsWith('(')) {
    return '';
  }
  if (value === '/dev/null' || value.startsWith('/dev/fd/')) {
    return '';
  }
  return value;
}

function metadataValue(value) {
  if (value === undefined || value === null) {
    return 'null';
  }
  if (typeof value === 'object') {
    return truncateText(prettyJSON(value), 80);
  }
  return truncateText(String(value), 80);
}

function shortenPath(path) {
  const text = String(path || '');
  if (text.length <= 42) {
    return text;
  }
  return `…${text.slice(-41)}`;
}

function formatTimestamp(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return String(value || '');
  }
  return date.toLocaleString();
}

function formatRelativeTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return String(value || '');
  }
  const now = Date.now();
  const diffMs = now - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHr = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHr / 24);

  if (diffSec < 60) return 'just now';
  if (diffMin < 60) return `${diffMin}m ago`;
  if (diffHr < 24) return `${diffHr}h ago`;
  if (diffDay < 7) return `${diffDay}d ago`;
  return date.toLocaleDateString();
}

function formatClock(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return String(value || '');
  }
  return date.toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  });
}

const MARKDOWN_CACHE_LIMIT = 200;
const markdownCache = new Map();

function djb2Hash(value) {
  const text = String(value || '');
  let hash = 5381;
  for (let i = 0; i < text.length; i += 1) {
    hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0;
  }
  return (hash >>> 0).toString(36);
}

function renderMarkdownCached(key, text) {
  const source = String(text || '');
  if (!source) {
    return safeMarkdown(source);
  }
  const cacheKey = `${key || 'anon'}::${source.length}::${djb2Hash(source)}`;
  const cached = markdownCache.get(cacheKey);
  if (cached !== undefined) {
    // Refresh LRU position.
    markdownCache.delete(cacheKey);
    markdownCache.set(cacheKey, cached);
    return cached;
  }
  const html = safeMarkdown(source);
  markdownCache.set(cacheKey, html);
  if (markdownCache.size > MARKDOWN_CACHE_LIMIT) {
    const oldestKey = markdownCache.keys().next().value;
    if (oldestKey !== undefined) {
      markdownCache.delete(oldestKey);
    }
  }
  return html;
}

function clearMarkdownCache() {
  markdownCache.clear();
}
