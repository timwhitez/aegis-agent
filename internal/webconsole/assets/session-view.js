/*
 * Session workspace rendering helpers.
 * Loaded before app.js and uses the shared WebConsole state/nodes globals.
 */

function renderShortcutHelp() {
  let overlay = document.getElementById('shortcut-help-overlay');
  const isNew = !overlay;

  if (!isHelpVisible()) {
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
        setHelpVisible(false);
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
  const shouldStick = isChatNearBottom(nodes.chatContainer) || !chatRenderCacheValue('body');
  const prependScrollHeight = preserveScrollAfterRenderHeight();
  const sections = renderMessageStream();
  let mutated = false;

  mutated = patchChatSlot(slots.activity, 'activity', sections.activity) || mutated;
  mutated = patchChatSlot(slots.flow, 'flow', sections.flow) || mutated;
  mutated = patchChatSlot(slots.body, 'body', sections.body) || mutated;
  mutated = patchChatSlot(slots.pending, 'pending', sections.pending) || mutated;
  if (nodes.sessionRail && window.getComputedStyle(nodes.sessionRail).display !== 'none') {
    mutated = patchAuxSlot(nodes.sessionRail, 'rail', renderSessionRail()) || mutated;
  }
  const fixedInspectorVisible = Boolean(nodes.inspectorPanel && window.getComputedStyle(nodes.inspectorPanel).display !== 'none');
  const slideOutInspectorAvailable = Boolean(nodes.inspectorSlideOut);
  if (fixedInspectorVisible || slideOutInspectorAvailable) {
    const inspectorHTML = renderInspectorPanel();
    if (fixedInspectorVisible) {
      mutated = patchAuxSlot(nodes.inspectorPanel, 'inspector', inspectorHTML) || mutated;
    }
    if (slideOutInspectorAvailable) {
      patchScrollableAuxSlot(nodes.inspectorSlideOut, 'inspectorSlideOut', inspectorHTML);
    }
  }
  if (nodes.todoFloatPanel) {
    patchAuxSlot(nodes.todoFloatPanel, 'todoFloat', renderActivityFloat());
  }

  if (!mutated) {
    return;
  }

  if (prependScrollHeight !== null) {
    nodes.chatContainer.scrollTop = nodes.chatContainer.scrollHeight - prependScrollHeight;
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
  return patchCachedMarkup(node, key, html);
}

function patchScrollableAuxSlot(node, key, html) {
  const previousScrollTop = Number(node?.scrollTop || 0);
  const mutated = patchAuxSlot(node, key, html);
  if (mutated && node) {
    node.scrollTop = previousScrollTop;
  }
  return mutated;
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
    resetChatRenderCache();
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
  return patchCachedMarkup(node, key, html, { hideWhenEmpty: true });
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
  const optimisticMessages = currentOptimisticMessages().slice();
  const stream = detailMessages.length ? detailMessages.concat(optimisticMessages) : optimisticMessages;
  const displayStream = buildDisplayMessageStream(stream);

  if (!displayStream.length) {
    return {
      activity: hasDurableSession() || isGenerating() ? renderSessionActivityCard() : '',
      flow: renderFlowLane(),
      body: renderEmptySessionState(),
      pending: isGenerating() ? renderPendingStageCard() : ''
    };
  }

  const loadEarlierHTML = hasMoreMessagesToLoad() ? `
    <div class="load-earlier-bar">
      <button class="load-earlier-btn" type="button" data-load-earlier ${isLoadingEarlierMessages() ? 'disabled' : ''}>
        ${isLoadingEarlierMessages() ? 'Loading...' : 'Load earlier messages'}
      </button>
    </div>
  ` : '';

  const bodyHTML = loadEarlierHTML + displayStream.map((message) => renderMessage(message)).join('');

  return {
    activity: hasDurableSession() || isGenerating() ? renderSessionActivityCard() : '',
    flow: renderFlowLane(),
    body: bodyHTML,
    pending: isGenerating() ? renderPendingStageCard() : ''
  };
}

function buildDisplayMessageStream(messages) {
  const display = [];
  maybeArray(messages).forEach((message) => {
    const current = cloneDisplayMessage(message);
    const previous = display[display.length - 1];
    if (previous && isMergeableToolResultMessage(current)) {
      const partition = partitionMatchingToolResults(previous, current);
      if (partition.matched.length) {
        mergeToolResultsIntoDisplayMessage(previous, current, partition.matched);
        if (!partition.unmatched.length) {
          return;
        }
        current.tool_results = partition.unmatched;
      }
    }
    display.push(current);
  });
  return display;
}

function cloneDisplayMessage(message) {
  const copy = { ...(message || {}) };
  if (Array.isArray(message?.tool_calls)) {
    copy.tool_calls = message.tool_calls.slice();
  }
  if (Array.isArray(message?.tool_results)) {
    copy.tool_results = message.tool_results.slice();
  }
  return copy;
}

function isMergeableToolResultMessage(message) {
  return message?.role === 'tool' &&
    !String(message.text || '').trim() &&
    !String(message.thinking || '').trim() &&
    maybeArray(message.tool_calls).length === 0 &&
    maybeArray(message.tool_results).length > 0;
}

function partitionMatchingToolResults(previous, toolMessage) {
  const callIDs = new Set(maybeArray(previous.tool_calls).flatMap(toolCallIDs));
  if (!callIDs.size) {
    return { matched: [], unmatched: maybeArray(toolMessage.tool_results) };
  }
  const matched = [];
  const unmatched = [];
  maybeArray(toolMessage.tool_results).forEach((result) => {
    if (result?.tool_call_id && callIDs.has(result.tool_call_id)) {
      matched.push(result);
    } else {
      unmatched.push(result);
    }
  });
  return { matched, unmatched };
}

function mergeToolResultsIntoDisplayMessage(previous, toolMessage, results) {
  previous.tool_results = maybeArray(previous.tool_results).concat(results);
  const mergedIDs = maybeArray(previous._merged_tool_result_message_ids).slice();
  if (toolMessage?.id && !mergedIDs.includes(toolMessage.id)) {
    mergedIDs.push(toolMessage.id);
  }
  previous._merged_tool_result_message_ids = mergedIDs;
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

function goalStatusTone(status) {
  switch (String(status || '').toLowerCase()) {
    case 'active':
      return 'live';
    case 'paused':
    case 'budget_limited':
      return 'queued';
    case 'complete':
      return 'live';
    case 'cleared':
      return 'danger';
    default:
      return 'neutral';
  }
}

function goalStatusLabel(goal) {
  if (!goal) {
    return 'Goal off';
  }
  return `Goal ${humanizeStatus(goal.status || 'active')}`;
}

function goalRuntimeFacts(detail) {
  const goal = detail?.goal || null;
  if (!goal) {
    return null;
  }
  const latestHistory = detail?.goal_facts?.latest_history || null;
  const progress = maybeArray(detail?.goal_facts?.progress || goal.progress);
  const latestProgress = progress.length ? progress[progress.length - 1] : null;
  const latestAt = latestHistory?.created_at || latestProgress?.created_at || goal.updated_at || goal.created_at || '';
  const latestType = latestHistory?.type || latestProgress?.kind || '';
  const runStatus = detail?.state?.status || '';
  const runPhase = detail?.state?.phase || '';
  return {
    goal,
    latestHistory,
    latestProgress,
    latestAt,
    latestType,
    progressCount: progress.length,
    runStatus,
    runPhase
  };
}

function humanizeGoalEventType(value) {
  return humanizeStatus(String(value || '').replaceAll('.', ' '));
}

function isHistoricalCompletedGoal(detail) {
  return String(detail?.goal?.status || '').toLowerCase() === 'complete' &&
    String(detail?.state?.status || '').toLowerCase() !== 'completed';
}

function renderSessionGoalLine(detail) {
  const facts = goalRuntimeFacts(detail);
  if (!facts) {
    return `
      <div class="session-goal-line">
        <span class="surface-chip">Goal off</span>
        <span>No durable Goal is attached to this session.</span>
      </div>
    `;
  }
  const goal = facts.goal;
  const pieces = [
    facts.runStatus ? `session ${humanizeStatus(facts.runStatus)}` : '',
    facts.runPhase ? phaseHeadline(facts.runPhase) : '',
    `tokens ${formatBudget(goal.tokens_used, goal.token_budget)}`,
    `provider time ${formatSecondsBudget(goal.provider_time_used_seconds ?? goal.time_used_seconds, goal.provider_time_budget_seconds ?? goal.time_budget_seconds)}`,
    facts.latestType ? `latest ${humanizeGoalEventType(facts.latestType)}` : '',
    facts.latestAt ? formatTimestamp(facts.latestAt) : ''
  ].filter(Boolean);
  return `
    <div class="session-goal-line">
      <span class="status-badge ${goalStatusTone(goal.status)}">${escapeHTML(goalStatusLabel(goal))}</span>
      <span>${escapeHTML(pieces.join(' · '))}</span>
    </div>
  `;
}

function renderSessionActivityCard() {
  const detail = state.sessionDetail;
  const liveActivity = currentLiveActivity();
  const counters = summarizeCurrentSession();
  const rawStatus = detail?.state?.status || '';
  const status = typeof sessionDetailDisplayStatus === 'function'
    ? sessionDetailDisplayStatus(detail) || (isGenerating() ? 'running' : 'idle')
    : rawStatus || (isGenerating() ? 'running' : 'idle');
  const goal = detail?.goal || null;
  const planMode = detail?.plan_mode || null;
  const planModeStatus = String(planMode?.status || '').toLowerCase();
  const awaitingPlanApproval = planModeStatus === 'awaiting_approval';
  const awaitingPlanInput = planModeStatus === 'awaiting_user_input';
  const phase = awaitingPlanApproval
    ? 'Plan approval'
    : awaitingPlanInput
      ? 'Plan input'
      : detail?.state?.phase ? phaseHeadline(detail.state.phase) : liveActivity.title;
  const tone = toneForStatus(status);
  const failureSummary = detail ? summarizeProviderFailure(detail) : null;
  const copy = awaitingPlanApproval
    ? (planMode?.summary
        ? `Review the submitted plan, then approve it to run, ask for changes, or cancel Plan Mode. Summary: ${planMode.summary}`
        : 'Review the submitted plan, then approve it to run, ask for changes, or cancel Plan Mode.')
    : awaitingPlanInput
      ? 'Plan Mode is waiting for your answer in the Plan inspector before it can continue planning.'
      : failureSummary?.activityCopy || detail?.state?.last_error || liveActivity.copy;
  const summary = summarizeLiveCounters(counters);
  const canContinue = hasDurableSession() &&
    ['paused', 'awaiting_input', 'failed', 'completed'].includes(rawStatus) &&
    !awaitingPlanApproval &&
    !awaitingPlanInput;

  return `
    <section class="session-flow-card">
      <div class="session-flow-head">
        <div class="session-flow-state">
          <span class="status-badge ${tone}">${escapeHTML(humanizeStatus(status))}</span>
          <span class="session-flow-phase">${escapeHTML(phase)}</span>
        </div>
        <div class="session-flow-meta">
          ${detail?.metadata?.id ? `<span class="tiny-code-chip">${escapeHTML(shortId(detail.metadata.id))}</span>` : `<span class="tiny-code-chip">${escapeHTML(shortId(state.sessionId))}</span>`}
          ${goal ? `<span class="status-badge ${goalStatusTone(goal.status)}">${escapeHTML(goalStatusLabel(goal))}</span>` : '<span class="surface-chip">Goal off</span>'}
          ${planMode ? `<span class="surface-chip">plan · ${escapeHTML(humanizeStatus(planMode.status || 'planning'))}</span>` : ''}
          ${summary ? `<span class="surface-chip">${escapeHTML(summary)}</span>` : ''}
        </div>
      </div>
      <div class="session-flow-copy">${escapeHTML(copy || 'Waiting for the next update.')}</div>
      ${detail ? renderSessionGoalLine(detail) : ''}
      ${awaitingPlanApproval ? `
        <div class="session-flow-actions plan-approval-actions">
          <button class="inline-action-btn" type="button" data-plan-action="approve">Approve & Run</button>
          <button class="mini-link-btn" type="button" data-plan-action="revise">Ask for Changes</button>
          <button class="mini-link-btn danger" type="button" data-plan-action="cancel">Cancel</button>
        </div>
      ` : ''}
      ${awaitingPlanInput ? `
        <div class="session-flow-actions plan-approval-actions">
          <button class="inline-action-btn" type="button" data-focus-inspector-tab="plan">Open Plan Input</button>
          <button class="mini-link-btn danger" type="button" data-plan-action="cancel">Cancel</button>
        </div>
      ` : ''}
      ${canContinue ? `<div class="session-flow-actions"><button class="inline-action-btn" type="button" data-continue-session="${escapeAttr(state.sessionId)}">Continue session</button></div>` : ''}
    </section>
  `;
}

function renderFlowLane() {
  const detailItems = maybeArray(state.sessionDetail?.timeline)
    .filter((item) => item.kind === 'event' && isCompactFlowEvent(item.event_type))
    .slice(0, 3)
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
  const hasThinking = String(message.thinking || '').trim().length > 0;
  const hasText = String(message.text || '').trim().length > 0;
  const thinkingHTML = hasThinking ? renderThinkingBlock(message.thinking) : '';
  const textHTML = hasText ? renderMessageText(message) : '';
  const finalToolResult = primaryFinalFinishResult(message);
  const finalToolResultHTML = !hasText && finalToolResult ? renderFinalToolResultBubble(finalToolResult) : '';
  const toolLaneHTML = renderToolLane(message, { finalTextRendered: Boolean(finalToolResultHTML) });

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
        ${thinkingHTML}
        ${textHTML}
        ${finalToolResultHTML}
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
		return `<div class="message-bubble message-bubble-plaintext" translate="no">${escapeHTML(String(message.text || ''))}</div>`;
  }
  const cacheKey = message.id
    ? `msg:${message.id}`
    : message.optimisticKey
      ? `optim:${message.optimisticKey}`
      : 'msg:anon';
	return `<div class="message-bubble prose" translate="no">${renderMarkdownCached(cacheKey, message.text)}</div>`;
}

function primaryFinalFinishResult(message) {
  return maybeArray(message?.tool_results).find((result) =>
    result?.name === 'finish' &&
    result?.final &&
    !result?.is_error &&
    String(result?.display_output || result?.llm_output || '').trim()
  ) || null;
}

function renderFinalToolResultBubble(result) {
  const text = result.display_output || result.llm_output || '';
  const cacheKey = result.tool_call_id ? `final:${result.tool_call_id}` : `final-anon:${(text || '').length}`;
	return `<div class="message-bubble prose final-response-bubble" translate="no">${renderMarkdownCached(cacheKey, text)}</div>`;
}

function renderThinkingBlock(thinking) {
  const preview = truncateText(thinking, 200);
  return `
    <details class="thinking-block">
      <summary class="thinking-summary">
        <i data-lucide="sparkles" class="thinking-icon"></i>
        <span>Thinking (${thinking.length} chars)</span>
        <span class="thinking-preview">${escapeHTML(preview)}</span>
      </summary>
		<div class="thinking-body" translate="no">${renderMarkdownCached(`thinking:${thinking.length}`, thinking)}</div>
    </details>
  `;
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
		return `<div class="message-bubble message-bubble-plaintext" translate="no">${escapeHTML(String(message.text || ''))}</div>`;
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
			<div class="agent-result-copy" translate="no">${escapeHTML(body)}</div>
      <div class="agent-result-meta">
        ${item.queue_job_id ? `<span class="tiny-code-chip">job ${escapeHTML(shortId(item.queue_job_id))}</span>` : ''}
        ${item.session_id ? `<span class="tiny-code-chip">child ${escapeHTML(shortId(item.session_id))}</span>` : ''}
        ${workdir ? `<span class="surface-chip">${escapeHTML(workdirBase(workdir))}</span>` : ''}
      </div>
      ${renderVisiblePaths(item.visible_paths)}
      <div class="card-actions">
        ${item.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.session_id)}">Open child session</button>` : ''}
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

function renderToolLane(message, options = {}) {
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
    const callResults = toolCallIDs(call).flatMap((id) => resultsByCallId.get(id) || []);
    for (const r of callResults) pairedResults.add(r);

    const delegate = isMultiAgentTool(call.name);
    const compactFinal = options.finalTextRendered && call.name === 'finish' && callResults.some(isFinalFinishResult);
    const hasExpanded = delegate || callResults.some(function(r) { return r.is_error || (r.final && !compactFinal); });
    const callPreview = summarizeToolCall(call, { finalTextRendered: options.finalTextRendered, pairedResults: callResults });
    const callPreviewRaw = toolCallPreviewIsRaw(call, compactFinal);

    treeHTML +=
      '<details class="tl-row tl-row-call"' + (hasExpanded ? ' open' : '') + '>' +
        '<summary class="tl-summary">' +
          '<span class="tl-type-chip call">Call</span>' +
          '<strong class="tl-name">' + escapeHTML(call.name) + '</strong>' +
          (call.id ? '<span class="tl-id-chip">' + escapeHTML(shortId(call.id)) + '</span>' : '') +
          '<span class="tl-preview"' + (callPreviewRaw ? ' translate="no" data-i18n-skip' : '') + '>' + escapeHTML(callPreview) + '</span>' +
        '</summary>' +
        renderToolCallBody(call, { finalTextRendered: options.finalTextRendered, pairedResults: callResults }) +
        callResults.map(function(r) { return renderToolLaneResultRow(r, true, options); }).join('') +
      '</details>';
  }

  for (var i = 0; i < results.length; i++) {
    if (pairedResults.has(results[i])) continue;
    treeHTML += renderToolLaneResultRow(results[i], false, options);
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

function renderToolLaneResultRow(result, indent, options = {}) {
  var payloadText = result.display_output || result.llm_output || '(no output)';
  var parsed = parseMaybeJSON(payloadText);
  var delegate = isMultiAgentTool(result.name);
  var compactFinal = options.finalTextRendered && isFinalFinishResult(result);
  var open = result.is_error || delegate || (result.final && !compactFinal);
  var special = renderSpecialToolResult(result, parsed);
  var body = special || '<pre class="tl-body">' + escapeHTML(truncateText(payloadText, 3200)) + '</pre>';

  return (
    '<details class="tl-row tl-row-result' + (indent ? ' tl-indent' : '') + (result.is_error ? ' tl-error' : '') + '"' + (open ? ' open' : '') + '>' +
      '<summary class="tl-summary">' +
        '<span class="tl-type-chip ' + (result.is_error ? 'error' : delegate ? 'child' : 'result') + '">' + (result.is_error ? 'Error' : delegate ? 'Child' : 'Result') + '</span>' +
        '<strong class="tl-name">' + escapeHTML(result.name) + '</strong>' +
        (result.final ? '<span class="tl-badge final">Final</span>' : '') +
        (delegate ? '<span class="tl-badge delegate">Delegate</span>' : '') +
        '<span class="tl-preview"' + (compactFinal ? '' : ' translate="no" data-i18n-skip') + '>' + escapeHTML(compactFinal ? 'Final response captured' : summarizeToolResult(result, parsed, payloadText)) + '</span>' +
      '</summary>' +
      body +
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
	chips.push(`<span class="message-meta-chip" translate="no" data-i18n-skip>${escapeHTML(sourceLabel)}</span>`);
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

function renderToolCallBody(call, options = {}) {
  if (isGoalToolName(call?.name)) {
    return renderGoalToolCallBody(call);
  }
  return '<pre class="tl-body">' + escapeHTML(prettyJSON(call.arguments)) + '</pre>';
}

function isFinalFinishResult(result) {
  return result?.name === 'finish' && result?.final && !result?.is_error;
}

function toolCallPreviewIsRaw(call, compactFinal) {
  if (compactFinal || isGoalToolName(call?.name)) {
    return false;
  }
  const parsed = parseMaybeJSON(call?.arguments);
  return !(parsed && typeof parsed === 'object' && !Array.isArray(parsed) && Array.isArray(parsed.todos));
}

function isGoalToolName(name) {
  return ['get_goal', 'create_goal', 'record_goal_progress', 'update_goal'].includes(String(name || ''));
}

function toolCallIDs(call) {
  const ids = [];
  if (call?.id) {
    ids.push(call.id);
  }
  if (call?.provider_call_id && call.provider_call_id !== call.id) {
    ids.push(call.provider_call_id);
  }
  return ids;
}

function summarizeToolCall(call, options = {}) {
  if (options.finalTextRendered && call?.name === 'finish' && maybeArray(options.pairedResults).some(isFinalFinishResult)) {
    return 'Final response captured';
  }
  if (isGoalToolName(call?.name)) {
    return summarizeGoalToolCall(call);
  }
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

function summarizeGoalToolCall(call) {
  const parsed = parseMaybeJSON(call?.arguments);
  const name = String(call?.name || '');
  if (name === 'get_goal') {
    return 'Read current durable Goal state';
  }
  if (name === 'create_goal') {
    const mode = parsed?.mode || 'goal';
    const chars = parsed?.objective ? ` (${String(parsed.objective).length} chars)` : '';
    return `Create ${mode} objective${chars}`;
  }
  if (name === 'record_goal_progress') {
    const kind = parsed?.kind || 'progress';
    return `${kind}: ${truncateText(parsed?.summary || '', 110) || 'record progress facts'}`;
  }
  if (name === 'update_goal') {
    const status = parsed?.status || 'complete';
    return `Mark Goal ${humanizeStatus(status)}`;
  }
  return 'Goal tool';
}

function summarizeToolResult(result, parsed, payloadText) {
  if (result.final) {
    return truncateText(payloadText, 120);
  }
  if (result.is_error) {
    return truncateText(payloadText, 140);
  }
  if (isGoalToolName(result?.name)) {
    return summarizeGoalToolResult(result, parsed, payloadText);
  }
  if (result.name === 'todo_write' && Array.isArray(parsed)) {
    if (result.metadata?.noop) {
      return `${parsed.length} todo item${parsed.length === 1 ? '' : 's'} unchanged`;
    }
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

function summarizeGoalToolResult(result, parsed, payloadText) {
  if (parsed === null) {
    return 'No durable Goal attached';
  }
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    const pieces = [
      parsed.mode || 'goal',
      parsed.status ? humanizeStatus(parsed.status) : '',
      parsed.goal_id ? shortId(parsed.goal_id) : '',
      Number.isFinite(Number(parsed.tokens_used)) ? `tokens ${Number(parsed.tokens_used)}` : '',
      Number.isFinite(Number(parsed.provider_time_used_seconds ?? parsed.time_used_seconds)) ? `provider time ${Number(parsed.provider_time_used_seconds ?? parsed.time_used_seconds)}s` : ''
    ].filter(Boolean);
    if (result.name === 'record_goal_progress') {
      const latest = maybeArray(parsed.progress).slice(-1)[0];
      if (latest?.kind || latest?.summary) {
        pieces.push(truncateText([latest.kind, latest.summary].filter(Boolean).join(': '), 80));
      }
    }
    return pieces.join(' · ') || 'Goal state updated';
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
      ${renderToolCallBody(call)}
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
  if (isGoalToolName(result?.name)) {
    return renderGoalToolSpecialResult(result, parsed);
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return '';
  }

  if (result.name === 'agent_spawn' || result.name === 'agent_status') {
    const statusTone = toneForStatus(parsed.status || parsed.session_status);
    const label = agentLabel(parsed.agent_name, parsed.agent_role) || shortId(parsed.session_id) || 'child agent';
    return `
      <div class="tool-special-card">
        <div class="sa-tree-row parent sa-tree-row-static">
          <span class="sa-tree-dot ${statusTone}"></span>
		  <span class="sa-tree-label" translate="no" data-i18n-skip>${escapeHTML(label)}</span>
          <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(parsed.status || parsed.session_status || 'unknown'))}</span>
		  <span class="sa-tree-meta" translate="no" data-i18n-skip>${escapeHTML(shortId(parsed.session_id || ''))}</span>
          ${parsed.session_id ? `<button class="mini-link-btn sa-tree-open" type="button" data-sub-agent-open="${escapeAttr(parsed.session_id)}">Open</button>` : ''}
        </div>
        ${parsed.last_error ? `<div class="tl-preview tool-special-error">${escapeHTML(truncateText(parsed.last_error, 120))}</div>` : ''}
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
        <div class="sa-tree-row parent sa-tree-row-static">
          <span class="sa-tree-dot ${statusTone}"></span>
          <span class="sa-tree-label">${escapeHTML(label)}</span>
          <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(sess.status))}</span>
          <span class="sa-tree-meta">${escapeHTML(sess.model || sess.provider || 'n/a')} · ${escapeHTML(shortId(sess.id))}</span>
          <button class="mini-link-btn sa-tree-open" type="button" data-sub-agent-open="${escapeAttr(sess.id)}">Open</button>
        </div>
      `);
    });
    orphanJobs.forEach((job) => {
      const statusTone = toneForStatus(job.status);
      const label = agentLabel(job.agent_name, job.agent_role) || shortId(job.id);
      const action = job.session_id
        ? `<button class="mini-link-btn sa-tree-open" type="button" data-sub-agent-open="${escapeAttr(job.session_id)}">Open</button>`
        : '';
      rows.push(`
        <div class="sa-tree-row orphan sa-tree-row-static">
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
        <div class="section-title-row tool-special-title-row">
          <h4>Background agents</h4>
          <span class="tf-sum-chip sa-sum-sessions tool-special-count">${sessions.length} session${sessions.length !== 1 ? 's' : ''}</span>
          ${orphanJobs.length ? `<span class="tf-sum-chip sa-sum-jobs">${orphanJobs.length} job${orphanJobs.length !== 1 ? 's' : ''}</span>` : ''}
        </div>
        ${rows.join('')}
      </div>
    `;
  }

  return '';
}

function renderGoalToolSpecialResult(result, parsed) {
  if (parsed === null) {
    return `
      <div class="tool-special-card goal-tool-card">
        <div class="goal-tool-head">
          <span class="status-badge neutral">Goal off</span>
          <span class="goal-tool-title">No durable Goal is attached to this session.</span>
        </div>
      </div>
    `;
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return '';
  }
  const status = parsed.status || result?.metadata?.status || 'active';
  const progress = maybeArray(parsed.progress);
  const latestProgress = progress.length ? progress[progress.length - 1] : null;
  const metricParts = [
    `tokens ${formatBudget(parsed.tokens_used, parsed.token_budget)}`,
    `provider time ${formatSecondsBudget(parsed.provider_time_used_seconds ?? parsed.time_used_seconds, parsed.provider_time_budget_seconds ?? parsed.time_budget_seconds)}`,
    parsed.updated_at ? `updated ${formatTimestamp(parsed.updated_at)}` : ''
  ].filter(Boolean);
  const counts = [
    maybeArray(parsed.success_criteria).length ? `${maybeArray(parsed.success_criteria).length} criteria` : '',
    maybeArray(parsed.validation_plan).length ? `${maybeArray(parsed.validation_plan).length} validation` : '',
    progress.length ? `${progress.length} progress` : ''
  ].filter(Boolean);
  return `
    <div class="tool-special-card goal-tool-card">
      <div class="goal-tool-head">
        <span class="status-badge ${goalStatusTone(status)}">${escapeHTML(goalStatusLabel(parsed))}</span>
        <span class="tiny-code-chip">${escapeHTML(shortId(parsed.goal_id || result?.metadata?.goal_id || 'goal'))}</span>
      </div>
      ${parsed.objective ? `<div class="goal-tool-objective">${escapeHTML(goalObjectiveReference(parsed.objective))}</div>` : ''}
      <div class="meta-chip-row">
        ${metricParts.map((part) => `<span class="surface-chip">${escapeHTML(part)}</span>`).join('')}
        ${counts.map((part) => `<span class="surface-chip">${escapeHTML(part)}</span>`).join('')}
      </div>
      ${latestProgress ? `
        <div class="goal-tool-progress">
          <span class="goal-section-title">Latest progress</span>
          <div class="goal-meta-line">${escapeHTML([latestProgress.kind, latestProgress.summary].filter(Boolean).join(': ') || latestProgress.id || 'progress')}</div>
        </div>
      ` : ''}
    </div>
  `;
}

function renderGoalToolCallBody(call) {
  const parsed = parseMaybeJSON(call?.arguments);
  const name = String(call?.name || 'goal tool');
  const action = summarizeGoalToolCall(call);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return `
      <div class="tool-special-card goal-tool-card">
        <div class="goal-tool-head">
          <span class="status-badge neutral goal-raw" translate="no">${escapeHTML(name)}</span>
          <span class="goal-tool-title">${escapeHTML(action)}</span>
        </div>
      </div>
    `;
  }
  const chips = [
    parsed.mode ? `mode ${parsed.mode}` : '',
    parsed.status ? `status ${parsed.status}` : '',
    parsed.kind ? `kind ${parsed.kind}` : '',
    parsed.token_budget ? `token budget ${parsed.token_budget}` : '',
    parsed.provider_time_budget_minutes ? `provider time budget ${parsed.provider_time_budget_minutes}m` : parsed.time_budget_minutes ? `provider time budget ${parsed.time_budget_minutes}m` : '',
    maybeArray(parsed.success_criteria).length ? `${maybeArray(parsed.success_criteria).length} criteria` : '',
    maybeArray(parsed.validation_plan).length ? `${maybeArray(parsed.validation_plan).length} validation` : '',
    maybeArray(parsed.evidence).length ? `${maybeArray(parsed.evidence).length} evidence` : '',
    maybeArray(parsed.blockers).length ? `${maybeArray(parsed.blockers).length} blockers` : ''
  ].filter(Boolean);
  return `
    <div class="tool-special-card goal-tool-card">
      <div class="goal-tool-head">
        <span class="status-badge neutral goal-raw" translate="no">${escapeHTML(name)}</span>
        <span class="goal-tool-title">${escapeHTML(action)}</span>
      </div>
      ${parsed.objective ? `<div class="goal-tool-objective">${escapeHTML(goalObjectiveReference(parsed.objective))}</div>` : ''}
      ${parsed.summary ? `<div class="goal-meta-line">${escapeHTML(truncateText(parsed.summary, 220))}</div>` : ''}
      ${chips.length ? `<div class="meta-chip-row">${chips.map((chip) => `<span class="surface-chip">${escapeHTML(chip)}</span>`).join('')}</div>` : ''}
    </div>
  `;
}

function goalObjectiveReference(objective) {
  const chars = String(objective || '').length;
  return chars ? `Objective stored in Goal panel (${chars} chars).` : '';
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
    <div class="meta-chip-row padded" translate="no" data-i18n-skip>
      ${entries.map(([key, value]) => `<span class="surface-chip">${escapeHTML(key)}: ${escapeHTML(metadataValue(value))}</span>`).join('')}
    </div>
  `;
}

function renderPendingStageCard() {
  const liveActivity = currentLiveActivity();
  return `
    <section class="message assistant pending">
      <div class="pending-stage-card">
        <div class="pending-stage-indicator" aria-hidden="true"></div>
        <div class="pending-stage-body">
          <div class="pending-stage-topline">
            <span class="status-badge ${liveActivity.tone || 'neutral'}">${escapeHTML(isGenerating() ? 'Running' : 'Settling')}</span>
            <span class="pending-stage-title">${escapeHTML(liveActivity.title)}</span>
            ${isNextSendInterruptArmed() ? '<span class="status-badge queued">Interrupt armed</span>' : ''}
          </div>
          <div class="pending-stage-copy">${escapeHTML(liveActivity.copy)}</div>
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
  const parentFacts = parentCoordinationFacts(detail);
  const checkpointFacts = checkpointRecoveryFacts(detail);
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
        ${renderKVRow('Tool profile', detail.metadata.tool_profile || 'default')}
        ${renderKVRow('Provider', detail.metadata.provider || 'n/a')}
        ${renderKVRow('Model', detail.metadata.model || 'n/a')}
        ${renderKVRow('Reasoning effort', detail.metadata.provider_options?.reasoning_effort || 'provider default')}
        ${renderKVRow('Max output tokens', detail.metadata.provider_options?.max_output_tokens > 0 ? String(detail.metadata.provider_options.max_output_tokens) : 'provider default')}
        ${renderKVRow('Mode', detail.metadata.mode || 'run')}
        ${renderKVRow('Workdir', detail.metadata.workdir || 'n/a')}
        ${detail.metadata.requested_workdir ? renderKVRow('Requested workdir', detail.metadata.requested_workdir) : ''}
        ${detail.metadata.isolation?.mode ? renderKVRow('Isolation', `${detail.metadata.isolation.mode}${detail.metadata.isolation.requested_mode ? ` (requested ${detail.metadata.isolation.requested_mode})` : ''}`) : ''}
        ${detail.metadata.effective_budget ? renderKVRow('Child budget', effectiveBudgetSummary(detail.metadata.effective_budget, detail.state.pause_reason)) : ''}
        ${loadedSkills.length ? renderKVRow('Loaded skills', loadedSkills.join(', ')) : ''}
        ${detail.active_handle ? renderKVRow('Webconsole handle', 'active') : ''}
        ${failureSummary ? renderKVRow('Failure class', failureSummary.label) : ''}
        ${failureSummary ? renderKVRow('Operator hint', failureSummary.hint) : ''}
        ${parentFacts ? renderKVRow('Parent wait', parentFacts.summary) : ''}
        ${checkpointFacts ? renderKVRow('Checkpoint', checkpointFacts.summary) : ''}
        ${detail.state.last_error ? renderKVRow('Last error', detail.state.last_error) : ''}
      </div>
    </section>

    ${renderRecoveryFactsSection(parentFacts, checkpointFacts)}

    ${renderProviderAttemptsSection(detail.provider_attempts)}

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

	${isModernWebConsole() ? '' : `
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
	`}
  `;
}

function parentCoordinationFacts(detail) {
  const coordination = detail?.parent_coordination;
  if (!coordination || !coordination.parent_session_id) {
    return null;
  }
  const unresolvedChildren = maybeArray(coordination.unresolved_child_sessions);
  const unresolvedJobs = maybeArray(coordination.unresolved_queue_jobs);
  const completedChildren = maybeArray(coordination.completed_child_sessions);
  const completedJobs = maybeArray(coordination.completed_queue_jobs);
  const failedChildren = maybeArray(coordination.failed_child_sessions);
  const failedJobs = maybeArray(coordination.failed_queue_jobs);
  const cancelledChildren = maybeArray(coordination.cancelled_child_sessions);
  const cancelledJobs = maybeArray(coordination.cancelled_queue_jobs);
  const waitMode = coordination.wait_mode || 'wait-all';
  const waitState = coordination.parked
    ? 'parked'
    : unresolvedChildren.length || unresolvedJobs.length
      ? 'waiting'
      : 'ready';
  const parts = [
    waitMode,
    waitState,
    unresolvedChildren.length || unresolvedJobs.length ? `unresolved ${unresolvedChildren.length}/${unresolvedJobs.length}` : '',
    completedChildren.length || completedJobs.length ? `completed ${completedChildren.length}/${completedJobs.length}` : '',
    cancelledChildren.length || cancelledJobs.length ? `cancelled ${cancelledChildren.length}/${cancelledJobs.length}` : '',
    failedChildren.length || failedJobs.length ? `failed ${failedChildren.length}/${failedJobs.length}` : ''
  ].filter(Boolean);
  return {
    waitMode,
    waitState,
    unresolvedChildren,
    unresolvedJobs,
    completedChildren,
    completedJobs,
    cancelledChildren,
    cancelledJobs,
    failedChildren,
    failedJobs,
    updatedAt: coordination.updated_at || '',
    summary: parts.join(' · ')
  };
}

function checkpointRecoveryFacts(detail) {
  const checkpoint = detail?.longrun_checkpoint;
  if (!checkpoint || !checkpoint.session_id) {
    return null;
  }
  const unresolvedChildren = maybeArray(checkpoint.unresolved_child_sessions);
  const unresolvedJobs = maybeArray(checkpoint.unresolved_queue_jobs);
  const hints = maybeArray(checkpoint.resume_hints);
  const waitState = checkpoint.parent_wait_state || '';
  const parts = [
    waitState ? `wait ${waitState}` : '',
    unresolvedChildren.length || unresolvedJobs.length ? `unresolved ${unresolvedChildren.length}/${unresolvedJobs.length}` : '',
    hints.length ? `${hints.length} resume hint${hints.length === 1 ? '' : 's'}` : '',
    checkpoint.created_at ? `created ${formatTimestamp(checkpoint.created_at)}` : ''
  ].filter(Boolean);
  return {
    waitState,
    unresolvedChildren,
    unresolvedJobs,
    hints,
    createdAt: checkpoint.created_at || '',
    summary: parts.join(' · ') || 'recorded'
  };
}

function renderRecoveryFactsSection(parentFacts, checkpointFacts) {
  if (!parentFacts && !checkpointFacts) {
    return '';
  }
  const cards = [];
  if (parentFacts) {
    const chips = [
      `mode ${parentFacts.waitMode}`,
      `state ${parentFacts.waitState}`,
      parentFacts.updatedAt ? `updated ${formatTimestamp(parentFacts.updatedAt)}` : ''
    ].filter(Boolean);
    cards.push(`
      <div class="notification-card recovery-fact-card">
        <div class="job-card-top">
          <div class="job-card-title">Parent coordination</div>
          <span class="status-badge ${toneForStatus(parentFacts.waitState)}">${escapeHTML(humanizeStatus(parentFacts.waitState))}</span>
        </div>
        <div class="notification-copy" translate="no" data-i18n-skip>${escapeHTML(parentFacts.summary)}</div>
        <div class="path-pill-row">
          ${chips.map((chip) => `<span class="surface-chip">${escapeHTML(chip)}</span>`).join('')}
        </div>
        ${renderFactIdList('Unresolved children', parentFacts.unresolvedChildren)}
        ${renderFactIdList('Unresolved jobs', parentFacts.unresolvedJobs)}
      </div>
    `);
  }
  if (checkpointFacts) {
    cards.push(`
      <div class="notification-card recovery-fact-card">
        <div class="job-card-top">
          <div class="job-card-title">Long-run checkpoint</div>
          ${checkpointFacts.waitState ? `<span class="status-badge ${toneForStatus(checkpointFacts.waitState)}">${escapeHTML(humanizeStatus(checkpointFacts.waitState))}</span>` : ''}
        </div>
        <div class="notification-copy" translate="no" data-i18n-skip>${escapeHTML(checkpointFacts.summary)}</div>
        ${renderFactIdList('Checkpoint children', checkpointFacts.unresolvedChildren)}
        ${renderFactIdList('Checkpoint jobs', checkpointFacts.unresolvedJobs)}
        ${checkpointFacts.hints.length ? `<div class="goal-meta-line">${escapeHTML(checkpointFacts.hints.join(' · '))}</div>` : ''}
      </div>
    `);
  }
  return `
    <section class="panel-section">
      <div class="section-title-row">
        <h4>Recovery facts</h4>
      </div>
      <div class="card-stack">
        ${cards.join('')}
      </div>
    </section>
  `;
}

function renderFactIdList(label, ids) {
  const values = maybeArray(ids);
  if (!values.length) {
    return '';
  }
  return `<div class="goal-meta-line">${escapeHTML(label)}: ${escapeHTML(values.join(', '))}</div>`;
}

function renderProviderAttemptsSection(items) {
  const attempts = maybeArray(items);
  if (!attempts.length) {
    return '';
  }
  const summary = summarizeProviderAttemptCounters(attempts);
  const recoveryCount = summary.retry + summary.autoResume;
  const recent = attempts.slice(-4).reverse();
  return `
    <section class="panel-section">
      <div class="section-title-row">
        <h4>Provider attempts</h4>
      </div>
      <div class="summary-grid wide">
        ${renderMetricCard('Attempts', String(attempts.length), `${summary.success} success · ${summary.failure} failed`)}
        ${renderMetricCard('Recovery', String(recoveryCount), `${summary.retry} retry · ${summary.autoResume} auto-resume`)}
        ${renderMetricCard('Cache read', String(summary.cacheRead), `${summary.cacheCreation} creation`)}
      </div>
      <div class="card-stack">
        ${recent.map((attempt) => renderProviderAttemptCard(attempt)).join('')}
      </div>
    </section>
  `;
}

function summarizeProviderAttemptCounters(items) {
  return maybeArray(items).reduce((summary, attempt) => {
    const outcome = String(attempt?.outcome || '').toLowerCase();
    if (outcome === 'success') summary.success += 1;
    if (outcome === 'failure') summary.failure += 1;
    if (outcome === 'retry') summary.retry += 1;
    if (outcome === 'auto_resume') summary.autoResume += 1;
    summary.cacheRead += Number(attempt?.cache_read_input_tokens || 0);
    summary.cacheCreation += Number(attempt?.cache_creation_input_tokens || 0);
    return summary;
  }, {
    success: 0,
    failure: 0,
    retry: 0,
    autoResume: 0,
    cacheRead: 0,
    cacheCreation: 0
  });
}

function renderProviderAttemptCard(attempt) {
  attempt = attempt || {};
  const outcome = String(attempt.outcome || 'unknown');
  const responseID = String(attempt.provider_response_id || '').trim();
  const copy = attempt.error
    ? truncateText(attempt.error, 180)
    : responseID
      ? `response ${shortId(responseID)}`
      : `${attempt.provider || 'provider'} / ${attempt.model || 'model'}`;
  const metaParts = [
    attempt.provider || '',
    attempt.model || '',
    attempt.turn ? `turn ${attempt.turn}` : '',
    attempt.attempt ? `attempt ${attempt.attempt}` : '',
    attempt.error_class ? `class ${attempt.error_class}` : '',
    attempt.timeout_kind || '',
    attempt.status_code ? `HTTP ${attempt.status_code}` : '',
    attempt.cache_read_input_tokens ? `cache read ${attempt.cache_read_input_tokens}` : '',
    attempt.cache_creation_input_tokens ? `cache create ${attempt.cache_creation_input_tokens}` : '',
    attempt.created_at ? formatTimestamp(attempt.created_at) : ''
  ].filter(Boolean);
  return `
    <div class="notification-card provider-attempt-card">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(providerAttemptTitle(attempt))}</div>
        <span class="status-badge ${providerAttemptTone(outcome)}">${escapeHTML(humanizeStatus(outcome))}</span>
      </div>
			<div class="notification-copy" translate="no" data-i18n-skip>${escapeHTML(copy)}</div>
      <div class="job-card-meta">${escapeHTML(metaParts.join(' · '))}</div>
    </div>
  `;
}

function providerAttemptTitle(attempt) {
  const parts = [];
  if (attempt?.turn) parts.push(`turn ${attempt.turn}`);
  if (attempt?.attempt) parts.push(`attempt ${attempt.attempt}`);
  return parts.join(' · ') || 'provider attempt';
}

function providerAttemptTone(outcome) {
  switch (String(outcome || '').toLowerCase()) {
    case 'success':
      return 'live';
    case 'failure':
      return 'danger';
    case 'retry':
    case 'auto_resume':
      return 'queued';
    default:
      return 'neutral';
  }
}

function renderTimelinePanel(detail) {
  const items = maybeArray(detail.timeline).slice().reverse();
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
  const parentFacts = parentCoordinationFacts(detail);
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
      ${parentFacts ? renderParentCoordinationBanner(parentFacts) : ''}
      ${renderSelectedQueueJobPanel()}
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

function renderParentCoordinationBanner(parentFacts) {
  return `
    <div class="selected-queue-job-panel parent-coordination-panel">
      <div class="job-card-top">
        <div>
          <div class="job-card-title">Parent coordination</div>
          <div class="job-card-meta">${escapeHTML(parentFacts.summary)}</div>
        </div>
        <span class="status-badge ${toneForStatus(parentFacts.waitState)}">${escapeHTML(humanizeStatus(parentFacts.waitState))}</span>
      </div>
      ${renderFactIdList('Unresolved children', parentFacts.unresolvedChildren)}
      ${renderFactIdList('Unresolved jobs', parentFacts.unresolvedJobs)}
    </div>
  `;
}

function renderSelectedQueueJobPanel() {
  const jobID = String(selectedQueueJobId() || '');
  if (!jobID) {
    return '';
  }
  const job = selectedQueueJobDetail() || queueJobByID(jobID) || { id: jobID };
  const status = queueJobDisplayStatus(job);
  const detailCopy = job.last_error || job.final_text || job.prompt || 'Job detail is loading.';
  const detailCopyIsRaw = Boolean(job.last_error || job.final_text || job.prompt);
  const created = job.created_at ? formatTimestamp(job.created_at) : '';
  const updated = job.updated_at ? formatTimestamp(job.updated_at) : '';
  const isUnavailable = status === 'unavailable';
  return `
    <div class="selected-queue-job-panel" data-selected-queue-job="${escapeAttr(jobID)}">
      <div class="job-card-top">
        <div>
          <div class="job-card-title">${escapeHTML(agentLabel(job.agent_name, job.agent_role) || shortId(jobID))}</div>
          <div class="job-card-meta">job ${escapeHTML(shortId(jobID))}${job.mode ? ` · ${escapeHTML(job.mode)}` : ''}${created ? ` · ${escapeHTML(created)}` : ''}</div>
        </div>
        <span class="status-badge ${toneForStatus(status)}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      <div class="${job.last_error ? 'notification-copy danger' : 'job-card-copy'}"${detailCopyIsRaw ? ' translate="no" data-i18n-skip' : ''}>${escapeHTML(truncateText(detailCopy, 260))}</div>
      <div class="path-pill-row">
        ${job.session_id ? `<span class="surface-chip">child ${escapeHTML(shortId(job.session_id))}</span>` : ''}
        ${job.parent_session_id ? `<span class="surface-chip">parent ${escapeHTML(shortId(job.parent_session_id))}</span>` : ''}
        ${updated ? `<span class="surface-chip">updated ${escapeHTML(updated)}</span>` : ''}
      </div>
      <div class="card-actions">
        ${job.session_id ? renderSessionStopButton(job.session_id, status) : ''}
        ${job.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(job.session_id)}">Open child session</button>` : ''}
        ${job.parent_session_id ? `<button class="mini-link-btn" type="button" data-open-parent-session="${escapeAttr(job.parent_session_id)}">Open parent session</button>` : ''}
        ${isUnavailable ? '<span class="job-card-meta">Job facts are no longer available in the local queue store.</span>' : ''}
      </div>
    </div>
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
	const counters = taskBoard.counters || {};
	const groups = taskBoard.groups || {};
	const todoInProgress = todos.filter((item) => String(item?.status || '').toLowerCase() === 'in_progress').length;
	const groupOrder = [
		['in_progress', 'In progress'],
		['ready', 'Ready'],
		['blocked', 'Blocked'],
		['completed', 'Completed'],
		['cancelled', 'Cancelled']
	];
	const groupedTasks = groupOrder.map(([key, label]) => [key, label, maybeArray(groups[key])]);
	const taskCount = groupedTasks.reduce((count, [, , items]) => count + items.length, 0);
	return `
    <section class="panel-section">
      <div class="summary-grid wide">
		${renderMetricCard('Todo items', String(todos.length), `${todoInProgress} in progress`)}
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
		${taskCount ? `<div class="task-group-stack">${groupedTasks.filter(([, , items]) => items.length).map(([key, label, items]) => `
			<section class="task-derived-group" data-task-group="${escapeAttr(key)}">
				<div class="task-group-heading"><span>${escapeHTML(label)}</span><span class="task-chip">${items.length}</span></div>
				<div class="card-stack">${items.map((task) => renderTaskItem(task, key)).join('')}</div>
			</section>
		`).join('')}</div>` : '<div class="empty-panel">No persistent tasks.</div>'}
    </section>
  `;
}

function collectFileChanges() {
  const detail = state.sessionDetail;
  if (!detail) return [];
  const durableChanges = maybeArray(detail.file_changes);
  if (durableChanges.length) {
    return durableChanges
      .filter((item) => item && item.path)
      .map((item) => ({
        path: String(item.path),
        writes: Number(item.writes || 0),
        edits: Number(item.edits || 0),
        linesAdded: Number(item.lines_added || item.linesAdded || 0),
        linesRemoved: Number(item.lines_removed || item.linesRemoved || 0)
      }));
  }
  const messages = maybeArray(detail.messages);
  const fileMap = {};
  // Mirror the backend: only count tool calls whose result succeeded. A failed
  // write_file/edit_file (e.g. "old_text not found") or errored shell must not
  // appear. This fallback only runs when no durable record is available.
  const failedCallIds = new Set();
  messages.forEach((msg) => {
    maybeArray(msg.tool_results).forEach((res) => {
      if (res && res.is_error && res.tool_call_id) {
        failedCallIds.add(String(res.tool_call_id));
      }
    });
  });
  messages.forEach((msg) => {
    maybeArray(msg.tool_calls).forEach((call) => {
      if (call && call.id && failedCallIds.has(String(call.id))) return;
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

  const expanded = isFloatingPanelExpanded('todo');

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
		<div class="tf-header" data-todo-float-toggle role="button" tabindex="0" aria-expanded="${expanded ? 'true' : 'false'}">
        <div class="tf-header-left">
          <span class="tf-title">Todo / Tasks</span>
          <div class="tf-progress-bar" role="progressbar" aria-label="Task completion" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${progressPct}">
            <div class="tf-progress-fill" style="--progress:${progressPct}%"></div>
          </div>
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

  const expanded = isFloatingPanelExpanded('files');
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
		<div class="tf-header" data-files-float-toggle role="button" tabindex="0" aria-expanded="${expanded ? 'true' : 'false'}">
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

  const expanded = isFloatingPanelExpanded('subAgents');
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
		<div class="tf-header" data-sub-agent-toggle role="button" tabindex="0" aria-expanded="${expanded ? 'true' : 'false'}">
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
  const status = subAgentDisplayStatus(sess, job);
  const statusTone = toneForStatus(status);
  const label = agentLabel(sess.agent_name, sess.agent_role) || shortId(sess.id);
  const jobMeta = job ? ` · ${escapeHTML(job.mode || '')}` : '';
  return `
		<div class="sa-tree-row parent" data-sub-agent-open="${escapeAttr(sess.id)}" role="button" tabindex="0" aria-label="Open child session ${escapeAttr(label)}" title="Click to open child session">
      <span class="sa-tree-dot ${statusTone}"></span>
      <span class="sa-tree-label">${escapeHTML(label)}</span>
      <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(status))}</span>
      <span class="sa-tree-meta">${escapeHTML(sess.model || sess.provider || 'n/a')}${jobMeta} · ${escapeHTML(shortId(sess.id))}</span>
      ${renderSessionStopButton(sess.id, status, '')}
    </div>
  `;
}

function renderSubAgentJobRow(job) {
  const status = queueJobDisplayStatus(job);
  const statusTone = toneForStatus(status);
  const label = agentLabel(job.agent_name, job.agent_role) || shortId(job.id);
	const targetAttr = job.session_id
		? `data-sub-agent-open="${escapeAttr(job.session_id)}" role="button" tabindex="0" aria-label="Open child session ${escapeAttr(label)}" title="Click to open child session"`
    : 'data-static-row';
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
  const overviewError = currentOverviewError();
  const recent = maybeArray(state.overview?.recent_sessions);
  const pinned = state.sessionDetail?.metadata
    ? [sessionSummaryFromDetail(state.sessionDetail)]
    : [];
  const items = uniqueById(pinned.concat(recent)).slice(0, 12);
  const emptyHTML = overviewError
    ? `<div class="empty-panel compact">${escapeHTML(overviewError)}</div>`
    : '<div class="empty-panel compact">No durable sessions yet.</div>';
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
		  <span class="session-rail-meta" translate="no" data-i18n-skip>${escapeHTML(item.provider || 'provider')} · ${escapeHTML(item.model || 'model')}</span>
		  <span class="session-rail-meta" translate="no" data-i18n-skip>${escapeHTML(workdirBase(item.workdir))}${item.agent_role ? ` · ${escapeHTML(item.agent_role)}` : ''}${item.goal_status ? ` · goal:${escapeHTML(item.goal_status)}` : ''}</span>
        </button>
      `).join('') : emptyHTML}
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
        <button class="inspector-close-btn" type="button" data-close-inspector aria-label="Close inspector" title="Close inspector">×</button>
      </div>
      <div class="inspector-content">
        <div class="empty-panel compact">No todo/task state recorded.</div>
      </div>
    `;
  }
	const tabs = [
    ['summary', 'Summary'],
    ['goal', 'Goal'],
    ['plan', 'Plan'],
    ['tasks', 'Tasks'],
		...(isModernWebConsole() ? [] : [['agents', 'Background']]),
    ['context', 'Context'],
    ['timeline', 'Timeline']
  ];
  const selectedTab = activeInspectorTab();
  const active = tabs.some(([key]) => key === selectedTab) ? selectedTab : 'tasks';
  const panel = active === 'summary'
    ? renderSummaryPanel(detail)
    : active === 'goal'
      ? renderGoalPanel(detail)
    : active === 'plan'
      ? renderPlanPanel(detail)
    : active === 'agents'
      ? renderAgentsPanel(detail)
      : active === 'context'
        ? renderContextPanel(detail)
      : active === 'timeline'
        ? renderTimelinePanel(detail)
        : renderTasksPanel(detail);
  return `
    <div class="inspector-header">
      <div>
        <div class="inspector-eyebrow">Tracker</div>
        <h3 translate="no" data-i18n-skip>${escapeHTML(shortId(detail.metadata?.id || state.sessionId))}</h3>
      </div>
      <div class="inspector-header-actions">
        <span class="status-badge ${toneForStatus(detail.state?.status)}">${escapeHTML(humanizeStatus(detail.state?.status || 'idle'))}</span>
        <button class="inspector-close-btn" type="button" data-close-inspector aria-label="Close inspector" title="Close inspector">×</button>
      </div>
    </div>
    <div class="inspector-tabs" role="tablist">
		${tabs.map(([key, label]) => `<button class="inspector-tab ${key === active ? 'active' : ''}" type="button" role="tab" id="inspector-tab-${escapeAttr(key)}" aria-controls="inspector-panel-${escapeAttr(key)}" aria-selected="${key === active ? 'true' : 'false'}" tabindex="${key === active ? '0' : '-1'}" data-inspector-tab="${escapeAttr(key)}">${escapeHTML(label)}</button>`).join('')}
	</div>
	<div class="inspector-content" role="tabpanel" id="inspector-panel-${escapeAttr(active)}" aria-labelledby="inspector-tab-${escapeAttr(active)}" tabindex="0">${panel}</div>
  `;
}

function renderContextPanel(detail) {
  const sessionID = String(detail?.metadata?.id || '').trim();
  const loading = contextReportViewState.loading && contextReportViewState.sessionID === sessionID;
  const error = contextReportViewState.sessionID === sessionID ? contextReportViewState.error : '';
  const report = contextReportViewState.sessionID === sessionID ? contextReportViewState.report : null;
  if (loading && !report) {
    return '<div class="empty-panel">Loading context budget and lineage telemetry…</div>';
  }
  if (error && !report) {
    return `
      <div class="empty-panel" translate="no" data-i18n-skip>${escapeHTML(error)}</div>
      <button class="mini-link-btn" type="button" data-context-report-refresh>Retry</button>
    `;
  }
  if (!report) {
    return `
      <div class="empty-panel">Context telemetry is loaded only when this inspector tab is opened.</div>
      <button class="mini-link-btn" type="button" data-context-report-refresh>Load context</button>
    `;
  }
  const aggregate = report.aggregate || {};
  const usage = aggregate.total_provider_usage || {};
  const truncation = report.truncation;
  const sessions = maybeArray(report.sessions);
  return `
    <div class="goal-panel context-panel">
      <div class="goal-panel-head">
        <div>
          <div class="inspector-eyebrow"><span>Context report</span> v<span translate="no" data-i18n-skip>${escapeHTML(String(report.schema_version || 1))}</span></div>
          <h4 translate="no" data-i18n-skip>${escapeHTML(shortId(report.root_session_id || sessionID))}</h4>
        </div>
        <button class="mini-link-btn" type="button" data-context-report-refresh ${loading ? 'disabled' : ''}>Refresh</button>
      </div>
      <div class="goal-budget-row">
        ${renderMiniMetric('Root peak', contextMetric(aggregate.root_peak_estimated_input_tokens))}
        ${renderMiniMetric('Child peak', contextMetric(aggregate.child_peak_estimated_input_tokens))}
        ${renderMiniMetric('Root aggregate', contextMetric(aggregate.root_aggregate_estimated_input_tokens))}
        ${renderMiniMetric('Child aggregate', contextMetric(aggregate.child_aggregate_estimated_input_tokens))}
        ${renderMiniMetric('Total input', contextMetric(aggregate.total_estimated_input_tokens))}
        ${renderMiniMetric('Unknown usage', contextMetric(aggregate.unknown_usage_request_count))}
      </div>
      <div class="goal-section">
        <div class="goal-section-title">Provider usage</div>
        <div class="kv-list">
          ${renderKVRow('Input tokens', contextMetric(usage.input_tokens))}
          ${renderKVRow('Output tokens', contextMetric(usage.output_tokens))}
          ${renderKVRow('Cache read', contextMetric(usage.cache_read_input_tokens))}
          ${renderKVRow('Cache creation', contextMetric(usage.cache_creation_input_tokens))}
        </div>
      </div>
      <div class="goal-section">
        <div class="goal-section-title">Lineage</div>
        <div class="kv-list">
          ${renderKVRow('Sessions', contextMetric(aggregate.session_count))}
          ${renderKVRow('Child sessions', contextMetric(aggregate.child_session_count))}
          ${renderKVRow('Requests', contextMetric(aggregate.total_request_count))}
          ${renderKVRow('Turns', contextMetric(aggregate.total_turn_count))}
          ${renderKVRow('Tool calls', contextMetric(aggregate.total_tool_call_count))}
          ${renderKVRow('Compactions', contextMetric(aggregate.total_compaction_count))}
          ${renderKVRow('Wall time', `${contextMetric(aggregate.wall_time_ms)} ms`)}
        </div>
      </div>
      <div class="goal-section">
        <div class="goal-section-title">Session detail</div>
        <div class="goal-item-list">
          ${sessions.map((item) => `
            <div class="goal-item">
              <div class="goal-item-top">
                <span translate="no" data-i18n-skip>${escapeHTML(shortId(item.session_id || 'session'))}</span>
                <span class="status-badge ${item.session_id === report.root_session_id ? 'completed' : 'queued'}">${renderContextSessionRole(item, report.root_session_id)}</span>
              </div>
              <div class="goal-meta-line"><span>peak</span> <span translate="no" data-i18n-skip>${contextMetric(item.metrics?.peak_estimated_input_tokens)}</span> · <span>aggregate</span> <span translate="no" data-i18n-skip>${contextMetric(item.metrics?.aggregate_estimated_input_tokens)}</span> · <span>requests</span> <span translate="no" data-i18n-skip>${contextMetric(item.metrics?.request_count)}</span></div>
            </div>
          `).join('') || '<div class="empty-panel compact">No request snapshots recorded.</div>'}
        </div>
      </div>
      ${truncation?.truncated ? `<div class="goal-meta-line">Bounded view: ${contextMetric(truncation.omitted_session_count)} sessions and ${contextMetric(truncation.omitted_request_count)} requests omitted. Aggregate totals are complete.</div>` : ''}
    </div>
  `;
}

function renderContextSessionRole(item, rootSessionID) {
  if (item?.session_id === rootSessionID) {
    return 'root';
  }
  if (item?.agent_role) {
    return `<span translate="no" data-i18n-skip>${escapeHTML(item.agent_role)}</span>`;
  }
  return 'child';
}

function contextMetric(value) {
  const number = Number(value || 0);
  if (!Number.isFinite(number)) {
    return '0';
  }
	return Math.trunc(number).toLocaleString(currentDisplayLocale());
}

function renderPlanPanel(detail) {
  const planMode = detail?.plan_mode;
  if (!planMode) {
    return '<div class="empty-panel">No Plan Mode gate is attached to this session.</div>';
  }
  const status = planMode.status || 'planning';
  const canApprove = status === 'awaiting_approval';
  const canCancel = ['planning', 'awaiting_user_input', 'awaiting_approval'].includes(status);
  const canRevise = status === 'awaiting_approval';
  const pendingRequest = status === 'awaiting_user_input' ? planMode.pending_request || null : null;
  const planMarkdown = String(planMode.plan_markdown || '').trim();
  return `
    <div class="goal-panel plan-panel">
      <div class="goal-panel-head">
        <div>
          <div class="inspector-eyebrow">Plan Mode</div>
          <h4>${escapeHTML(humanizeStatus(status))}</h4>
        </div>
        <span class="tiny-code-chip">${escapeHTML(shortId(planMode.plan_mode_id || 'plan'))}</span>
      </div>
      <div class="goal-objective">${escapeHTML(planMode.objective || '')}</div>
      <div class="goal-budget-row">
        ${renderMiniMetric('Version', String(planMode.plan_version || 0))}
        ${renderMiniMetric('Approved', String(planMode.approved_version || 0))}
      </div>
      <div class="goal-actions">
        ${canApprove ? '<button class="mini-link-btn" type="button" data-plan-action="approve">Approve & Run</button>' : ''}
        ${canRevise ? '<button class="mini-link-btn" type="button" data-plan-action="revise">Ask for Changes</button>' : ''}
        ${canCancel ? '<button class="mini-link-btn danger" type="button" data-plan-action="cancel">Cancel</button>' : ''}
      </div>
      ${pendingRequest ? renderPlanInputRequest(pendingRequest) : ''}
      ${planMode.summary ? `
        <div class="goal-section">
          <div class="goal-section-title">Summary</div>
          <div class="goal-meta-line goal-raw" translate="no">${escapeHTML(planMode.summary)}</div>
        </div>
      ` : ''}
      ${renderPlanList('Assumptions', planMode.assumptions)}
      ${renderPlanList('Risks', planMode.risks)}
      ${renderPlanList('Verification', planMode.verification)}
      ${planMarkdown ? `
        <div class="goal-section">
          <div class="goal-section-title">Plan</div>
		<div class="message-bubble prose plan-markdown" translate="no">${renderMarkdownCached('plan-markdown', planMarkdown)}</div>
        </div>
      ` : ''}
    </div>
  `;
}

function renderPlanInputRequest(request) {
  const questions = maybeArray(request?.questions);
  if (!questions.length) {
    return '';
  }
  const selections = typeof getPlanInputSelections === 'function' ? getPlanInputSelections(request) : {};
  const answers = typeof collectPlanInputAnswers === 'function' ? collectPlanInputAnswers(request, selections) : [];
  const canSubmit = answers.length === questions.length;
  return `
    <div class="goal-section">
      <div class="goal-section-title">Input requested</div>
      <div class="goal-item-list">
        ${questions.map((question) => renderPlanInputQuestion(request.request_id, question, selections[question.id])).join('')}
      </div>
      <div class="goal-actions">
        <button class="mini-link-btn" type="button"
          data-plan-input-action="submit"
          data-request-id="${escapeAttr(request.request_id || '')}"
          ${canSubmit ? '' : 'disabled'}>
          Submit answers
        </button>
      </div>
    </div>
  `;
}

function renderPlanInputQuestion(requestID, question, selected) {
  const options = maybeArray(question?.options);
  const selectedValue = String(selected?.value || '');
  const selectedIsOther = Boolean(selected?.is_other);
  return `
    <div class="goal-item plan-question">
      <div class="goal-item-top">
		<span translate="no" data-i18n-skip>${escapeHTML(question.header || question.id || 'Question')}</span>
        <span class="status-badge queued">Input</span>
      </div>
	  <div class="goal-meta-line" translate="no" data-i18n-skip>${escapeHTML(question.question || '')}</div>
      <div class="goal-actions plan-option-row">
        ${options.map((option) => `
		  <button class="mini-link-btn${!selectedIsOther && selectedValue === String(option.label || '') ? ' is-selected' : ''}" type="button" translate="no" data-i18n-skip
            data-plan-input-action="select"
            data-request-id="${escapeAttr(requestID || '')}"
            data-question-id="${escapeAttr(question.id || '')}"
            data-label="${escapeAttr(option.label || '')}"
            data-value="${escapeAttr(option.label || '')}"
            aria-pressed="${!selectedIsOther && selectedValue === String(option.label || '') ? 'true' : 'false'}">
            ${escapeHTML(option.label || 'Option')}
          </button>
        `).join('')}
        <button class="mini-link-btn${selectedIsOther ? ' is-selected' : ''}" type="button"
          data-plan-input-action="select"
          data-request-id="${escapeAttr(requestID || '')}"
          data-question-id="${escapeAttr(question.id || '')}"
          data-label="Other"
          data-other="1"
          aria-pressed="${selectedIsOther ? 'true' : 'false'}">Other</button>
      </div>
	  ${options.length ? `<div class="goal-meta-line" translate="no" data-i18n-skip>${escapeHTML(options.map((option) => option.description).filter(Boolean).join(' · '))}</div>` : ''}
    </div>
  `;
}

function renderPlanList(label, items) {
  const values = maybeArray(items).filter((item) => String(item || '').trim());
  if (!values.length) {
    return '';
  }
  return `
    <div class="goal-section">
      <div class="goal-section-title">${escapeHTML(label)}</div>
      <div class="goal-item-list">
        ${values.map((item) => `<div class="goal-item"><div class="goal-meta-line goal-raw" translate="no">${escapeHTML(String(item))}</div></div>`).join('')}
      </div>
    </div>
  `;
}

function renderGoalPanel(detail) {
  const goal = detail?.goal;
  if (!goal) {
    const status = detail?.state?.status || 'idle';
    return `
      <div class="goal-panel">
        <div class="empty-panel">No durable Goal is attached to this session.</div>
        <div class="goal-section">
          <div class="goal-section-title">Runtime</div>
          <div class="goal-meta-line">Session ${escapeHTML(humanizeStatus(status))}${detail?.state?.phase ? ` · ${escapeHTML(phaseHeadline(detail.state.phase))}` : ''}</div>
        </div>
      </div>
    `;
  }
  const mission = goal.mission || null;
  const criteria = maybeArray(goal.success_criteria);
  const validations = maybeArray(goal.validation_plan);
  const features = maybeArray(mission?.features);
  const milestones = maybeArray(mission?.milestones);
  const roles = maybeArray(mission?.role_plan);
  const completionAudit = goal.completion_audit || null;
  const goalFacts = detail?.goal_facts || null;
  const canPause = goal.status === 'active' || goal.status === 'budget_limited';
  const canResume = goal.status === 'paused' || goal.status === 'budget_limited';
  const canComplete = goal.status !== 'complete';
  const planMode = detail?.plan_mode || null;
  const canApprove = mission && mission.plan_status !== 'approved' && (!planMode || planMode.status === 'awaiting_approval');
  const facts = goalRuntimeFacts(detail);
  const historicalCompletion = isHistoricalCompletedGoal(detail);

  return `
    <div class="goal-panel">
      <div class="goal-panel-head">
        <div>
          <div class="inspector-eyebrow">Goal</div>
          <h4>${escapeHTML(goalStatusLabel(goal))}</h4>
        </div>
        <div class="goal-panel-head-chips">
          <span class="status-badge ${goalStatusTone(goal.status)}">${escapeHTML(humanizeStatus(goal.status || 'active'))}</span>
          ${historicalCompletion ? '<span class="status-badge neutral">Historical</span>' : ''}
          <span class="tiny-code-chip">${escapeHTML(shortId(goal.goal_id || 'goal'))}</span>
        </div>
      </div>
      <div class="goal-objective">${escapeHTML(goal.objective || '')}</div>
      ${renderGoalRuntimeStatus(facts)}
      <div class="goal-budget-row">
        ${renderMiniMetric('Tokens', formatBudget(goal.tokens_used, goal.token_budget))}
        ${renderMiniMetric('Provider time', formatSecondsBudget(goal.provider_time_used_seconds ?? goal.time_used_seconds, goal.provider_time_budget_seconds ?? goal.time_budget_seconds))}
      </div>
      <div class="goal-actions">
        ${canPause ? '<button class="mini-link-btn" type="button" data-goal-action="pause">Pause</button>' : ''}
        ${canResume ? '<button class="mini-link-btn" type="button" data-goal-action="resume">Resume</button>' : ''}
        ${canComplete ? '<button class="mini-link-btn" type="button" data-goal-action="complete">Complete</button>' : ''}
        ${canApprove ? '<button class="mini-link-btn" type="button" data-goal-action="approve-plan">Approve plan</button>' : ''}
        <button class="mini-link-btn danger" type="button" data-goal-action="clear">Clear</button>
      </div>
      ${goalFacts ? renderGoalFacts(goalFacts) : ''}
      ${completionAudit ? renderGoalCompletionAudit(completionAudit) : ''}
      ${renderGoalItems('Success criteria', criteria, 'text')}
      ${renderGoalItems('Validation', validations, 'validation')}
      ${mission ? `
        <div class="goal-section">
          <div class="goal-section-title">Goal plan</div>
          <div class="goal-meta-line">Plan ${escapeHTML(mission.plan_status || 'draft')}${mission.approved_at ? ` · approved ${escapeHTML(formatTimestamp(mission.approved_at))}` : ''}</div>
          ${features.length ? renderGoalItems('Features', features, 'feature') : ''}
          ${milestones.length ? renderGoalItems('Milestones', milestones, 'milestone') : ''}
          ${roles.length ? renderGoalItems('Roles', roles, 'role') : ''}
        </div>
      ` : ''}
    </div>
  `;
}

function renderGoalRuntimeStatus(facts) {
  if (!facts?.goal) {
    return '';
  }
  const goal = facts.goal;
  const latestSummary = [
    facts.latestType ? `latest ${humanizeGoalEventType(facts.latestType)}` : '',
    facts.latestAt ? formatTimestamp(facts.latestAt) : ''
  ].filter(Boolean).join(' · ');
  const chips = [
    goal.source ? `source ${goal.source}` : '',
    facts.runStatus ? `session ${humanizeStatus(facts.runStatus)}` : '',
    facts.runPhase ? phaseHeadline(facts.runPhase) : '',
    goal.updated_at ? `goal updated ${formatTimestamp(goal.updated_at)}` : '',
    facts.progressCount ? `${facts.progressCount} progress records` : ''
  ].filter(Boolean);
  return `
    <div class="goal-runtime-card">
      <div class="goal-runtime-row">
        <span class="status-badge ${goalStatusTone(goal.status)}">${escapeHTML(goalStatusLabel(goal))}</span>
        <span class="goal-runtime-copy">${escapeHTML(latestSummary || 'Goal snapshot is loaded from durable session state.')}</span>
      </div>
      <div class="meta-chip-row">
        ${chips.map((chip) => `<span class="surface-chip">${escapeHTML(chip)}</span>`).join('')}
      </div>
    </div>
  `;
}

function renderGoalFacts(facts) {
  const coverage = facts?.coverage || {};
  const latest = facts?.latest_history || null;
  const progress = maybeArray(facts?.progress).slice(-3).reverse();
  const unresolvedChildren = maybeArray(facts?.unresolved_child_session_ids);
  const unresolvedJobs = maybeArray(facts?.unresolved_queue_job_ids);
  const lines = [];
  if (Number(coverage.validation_total || 0) > 0) {
    lines.push(`coverage ${Number(coverage.covered_assertions || 0)}/${Number(coverage.validation_total || 0)}`);
    if (coverage.approval_blocked) {
      lines.push('approval blocked');
    }
  }
  if (Number(facts?.evaluator_evidence_count || 0) > 0) {
    lines.push(`evaluator evidence ${Number(facts.evaluator_evidence_count)}`);
  }
  if (unresolvedChildren.length || unresolvedJobs.length) {
    lines.push(`unresolved child/queue ${unresolvedChildren.length}/${unresolvedJobs.length}`);
  }
  if (facts?.latest_blocker) {
    lines.push(`blocker ${facts.latest_blocker}`);
  }
  return `
    <div class="goal-section">
      <div class="goal-section-title">Goal facts</div>
      ${lines.length ? `<div class="goal-meta-line">${escapeHTML(lines.join(' · '))}</div>` : '<div class="goal-meta-line">No coverage, evaluator, child, queue, or blocker facts recorded.</div>'}
      ${maybeArray(coverage.uncovered_assertions).length ? `<div class="goal-meta-line">Uncovered ${escapeHTML(maybeArray(coverage.uncovered_assertions).join(', '))}</div>` : ''}
      ${coverage.approval_blocked ? '<div class="goal-meta-line">Approval override requires explicit confirmation for this local session.</div>' : ''}
      ${latest ? `<div class="goal-meta-line">Latest ${escapeHTML(humanizeGoalEventType(latest.type || 'goal event'))} · ${escapeHTML(formatTimestamp(latest.created_at))}</div>` : ''}
      ${progress.length ? `
        <div class="goal-section-title sub">Recent progress</div>
        <div class="goal-item-list">
          ${progress.map((item) => `
            <div class="goal-item">
              <div class="goal-item-top">
                <span class="goal-raw" translate="no">${escapeHTML(item.summary || item.kind || 'progress')}</span>
                <span class="status-badge neutral">${escapeHTML(item.kind || 'progress')}</span>
              </div>
              ${maybeArray(item.evidence).length ? `<div class="goal-meta-line goal-raw" translate="no">${escapeHTML(maybeArray(item.evidence).join(' · '))}</div>` : ''}
              ${maybeArray(item.blockers).length ? `<div class="goal-meta-line goal-raw" translate="no">${escapeHTML(`blockers ${maybeArray(item.blockers).join(' · ')}`)}</div>` : ''}
            </div>
          `).join('')}
        </div>
      ` : ''}
    </div>
  `;
}

function renderGoalCompletionAudit(audit) {
  const evidence = maybeArray(audit?.evidence);
  return `
    <div class="goal-section">
      <div class="goal-section-title">Completion audit</div>
      ${audit?.summary ? `<div class="goal-meta-line goal-raw" translate="no">${escapeHTML(audit.summary)}</div>` : ''}
      <div class="goal-meta-line">${escapeHTML([audit?.completed_by ? `by ${audit.completed_by}` : '', audit?.completed_at ? formatTimestamp(audit.completed_at) : ''].filter(Boolean).join(' · '))}</div>
      ${evidence.length ? `<div class="goal-item-list">${evidence.map((item) => `<div class="goal-item"><div class="goal-meta-line goal-raw" translate="no">${escapeHTML(item)}</div></div>`).join('')}</div>` : ''}
    </div>
  `;
}

function renderGoalItems(label, items, kind) {
  const values = maybeArray(items);
  if (!values.length) {
    return '';
  }
  return `
    <div class="goal-section">
      <div class="goal-section-title">${escapeHTML(label)}</div>
      <div class="goal-item-list">
        ${values.map((item) => renderGoalItem(item, kind)).join('')}
      </div>
    </div>
  `;
}

function renderGoalItem(item, kind) {
  const status = item?.status || 'pending';
  const title = kind === 'validation'
    ? item?.command || item?.artifact || item?.description || item?.kind || 'validation'
    : kind === 'role'
      ? item?.name || item?.role || 'role'
      : item?.title || item?.text || item?.id || 'item';
  const meta = kind === 'validation'
    ? item?.kind || ''
    : kind === 'role'
      ? [item?.role, item?.scope].filter(Boolean).join(' · ')
      : item?.milestone_id || '';
  return `
    <div class="goal-item">
      <div class="goal-item-top">
        <span>${escapeHTML(title)}</span>
        <span class="status-badge ${toneForStatus(status)}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      ${meta ? `<div class="goal-meta-line">${escapeHTML(meta)}</div>` : ''}
      ${renderGoalLinkedFacts(item)}
      ${maybeArray(item?.evidence).length ? `<div class="goal-meta-line">${escapeHTML(maybeArray(item.evidence).join(' · '))}</div>` : ''}
    </div>
  `;
}

function renderGoalLinkedFacts(item) {
  const parts = [];
  const tasks = maybeArray(item?.task_ids);
  const children = maybeArray(item?.child_session_ids);
  const queueJobs = maybeArray(item?.queue_job_ids);
  const evaluatorEvidence = maybeArray(item?.evaluator_evidence);
  if (tasks.length) {
    parts.push(`tasks ${tasks.map(shortId).join(', ')}`);
  }
  if (children.length) {
    parts.push(`children ${children.map(shortId).join(', ')}`);
  }
  if (queueJobs.length) {
    parts.push(`queue ${queueJobs.map(shortId).join(', ')}`);
  }
  if (evaluatorEvidence.length) {
    const refs = evaluatorEvidence
      .map((evidence) => evidence.child_session_id || evidence.queue_job_id || evidence.artifact || evidence.summary)
      .filter(Boolean)
      .map(shortId);
    parts.push(`validated by evaluator ${refs.length ? refs.join(', ') : evaluatorEvidence.length}`);
  }
  if (!parts.length) {
    return '';
  }
  return `<div class="goal-meta-line">${escapeHTML(parts.join(' · '))}</div>`;
}

function formatBudget(used, budget) {
  const usedText = String(Number(used || 0));
  if (budget === undefined || budget === null) {
    return usedText;
  }
  return `${usedText} / ${budget}`;
}

function formatSecondsBudget(used, budget) {
  const usedText = `${Number(used || 0)}s`;
  if (budget === undefined || budget === null) {
    return usedText;
  }
  return `${usedText} / ${budget}s`;
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
    agent_role: detail.metadata?.agent_role || '',
    goal_status: detail.goal?.status || '',
    goal_mode: detail.goal?.mode || '',
    goal_objective: detail.goal?.objective || ''
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
      <div class="task-card-copy" translate="no" data-i18n-skip>${escapeHTML(entry.kind === 'result' ? truncateText(entry.display_output || '(no output)', 160) : truncateText(prettyJSON(entry.arguments), 160))}</div>
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
      <div class="notification-copy" translate="no" data-i18n-skip>${escapeHTML(truncateText(item.text, 180))}</div>
      <div class="job-card-meta">${escapeHTML(formatTimestamp(item.created_at))}${item.interrupt ? ' · interrupt' : ''}</div>
    </div>
  `).join('');
}

function renderBackgroundNotificationsPreview(items) {
  const notifications = maybeArray(items).slice(-3).reverse();
  if (!notifications.length) {
    return '<div class="empty-panel">No background notifications yet.</div>';
  }
  return notifications.map((item) => {
    const pendingHint = backgroundNotificationPendingHint(item);
    const label = agentLabel(item.agent_name, item.agent_role);
    return `
      <div class="notification-card">
        <div class="job-card-top">
          <div class="job-card-title">${label ? `<span translate="no" data-i18n-skip>${escapeHTML(label)}</span>` : 'Background result'}</div>
          <span class="status-badge ${toneForStatus(item.status || item.session_status)}">${escapeHTML(humanizeStatus(item.status || item.session_status || 'unknown'))}</span>
        </div>
        <div class="notification-copy"${backgroundNotificationCopyIsRaw(item) ? ' translate="no" data-i18n-skip' : ''}>${escapeHTML(truncateText(backgroundNotificationCopy(item), 180))}</div>
        ${pendingHint ? `<div class="job-card-meta">${escapeHTML(pendingHint)}</div>` : ''}
        ${maybeArray(item.available_actions).length ? `<div class="goal-meta-line">Parent actions: ${escapeHTML(maybeArray(item.available_actions).map(humanizeStatus).join(' · '))}</div>` : ''}
        ${item.effective_budget ? `<div class="goal-meta-line">Budget ${escapeHTML(effectiveBudgetSummary(item.effective_budget, item.effective_budget.last_reason))}</div>` : ''}
        <div class="job-card-meta">${renderBackgroundNotificationMeta(item)}</div>
        ${item.session_id ? `
          <div class="card-actions">
            <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.session_id)}">Open child session</button>
          </div>
        ` : ''}
      </div>
    `;
  }).join('');
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
        <div class="timeline-card-text"${descriptor.copyIsRaw ? ' translate="no" data-i18n-skip' : ''}>${escapeHTML(descriptor.copy)}</div>
        ${descriptor.meta ? `<div class="timeline-card-meta"${descriptor.metaIsRaw ? ' translate="no" data-i18n-skip' : ''}>${escapeHTML(descriptor.meta)}</div>` : ''}
        ${showData ? `<pre class="timeline-card-data">${escapeHTML(descriptor.data)}</pre>` : ''}
      </div>
    </div>
  `;
}

function renderSubAgentCard(row) {
  const sessionItem = row.session || null;
  const job = row.job || null;
  const status = subAgentDisplayStatus(sessionItem, job);
  const label = agentLabel(sessionItem?.agent_name || job?.agent_name, sessionItem?.agent_role || job?.agent_role) || shortId(sessionItem?.id || job?.id || '');
  const model = sessionItem?.model || job?.model || sessionItem?.provider || job?.provider || 'n/a';
  const phase = sessionItem?.phase ? phaseHeadline(sessionItem.phase) : (job?.mode || 'exec');
  const sessionId = sessionItem?.id || job?.session_id || '';
  const jobId = job?.id || sessionItem?.queue_job_id || '';
  const error = sessionItem?.last_error || job?.last_error || '';
  const finalText = job?.final_text || sessionItem?.final_text || sessionItem?.last_assistant_excerpt || '';
  const effectiveBudget = sessionItem?.effective_budget || job?.effective_budget || null;
  const budgetReason = sessionItem?.pause_reason || effectiveBudget?.last_reason || '';
  return `
    <div class="agent-card">
      <div class="agent-card-top">
        <div class="agent-card-title">${escapeHTML(label)}</div>
        <span class="status-badge ${toneForStatus(status)}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      <div class="agent-card-copy">${escapeHTML(model)} · ${escapeHTML(phase)}</div>
      ${error ? `<div class="notification-copy danger" translate="no" data-i18n-skip>${escapeHTML(truncateText(error, 180))}</div>` : ''}
      ${!error && finalText ? `<div class="agent-card-copy">${escapeHTML(truncateText(finalText, 180))}</div>` : ''}
      ${effectiveBudget ? `<div class="goal-meta-line">Budget ${escapeHTML(effectiveBudgetSummary(effectiveBudget, budgetReason))}</div>` : ''}
      <div class="agent-card-meta">${sessionId ? escapeHTML(shortId(sessionId)) : ''}${jobId ? `${sessionId ? ' · ' : ''}job ${escapeHTML(shortId(jobId))}` : ''}</div>
      ${renderVisiblePaths(sessionItem?.visible_paths || job?.visible_paths)}
      <div class="card-actions">
        ${sessionId ? renderSessionStopButton(sessionId, status) : ''}
        ${sessionId ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(sessionId)}">Open child session</button>` : ''}
        ${job?.parent_session_id ? `<button class="mini-link-btn" type="button" data-open-parent-session="${escapeAttr(job.parent_session_id)}">Open parent session</button>` : ''}
      </div>
    </div>
  `;
}

function subAgentDisplayStatus(sessionItem, job) {
  const jobStatus = job ? queueJobDisplayStatus(job) : '';
  if (jobStatus && (isTerminalStatus(jobStatus) || jobStatus === 'blocked')) {
    return jobStatus;
  }
  return sessionItem?.status || jobStatus || 'unknown';
}

function isTerminalStatus(status) {
  return status === 'completed' || status === 'cancelled' || status === 'failed';
}

function effectiveBudgetSummary(budget, pauseReason = '') {
  if (!budget || typeof budget !== 'object') {
    return 'unknown';
  }
  if (budget.status === 'disabled') {
    return 'Off (snapshotted)';
  }
  const parts = [];
  if (Number(budget.attempt || 0) > 0) {
    parts.push(`attempt ${Number(budget.attempt)}`);
  }
  if (Number(budget.max_turns_per_attempt || 0) > 0) {
    const used = Number(budget.used_turns || 0);
    const limit = Number(budget.max_turns_per_attempt);
    const remaining = budget.remaining_turns === undefined ? Math.max(0, limit - used) : Number(budget.remaining_turns || 0);
    parts.push(`turns ${used}/${limit}, ${remaining} left`);
  }
  if (Number(budget.max_active_runtime_ms || 0) > 0) {
    const usedSec = Math.ceil(Number(budget.used_active_runtime_ms || 0) / 1000);
    const limitSec = Math.ceil(Number(budget.max_active_runtime_ms) / 1000);
    const remainingMS = budget.remaining_active_runtime_ms === undefined
      ? Math.max(0, Number(budget.max_active_runtime_ms) - Number(budget.used_active_runtime_ms || 0))
      : Number(budget.remaining_active_runtime_ms || 0);
    parts.push(`active ${usedSec}s/${limitSec}s, ${Math.ceil(remainingMS / 1000)}s left`);
    if (budget.active_runtime_checkpoint_at) {
      const leaseState = budget.active_runtime_lease_open ? 'lease open' : 'lease closed';
      parts.push(`checkpoint ${formatTimestamp(budget.active_runtime_checkpoint_at)}, ${leaseState}`);
    }
    const recoveryMS = Number(budget.active_runtime_last_recovery_ms || 0);
    if (recoveryMS > 0) {
      const recoveryValue = recoveryMS < 1000 ? `${recoveryMS}ms` : `${Number((recoveryMS / 1000).toFixed(3))}s`;
      const recoveryAt = budget.active_runtime_last_recovery_at
        ? ` at ${formatTimestamp(budget.active_runtime_last_recovery_at)}`
        : '';
      parts.push(`crash recovery +${recoveryValue}${recoveryAt}`);
    }
  }
  if (budget.absolute_deadline_at) {
    parts.push(`deadline ${formatTimestamp(budget.absolute_deadline_at)}`);
  }
  const reason = String(pauseReason || budget.last_reason || '').trim();
  if (reason) {
    parts.push(reason);
  }
  if (budget.source) {
    parts.push(String(budget.source));
  }
  return parts.length ? parts.join(' · ') : humanizeStatus(budget.status || 'active');
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
  const status = queueJobDisplayStatus(job);
  return `
    <div class="job-card ${job.id === selectedQueueJobId() ? 'active' : ''}" data-queue-job-id="${escapeAttr(job.id)}">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(agentLabel(job.agent_name, job.agent_role) || shortId(job.id))}</div>
        <span class="status-badge ${toneForStatus(status)}">${escapeHTML(humanizeStatus(status))}</span>
      </div>
      <div class="job-card-copy">${escapeHTML(truncateText(job.prompt || '(no prompt)', 180))}</div>
      <div class="job-card-meta">${escapeHTML(shortId(job.id))} · ${escapeHTML(job.mode || 'exec')}${job.session_id ? ` · child ${escapeHTML(shortId(job.session_id))}` : ''}</div>
      ${renderVisiblePaths(job.visible_paths)}
      <div class="card-actions">
        ${job.session_id ? renderSessionStopButton(job.session_id, status) : ''}
        ${job.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(job.session_id)}">Open child session</button>` : ''}
        ${job.parent_session_id ? `<button class="mini-link-btn" type="button" data-open-parent-session="${escapeAttr(job.parent_session_id)}">Open parent session</button>` : ''}
      </div>
    </div>
  `;
}

function queueJobDisplayStatus(job) {
  if (!job) {
    return 'unknown';
  }
  if (isTerminalStatus(job.status) || job.status === 'blocked') {
    return job.status;
  }
  return job.session_status || job.status || 'unknown';
}

function renderSessionStopButton(sessionID, status, label = 'Stop') {
  if (!sessionID) {
    return '';
  }
  const stoppable = isStoppableSessionStatus(status);
  const busy = isStoppingSession(sessionID);
  const displayLabel = busy && label === 'Stop' ? 'Stopping' : label;
  const text = displayLabel ? `<span>${escapeHTML(displayLabel)}</span>` : '';
  const title = busy ? 'Stop already requested' : stoppable ? 'Stop session' : 'Session is not running';
  return `
    <button class="mini-link-btn danger" type="button" data-stop-session-id="${escapeAttr(sessionID)}" aria-label="Stop session ${escapeAttr(shortId(sessionID))}" title="${escapeAttr(title)}" ${busy || !stoppable ? 'disabled' : ''}>
      <i data-lucide="square"></i>
      ${text}
    </button>
  `;
}

function renderNotificationCard(item) {
  const pendingHint = backgroundNotificationPendingHint(item);
  const label = agentLabel(item.agent_name, item.agent_role);
  return `
    <div class="notification-card">
      <div class="job-card-top">
        <div class="job-card-title">${label ? `<span translate="no" data-i18n-skip>${escapeHTML(label)}</span>` : 'Background notification'}</div>
        <span class="status-badge ${toneForStatus(item.status || item.session_status)}">${escapeHTML(humanizeStatus(item.status || item.session_status || 'unknown'))}</span>
      </div>
      <div class="notification-copy"${backgroundNotificationCopyIsRaw(item) ? ' translate="no" data-i18n-skip' : ''}>${escapeHTML(truncateText(backgroundNotificationCopy(item), 200))}</div>
      ${pendingHint ? `<div class="job-card-meta">${escapeHTML(pendingHint)}</div>` : ''}
      ${maybeArray(item.available_actions).length ? `<div class="goal-meta-line">Parent actions: ${escapeHTML(maybeArray(item.available_actions).map(humanizeStatus).join(' · '))}</div>` : ''}
      ${item.effective_budget ? `<div class="goal-meta-line">Budget ${escapeHTML(effectiveBudgetSummary(item.effective_budget, item.effective_budget.last_reason))}</div>` : ''}
      <div class="job-card-meta">${renderBackgroundNotificationMeta(item)}</div>
      ${renderVisiblePaths(item.visible_paths)}
      <div class="card-actions">
        ${item.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.session_id)}">Open child session</button>` : ''}
      </div>
    </div>
  `;
}

function backgroundNotificationCopy(item) {
  if (item?.last_error) {
    return item.last_error;
  }
  if (item?.final_text) {
    return item.final_text;
  }
  if (isPendingBackgroundNotification(item)) {
    return 'Delivery is pending; the parent session has not consumed this background result yet.';
  }
  return 'No final text recorded.';
}

function backgroundNotificationCopyIsRaw(item) {
  return Boolean(item?.last_error || item?.final_text);
}

function backgroundNotificationPendingHint(item) {
  if (!isPendingBackgroundNotification(item)) {
    return '';
  }
  return 'Pending delivery means the child/job result is recorded, but the parent run has not continued to accept it.';
}

function renderBackgroundNotificationMeta(item) {
  const parts = [`<span translate="no" data-i18n-skip>${escapeHTML(shortId(item?.queue_job_id || item?.session_id || item?.id))}</span>`];
  if (item?.delivery_status) {
    parts.push(`<span translate="no" data-i18n-skip>${escapeHTML(item.delivery_status)}</span>`);
  }
  if (item?.session_status) {
    parts.push(`<span>session</span> <span>${escapeHTML(humanizeStatus(item.session_status))}</span>`);
  }
  return parts.filter(Boolean).join(' · ');
}

function isPendingBackgroundNotification(item) {
  return String(item?.delivery_status || '').toLowerCase() === 'pending';
}

function renderTodoItem(item) {
  const content = item.content || '';
  return `
    <div class="todo-card">
      <div class="job-card-top">
				<div class="todo-card-title">${content ? `<span translate="no" data-i18n-skip>${escapeHTML(content)}</span>` : 'Untitled todo'}</div>
        <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
      </div>
      <div class="todo-pill-row">
        ${item.priority ? `<span class="task-chip" translate="no" data-i18n-skip>${escapeHTML(item.priority)}</span>` : ''}
        ${item.updated_at ? `<span class="task-chip">${escapeHTML(formatTimestamp(item.updated_at))}</span>` : ''}
      </div>
    </div>
  `;
}

function renderTaskItem(task, derivedStatus = '') {
	const displayStatus = derivedStatus || task.status;
	const title = task.subject || task.id || '';
	return `
		<div class="task-card" data-task-id="${escapeAttr(task.id || '')}" data-derived-status="${escapeAttr(displayStatus || '')}">
			<div class="job-card-top">
				<div class="task-card-title">${title ? `<span translate="no" data-i18n-skip>${escapeHTML(title)}</span>` : 'Task'}</div>
				<span class="status-badge ${toneForStatus(displayStatus)}">${escapeHTML(humanizeStatus(displayStatus))}</span>
      </div>
			<div class="task-card-copy">${task.description ? `<span translate="no" data-i18n-skip>${escapeHTML(task.description)}</span>` : 'No description.'}</div>
			<div class="task-pill-row">
				${task.id ? `<span class="task-chip" translate="no" data-i18n-skip>${escapeHTML(task.id)}</span>` : ''}
        ${task.priority ? `<span class="task-chip" translate="no" data-i18n-skip>${escapeHTML(task.priority)}</span>` : ''}
        ${task.owner ? `<span class="task-chip" translate="no" data-i18n-skip>${escapeHTML(task.owner)}</span>` : ''}
        ${maybeArray(task.blocked_by).length ? `<span class="task-chip">blocked by ${escapeHTML(String(task.blocked_by.length))}</span>` : ''}
        ${maybeArray(task.blocks).length ? `<span class="task-chip">blocks ${escapeHTML(String(task.blocks.length))}</span>` : ''}
      </div>
    </div>
  `;
}
