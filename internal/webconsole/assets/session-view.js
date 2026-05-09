/*
 * Session workspace rendering helpers.
 * Loaded before app.js and uses the shared WebConsole state/nodes globals.
 */

function renderShortcutHelp() {
  let overlay = document.getElementById('shortcut-help-overlay');
  const isNew = !overlay;

  if (!state.showHelp) {
    if (overlay) {
      overlay.remove();
    }
    return;
  }

  if (isNew) {
    overlay = document.createElement('div');
    overlay.id = 'shortcut-help-overlay';
    overlay.className = 'shortcut-help-overlay';
    document.body.appendChild(overlay);
    // Bind delegated listeners once — they survive innerHTML changes on children
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay || e.target.classList.contains('shortcut-close-btn')) {
        state.showHelp = false;
        renderShortcutHelp();
      }
    });
    overlay.addEventListener('click', (e) => {
      if (e.target.closest('.shortcut-help-panel')) {
        e.stopPropagation();
      }
    });
  }

  const shortcuts = [
    ['Escape', 'Stop execution'],
    ['/', 'Command mode'],
    ['Ctrl+K', 'Search'],
    ['Ctrl+N', 'New session'],
    ['Ctrl+,', 'Settings'],
    ['?', 'Show this help']
  ];

  overlay.innerHTML = `
    <div class="shortcut-help-panel">
      <h3>Keyboard Shortcuts</h3>
      <table class="shortcuts-table">
        <tbody>
          ${shortcuts.map(([key, desc]) => `
            <tr>
              <td class="shortcut-key">${key}</td>
              <td class="shortcut-desc">${desc}</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
      <button class="shortcut-close-btn">Close</button>
    </div>
  `;
}

function renderCurrentSession() {
  const slots = ensureChatSlots();
  const previousScrollTop = nodes.chatContainer.scrollTop;
  const previousScrollHeight = nodes.chatContainer.scrollHeight;
  const shouldStick = isChatNearBottom(nodes.chatContainer) || !state.chatRenderCache.body;
  const sections = renderMessageStream();
  let mutated = false;

  mutated = patchChatSlot(slots.activity, 'activity', sections.activity) || mutated;
  mutated = patchChatSlot(slots.flow, 'flow', sections.flow) || mutated;
  mutated = patchChatSlot(slots.body, 'body', sections.body) || mutated;
  mutated = patchChatSlot(slots.pending, 'pending', sections.pending) || mutated;
  if (nodes.sessionRail && window.getComputedStyle(nodes.sessionRail).display !== 'none') {
    mutated = patchAuxSlot(nodes.sessionRail, 'rail', renderSessionRail()) || mutated;
  }
  if (nodes.inspectorPanel && window.getComputedStyle(nodes.inspectorPanel).display !== 'none') {
    const inspectorHTML = renderInspectorPanel();
    mutated = patchAuxSlot(nodes.inspectorPanel, 'inspector', inspectorHTML) || mutated;
    if (isCompactLayout() && nodes.inspectorSlideOut) {
      patchAuxSlot(nodes.inspectorSlideOut, 'inspector', inspectorHTML);
    }
  }
  if (nodes.todoFloatPanel) {
    patchAuxSlot(nodes.todoFloatPanel, 'todoFloat', renderActivityFloat());
  }

  if (!mutated) {
    return;
  }

  if (shouldStick) {
    nodes.chatContainer.scrollTo({
      top: nodes.chatContainer.scrollHeight,
      behavior: prefersReducedMotion() || previousScrollHeight === 0 ? 'auto' : 'smooth'
    });
    return;
  }
  nodes.chatContainer.scrollTop = previousScrollTop;
}

function patchAuxSlot(node, key, html) {
  const markup = html || '';
  if (state.chatRenderCache[key] === markup) {
    return false;
  }
  node.innerHTML = markup;
  state.chatRenderCache[key] = markup;
  if (window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: node });
  }
  return true;
}

function prefersReducedMotion() {
  return window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches ?? false;
}

function isChatNearBottom(container) {
  if (!container) {
    return true;
  }
  const remaining = container.scrollHeight - container.scrollTop - container.clientHeight;
  return remaining <= 96;
}

function ensureChatSlots() {
  let shell = nodes.chatMessages.querySelector('.chat-stream-shell');
  if (!shell) {
    nodes.chatMessages.innerHTML = `
      <div class="chat-stream-shell">
        <div class="chat-slot chat-slot-activity" data-chat-slot="activity"></div>
        <div class="chat-slot chat-slot-flow" data-chat-slot="flow"></div>
        <div class="chat-slot chat-slot-body" data-chat-slot="body"></div>
        <div class="chat-slot chat-slot-pending" data-chat-slot="pending"></div>
      </div>
    `;
    state.chatRenderCache = {
      activity: '',
      flow: '',
      body: '',
      pending: '',
      rail: '',
      inspector: '',
      todoFloat: ''
    };
    shell = nodes.chatMessages.querySelector('.chat-stream-shell');
  }
  return {
    activity: shell.querySelector('[data-chat-slot="activity"]'),
    flow: shell.querySelector('[data-chat-slot="flow"]'),
    body: shell.querySelector('[data-chat-slot="body"]'),
    pending: shell.querySelector('[data-chat-slot="pending"]')
  };
}

function patchChatSlot(node, key, html) {
  const markup = html || '';
  if (state.chatRenderCache[key] === markup) {
    return false;
  }
  node.innerHTML = markup;
  node.hidden = markup === '';
  state.chatRenderCache[key] = markup;
  if (window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: node });
  }
  return true;
}

function summarizeCurrentSession() {
  const detail = state.sessionDetail;
  if (!detail) {
    return {
      toolCalls: 0,
      toolResults: 0,
      delegateCalls: 0,
      childSessions: 0,
      queueJobs: 0
    };
  }
  const messages = maybeArray(detail.messages);
  let toolCalls = 0;
  let toolResults = 0;
  let delegateCalls = 0;
  messages.forEach((msg) => {
    toolCalls += maybeArray(msg.tool_calls).length;
    toolResults += maybeArray(msg.tool_results).length;
    maybeArray(msg.tool_calls).forEach((call) => {
      if (isMultiAgentTool(call.name)) {
        delegateCalls += 1;
      }
    });
  });
  return {
    toolCalls,
    toolResults,
    delegateCalls,
    childSessions: maybeArray(detail.children?.sessions).length,
    queueJobs: maybeArray(detail.children?.jobs).length
  };
}

function summarizeProviderFailure(detail) {
  const raw = String(detail?.state?.last_error || '').trim();
  if (!raw) {
    return null;
  }
  const lower = raw.toLowerCase();
  const retryCount = maybeArray(detail?.events).filter((event) => event.type === 'provider.retry').length;
  if (lower.includes('auth_unavailable') || lower.includes('no auth available')) {
    return {
      label: 'Upstream auth routing',
      hint: 'The gateway accepted earlier work, then later reported auth_unavailable. Credentials may still be valid; retry later or switch provider routing.',
      activityCopy: 'Upstream auth routing failed after the session had already started. Credentials may still be valid; retry later or check provider routing.'
    };
  }
  if (lower.includes('token_invalidated') || lower.includes('token_revoked') || lower.includes('invalid api key') || lower.includes('auth_error') || lower.includes('unauthorized') || lower.includes('forbidden')) {
    return {
      label: 'Credential failure',
      hint: 'The provider rejected the current credentials. Refresh the API key or choose another configured provider.',
      activityCopy: 'The provider rejected the current credentials. Refresh the API key or switch providers before continuing.'
    };
  }
  if (lower.includes('rate_limit') || lower.includes('429')) {
    return {
      label: 'Rate limit',
      hint: 'The provider throttled this request. Wait, reduce concurrency, or continue later.',
      activityCopy: 'The provider throttled this session. Wait or reduce concurrency before continuing.'
    };
  }
  if (lower.includes('invalid_request') || lower.includes('unprocessable') || lower.includes('unsupported parameter')) {
    return {
      label: 'Request contract',
      hint: 'The provider rejected the request payload. Check the selected provider, model, and request options.',
      activityCopy: 'The provider rejected the request payload. Review provider, model, and request options.'
    };
  }
  if (
    lower.includes('upstream_timeout') ||
    lower.includes('transport timeout') ||
    lower.includes('context deadline exceeded') ||
    lower.includes('i/o timeout') ||
    lower.includes('connection reset') ||
    lower.includes('stream disconnected') ||
    lower.includes('stream disconnect')
  ) {
    const retryNote = retryCount > 0 ? ` The session already emitted ${retryCount} provider.retry event${retryCount === 1 ? '' : 's'}.` : '';
    return {
      label: 'Retryable upstream transport',
      hint: `The provider connection timed out or disconnected mid-run.${retryNote} Retry or continue later; credentials may still be valid.`,
      activityCopy: `The provider connection timed out or disconnected mid-run.${retryNote} Retry or continue later; credentials may still be valid.`
    };
  }
  if (lower.includes('server_error')) {
    return {
      label: 'Upstream provider error',
      hint: 'The upstream provider returned a server-side error. Retry later and inspect the raw provider error below if it repeats.',
      activityCopy: 'The upstream provider returned a server-side error. Retry later and inspect the raw provider error if it repeats.'
    };
  }
  return null;
}

function renderMessageStream() {
  const detailMessages = maybeArray(state.sessionDetail?.messages);
  const optimisticMessages = state.optimisticMessages.slice();
  const stream = detailMessages.length ? detailMessages.concat(optimisticMessages) : optimisticMessages;

  if (!stream.length) {
    return {
      activity: hasDurableSession() || state.isGenerating ? renderSessionActivityCard() : '',
      flow: renderFlowLane(),
      body: renderEmptySessionState(),
      pending: state.isGenerating ? renderPendingStageCard() : ''
    };
  }

  const bodyHTML = stream.map((message) => renderMessage(message)).join('');

  return {
    activity: hasDurableSession() || state.isGenerating ? renderSessionActivityCard() : '',
    flow: renderFlowLane(),
    body: bodyHTML,
    pending: state.isGenerating ? renderPendingStageCard() : ''
  };
}

function renderEmptySessionState() {
  const recentSessions = maybeArray(state.overview?.recent_sessions).length;
  return `
    <section class="empty-session-state">
      <div class="status-badge neutral">Ready</div>
      <h1 class="empty-session-title">Start a session.</h1>
      <p class="empty-session-copy">Answers, tool calls, and running flow will appear here. Use Sessions to reopen older sessions.</p>
      ${recentSessions ? `<div class="empty-session-note">${escapeHTML(String(recentSessions))} recent sessions available in Sessions.</div>` : ''}
    </section>
  `;
}

function summarizeLiveCounters(counters) {
  const items = [];
  if (counters.toolCalls) {
    items.push(`${counters.toolCalls} call${counters.toolCalls === 1 ? '' : 's'}`);
  }
  if (counters.childSessions) {
    items.push(`${counters.childSessions} child`);
  }
  if (counters.queueJobs) {
    items.push(`${counters.queueJobs} queue`);
  }
  return items.join(' · ');
}

function renderSessionActivityCard() {
  const detail = state.sessionDetail;
  const counters = summarizeCurrentSession();
  const status = detail?.state?.status || (state.isGenerating ? 'running' : 'idle');
  const phase = detail?.state?.phase ? phaseHeadline(detail.state.phase) : state.liveActivity.title;
  const tone = toneForStatus(status);
  const failureSummary = detail ? summarizeProviderFailure(detail) : null;
  const copy = failureSummary?.activityCopy || detail?.state?.last_error || state.liveActivity.copy;
  const summary = summarizeLiveCounters(counters);
  const canContinue = hasDurableSession() &&
    ['paused', 'awaiting_input', 'failed'].includes(status);

  return `
    <section class="session-flow-card">
      <div class="session-flow-head">
        <div class="session-flow-state">
          <span class="status-badge ${tone}">${escapeHTML(humanizeStatus(status))}</span>
          <span class="session-flow-phase">${escapeHTML(phase)}</span>
        </div>
        <div class="session-flow-meta">
          ${detail?.metadata?.id ? `<span class="tiny-code-chip">${escapeHTML(shortId(detail.metadata.id))}</span>` : `<span class="tiny-code-chip">${escapeHTML(shortId(state.sessionId))}</span>`}
          ${summary ? `<span class="surface-chip">${escapeHTML(summary)}</span>` : ''}
        </div>
      </div>
      <div class="session-flow-copy">${escapeHTML(copy || 'Waiting for the next update.')}</div>
      ${canContinue ? `<div class="session-flow-actions"><button class="inline-action-btn" type="button" data-continue-session="${escapeAttr(state.sessionId)}">Continue session</button></div>` : ''}
    </section>
  `;
}

function renderFlowLane() {
  const detailItems = maybeArray(state.sessionDetail?.timeline)
    .filter((item) => item.kind === 'event' && isCompactFlowEvent(item.event_type))
    .slice(-3)
    .reverse();
  if (!detailItems.length) {
    return '';
  }
  return `
    <section class="flow-lane">
      <div class="flow-lane-label">Flow</div>
      <div class="flow-lane-stack">
        ${detailItems.map((item) => renderTimelineItem(item, { compact: true, hideData: true })).join('')}
      </div>
    </section>
  `;
}

function renderMessage(message) {
  const role = message.role || 'assistant';
  const backgroundResults = isBackgroundResultsMessage(message);
  const visualRole = backgroundResults ? 'assistant background-results' : role === 'user' ? 'user' : role === 'system' ? 'system' : 'assistant';
  const actor = actorNameForMessage(message);
  const icon = backgroundResults ? 'git-branch' : iconForRole(role);
  const textHTML = message.text ? renderMessageText(message) : '';
  const toolLaneHTML = renderToolLane(message);

  return `
    <article class="message ${visualRole} ${message.pending ? 'optimistic' : ''}">
      <div class="message-header">
        <div class="message-header-main">
          <i data-lucide="${escapeAttr(icon)}" class="message-header-icon icon-small"></i>
          <span class="message-header-name">${escapeHTML(actor)}</span>
        </div>
        <div class="message-header-meta">
          ${renderMessageMetaChips(message)}
        </div>
      </div>
      <div class="message-body">
        ${textHTML}
        ${toolLaneHTML}
      </div>
    </article>
  `;
}

function renderMessageText(message) {
  if (isBackgroundResultsMessage(message)) {
    return renderBackgroundResultsMessage(message);
  }
  if (message.role === 'user') {
    return `<div class="message-bubble message-bubble-plaintext">${escapeHTML(String(message.text || ''))}</div>`;
  }
  return `<div class="message-bubble prose">${safeMarkdown(message.text)}</div>`;
}

function messageMeta(message) {
  if (message?.meta && typeof message.meta === 'object' && !Array.isArray(message.meta)) {
    return message.meta;
  }
  if (message?.data && typeof message.data === 'object' && !Array.isArray(message.data)) {
    return message.data;
  }
  return {};
}

function messageSource(message) {
  return String(messageMeta(message).source || '');
}

function isBackgroundResultsMessage(message) {
  if (!message) return false;
  if (messageSource(message) === 'background_results') return true;
  const text = String(message.text || '');
  if (!text.includes('<background-agent-results>')) return false;
  const payload = parseBackgroundResultsPayload(text);
  return maybeArray(payload?.background_results).length > 0;
}

function parseBackgroundResultsPayload(text) {
  const raw = String(text || '').trim();
  const match = raw.match(/<background-agent-results>\s*([\s\S]*?)\s*<\/background-agent-results>/);
  const jsonText = match ? match[1] : raw;
  const parsed = parseMaybeJSON(jsonText);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return null;
  }
  return parsed;
}

function renderBackgroundResultsMessage(message) {
  const payload = parseBackgroundResultsPayload(message.text);
  const results = maybeArray(payload?.background_results);
  if (!results.length) {
    return `<div class="message-bubble message-bubble-plaintext">${escapeHTML(String(message.text || ''))}</div>`;
  }
  const completed = results.filter((item) => backgroundResultStatus(item).toLowerCase() === 'completed').length;
  const failed = results.filter((item) => backgroundResultTone(item) === 'danger').length;
  return `
    <div class="agent-result-panel">
      <div class="agent-result-panel-head">
        <div>
          <div class="agent-result-eyebrow">Background agent results</div>
          <div class="agent-result-title">${escapeHTML(String(results.length))} delegated result${results.length === 1 ? '' : 's'} accepted</div>
        </div>
        <div class="agent-result-summary">
          ${completed ? `<span class="status-badge live">${escapeHTML(String(completed))} completed</span>` : ''}
          ${failed ? `<span class="status-badge danger">${escapeHTML(String(failed))} failed</span>` : ''}
          ${message.created_at ? `<span class="surface-chip">${escapeHTML(formatClock(message.created_at))}</span>` : ''}
        </div>
      </div>
      <div class="agent-result-list">
        ${results.map((item) => renderBackgroundResultItem(item)).join('')}
      </div>
    </div>
  `;
}

function renderBackgroundResultItem(item) {
  item = item || {};
  const status = backgroundResultStatus(item);
  const tone = backgroundResultTone(item);
  const label = agentLabel(item.agent_name, item.agent_role) || shortId(item.session_id) || 'Child agent';
  const body = item.last_error || item.final_text || 'No final text recorded.';
  const workdir = item.effective_workdir || item.requested_workdir || '';
  return `
    <section class="agent-result-item ${tone}">
      <div class="agent-result-item-top">
        <div class="agent-result-identity">
          <span class="agent-result-dot ${tone}"></span>
          <span class="agent-result-name">${escapeHTML(label)}</span>
        </div>
        <span class="status-badge ${tone}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      <div class="agent-result-copy">${escapeHTML(body)}</div>
      <div class="agent-result-meta">
        ${item.queue_job_id ? `<span class="tiny-code-chip">job ${escapeHTML(shortId(item.queue_job_id))}</span>` : ''}
        ${item.session_id ? `<span class="tiny-code-chip">child ${escapeHTML(shortId(item.session_id))}</span>` : ''}
        ${workdir ? `<span class="surface-chip">${escapeHTML(workdirBase(workdir))}</span>` : ''}
      </div>
      ${renderVisiblePaths(item.visible_paths)}
      <div class="card-actions">
        ${item.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.session_id)}">Open child session</button>` : ''}
        ${item.queue_job_id ? `<button class="mini-link-btn" type="button" data-open-job="${escapeAttr(item.queue_job_id)}">Open job</button>` : ''}
      </div>
    </section>
  `;
}

function backgroundResultStatus(item) {
  return String(item?.status || item?.session_status || 'unknown');
}

function backgroundResultTone(item) {
  if (item?.last_error) {
    return 'danger';
  }
  return toneForStatus(backgroundResultStatus(item));
}

function summarizeBackgroundResultsPayload(payload) {
  const results = maybeArray(payload?.background_results);
  if (!results.length) {
    return '';
  }
  const failed = results.filter((item) => backgroundResultTone(item) === 'danger').length;
  const completed = results.filter((item) => backgroundResultStatus(item).toLowerCase() === 'completed').length;
  const first = results[0] || {};
  const firstCopy = first.last_error || first.final_text || agentLabel(first.agent_name, first.agent_role) || '';
  const statusSummary = [
    completed ? `${completed} completed` : '',
    failed ? `${failed} failed` : ''
  ].filter(Boolean).join(', ');
  const prefix = `${results.length} delegated result${results.length === 1 ? '' : 's'} accepted`;
  return truncateText([prefix, statusSummary, firstCopy].filter(Boolean).join(': '), 180);
}

function renderToolLane(message) {
  const calls = maybeArray(message.tool_calls);
  const results = maybeArray(message.tool_results);

  if (!calls.length && !results.length) return '';

  const resultsByCallId = new Map();
  for (const r of results) {
    if (r.tool_call_id) {
      if (!resultsByCallId.has(r.tool_call_id)) {
        resultsByCallId.set(r.tool_call_id, []);
      }
      resultsByCallId.get(r.tool_call_id).push(r);
    }
  }

  const pairedResults = new Set();
  let treeHTML = '';

  for (const call of calls) {
    const callResults = resultsByCallId.get(call.id) || [];
    for (const r of callResults) pairedResults.add(r);

    const delegate = isMultiAgentTool(call.name);
    const hasExpanded = callResults.some(function(r) { return r.is_error || r.final; }) || delegate;

    treeHTML +=
      '<details class="tl-row tl-row-call"' + (hasExpanded ? ' open' : '') + '>' +
        '<summary class="tl-summary">' +
          '<span class="tl-type-chip call">Call</span>' +
          '<strong class="tl-name">' + escapeHTML(call.name) + '</strong>' +
          (call.id ? '<span class="tl-id-chip">' + escapeHTML(shortId(call.id)) + '</span>' : '') +
          '<span class="tl-preview">' + escapeHTML(summarizeToolCall(call)) + '</span>' +
        '</summary>' +
        '<pre class="tl-body">' + escapeHTML(prettyJSON(call.arguments)) + '</pre>' +
        callResults.map(function(r) { return renderToolLaneResultRow(r, true); }).join('') +
      '</details>';
  }

  for (var i = 0; i < results.length; i++) {
    if (pairedResults.has(results[i])) continue;
    treeHTML += renderToolLaneResultRow(results[i], false);
  }

  return (
    '<div class="tool-lane">' +
      '<div class="tool-lane-header">' +
        '<i data-lucide="wrench" class="tl-header-icon"></i>' +
        '<span>Tool Lane</span>' +
        '<span class="tl-header-count">' +
          escapeHTML(String(calls.length)) + ' call' + (calls.length !== 1 ? 's' : '') +
          ' &middot; ' +
          escapeHTML(String(results.length)) + ' result' + (results.length !== 1 ? 's' : '') +
        '</span>' +
      '</div>' +
      '<div class="tool-lane-tree">' +
        treeHTML +
      '</div>' +
    '</div>'
  );
}

function renderToolLaneResultRow(result, indent) {
  var payloadText = result.display_output || result.llm_output || '(no output)';
  var parsed = parseMaybeJSON(payloadText);
  var delegate = isMultiAgentTool(result.name);
  var open = result.is_error || result.final || delegate;
  var special = renderSpecialToolResult(result, parsed);

  return (
    '<details class="tl-row tl-row-result' + (indent ? ' tl-indent' : '') + (result.is_error ? ' tl-error' : '') + '"' + (open ? ' open' : '') + '>' +
      '<summary class="tl-summary">' +
        '<span class="tl-type-chip ' + (result.is_error ? 'error' : delegate ? 'child' : 'result') + '">' + (result.is_error ? 'Error' : delegate ? 'Child' : 'Result') + '</span>' +
        '<strong class="tl-name">' + escapeHTML(result.name) + '</strong>' +
        (result.final ? '<span class="tl-badge final">Final</span>' : '') +
        (delegate ? '<span class="tl-badge delegate">Delegate</span>' : '') +
        '<span class="tl-preview">' + escapeHTML(summarizeToolResult(result, parsed, payloadText)) + '</span>' +
      '</summary>' +
      (special || '<pre class="tl-body">' + escapeHTML(truncateText(payloadText, 3200)) + '</pre>') +
      renderMetadataChips(result.metadata) +
    '</details>'
  );
}

function renderMessageMetaChips(message) {
  const chips = [];
  const meta = messageMeta(message);
  if (message.pending) {
    chips.push('<span class="message-meta-chip">Pending</span>');
  }
  if (meta.source) {
    const sourceLabel = meta.source === 'background_results' ? 'background results' : meta.source;
    chips.push(`<span class="message-meta-chip">${escapeHTML(sourceLabel)}</span>`);
  }
  if (meta.interrupt) {
    chips.push('<span class="message-meta-chip">interrupt</span>');
  }
  if (message.created_at) {
    chips.push(`<span class="message-meta-chip">${escapeHTML(formatClock(message.created_at))}</span>`);
  }
  if (maybeArray(message.tool_calls).length) {
    chips.push(`<span class="message-meta-chip">${escapeHTML(String(message.tool_calls.length))} call${message.tool_calls.length === 1 ? '' : 's'}</span>`);
  }
  if (maybeArray(message.tool_results).length) {
    chips.push(`<span class="message-meta-chip">${escapeHTML(String(message.tool_results.length))} result${message.tool_results.length === 1 ? '' : 's'}</span>`);
  }
  return chips.join('');
}

function renderUniqueCodeChips(...values) {
  const seen = new Set();
  return values
    .map((value) => shortId(value || ''))
    .filter((value) => value && value !== 'n/a' && !seen.has(value) && seen.add(value))
    .map((value) => `<span class="tiny-code-chip">${escapeHTML(value)}</span>`)
    .join('');
}

function summarizeToolCall(call) {
  const parsed = parseMaybeJSON(call.arguments);
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    if (typeof parsed.path === 'string' && parsed.path) {
      return shortenPath(parsed.path);
    }
    if (typeof parsed.pattern === 'string' && parsed.pattern) {
      return `pattern: ${parsed.pattern}`;
    }
    if (typeof parsed.prompt === 'string' && parsed.prompt) {
      return truncateText(parsed.prompt, 120);
    }
    if (Array.isArray(parsed.todos)) {
      return `${parsed.todos.length} todo item${parsed.todos.length === 1 ? '' : 's'}`;
    }
    if (typeof parsed.message === 'string' && parsed.message) {
      return truncateText(parsed.message, 120);
    }
  }
  return compactText(prettyJSON(call.arguments), 120);
}

function summarizeToolResult(result, parsed, payloadText) {
  if (result.final) {
    return truncateText(payloadText, 120);
  }
  if (result.is_error) {
    return truncateText(payloadText, 140);
  }
  if (result.name === 'todo_write' && Array.isArray(parsed)) {
    return `${parsed.length} todo item${parsed.length === 1 ? '' : 's'} saved`;
  }
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    if (typeof parsed.final_text === 'string' && parsed.final_text) {
      return truncateText(parsed.final_text, 120);
    }
    if (typeof parsed.status === 'string') {
      return humanizeStatus(parsed.status);
    }
  }
  return compactText(payloadText, 140);
}

function compactText(value, maxLength = 120) {
  return truncateText(String(value || '').replace(/\s+/g, ' ').trim(), maxLength);
}

function renderToolCall(call) {
  const delegate = isMultiAgentTool(call.name);
  const preview = summarizeToolCall(call);
  return `
    <details class="tool-card tool-call-card ${delegate ? 'delegate' : ''}" ${delegate ? 'open' : ''}>
      <summary>
        <div class="tool-card-heading">
          <div class="tool-card-title-row">
            <span class="tool-card-type">Call</span>
            <strong>${escapeHTML(call.name || 'tool')}</strong>
          </div>
          <div class="tool-card-meta">
            ${renderUniqueCodeChips(call.id, call.provider_call_id)}
          </div>
        </div>
        ${preview ? `<div class="tool-card-summary-copy">${escapeHTML(preview)}</div>` : ''}
      </summary>
      <pre class="tool-json-block">${escapeHTML(prettyJSON(call.arguments))}</pre>
    </details>
  `;
}

function renderToolResult(result) {
  const payloadText = result.display_output || result.llm_output || '(no output)';
  const parsed = parseMaybeJSON(payloadText);
  const delegate = isMultiAgentTool(result.name);
  const open = delegate || result.is_error || result.final;
  const special = renderSpecialToolResult(result, parsed);
  const preview = summarizeToolResult(result, parsed, payloadText);
  return `
    <details class="tool-card tool-result-card ${delegate ? 'delegate' : ''} ${result.is_error ? 'error' : ''}" ${open ? 'open' : ''}>
      <summary>
        <div class="tool-card-heading">
          <div class="tool-card-title-row">
            <span class="tool-card-type">${result.is_error ? 'Error' : 'Result'}</span>
            <strong>${escapeHTML(result.name || 'tool')}</strong>
          </div>
          <div class="tool-card-meta">
            ${result.tool_call_id ? `<span class="tiny-code-chip">${escapeHTML(shortId(result.tool_call_id))}</span>` : ''}
            ${result.final ? '<span class="status-badge live">Final</span>' : ''}
          </div>
        </div>
        ${preview ? `<div class="tool-card-summary-copy">${escapeHTML(preview)}</div>` : ''}
      </summary>
      ${special || `<pre class="tool-output-block">${escapeHTML(truncateText(payloadText, 3200))}</pre>`}
      ${renderMetadataChips(result.metadata)}
    </details>
  `;
}

function renderSpecialToolResult(result, parsed) {
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return '';
  }

  if (result.name === 'agent_spawn' || result.name === 'agent_status') {
    const statusTone = toneForStatus(parsed.status || parsed.session_status);
    const label = agentLabel(parsed.agent_name, parsed.agent_role) || shortId(parsed.session_id) || 'child agent';
    return `
      <div class="tool-special-card">
        <div class="sa-tree-row parent" style="cursor:default; background:transparent; padding:0;">
          <span class="sa-tree-dot ${statusTone}"></span>
          <span class="sa-tree-label">${escapeHTML(label)}</span>
          <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(parsed.status || parsed.session_status || 'unknown'))}</span>
          <span class="sa-tree-meta">${escapeHTML(shortId(parsed.session_id || ''))}</span>
          ${parsed.session_id ? `<button class="mini-link-btn" type="button" data-sub-agent-open="${escapeAttr(parsed.session_id)}" style="margin-left:4px;flex-shrink:0;">Open</button>` : ''}
        </div>
        ${parsed.last_error ? `<div class="tl-preview" style="color:#dc2626;font-size:11px;margin-top:4px;">${escapeHTML(truncateText(parsed.last_error, 120))}</div>` : ''}
      </div>
    `;
  }

  if (result.name === 'agent_list') {
    const sessions = maybeArray(parsed.sessions).slice().sort(compareCreatedAscending);
    const jobs = maybeArray(parsed.jobs).slice().sort(compareCreatedAscending);
    const sessionIds = new Set(sessions.map((s) => s.id));
    const sessionQueueJobIds = new Set(sessions.map((s) => s.queue_job_id).filter(Boolean));
    const orphanJobs = jobs.filter((job) => !sessionIds.has(job.session_id) && !sessionQueueJobIds.has(job.id));
    if (!sessions.length && !orphanJobs.length) return '';

    const rows = [];
    sessions.forEach((sess) => {
      const statusTone = toneForStatus(sess.status);
      const label = agentLabel(sess.agent_name, sess.agent_role) || shortId(sess.id);
      rows.push(`
        <div class="sa-tree-row parent" style="cursor:default; background:transparent; padding:0;">
          <span class="sa-tree-dot ${statusTone}"></span>
          <span class="sa-tree-label">${escapeHTML(label)}</span>
          <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(sess.status))}</span>
          <span class="sa-tree-meta">${escapeHTML(sess.model || sess.provider || 'n/a')} · ${escapeHTML(shortId(sess.id))}</span>
          <button class="mini-link-btn" type="button" data-sub-agent-open="${escapeAttr(sess.id)}" style="margin-left:4px;flex-shrink:0;">Open</button>
        </div>
      `);
    });
    orphanJobs.forEach((job) => {
      const statusTone = toneForStatus(job.status);
      const label = agentLabel(job.agent_name, job.agent_role) || shortId(job.id);
      const action = job.session_id
        ? `<button class="mini-link-btn" type="button" data-sub-agent-open="${escapeAttr(job.session_id)}" style="margin-left:4px;flex-shrink:0;">Open</button>`
        : `<button class="mini-link-btn" type="button" data-open-job="${escapeAttr(job.id)}" style="margin-left:4px;flex-shrink:0;">Open job</button>`;
      rows.push(`
        <div class="sa-tree-row orphan" style="cursor:default; background:transparent; padding:0;">
          <span class="sa-tree-dot ${statusTone}"></span>
          <span class="sa-tree-label">${escapeHTML(label)}</span>
          <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(job.status))}</span>
          <span class="sa-tree-meta">${escapeHTML(job.mode || '')} · ${escapeHTML(shortId(job.id))}</span>
          ${action}
        </div>
      `);
    });

    return `
      <div class="tool-special-card">
        <div class="section-title-row" style="padding-bottom:6px;">
          <h4>Background agents</h4>
          <span class="tf-sum-chip sa-sum-sessions" style="margin-left:auto;">${sessions.length} session${sessions.length !== 1 ? 's' : ''}</span>
          ${orphanJobs.length ? `<span class="tf-sum-chip sa-sum-jobs">${orphanJobs.length} job${orphanJobs.length !== 1 ? 's' : ''}</span>` : ''}
        </div>
        ${rows.join('')}
      </div>
    `;
  }

  return '';
}

function renderVisiblePaths(paths) {
  const items = maybeArray(paths);
  if (!items.length) {
    return '';
  }
  return `<div class="path-pill-row">${items.slice(0, 6).map((path) => `<span class="surface-chip">${escapeHTML(shortenPath(path))}</span>`).join('')}</div>`;
}

function renderMetadataChips(metadata) {
  const entries = Object.entries(metadata || {}).slice(0, 8);
  if (!entries.length) {
    return '';
  }
  return `
    <div class="meta-chip-row padded">
      ${entries.map(([key, value]) => `<span class="surface-chip">${escapeHTML(key)}: ${escapeHTML(metadataValue(value))}</span>`).join('')}
    </div>
  `;
}

function renderPendingStageCard() {
  return `
    <section class="message assistant pending">
      <div class="pending-stage-card">
        <div class="pending-stage-indicator" aria-hidden="true"></div>
        <div class="pending-stage-body">
          <div class="pending-stage-topline">
            <span class="status-badge ${state.liveActivity.tone || 'neutral'}">${escapeHTML(state.isGenerating ? 'Running' : 'Settling')}</span>
            <span class="pending-stage-title">${escapeHTML(state.liveActivity.title)}</span>
            ${state.nextSendInterrupt ? '<span class="status-badge queued">Interrupt armed</span>' : ''}
          </div>
          <div class="pending-stage-copy">${escapeHTML(state.liveActivity.copy)}</div>
        </div>
      </div>
    </section>
  `;
}


function renderSummaryPanel(detail) {
  const counters = summarizeCurrentSession();
  const recentTools = collectRecentToolEntries(detail.messages).slice(-6).reverse();
  const loadedSkills = maybeArray(detail.state?.loaded_skills);
  const failureSummary = summarizeProviderFailure(detail);
  return `
    <section class="panel-section">
      <div class="summary-grid wide">
        ${renderMetricCard('Status', humanizeStatus(detail.state.status), phaseHeadline(detail.state.phase))}
        ${renderMetricCard('Turns', String(detail.state.turn || 0), detail.metadata.mode || 'run')}
        ${renderMetricCard('Tool calls', String(counters.toolCalls), `${counters.toolResults} results`)}
        ${renderMetricCard('Delegations', String(counters.delegateCalls), `${counters.childSessions} children · ${counters.queueJobs} jobs`)}
      </div>
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Session facts</h4>
      </div>
      <div class="kv-list">
        ${renderKVRow('Session', detail.metadata.id)}
        ${renderKVRow('Agent', agentLabel(detail.metadata.agent_name, detail.metadata.agent_role) || 'Master session')}
        ${renderKVRow('Provider', detail.metadata.provider || 'n/a')}
        ${renderKVRow('Model', detail.metadata.model || 'n/a')}
        ${renderKVRow('Mode', detail.metadata.mode || 'run')}
        ${renderKVRow('Workdir', detail.metadata.workdir || 'n/a')}
        ${detail.metadata.requested_workdir ? renderKVRow('Requested workdir', detail.metadata.requested_workdir) : ''}
        ${detail.metadata.isolation?.mode ? renderKVRow('Isolation', `${detail.metadata.isolation.mode}${detail.metadata.isolation.requested_mode ? ` (requested ${detail.metadata.isolation.requested_mode})` : ''}`) : ''}
        ${loadedSkills.length ? renderKVRow('Loaded skills', loadedSkills.join(', ')) : ''}
        ${detail.active_handle ? renderKVRow('Webconsole handle', 'active') : ''}
        ${failureSummary ? renderKVRow('Failure class', failureSummary.label) : ''}
        ${failureSummary ? renderKVRow('Operator hint', failureSummary.hint) : ''}
        ${detail.state.last_error ? renderKVRow('Last error', detail.state.last_error) : ''}
      </div>
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Recent tool activity</h4>
        <button class="inline-action-btn" type="button" data-focus-inspector-tab="timeline">Open timeline</button>
      </div>
      ${recentTools.length ? `
        <div class="card-stack">
          ${recentTools.map((entry) => renderLiveToolEntry(entry)).join('')}
        </div>
      ` : '<div class="empty-panel">No tool activity yet.</div>'}
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Queued input and notifications</h4>
        <button class="inline-action-btn" type="button" data-focus-inspector-tab="agents">Open agents</button>
      </div>
      <div class="card-stack">
        ${renderSteerQueue(detail.steer_requests)}
        ${renderBackgroundNotificationsPreview(detail.background_notifications)}
      </div>
    </section>
  `;
}

function renderTimelinePanel(detail) {
  const items = maybeArray(detail.timeline);
  if (!items.length) {
    return '<div class="empty-panel">No timeline entries yet.</div>';
  }
  return `
    <section class="panel-section">
      <div class="section-title-row">
        <h4>Durable timeline</h4>
      </div>
      <div class="timeline-stack">
        ${items.map((item) => renderTimelineItem(item)).join('')}
      </div>
    </section>
  `;
}

function renderAgentsPanel(detail) {
  const children = maybeArray(detail.children?.sessions);
  const jobs = maybeArray(detail.children?.jobs);
  const notifications = maybeArray(detail.background_notifications);
  const agents = mergeSubAgentRows(children, jobs);
  return `
    <section class="panel-section">
      <div class="summary-grid wide">
        ${renderMetricCard('Sub agents', String(agents.length), 'stable creation order')}
        ${renderMetricCard('Background jobs', String(jobs.length), 'queued or completed')}
        ${renderMetricCard('Notifications', String(notifications.length), 'background results')}
      </div>
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Sub agents</h4>
      </div>
      ${agents.length ? `<div class="card-stack">${agents.map((item) => renderSubAgentCard(item)).join('')}</div>` : '<div class="empty-panel">No sub agents yet.</div>'}
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Background notifications</h4>
      </div>
      ${notifications.length ? `<div class="card-stack">${notifications.map((note) => renderNotificationCard(note)).join('')}</div>` : '<div class="empty-panel">No background notifications yet.</div>'}
    </section>
  `;
}

function mergeSubAgentRows(children, jobs) {
  const sortedChildren = maybeArray(children).slice().sort(compareCreatedAscending);
  const sortedJobs = maybeArray(jobs).slice().sort(compareCreatedAscending);
  const rows = [];
  const bySessionId = new Map();
  const byQueueJobId = new Map();

  sortedChildren.forEach((sessionItem) => {
    const row = { session: sessionItem, job: null };
    rows.push(row);
    if (sessionItem.id) bySessionId.set(sessionItem.id, row);
    if (sessionItem.queue_job_id) byQueueJobId.set(sessionItem.queue_job_id, row);
  });

  sortedJobs.forEach((job) => {
    const row = (job.session_id && bySessionId.get(job.session_id)) || (job.id && byQueueJobId.get(job.id));
    if (row) {
      row.job = job;
      return;
    }
    rows.push({ session: null, job });
  });

  return rows.sort((left, right) => compareCreatedAscending(agentRowSortSource(left), agentRowSortSource(right)));
}

function agentRowSortSource(row) {
  return row.session || row.job || {};
}

function compareCreatedAscending(left, right) {
  const leftCreated = left?.created_at || '';
  const rightCreated = right?.created_at || '';
  if (leftCreated !== rightCreated) return leftCreated < rightCreated ? -1 : 1;
  const leftId = left?.id || left?.session_id || left?.queue_job_id || '';
  const rightId = right?.id || right?.session_id || right?.queue_job_id || '';
  return leftId.localeCompare(rightId);
}

function renderTasksPanel(detail) {
  const taskBoard = detail.task_board || {};
  const todos = maybeArray(taskBoard.todo);
  const tasks = maybeArray(taskBoard.tasks);
  const counters = taskBoard.counters || {};
  return `
    <section class="panel-section">
      <div class="summary-grid wide">
        ${renderMetricCard('Todo items', String(todos.length), `${counters.in_progress || 0} in progress`)}
        ${renderMetricCard('Ready tasks', String((taskBoard.groups?.ready || []).length), `${(taskBoard.groups?.blocked || []).length} blocked`)}
        ${renderMetricCard('Completed', String(counters.completed || 0), `${counters.cancelled || 0} cancelled`)}
      </div>
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Todo lane</h4>
      </div>
      ${todos.length ? `<div class="card-stack">${todos.map((item) => renderTodoItem(item)).join('')}</div>` : '<div class="empty-panel">No todo items.</div>'}
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Task graph</h4>
      </div>
      ${tasks.length ? `<div class="card-stack">${tasks.map((task) => renderTaskItem(task)).join('')}</div>` : '<div class="empty-panel">No persistent tasks.</div>'}
    </section>
  `;
}

function collectFileChanges() {
  const detail = state.sessionDetail;
  if (!detail) return [];
  const messages = maybeArray(detail.messages);
  const fileMap = {};
  messages.forEach((msg) => {
    maybeArray(msg.tool_calls).forEach((call) => {
      const parsed = parseMaybeJSON(call.arguments);
      if (call.name === 'shell') {
        collectShellRedirectPaths(parsed?.command).forEach((redirect) => {
          const p = redirect.path;
          if (!fileMap[p]) fileMap[p] = { path: p, writes: 0, edits: 0, linesAdded: 0, linesRemoved: 0 };
          if (redirect.mode === 'append') {
            fileMap[p].edits++;
          } else {
            fileMap[p].writes++;
          }
        });
        return;
      }
      if (call.name !== 'write_file' && call.name !== 'edit_file') return;
      if (!parsed || !parsed.path) return;
      const p = parsed.path;
      if (!fileMap[p]) fileMap[p] = { path: p, writes: 0, edits: 0, linesAdded: 0, linesRemoved: 0 };
      if (call.name === 'write_file') {
        fileMap[p].writes++;
        const lines = (parsed.content || '').split('\n').length;
        fileMap[p].linesAdded += lines;
      } else {
        fileMap[p].edits++;
        const oldLines = (parsed.old_text || '').split('\n').length;
        const newLines = (parsed.new_text || '').split('\n').length;
        fileMap[p].linesAdded += Math.max(0, newLines - oldLines);
        fileMap[p].linesRemoved += Math.max(0, oldLines - newLines);
      }
    });
  });
  return Object.values(fileMap);
}

function renderTodoFloat() {
  const detail = state.sessionDetail;
  if (!detail) {
    return '';
  }
  const taskBoard = detail.task_board || {};
  const todos = maybeArray(taskBoard.todo);
  const tasks = maybeArray(taskBoard.tasks);
  const totalItems = todos.length + tasks.length;
  if (totalItems === 0) {
    return '';
  }

  const expanded = state.todoFloatExpanded;

  // Build a unified flat list sorted by status priority
  const items = [];
  todos.forEach((t) => {
    items.push({ kind: 'todo', label: t.content || 'Untitled', status: t.status || 'pending' });
  });
  tasks.forEach((t) => {
    items.push({ kind: 'task', label: t.subject || t.id || 'Task', status: t.status || 'pending', owner: t.owner || '' });
  });

  // Status order: in_progress first, then pending, then completed/cancelled
  const statusOrder = (s) => {
    const sl = (s || '').toLowerCase();
    if (sl === 'in_progress' || sl === 'running') return 0;
    if (sl === 'pending' || sl === 'todo' || sl === 'ready') return 1;
    if (sl === 'completed' || sl === 'done') return 2;
    return 3;
  };
  items.sort((a, b) => statusOrder(a.status) - statusOrder(b.status));

  // Count from actual items to include both todos and tasks
  let doneCount = 0;
  let activeCount = 0;
  items.forEach((item) => {
    const sl = (item.status || '').toLowerCase();
    if (sl === 'completed' || sl === 'done' || sl === 'cancelled') doneCount++;
    if (sl === 'in_progress' || sl === 'running') activeCount++;
  });
  const progressPct = totalItems > 0 ? Math.round((doneCount / totalItems) * 100) : 0;

  const chevronSVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"></polyline></svg>';

  const statusIcon = (s) => {
    const sl = (s || '').toLowerCase();
    if (sl === 'completed' || sl === 'done') {
      return '<svg class="tf-icon tf-icon-done" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg>';
    }
    if (sl === 'in_progress' || sl === 'running') {
      return '<svg class="tf-icon tf-icon-active" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>';
    }
    if (sl === 'cancelled' || sl === 'failed' || sl === 'error') {
      return '<svg class="tf-icon tf-icon-cancel" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>';
    }
    return '<svg class="tf-icon tf-icon-pending" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/></svg>';
  };

  // Header summary line
  const summaryParts = [];
  if (activeCount > 0) summaryParts.push(`<span class="tf-sum-chip tf-sum-active">${activeCount} active</span>`);
  summaryParts.push(`<span class="tf-sum-chip">${doneCount}/${totalItems} done</span>`);

  let body = '';
  if (expanded) {
    const rows = items.map((item) => {
      const sl = (item.status || '').toLowerCase();
      const rowClass = (sl === 'completed' || sl === 'done') ? ' is-done' : (sl === 'in_progress' || sl === 'running') ? ' is-active' : '';
      return `<div class="tf-row${rowClass}">${statusIcon(item.status)}<span class="tf-row-label">${escapeHTML(item.label)}</span></div>`;
    }).join('');
    body = `<div class="tf-body">${rows}</div>`;
  }

  return `
    <div class="tf-inner ${expanded ? 'is-expanded' : ''}">
      <div class="tf-header" data-todo-float-toggle>
        <div class="tf-header-left">
          <span class="tf-title">Tasks</span>
          <div class="tf-progress-bar"><div class="tf-progress-fill" style="width:${progressPct}%"></div></div>
          <span class="tf-progress-label">${progressPct}%</span>
        </div>
        <div class="tf-header-right">
          ${summaryParts.join('')}
          <button class="tf-chevron" type="button" data-todo-float-toggle aria-label="${expanded ? 'Minimize' : 'Expand'} task panel">${chevronSVG}</button>
        </div>
      </div>
      ${body}
    </div>
  `;
}

function renderFileChangesFloat() {
  const files = collectFileChanges();
  if (files.length === 0) return '';

  const expanded = state.fileChangesExpanded;
  const chevronSVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"></polyline></svg>';
  const fileIconSVG = '<svg class="tf-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>';

  const totalAdded = files.reduce((s, f) => s + f.linesAdded, 0);
  const totalRemoved = files.reduce((s, f) => s + f.linesRemoved, 0);

  let body = '';
  if (expanded) {
    const rows = files.map((f) => {
      const addLabel = f.linesAdded > 0 ? `<span class="tf-file-stat tf-file-stat-add">+${f.linesAdded}</span>` : '';
      const delLabel = f.linesRemoved > 0 ? `<span class="tf-file-stat tf-file-stat-del">-${f.linesRemoved}</span>` : '';
      const opChip = f.writes > 0 && f.edits === 0 ? '<span class="tf-file-op tf-file-op-new">new</span>' : f.edits > 0 ? '<span class="tf-file-op tf-file-op-edit">edit</span>' : '';
      return `<div class="tf-file-row">${fileIconSVG}<span class="tf-file-path" title="${escapeAttr(f.path)}">${escapeHTML(shortenPath(f.path))}</span>${opChip}${addLabel}${delLabel}</div>`;
    }).join('');
    body = `<div class="tf-body">${rows}</div>`;
  }

  const summaryParts = [];
  summaryParts.push(`<span class="tf-sum-chip">${files.length} file${files.length === 1 ? '' : 's'}</span>`);
  if (totalAdded > 0) summaryParts.push(`<span class="tf-sum-chip tf-sum-add">+${totalAdded}</span>`);
  if (totalRemoved > 0) summaryParts.push(`<span class="tf-sum-chip tf-sum-del">-${totalRemoved}</span>`);

  return `
    <div class="tf-inner ${expanded ? 'is-expanded' : ''}">
      <div class="tf-header" data-files-float-toggle>
        <div class="tf-header-left">
          <span class="tf-title">Files</span>
        </div>
        <div class="tf-header-right">
          ${summaryParts.join('')}
          <button class="tf-chevron" type="button" data-files-float-toggle aria-label="${expanded ? 'Minimize' : 'Expand'} file panel">${chevronSVG}</button>
        </div>
      </div>
      ${body}
    </div>
  `;
}

function renderSubAgentFloat() {
  const detail = state.sessionDetail;
  if (!detail) return '';

  const sessions = maybeArray(detail.children?.sessions);
  const jobs = maybeArray(detail.children?.jobs);
  if (!sessions.length && !jobs.length) return '';

  const expanded = state.subAgentExpanded;
  const chevronSVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"></polyline></svg>';

  // Build set of session IDs that already have a session entry.
  // Jobs whose session_id is in this set are redundant — skip them.
  const sessionIds = new Set(sessions.map((s) => s.id));

  // Count orphan jobs for the summary chip
  const orphanJobs = jobs.filter((job) => !sessionIds.has(job.session_id));

  const summaryParts = [];
  summaryParts.push(`<span class="tf-sum-chip sa-sum-sessions">${sessions.length} session${sessions.length === 1 ? '' : 's'}</span>`);
  if (orphanJobs.length) summaryParts.push(`<span class="tf-sum-chip sa-sum-jobs">${orphanJobs.length} job${orphanJobs.length === 1 ? '' : 's'}</span>`);

  let body = '';
  if (expanded) {
    const rows = [];
    sessions.forEach((sess) => {
      // Look up matching job to show mode as extra context
      const matchedJob = jobs.find((job) => job.session_id === sess.id);
      rows.push(renderSubAgentSessionRow(sess, matchedJob));
    });
    orphanJobs.forEach((job) => {
      rows.push(renderSubAgentJobRow(job));
    });
    body = `<div class="sa-tree-body">${rows.join('')}</div>`;
  }

  return `
    <div class="sa-float-panel">
      <div class="tf-inner ${expanded ? 'is-expanded' : ''}">
        <div class="tf-header" data-sub-agent-toggle>
          <div class="tf-header-left">
            <span class="tf-title">Sub Agents</span>
          </div>
          <div class="tf-header-right">
            ${summaryParts.join('')}
            <button class="tf-chevron" type="button" data-sub-agent-toggle aria-label="${expanded ? 'Minimize' : 'Expand'} sub-agent panel">${chevronSVG}</button>
          </div>
        </div>
        ${body}
      </div>
    </div>
  `;
}

function renderSubAgentSessionRow(sess, job) {
  const statusTone = toneForStatus(sess.status);
  const label = agentLabel(sess.agent_name, sess.agent_role) || shortId(sess.id);
  const jobMeta = job ? ` · ${escapeHTML(job.mode || '')}` : '';
  return `
    <div class="sa-tree-row parent" data-sub-agent-open="${escapeAttr(sess.id)}" title="Click to open child session">
      <span class="sa-tree-dot ${statusTone}"></span>
      <span class="sa-tree-label">${escapeHTML(label)}</span>
      <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(sess.status))}</span>
      <span class="sa-tree-meta">${escapeHTML(sess.model || sess.provider || 'n/a')}${jobMeta} · ${escapeHTML(shortId(sess.id))}</span>
      ${renderSessionStopButton(sess.id, sess.status, '')}
    </div>
  `;
}

function renderSubAgentJobRow(job) {
  const status = job.session_status || job.status;
  const statusTone = toneForStatus(status);
  const label = agentLabel(job.agent_name, job.agent_role) || shortId(job.id);
  const targetAttr = job.session_id
    ? `data-sub-agent-open="${escapeAttr(job.session_id)}" title="Click to open child session"`
    : `data-open-job="${escapeAttr(job.id)}" title="Click to open queue job"`;
  return `
    <div class="sa-tree-row orphan" ${targetAttr}>
      <span class="sa-tree-dot ${statusTone}"></span>
      <span class="sa-tree-label">${escapeHTML(label)}</span>
      <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(status))}</span>
      <span class="sa-tree-meta">${escapeHTML(job.mode || '')} · ${escapeHTML(shortId(job.id))}</span>
      ${job.session_id ? renderSessionStopButton(job.session_id, status, '') : ''}
    </div>
  `;
}

function renderActivityFloat() {
  const subHTML = renderSubAgentFloat();
  const todoHTML = renderTodoFloat();
  const filesHTML = renderFileChangesFloat();
  if (!subHTML && !todoHTML && !filesHTML) return '';
  let duoHTML = '';
  if (todoHTML && filesHTML) {
    duoHTML = `<div class="tf-duo"><div class="tf-duo-left">${todoHTML}</div><div class="tf-duo-right">${filesHTML}</div></div>`;
  } else {
    duoHTML = todoHTML || filesHTML || '';
  }
  return duoHTML + subHTML;
}

function renderSessionRail() {
  const currentID = state.sessionBacked ? state.sessionId : '';
  const recent = maybeArray(state.overview?.recent_sessions);
  const pinned = state.sessionDetail?.metadata
    ? [sessionSummaryFromDetail(state.sessionDetail)]
    : [];
  const items = uniqueById(pinned.concat(recent)).slice(0, 12);
  return `
    <div class="rail-header">
      <div>
        <div class="inspector-eyebrow">Sessions</div>
        <h3>Workspace</h3>
      </div>
      <button class="mini-link-btn" type="button" data-view-shortcut="history">Sessions</button>
    </div>
    <div class="session-rail-list">
      ${items.length ? items.map((item) => `
        <button class="session-rail-row ${item.id === currentID ? 'active' : ''}" type="button" data-open-session="${escapeAttr(item.id)}" data-session-id="${escapeAttr(item.id)}">
          <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
          <span class="session-rail-id">${escapeHTML(shortId(item.id))}</span>
          <span class="session-rail-meta">${escapeHTML(item.provider || 'provider')} · ${escapeHTML(item.model || 'model')}</span>
          <span class="session-rail-meta">${escapeHTML(workdirBase(item.workdir))}${item.agent_role ? ` · ${escapeHTML(item.agent_role)}` : ''}</span>
        </button>
      `).join('') : '<div class="empty-panel compact">No durable sessions yet.</div>'}
    </div>
  `;
}

function renderInspectorPanel() {
  const detail = state.sessionDetail;
  if (!detail) {
    return `
      <div class="inspector-header">
        <div>
          <div class="inspector-eyebrow">Tracker</div>
          <h3>No session loaded</h3>
        </div>
      </div>
      <div class="inspector-content">
        <div class="empty-panel compact">No todo/task state recorded.</div>
      </div>
    `;
  }
  const tabs = [
    ['summary', 'Summary'],
    ['tasks', 'Tasks'],
    ['agents', 'Background'],
    ['timeline', 'Timeline']
  ];
  const active = tabs.some(([key]) => key === state.inspectorTab) ? state.inspectorTab : 'tasks';
  const panel = active === 'summary'
    ? renderSummaryPanel(detail)
    : active === 'agents'
      ? renderAgentsPanel(detail)
      : active === 'timeline'
        ? renderTimelinePanel(detail)
        : renderTasksPanel(detail);
  return `
    <div class="inspector-header">
      <div>
        <div class="inspector-eyebrow">Tracker</div>
        <h3>${escapeHTML(shortId(detail.metadata?.id || state.sessionId))}</h3>
      </div>
      <span class="status-badge ${toneForStatus(detail.state?.status)}">${escapeHTML(humanizeStatus(detail.state?.status || 'idle'))}</span>
    </div>
    <div class="inspector-tabs" role="tablist">
      ${tabs.map(([key, label]) => `<button class="inspector-tab ${key === active ? 'active' : ''}" type="button" data-inspector-tab="${escapeAttr(key)}">${escapeHTML(label)}</button>`).join('')}
    </div>
    <div class="inspector-content">${panel}</div>
  `;
}

function sessionSummaryFromDetail(detail) {
  return {
    id: detail.metadata?.id || state.sessionId,
    status: detail.state?.status || 'running',
    provider: detail.metadata?.provider || '',
    model: detail.metadata?.model || '',
    updated_at: detail.state?.updated_at || detail.metadata?.created_at || '',
    phase: detail.state?.phase || '',
    workdir: detail.metadata?.workdir || '',
    agent_name: detail.metadata?.agent_name || '',
    agent_role: detail.metadata?.agent_role || ''
  };
}

function uniqueById(items) {
  const seen = new Set();
  return maybeArray(items).filter((item) => {
    const id = item?.id;
    if (!id || seen.has(id)) {
      return false;
    }
    seen.add(id);
    return true;
  });
}

function workdirBase(path) {
  const text = String(path || '').replaceAll('\\', '/');
  if (!text) {
    return 'workspace';
  }
  return text.split('/').filter(Boolean).pop() || text;
}

function renderMetricCard(label, value, copy) {
  return `
    <div class="metric-card">
      <span class="metric-label">${escapeHTML(label)}</span>
      <div class="metric-card-value">${escapeHTML(value)}</div>
      <div class="metric-card-copy">${escapeHTML(copy || '')}</div>
    </div>
  `;
}

function renderMiniMetric(label, value) {
  return `
    <div class="summary-card">
      <span class="metric-label">${escapeHTML(label)}</span>
      <div class="metric-card-value mini-metric-value">${escapeHTML(value)}</div>
    </div>
  `;
}

function renderKVRow(label, value) {
  if (value === undefined || value === null || value === '') {
    return '';
  }
  return `
    <div class="kv-row">
      <div class="kv-row-label">${escapeHTML(label)}</div>
      <div class="kv-row-value">${escapeHTML(String(value))}</div>
    </div>
  `;
}

function renderLiveToolEntry(entry) {
  return `
    <div class="live-tool-card">
      <div class="job-card-top">
        <div class="live-tool-title">${escapeHTML(entry.name)}</div>
        <span class="status-badge ${entry.kind === 'result' ? (entry.is_error ? 'danger' : 'live') : 'neutral'}">${escapeHTML(entry.kind === 'result' ? 'Result' : 'Call')}</span>
      </div>
      <div class="task-card-copy">${escapeHTML(entry.kind === 'result' ? truncateText(entry.display_output || '(no output)', 160) : truncateText(prettyJSON(entry.arguments), 160))}</div>
      <div class="job-card-meta">${escapeHTML(formatTimestamp(entry.created_at))}</div>
    </div>
  `;
}

function renderSteerQueue(items) {
  const steers = maybeArray(items).slice(-4).reverse();
  if (!steers.length) {
    return '<div class="empty-panel">No steer requests yet.</div>';
  }
  return steers.map((item) => `
    <div class="notification-card">
      <div class="job-card-top">
        <div class="job-card-title">Steer ${escapeHTML(shortId(item.id))}</div>
        <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
      </div>
      <div class="notification-copy">${escapeHTML(truncateText(item.text, 180))}</div>
      <div class="job-card-meta">${escapeHTML(formatTimestamp(item.created_at))}${item.interrupt ? ' · interrupt' : ''}</div>
    </div>
  `).join('');
}

function renderBackgroundNotificationsPreview(items) {
  const notifications = maybeArray(items).slice(-3).reverse();
  if (!notifications.length) {
    return '<div class="empty-panel">No background notifications yet.</div>';
  }
  return notifications.map((item) => `
    <div class="notification-card">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(agentLabel(item.agent_name, item.agent_role) || 'Background result')}</div>
        <span class="status-badge ${toneForStatus(item.status || item.session_status)}">${escapeHTML(humanizeStatus(item.status || item.session_status || 'unknown'))}</span>
      </div>
      <div class="notification-copy">${escapeHTML(truncateText(item.final_text || item.last_error || 'No final text recorded.', 180))}</div>
      <div class="job-card-meta">${escapeHTML(shortId(item.queue_job_id || item.session_id || item.id))}</div>
    </div>
  `).join('');
}

function renderTimelineItem(item, options = {}) {
  const descriptor = describeTimelineItem(item);
  const showData = !options.hideData && descriptor.data;
  return `
    <div class="timeline-card ${options.compact ? 'compact' : ''}">
      <div class="timeline-icon ${descriptor.tone === 'danger' ? 'danger' : descriptor.tone === 'queued' ? 'warning' : ''}">
        <i data-lucide="${escapeAttr(descriptor.icon)}" class="icon-small"></i>
      </div>
      <div class="timeline-card-copy">
        <div class="timeline-card-top">
          <div class="timeline-card-title">${escapeHTML(descriptor.title)}</div>
          <span class="timeline-card-meta">${escapeHTML(formatTimestamp(item.time))}</span>
        </div>
        <div class="timeline-card-text">${escapeHTML(descriptor.copy)}</div>
        ${descriptor.meta ? `<div class="timeline-card-meta">${escapeHTML(descriptor.meta)}</div>` : ''}
        ${showData ? `<pre class="timeline-card-data">${escapeHTML(descriptor.data)}</pre>` : ''}
      </div>
    </div>
  `;
}

function renderSubAgentCard(row) {
  const sessionItem = row.session || null;
  const job = row.job || null;
  const status = isTerminalStatus(job?.status) ? job.status : (sessionItem?.status || job?.status || 'unknown');
  const label = agentLabel(sessionItem?.agent_name || job?.agent_name, sessionItem?.agent_role || job?.agent_role) || shortId(sessionItem?.id || job?.id || '');
  const model = sessionItem?.model || job?.model || sessionItem?.provider || job?.provider || 'n/a';
  const phase = sessionItem?.phase ? phaseHeadline(sessionItem.phase) : (job?.mode || 'exec');
  const sessionId = sessionItem?.id || job?.session_id || '';
  const jobId = job?.id || sessionItem?.queue_job_id || '';
  const error = sessionItem?.last_error || job?.last_error || '';
  return `
    <div class="agent-card">
      <div class="agent-card-top">
        <div class="agent-card-title">${escapeHTML(label)}</div>
        <span class="status-badge ${toneForStatus(status)}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      <div class="agent-card-copy">${escapeHTML(model)} · ${escapeHTML(phase)}</div>
      ${error ? `<div class="notification-copy danger">${escapeHTML(truncateText(error, 180))}</div>` : ''}
      <div class="agent-card-meta">${sessionId ? escapeHTML(shortId(sessionId)) : ''}${jobId ? `${sessionId ? ' · ' : ''}job ${escapeHTML(shortId(jobId))}` : ''}</div>
      ${renderVisiblePaths(sessionItem?.visible_paths || job?.visible_paths)}
      <div class="card-actions">
        ${sessionId ? renderSessionStopButton(sessionId, status) : ''}
        ${sessionId ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(sessionId)}">Open child session</button>` : ''}
        ${jobId ? `<button class="mini-link-btn" type="button" data-open-job="${escapeAttr(jobId)}">Open job</button>` : ''}
        ${job?.parent_session_id ? `<button class="mini-link-btn" type="button" data-open-parent-session="${escapeAttr(job.parent_session_id)}">Open parent session</button>` : ''}
      </div>
    </div>
  `;
}

function isTerminalStatus(status) {
  return status === 'completed' || status === 'failed';
}

function renderChildSessionCard(item) {
  return `
    <div class="agent-card">
      <div class="agent-card-top">
        <div class="agent-card-title">${escapeHTML(agentLabel(item.agent_name, item.agent_role) || shortId(item.id))}</div>
        <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
      </div>
      <div class="agent-card-copy">${escapeHTML(item.model || item.provider || 'n/a')} · ${escapeHTML(phaseHeadline(item.phase || 'prepare'))}</div>
      <div class="agent-card-meta">${escapeHTML(shortId(item.id))}${item.queue_job_id ? ` · job ${escapeHTML(shortId(item.queue_job_id))}` : ''}</div>
      <div class="card-actions">
        ${renderSessionStopButton(item.id, item.status)}
        <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.id)}">Open child session</button>
      </div>
    </div>
  `;
}

function renderQueueJobCard(job) {
  const status = job.session_status || job.status;
  return `
    <div class="job-card ${job.id === state.selectedQueueJobId ? 'active' : ''}" data-queue-job-id="${escapeAttr(job.id)}">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(agentLabel(job.agent_name, job.agent_role) || shortId(job.id))}</div>
        <span class="status-badge ${toneForStatus(status)}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      <div class="job-card-copy">${escapeHTML(truncateText(job.prompt || '(no prompt)', 180))}</div>
      <div class="job-card-meta">${escapeHTML(shortId(job.id))} · ${escapeHTML(job.mode || 'exec')}${job.session_id ? ` · child ${escapeHTML(shortId(job.session_id))}` : ''}</div>
      ${renderVisiblePaths(job.visible_paths)}
      <div class="card-actions">
        ${job.session_id ? renderSessionStopButton(job.session_id, status) : ''}
        <button class="mini-link-btn" type="button" data-open-job="${escapeAttr(job.id)}">Open job</button>
        ${job.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(job.session_id)}">Open child session</button>` : ''}
        ${job.parent_session_id ? `<button class="mini-link-btn" type="button" data-open-parent-session="${escapeAttr(job.parent_session_id)}">Open parent session</button>` : ''}
      </div>
    </div>
  `;
}

function renderSessionStopButton(sessionID, status, label = 'Stop') {
  if (!sessionID || !isStoppableSessionStatus(status)) {
    return '';
  }
  const busy = isStoppingSession(sessionID);
  const text = label ? `<span>${escapeHTML(label)}</span>` : '';
  return `
    <button class="mini-link-btn danger" type="button" data-stop-session-id="${escapeAttr(sessionID)}" aria-label="Stop session ${escapeAttr(shortId(sessionID))}" title="Stop session" ${busy ? 'disabled' : ''}>
      <i data-lucide="square"></i>
      ${text}
    </button>
  `;
}

function renderNotificationCard(item) {
  return `
    <div class="notification-card">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(agentLabel(item.agent_name, item.agent_role) || 'Background notification')}</div>
        <span class="status-badge ${toneForStatus(item.status || item.session_status)}">${escapeHTML(humanizeStatus(item.status || item.session_status || 'unknown'))}</span>
      </div>
      <div class="notification-copy">${escapeHTML(truncateText(item.final_text || item.last_error || 'No final text recorded.', 200))}</div>
      <div class="job-card-meta">${escapeHTML(shortId(item.queue_job_id || item.session_id || item.id))}${item.delivery_status ? ` · ${escapeHTML(item.delivery_status)}` : ''}</div>
      ${renderVisiblePaths(item.visible_paths)}
      <div class="card-actions">
        ${item.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.session_id)}">Open child session</button>` : ''}
      </div>
    </div>
  `;
}

function renderTodoItem(item) {
  return `
    <div class="todo-card">
      <div class="job-card-top">
        <div class="todo-card-title">${escapeHTML(item.content || 'Untitled todo')}</div>
        <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
      </div>
      <div class="todo-pill-row">
        ${item.priority ? `<span class="task-chip">${escapeHTML(item.priority)}</span>` : ''}
        ${item.updated_at ? `<span class="task-chip">${escapeHTML(formatTimestamp(item.updated_at))}</span>` : ''}
      </div>
    </div>
  `;
}

function renderTaskItem(task) {
  return `
    <div class="task-card">
      <div class="job-card-top">
        <div class="task-card-title">${escapeHTML(task.subject || task.id || 'Task')}</div>
        <span class="status-badge ${toneForStatus(task.status)}">${escapeHTML(humanizeStatus(task.status))}</span>
      </div>
      <div class="task-card-copy">${escapeHTML(task.description || 'No description.')}</div>
      <div class="task-pill-row">
        ${task.priority ? `<span class="task-chip">${escapeHTML(task.priority)}</span>` : ''}
        ${task.owner ? `<span class="task-chip">${escapeHTML(task.owner)}</span>` : ''}
        ${maybeArray(task.blocked_by).length ? `<span class="task-chip">blocked by ${escapeHTML(String(task.blocked_by.length))}</span>` : ''}
        ${maybeArray(task.blocks).length ? `<span class="task-chip">blocks ${escapeHTML(String(task.blocks.length))}</span>` : ''}
      </div>
    </div>
  `;
}
