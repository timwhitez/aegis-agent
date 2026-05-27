function maybeArray(value) {
  return Array.isArray(value) ? value : [];
}

function collectPlanInputAnswers(request, selections) {
  const questions = maybeArray(request?.questions);
  if (!questions.length || !selections || typeof selections !== 'object') {
    return [];
  }
  const answers = [];
  for (const question of questions) {
    const questionID = String(question?.id || '').trim();
    const selected = questionID ? selections[questionID] : null;
    if (!questionID || !selected || typeof selected !== 'object') {
      return [];
    }
    const label = String(selected.label || '').trim();
    const value = String(selected.value || '').trim();
    if (!label && !value) {
      return [];
    }
    answers.push({
      question_id: questionID,
      label,
      value: value || label,
      is_other: Boolean(selected.is_other)
    });
  }
  return answers;
}

function isAcceptedLaunchResponse(value) {
  return String(value?.status || '').toLowerCase() === 'accepted' && Boolean(value?.session_id);
}

function setSkillUploadPending(root, pending) {
  const doc = root || document;
  const uploadInput = doc.getElementById?.('skill-upload');
  const controls = [
    doc.getElementById?.('skill-upload-btn'),
    doc.getElementById?.('empty-upload-btn'),
    ...Array.from(doc.querySelectorAll?.('[data-skill-action][data-skill-installed="0"]') || [])
  ].filter(Boolean);
  const label = pending ? 'Uploading...' : '';
  for (const control of controls) {
    if (pending) {
      if (!control.dataset.uploadIdleLabel) {
        control.dataset.uploadIdleLabel = control.textContent || 'Upload .zip Skill';
      }
      control.disabled = true;
      control.textContent = label;
      control.setAttribute('aria-busy', 'true');
      continue;
    }
    control.disabled = false;
    if (control.dataset.uploadIdleLabel) {
      control.textContent = control.dataset.uploadIdleLabel;
    }
    delete control.dataset.uploadIdleLabel;
    control.removeAttribute('aria-busy');
  }
  if (uploadInput) {
    uploadInput.disabled = Boolean(pending);
  }
}

function mergeMessageWindows(currentMessages, nextMessages) {
  const current = maybeArray(currentMessages);
  const next = maybeArray(nextMessages);
  if (!current.length) {
    return {
      messages: next.slice(),
      hasGap: false,
      gapAnchorId: ''
    };
  }
  if (!next.length) {
    return {
      messages: current.slice(),
      hasGap: false,
      gapAnchorId: ''
    };
  }
  const nextIds = new Set(next.map((message) => message?.id).filter(Boolean));
  const firstOverlap = current.findIndex((message) => message?.id && nextIds.has(message.id));
  if (firstOverlap >= 0) {
    return {
      messages: current.slice(0, firstOverlap).filter((message) => !message?.id || !nextIds.has(message.id)).concat(next),
      hasGap: false,
      gapAnchorId: ''
    };
  }
  const preserved = current.filter((message) => !message?.id || !nextIds.has(message.id));
  return {
    messages: preserved.concat(next),
    hasGap: preserved.length > 0,
    gapAnchorId: preserved.length > 0 ? String(next[0]?.id || '') : ''
  };
}

function mergeMessagesBeforeAnchor(currentMessages, olderMessages, anchorId) {
  const current = maybeArray(currentMessages);
  const older = maybeArray(olderMessages);
  if (!older.length) {
    return current.slice();
  }
  const existingIds = new Set(current.map((message) => message?.id).filter(Boolean));
  const uniqueOlder = older.filter((message) => !message?.id || !existingIds.has(message.id));
  if (!uniqueOlder.length) {
    return current.slice();
  }
  const anchor = String(anchorId || '').trim();
  const anchorIndex = anchor ? current.findIndex((message) => message?.id === anchor) : -1;
  if (anchorIndex < 0) {
    return uniqueOlder.concat(current);
  }
  return current.slice(0, anchorIndex).concat(uniqueOlder, current.slice(anchorIndex));
}

function normalizeText(value) {
  return String(value || '')
    .trim()
    .replace(/\s+/g, ' ');
}

function safeMarkdown(text) {
  try {
    return safeMarkdownInner(text);
  } catch (err) {
    // Never let a malformed markdown fragment blank the message bubble.
    console.warn('safeMarkdown failed; falling back to escaped text', err);
    return `<pre class="markdown-fallback">${escapeHTML(String(text || ''))}</pre>`;
  }
}

function safeMarkdownInner(text) {
  const source = String(text || '').replace(/\r\n/g, '\n');
  if (!source.trim()) {
    return '<p></p>';
  }
  const lines = source.split('\n');
  const blocks = [];
  let codeFence = '';
  let codeLang = '';
  let codeLines = [];
  let listLines = [];
  const flushList = () => {
    if (!listLines.length) return;
    blocks.push(`<ul>${listLines.map((line) => `<li>${inlineMarkdown(line)}</li>`).join('')}</ul>`);
    listLines = [];
  };
  const flushCode = () => {
    const langClass = codeLang ? ` class="language-${escapeAttr(codeLang)}"` : '';
    blocks.push(`<pre><code${langClass}>${escapeHTML(codeLines.join('\n'))}</code></pre>`);
    codeLines = [];
    codeLang = '';
  };
  lines.forEach((line) => {
    if (codeFence) {
      // Closing fence: same character family, at least as long, and no language tag.
      const closingFenceMatch = /^\s{0,3}(`{3,}|~{3,})\s*$/.exec(line);
      if (closingFenceMatch) {
        const fence = closingFenceMatch[1];
        if (fence[0] === codeFence[0] && fence.length >= codeFence.length) {
          flushCode();
          codeFence = '';
          return;
        }
      }
      codeLines.push(line);
      return;
    }
    // Detect an opening fence: 3+ backticks (or tildes) optionally preceded by up to 3 spaces, with an optional language tag.
    const fenceMatch = /^\s{0,3}(`{3,}|~{3,})\s*([^\s`~]*)\s*$/.exec(line);
    if (fenceMatch) {
      const fence = fenceMatch[1];
      flushList();
      codeFence = fence;
      codeLang = fenceMatch[2] || '';
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
  if (codeFence) {
    // Unclosed fence: render the captured code without losing content.
    flushCode();
    codeFence = '';
  }
  flushList();
  return blocks.join('') || `<p>${inlineMarkdown(source.trim())}</p>`;
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
      tokens.push(`<img class="md-img" src="${escapeAttr(safeSrc)}" alt="${escapeAttr(alt)}" loading="lazy" />`);
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
      tokens.push(`<a href="${escapeAttr(safeHref)}" target="_blank" rel="noopener noreferrer">${escapeHTML(label)}</a>`);
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
  if (href.startsWith('//')) return '';
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
  let html;
  try {
    html = safeMarkdown(source);
  } catch (err) {
    console.warn('renderMarkdownCached failed; falling back to escaped text', err);
    html = `<pre class="markdown-fallback">${escapeHTML(source)}</pre>`;
  }
  if (!html) {
    html = `<pre class="markdown-fallback">${escapeHTML(source)}</pre>`;
  }
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
