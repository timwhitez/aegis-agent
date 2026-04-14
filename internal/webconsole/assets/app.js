/**
 * Go CLI Agent Webconsole
 */

const POLL_INTERVAL_MS = 1600;
const MAX_LIVE_EVENTS = 80;

const state = {
  currentView: 'chat',
  isGenerating: false,
  isConnected: false,
  ws: null,
  sessionId: nextEphemeralSessionId(),
  sessionBacked: false,
  sessionDetail: null,
  overview: null,
  historyData: null,
  historyPage: 1,
  historyPageSize: 8,
  refreshingHistory: false,
  toastCounter: 0,
  skills: [],
  fileTree: [],
  optimisticMessages: [],
  liveEvents: [],
  liveActivity: {
    title: 'Ready for a new session',
    copy: 'Send a prompt to start a durable session. Tool activity will appear here as it runs.',
    tone: 'neutral'
  },
  nextSendInterrupt: false,
  pollHandle: null,
  refreshingOverview: false,
  refreshingSession: false,
  pendingSessionRefresh: null,
  pendingOverviewRefresh: null
};

const nodes = {
  chatInput: document.getElementById('chat-input'),
  sendBtn: document.getElementById('send-btn'),
  chatMessages: document.getElementById('chat-messages'),
  chatContainer: document.getElementById('chat-container'),
  connectionDot: document.getElementById('connection-dot'),
  connectionStatus: document.getElementById('connection-status'),
  sessionIdDisplay: document.getElementById('session-id-display'),
  newSessionBtn: document.getElementById('new-session-btn'),
  interruptSessionBtn: document.getElementById('interrupt-session-btn'),
  interruptToggleBtn: document.getElementById('interrupt-toggle-btn'),
  inputContainer: document.getElementById('input-container'),
  inputStatusText: document.getElementById('input-status-text'),
  toastRack: document.getElementById('toast-rack'),
  skillsGrid: document.getElementById('skills-grid'),
  fileTree: document.getElementById('file-tree'),
  editorFilename: document.getElementById('editor-filename'),
  editorContent: document.getElementById('editor-content'),
  views: {
    chat: document.getElementById('chat-view'),
    skills: document.getElementById('skills-view'),
    workspace: document.getElementById('workspace-view'),
    history: document.getElementById('history-view'),
    settings: document.getElementById('settings-view')
  },
  navItems: document.querySelectorAll('.nav-item[data-view]')
};

function init() {
  if (window.marked?.setOptions) {
    window.marked.setOptions({
      breaks: true,
      gfm: true
    });
  }
  lucide.createIcons();
  setupWebSocket();
  setupEventListeners();
  resetChatSession({ notifyBackend: false });
  startPolling();
  refreshOverview();
  renderCurrentSession();
}

function nextEphemeralSessionId() {
  return '0x' + Math.random().toString(16).slice(2, 8).toUpperCase();
}

function hasDurableSession() {
  return state.sessionBacked && Boolean(state.sessionId);
}

function currentClientSessionId() {
  return state.sessionId;
}

function setupWebSocket() {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${window.location.host}/ws`;
  const ws = new WebSocket(wsUrl);
  state.ws = ws;

  ws.onopen = () => {
    state.isConnected = true;
    nodes.connectionStatus.innerText = 'Agent Connected';
    updateUI();
    queueOverviewRefresh(120);
  };

  ws.onmessage = (event) => {
    const data = JSON.parse(event.data);
    handleServerEvent(data);
  };

  ws.onclose = () => {
    state.isConnected = false;
    state.ws = null;
    nodes.connectionStatus.innerText = 'Disconnected';
    if (state.isGenerating) {
      state.isGenerating = false;
      state.liveActivity = {
        title: 'Disconnected from the local agent',
        copy: 'The webconsole will retry automatically. Durable session data remains on disk.',
        tone: 'danger'
      };
    }
    updateUI();
    renderCurrentSession();
    window.setTimeout(setupWebSocket, 3000);
  };
}

function handleServerEvent(data) {
  switch (data.type) {
    case 'session': {
      const clientSessionId = data.payload?.clientSessionId;
      if (clientSessionId && clientSessionId !== currentClientSessionId()) {
        return;
      }
      if (data.payload?.sessionId) {
        adoptSession(data.payload.sessionId, true);
        queueSessionRefresh(120);
        queueOverviewRefresh(240);
      }
      break;
    }
    case 'message': {
      if (!matchesCurrentSession(data.payload?.sessionId)) {
        return;
      }
      queueSessionRefresh(120);
      break;
    }
    case 'status': {
      const sessionID = data.payload?.sessionId;
      if (sessionID && !state.sessionBacked) {
        adoptSession(sessionID, true);
      }
      if (!matchesCurrentSession(sessionID)) {
        return;
      }
      if (sessionID) {
        adoptSession(sessionID, true);
      }
      if (data.payload?.status === 'running') {
        setGenerating(true, {
          title: phaseHeadline(data.payload?.phase),
          copy: 'Live session is active. Send again to queue steer input into the current run.',
          tone: 'live'
        });
      } else {
        setGenerating(false, {
          title: humanizeStatus(data.payload?.status || 'idle'),
          copy: 'Durable session state has been updated.',
          tone: toneForStatus(data.payload?.status)
        });
      }
      queueSessionRefresh(120);
      queueOverviewRefresh(260);
      break;
    }
    case 'engine_event': {
      const payload = data.payload || {};
      if (payload.sessionId && !state.sessionBacked) {
        adoptSession(payload.sessionId, true);
      }
      if (!matchesCurrentSession(payload.sessionId)) {
        return;
      }
      pushLiveEvent(payload);
      updateLiveActivityFromEvent(payload);
      if (shouldRefreshAfterEvent(payload.type)) {
        queueSessionRefresh(220);
      }
      if (needsOverviewRefresh(payload.type)) {
        queueOverviewRefresh(260);
      }
      renderCurrentSession();
      break;
    }
    case 'error': {
      const sessionID = data.payload?.sessionId;
      if (sessionID && !state.sessionBacked) {
        adoptSession(sessionID, true);
      }
      if (!matchesCurrentSession(sessionID)) {
        return;
      }
      setGenerating(false, {
        title: 'Session error',
        copy: data.payload?.content || 'The session failed.',
        tone: 'danger'
      });
      showToast(data.payload?.content || 'The session failed.', 'error');
      queueSessionRefresh(120);
      queueOverviewRefresh(240);
      break;
    }
  }
}

function matchesCurrentSession(sessionID) {
  if (!sessionID) {
    return true;
  }
  return sessionID === state.sessionId;
}

function pushLiveEvent(event) {
  state.liveEvents.push(event);
  if (state.liveEvents.length > MAX_LIVE_EVENTS) {
    state.liveEvents = state.liveEvents.slice(-MAX_LIVE_EVENTS);
  }
}

function setupEventListeners() {
  nodes.navItems.forEach((item) => {
    item.addEventListener('click', () => {
      const view = item.getAttribute('data-view');
      switchView(view);
    });
  });

  nodes.sendBtn.addEventListener('click', sendMessage);
  nodes.interruptSessionBtn?.addEventListener('click', requestInterrupt);
  nodes.interruptToggleBtn?.addEventListener('click', toggleInterruptArm);
  nodes.newSessionBtn?.addEventListener('click', () => {
    const wasGenerating = state.isGenerating;
    resetChatSession({ notifyBackend: true });
    showToast(
      wasGenerating
        ? 'Started a new session. The previous run may still settle in the background.'
        : 'Started a new session.',
      'info'
    );
  });

  nodes.chatInput.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      sendMessage();
    }
  });

  nodes.chatInput.addEventListener('input', function onInput() {
    this.style.height = 'auto';
    this.style.height = `${this.scrollHeight}px`;
    updateUI();
  });

  document.addEventListener('click', async (event) => {
    const historyPageButton = event.target.closest('[data-history-page]');
    if (historyPageButton) {
      const direction = historyPageButton.getAttribute('data-history-page');
      if (direction === 'prev') {
        await fetchHistory(Math.max(1, state.historyPage - 1));
      } else if (direction === 'next') {
        const nextPage = state.historyData?.total_pages
          ? Math.min(state.historyData.total_pages, state.historyPage + 1)
          : state.historyPage + 1;
        await fetchHistory(nextPage);
      }
      return;
    }

    const clearHistoryButton = event.target.closest('[data-history-clear]');
    if (clearHistoryButton) {
      await clearHistory();
      return;
    }

    const deleteHistoryButton = event.target.closest('[data-delete-session]');
    if (deleteHistoryButton) {
      const sessionID = deleteHistoryButton.getAttribute('data-delete-session');
      if (sessionID) {
        await deleteHistorySession(sessionID);
      }
      return;
    }

    const openSessionButton = event.target.closest('[data-open-session]');
    if (openSessionButton) {
      const sessionID = openSessionButton.getAttribute('data-open-session');
      if (sessionID) {
        await openSession(sessionID, { switchToChat: true });
      }
      return;
    }

    const openParentButton = event.target.closest('[data-open-parent-session]');
    if (openParentButton) {
      const parentSessionID = openParentButton.getAttribute('data-open-parent-session');
      if (parentSessionID) {
        await openSession(parentSessionID, { switchToChat: true });
      }
      return;
    }

  });

  document.addEventListener('change', async (event) => {
    if (!event.target || event.target.id !== 'skill-upload') {
      return;
    }
    const file = event.target.files?.[0];
    if (!file) {
      return;
    }
    const formData = new FormData();
    formData.append('file', file);
    try {
      await fetch('/api/skills/upload', {
        method: 'POST',
        body: formData
      });
      showToast('Skill uploaded and extracted successfully.', 'success');
      if (state.currentView === 'skills') {
        await fetchSkills();
      }
    } catch (err) {
      showToast('Failed to upload skill zip.', 'error');
    }
    event.target.value = '';
  });
}

function switchView(viewName) {
  if (!nodes.views[viewName]) {
    return;
  }
  Object.values(nodes.views).forEach((view) => {
    view.style.display = 'none';
  });
  nodes.navItems.forEach((item) => item.classList.remove('active'));
  nodes.views[viewName].style.display = 'flex';
  const activeNav = Array.from(nodes.navItems).find((item) => item.getAttribute('data-view') === viewName);
  if (activeNav) {
    activeNav.classList.add('active');
  }
  state.currentView = viewName;

  if (viewName === 'chat') {
    renderCurrentSession();
    queueSessionRefresh(60);
    queueOverviewRefresh(60);
  }
  if (viewName === 'history') {
    fetchHistory();
  }
  if (viewName === 'skills') {
    fetchSkills();
  }
  if (viewName === 'workspace') {
    fetchWorkspace();
  }
  if (viewName === 'settings') {
    renderSettings();
  }
}

async function sendMessage() {
  const text = nodes.chatInput.value.trim();
  if (!text) {
    return;
  }
  if (!state.isConnected) {
    showToast('The local agent connection is offline. Wait for reconnection and try again.', 'error');
    updateUI();
    return;
  }

  const optimisticID = appendOptimisticMessage('user', text, {
    source: state.isGenerating ? 'steer' : 'user',
    interrupt: state.nextSendInterrupt && state.isGenerating && hasDurableSession()
  });

  nodes.chatInput.value = '';
  nodes.chatInput.style.height = 'auto';
  updateUI();
  renderCurrentSession();

  if (state.isGenerating && hasDurableSession()) {
    try {
      await requestJSON(`/api/sessions/${encodeURIComponent(state.sessionId)}/steer`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          message: text,
          interrupt: state.nextSendInterrupt
        })
      });
      const usedInterrupt = state.nextSendInterrupt;
      state.nextSendInterrupt = false;
      state.liveActivity = {
        title: usedInterrupt ? 'Interrupt steer requested' : 'Steer queued',
        copy: usedInterrupt
          ? 'The runtime will interrupt at the nearest safe boundary and merge your new instruction.'
          : 'Your follow-up was queued for the current run without starting a new session.',
        tone: usedInterrupt ? 'queued' : 'live'
      };
      showToast(usedInterrupt ? 'Interrupt steer sent.' : 'Steer queued for the running session.', 'success');
      queueSessionRefresh(120);
      queueOverviewRefresh(220);
    } catch (err) {
      removeOptimisticMessage(optimisticID);
      showToast(err.message || 'Failed to queue steer input.', 'error');
    }
    updateUI();
    renderCurrentSession();
    return;
  }

  if (!state.ws || state.ws.readyState !== WebSocket.OPEN) {
    removeOptimisticMessage(optimisticID);
    showToast('The agent connection is offline. Wait for reconnection and try again.', 'error');
    updateUI();
    renderCurrentSession();
    return;
  }

  state.ws.send(JSON.stringify({
    type: 'chat',
    message: text,
    sessionId: state.sessionId
  }));
  setGenerating(true, {
    title: 'Launching session',
    copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
    tone: 'live'
  });
  queueOverviewRefresh(220);
}

function toggleInterruptArm() {
  if (!state.isGenerating || !hasDurableSession()) {
    return;
  }
  state.nextSendInterrupt = !state.nextSendInterrupt;
  updateUI();
}

async function requestInterrupt() {
  if (!state.isGenerating || !hasDurableSession()) {
    showToast('No running session is available for interrupt.', 'info');
    return;
  }
  try {
    await requestJSON(`/api/sessions/${encodeURIComponent(state.sessionId)}/interrupt`, {
      method: 'POST'
    });
    state.liveActivity = {
      title: 'Interrupt requested',
      copy: 'The runner will stop at the nearest safe boundary and surface the session state.',
      tone: 'queued'
    };
    showToast('Interrupt requested.', 'success');
    queueSessionRefresh(120);
  } catch (err) {
    showToast(err.message || 'Failed to request interrupt.', 'error');
  }
  renderCurrentSession();
}

function adoptSession(sessionID, backed) {
  if (!sessionID) {
    return;
  }
  state.sessionId = sessionID;
  state.sessionBacked = backed;
  updateSessionId();
}

function resetChatSession({ notifyBackend }) {
  state.sessionId = nextEphemeralSessionId();
  state.sessionBacked = false;
  state.sessionDetail = null;
  state.optimisticMessages = [];
  state.liveEvents = [];
  state.nextSendInterrupt = false;
  state.liveActivity = {
    title: 'Ready for a new session',
    copy: 'Send a prompt to create a durable session. Answers, tool calls, and running flow will appear here.',
    tone: 'neutral'
  };
  state.isGenerating = false;
  updateSessionId();
  if (notifyBackend && state.ws && state.ws.readyState === WebSocket.OPEN) {
    state.ws.send(JSON.stringify({
      type: 'reset_session',
      sessionId: state.sessionId
    }));
  }
  renderCurrentSession();
  updateUI();
}

function setGenerating(value, activity) {
  state.isGenerating = value;
  if (!value) {
    state.nextSendInterrupt = false;
  }
  if (activity) {
    state.liveActivity = activity;
  }
  updateUI();
  renderCurrentSession();
}

function updateSessionId() {
  nodes.sessionIdDisplay.innerText = `ID: ${state.sessionId}`;
}

function updateUI() {
  const hasDraft = nodes.chatInput.value.trim().length > 0;
  nodes.sendBtn.disabled = !state.isConnected || !hasDraft;
  nodes.sendBtn.classList.toggle('is-interrupt', state.nextSendInterrupt && state.isGenerating && hasDurableSession());
  nodes.inputContainer.classList.toggle('is-busy', state.isGenerating);
  nodes.inputContainer.classList.toggle('is-offline', !state.isConnected);
  nodes.newSessionBtn?.classList.toggle('is-busy', state.isGenerating);
  nodes.interruptSessionBtn?.classList.toggle('is-visible', state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.classList.toggle('is-visible', state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.classList.toggle('is-armed', state.nextSendInterrupt && state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.setAttribute('aria-pressed', state.nextSendInterrupt ? 'true' : 'false');
  if (nodes.interruptSessionBtn) {
    nodes.interruptSessionBtn.disabled = !state.isGenerating || !hasDurableSession();
  }
  nodes.chatInput.placeholder = !state.isConnected
    ? 'Waiting for the local agent connection…'
    : state.isGenerating && hasDurableSession()
      ? state.nextSendInterrupt
        ? 'Send an interrupt steer message to the running session...'
        : 'Send a steer message into the running session...'
      : 'Ask anything...';

  nodes.inputStatusText.textContent = !state.isConnected
    ? 'Reconnecting to the local agent…'
    : state.isGenerating && hasDurableSession()
      ? state.nextSendInterrupt
        ? 'Next send will request interrupt before merging your steer prompt.'
        : 'Session is running. Send again to queue steer input, or arm interrupt for the next send.'
      : 'Enter to send, Shift+Enter for new line';

  if (!state.isConnected) {
    nodes.connectionDot.className = 'dot';
    return;
  }
  nodes.connectionDot.className = state.isGenerating ? 'dot busy' : 'dot online';
}

function startPolling() {
  stopPolling();
  state.pollHandle = window.setInterval(() => {
    if (state.currentView === 'history') {
      fetchHistory(state.historyPage);
      return;
    }
    if (state.currentView === 'chat') {
      refreshOverview();
    }
    if (state.currentView === 'chat' && hasDurableSession()) {
      refreshCurrentSession();
    }
  }, POLL_INTERVAL_MS);
}

function stopPolling() {
  if (state.pollHandle) {
    window.clearInterval(state.pollHandle);
    state.pollHandle = null;
  }
}

function queueSessionRefresh(delay = 120) {
  if (!hasDurableSession()) {
    return;
  }
  window.clearTimeout(state.pendingSessionRefresh);
  state.pendingSessionRefresh = window.setTimeout(() => {
    refreshCurrentSession();
  }, delay);
}

function queueOverviewRefresh(delay = 180) {
  window.clearTimeout(state.pendingOverviewRefresh);
  state.pendingOverviewRefresh = window.setTimeout(() => {
    refreshOverview();
  }, delay);
}

async function refreshOverview() {
  if (state.refreshingOverview) {
    return;
  }
  state.refreshingOverview = true;
  try {
    state.overview = await requestJSON('/api/overview');
    renderCurrentSession();
  } catch (err) {
    console.error('overview error', err);
    if (state.currentView === 'history') {
      nodes.views.history.innerHTML = '<div class="empty-panel">Failed to load recent activity.</div>';
    }
  } finally {
    state.refreshingOverview = false;
  }
}

async function refreshCurrentSession() {
  if (!hasDurableSession() || state.refreshingSession) {
    return;
  }
  state.refreshingSession = true;
  try {
    const detail = await requestJSON(`/api/sessions/${encodeURIComponent(state.sessionId)}?limit=80`);
    state.sessionDetail = detail;
    reconcileOptimisticMessages(detail);
    if (detail?.state?.status === 'running') {
      state.isGenerating = true;
      if (!state.liveEvents.length) {
        state.liveActivity = {
          title: phaseHeadline(detail.state.phase),
          copy: 'The runner is active. Tool calls and child-agent transitions will stream into this panel as durable events.',
          tone: 'live'
        };
      }
    } else {
      state.isGenerating = false;
      if (!state.liveEvents.length || toneForStatus(detail?.state?.status) !== 'live') {
        state.liveActivity = {
          title: humanizeStatus(detail?.state?.status || 'idle'),
          copy: detail?.state?.last_error || detail?.state?.last_assistant_excerpt || 'Durable session data is loaded.',
          tone: toneForStatus(detail?.state?.status)
        };
      }
      state.nextSendInterrupt = false;
    }
    renderCurrentSession();
    updateUI();
  } catch (err) {
    console.error('session detail error', err);
  } finally {
    state.refreshingSession = false;
  }
}

function renderCurrentSession() {
  renderMessageStream();
  lucide.createIcons();
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

function renderMessageStream() {
  const detailMessages = maybeArray(state.sessionDetail?.messages);
  const optimisticMessages = state.optimisticMessages.slice();
  const stream = detailMessages.length ? detailMessages.concat(optimisticMessages) : optimisticMessages;
  const sections = [];

  if (hasDurableSession() || state.isGenerating) {
    sections.push(renderSessionActivityCard());
  }

  const flowLane = renderFlowLane();
  if (flowLane) {
    sections.push(flowLane);
  }

  if (!stream.length) {
    nodes.chatMessages.innerHTML = `
      ${sections.join('')}
      ${renderEmptySessionState()}
      ${state.isGenerating ? renderPendingStageCard() : ''}
    `;
    nodes.chatContainer.scrollTop = nodes.chatContainer.scrollHeight;
    return;
  }

  const html = stream.map((message) => renderMessage(message)).join('');
  sections.push(html);
  if (state.isGenerating) {
    sections.push(renderPendingStageCard());
  }
  nodes.chatMessages.innerHTML = sections.join('');
  nodes.chatContainer.scrollTop = nodes.chatContainer.scrollHeight;
}

function renderEmptySessionState() {
  const recentSessions = maybeArray(state.overview?.recent_sessions).length;
  return `
    <section class="empty-session-state">
      <div class="status-badge neutral">Ready</div>
      <h1 class="empty-session-title">Start a session.</h1>
      <p class="empty-session-copy">Answers, tool calls, and running flow will appear here. Use History to reopen older sessions.</p>
      ${recentSessions ? `<div class="empty-session-note">${escapeHTML(String(recentSessions))} recent sessions available in History.</div>` : ''}
    </section>
  `;
}

function renderSessionActivityCard() {
  const detail = state.sessionDetail;
  const counters = summarizeCurrentSession();
  const status = detail?.state?.status || (state.isGenerating ? 'running' : 'idle');
  const phase = detail?.state?.phase ? phaseHeadline(detail.state.phase) : state.liveActivity.title;
  const tone = toneForStatus(status);
  const copy = detail?.state?.last_error || detail?.state?.last_assistant_excerpt || state.liveActivity.copy;

  return `
    <section class="session-flow-card">
      <div class="session-flow-head">
        <div class="message-header-meta">
          <span class="status-badge ${tone}">${escapeHTML(humanizeStatus(status))}</span>
          ${detail?.metadata?.id ? `<span class="tiny-code-chip">${escapeHTML(shortId(detail.metadata.id))}</span>` : `<span class="tiny-code-chip">${escapeHTML(shortId(state.sessionId))}</span>`}
          <span class="surface-chip">${escapeHTML(phase)}</span>
          ${counters.toolCalls ? `<span class="surface-chip">${escapeHTML(String(counters.toolCalls))} tool call${counters.toolCalls === 1 ? '' : 's'}</span>` : ''}
          ${counters.childSessions ? `<span class="surface-chip">${escapeHTML(String(counters.childSessions))} child session${counters.childSessions === 1 ? '' : 's'}</span>` : ''}
          ${counters.queueJobs ? `<span class="surface-chip">${escapeHTML(String(counters.queueJobs))} queue job${counters.queueJobs === 1 ? '' : 's'}</span>` : ''}
        </div>
      </div>
      <div class="session-flow-copy">${escapeHTML(copy || 'Waiting for the next update.')}</div>
    </section>
  `;
}

function renderFlowLane() {
  const detailItems = maybeArray(state.sessionDetail?.timeline).slice(-4).reverse();
  if (!detailItems.length) {
    return '';
  }
  return `
    <section class="flow-lane">
      <div class="flow-lane-label">Flow</div>
      <div class="flow-lane-stack">
        ${detailItems.map((item) => renderTimelineItem(item)).join('')}
      </div>
    </section>
  `;
}

function renderMessage(message) {
  const role = message.role || 'assistant';
  const visualRole = role === 'user' ? 'user' : role === 'system' ? 'system' : 'assistant';
  const actor = actorNameForMessage(message);
  const icon = iconForRole(role);
  const textHTML = message.text
    ? `<div class="message-bubble prose">${safeMarkdown(message.text)}</div>`
    : '';
  const toolCallHTML = maybeArray(message.tool_calls).length
    ? `<div class="tool-cluster">${maybeArray(message.tool_calls).map((call) => renderToolCall(call)).join('')}</div>`
    : '';
  const toolResultHTML = maybeArray(message.tool_results).length
    ? `<div class="tool-cluster">${maybeArray(message.tool_results).map((result) => renderToolResult(result)).join('')}</div>`
    : '';

  return `
    <article class="message ${visualRole} ${message.pending ? 'optimistic' : ''}">
      <div class="message-header">
        <div style="display:flex; align-items:center; gap:8px;">
          <i data-lucide="${escapeAttr(icon)}" class="message-header-icon" style="width:14px;height:14px;"></i>
          <span class="message-header-name">${escapeHTML(actor)}</span>
        </div>
        <div class="message-header-meta">
          ${renderMessageMetaChips(message)}
        </div>
      </div>
      <div class="message-body">
        ${textHTML}
        ${toolCallHTML}
        ${toolResultHTML}
      </div>
    </article>
  `;
}

function renderMessageMetaChips(message) {
  const chips = [];
  if (message.pending) {
    chips.push('<span class="message-meta-chip">Pending</span>');
  }
  if (message.meta?.source) {
    chips.push(`<span class="message-meta-chip">${escapeHTML(message.meta.source)}</span>`);
  }
  if (message.meta?.interrupt) {
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

function renderToolCall(call) {
  const delegate = isMultiAgentTool(call.name);
  return `
    <details class="tool-card tool-call-card ${delegate ? 'delegate' : ''}" ${delegate ? 'open' : ''}>
      <summary>
        <div class="tool-card-heading">
          <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
            <span class="tool-card-type">Call</span>
            <strong>${escapeHTML(call.name || 'tool')}</strong>
          </div>
          <div class="tool-card-meta">
            ${call.id ? `<span class="tiny-code-chip">${escapeHTML(shortId(call.id))}</span>` : ''}
            ${call.provider_call_id ? `<span class="tiny-code-chip">${escapeHTML(shortId(call.provider_call_id))}</span>` : ''}
          </div>
        </div>
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
  return `
    <details class="tool-card tool-result-card ${delegate ? 'delegate' : ''} ${result.is_error ? 'error' : ''}" ${open ? 'open' : ''}>
      <summary>
        <div class="tool-card-heading">
          <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
            <span class="tool-card-type">${result.is_error ? 'Error' : 'Result'}</span>
            <strong>${escapeHTML(result.name || 'tool')}</strong>
          </div>
          <div class="tool-card-meta">
            ${result.tool_call_id ? `<span class="tiny-code-chip">${escapeHTML(shortId(result.tool_call_id))}</span>` : ''}
            ${result.final ? '<span class="status-badge live">Final</span>' : ''}
          </div>
        </div>
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
    return `
      <div class="tool-special-card">
        <div class="tool-special-grid">
          ${renderMiniMetric('Status', humanizeStatus(parsed.status || parsed.session_status || 'unknown'))}
          ${renderMiniMetric('Role', parsed.agent_role || 'master-decided')}
          ${renderMiniMetric('Session', shortId(parsed.session_id || 'n/a'))}
          ${renderMiniMetric('Queue job', shortId(parsed.queue_job_id || 'n/a'))}
        </div>
        <div class="kv-list">
          ${parsed.workdir ? renderKVRow('Workdir', parsed.workdir) : ''}
          ${parsed.last_error ? renderKVRow('Last error', parsed.last_error) : ''}
          ${parsed.final_text ? renderKVRow('Final text', truncateText(parsed.final_text, 180)) : ''}
        </div>
        <div class="card-actions">
          ${parsed.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(parsed.session_id)}">Open child session</button>` : ''}
        </div>
        ${renderVisiblePaths(parsed.visible_paths)}
      </div>
    `;
  }

  if (result.name === 'agent_list') {
    const sessions = maybeArray(parsed.sessions).slice(0, 4);
    const jobs = maybeArray(parsed.jobs).slice(0, 4);
    return `
      <div class="tool-special-card">
        <div class="tool-special-grid">
          ${renderMiniMetric('Child sessions', String(maybeArray(parsed.sessions).length))}
          ${renderMiniMetric('Queue jobs', String(maybeArray(parsed.jobs).length))}
        </div>
        ${sessions.length ? `
          <div class="card-stack">
            ${sessions.map((item) => `
              <div class="agent-card">
                <div class="agent-card-top">
                  <div class="agent-card-title">${escapeHTML(agentLabel(item.agent_name, item.agent_role) || shortId(item.id))}</div>
                  <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
                </div>
                <div class="agent-card-meta">${escapeHTML(item.model || item.provider || 'n/a')} · ${escapeHTML(shortId(item.id))}</div>
                <div class="card-actions">
                  <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.id)}">Open session</button>
                </div>
              </div>
            `).join('')}
          </div>
        ` : ''}
        ${jobs.length ? `
          <div class="card-stack">
            ${jobs.map((job) => `
              <div class="job-card">
                <div class="job-card-top">
                  <div class="job-card-title">${escapeHTML(agentLabel(job.agent_name, job.agent_role) || shortId(job.id))}</div>
                  <span class="status-badge ${toneForStatus(job.status)}">${escapeHTML(humanizeStatus(job.status))}</span>
                </div>
                <div class="job-card-meta">${escapeHTML(shortId(job.id))} · ${escapeHTML(job.mode || 'exec')}</div>
              </div>
            `).join('')}
          </div>
        ` : ''}
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
    <div class="meta-chip-row" style="padding:0 15px 15px;">
      ${entries.map(([key, value]) => `<span class="surface-chip">${escapeHTML(key)}: ${escapeHTML(metadataValue(value))}</span>`).join('')}
    </div>
  `;
}

function renderPendingStageCard() {
  return `
    <section class="message assistant pending">
      <div class="pending-stage-card">
        <div class="pending-stage-mark">
          <i data-lucide="activity" style="width:20px;height:20px;color:var(--accent-strong);"></i>
        </div>
        <div class="pending-stage-body">
          <div class="tool-card-meta">
            <span class="status-badge ${state.liveActivity.tone || 'neutral'}">${escapeHTML(state.isGenerating ? 'Running' : 'Settling')}</span>
            ${state.nextSendInterrupt ? '<span class="status-badge queued">Interrupt armed</span>' : ''}
          </div>
          <div class="pending-stage-title">${escapeHTML(state.liveActivity.title)}</div>
          <div class="pending-stage-copy">${escapeHTML(state.liveActivity.copy)}</div>
          <div class="pending-stage-track">
            <span class="pending-stage-segment"></span>
            <span class="pending-stage-segment"></span>
            <span class="pending-stage-segment"></span>
          </div>
        </div>
      </div>
    </section>
  `;
}

function renderInspector() {
  nodes.inspectorTabs.forEach((tab) => {
    tab.classList.toggle('active', tab.getAttribute('data-inspector-tab') === state.activeInspectorTab);
  });

  if (!state.sessionDetail) {
    nodes.inspectorStatusBadge.className = 'status-badge neutral';
    nodes.inspectorStatusBadge.textContent = hasDurableSession() ? 'Loading' : 'Idle';
    nodes.inspectorContent.innerHTML = `
      <div class="empty-panel">
        ${hasDurableSession()
          ? 'Loading durable session detail, timeline events, tools, and child-agent state…'
          : 'No durable session selected yet. Start a prompt or pick one from recent sessions.'}
      </div>
    `;
    return;
  }

  nodes.inspectorStatusBadge.className = `status-badge ${toneForStatus(state.sessionDetail.state.status)}`;
  nodes.inspectorStatusBadge.textContent = humanizeStatus(state.sessionDetail.state.status);

  switch (state.activeInspectorTab) {
    case 'timeline':
      nodes.inspectorContent.innerHTML = renderTimelinePanel(state.sessionDetail);
      break;
    case 'agents':
      nodes.inspectorContent.innerHTML = renderAgentsPanel(state.sessionDetail);
      break;
    case 'tasks':
      nodes.inspectorContent.innerHTML = renderTasksPanel(state.sessionDetail);
      break;
    case 'summary':
    default:
      nodes.inspectorContent.innerHTML = renderSummaryPanel(state.sessionDetail);
      break;
  }
}

function renderSummaryPanel(detail) {
  const counters = summarizeCurrentSession();
  const recentTools = collectRecentToolEntries(detail.messages).slice(-6).reverse();
  const loadedSkills = maybeArray(detail.state?.loaded_skills);
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
  const workers = maybeArray(state.overview?.workers?.workers);
  return `
    <section class="panel-section">
      <div class="summary-grid wide">
        ${renderMetricCard('Child sessions', String(children.length), 'durable sessions')}
        ${renderMetricCard('Background jobs', String(jobs.length), 'queued or completed')}
        ${renderMetricCard('Notifications', String(notifications.length), 'background results')}
        ${renderMetricCard('Workers', String(state.overview?.workers?.active_count ?? 0), `${state.overview?.workers?.desired_count ?? 0} desired`)}
      </div>
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Child sessions</h4>
      </div>
      ${children.length ? `<div class="card-stack">${children.map((item) => renderChildSessionCard(item)).join('')}</div>` : '<div class="empty-panel">No child sessions yet.</div>'}
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Background queue</h4>
      </div>
      ${jobs.length ? `<div class="card-stack">${jobs.map((job) => renderQueueJobCard(job)).join('')}</div>` : '<div class="empty-panel">No background jobs yet.</div>'}
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Background notifications</h4>
      </div>
      ${notifications.length ? `<div class="card-stack">${notifications.map((note) => renderNotificationCard(note)).join('')}</div>` : '<div class="empty-panel">No background notifications yet.</div>'}
    </section>

    <section class="panel-section">
      <div class="section-title-row">
        <h4>Worker pool</h4>
      </div>
      ${workers.length ? `<div class="worker-pill-row">${workers.map((worker) => `<span class="surface-chip">Worker ${escapeHTML(String(worker.id))}: ${escapeHTML(humanizeStatus(worker.state || 'idle'))}${worker.last_job_id ? ` · ${escapeHTML(shortId(worker.last_job_id))}` : ''}</span>`).join('')}</div>` : '<div class="empty-panel">No active workers reported.</div>'}
    </section>
  `;
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
      <div class="metric-card-value" style="font-size:16px;margin-top:8px;">${escapeHTML(value)}</div>
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

function renderTimelineItem(item) {
  const descriptor = describeTimelineItem(item);
  return `
    <div class="timeline-card">
      <div class="timeline-icon ${descriptor.tone === 'danger' ? 'danger' : descriptor.tone === 'queued' ? 'warning' : ''}">
        <i data-lucide="${escapeAttr(descriptor.icon)}" style="width:16px;height:16px;"></i>
      </div>
      <div class="timeline-card-copy">
        <div class="timeline-card-top">
          <div class="timeline-card-title">${escapeHTML(descriptor.title)}</div>
          <span class="timeline-card-meta">${escapeHTML(formatTimestamp(item.time))}</span>
        </div>
        <div class="timeline-card-text">${escapeHTML(descriptor.copy)}</div>
        ${descriptor.meta ? `<div class="timeline-card-meta">${escapeHTML(descriptor.meta)}</div>` : ''}
        ${descriptor.data ? `<pre class="timeline-card-data">${escapeHTML(descriptor.data)}</pre>` : ''}
      </div>
    </div>
  `;
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
        <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.id)}">Open child session</button>
      </div>
    </div>
  `;
}

function renderQueueJobCard(job) {
  return `
    <div class="job-card">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(agentLabel(job.agent_name, job.agent_role) || shortId(job.id))}</div>
        <span class="status-badge ${toneForStatus(job.status)}">${escapeHTML(humanizeStatus(job.status))}</span>
      </div>
      <div class="job-card-copy">${escapeHTML(truncateText(job.prompt || '(no prompt)', 180))}</div>
      <div class="job-card-meta">${escapeHTML(shortId(job.id))} · ${escapeHTML(job.mode || 'exec')}${job.session_id ? ` · child ${escapeHTML(shortId(job.session_id))}` : ''}</div>
      ${renderVisiblePaths(job.visible_paths)}
      <div class="card-actions">
        ${job.session_id ? `<button class="mini-link-btn" type="button" data-open-session="${escapeAttr(job.session_id)}">Open child session</button>` : ''}
        ${job.parent_session_id ? `<button class="mini-link-btn" type="button" data-open-parent-session="${escapeAttr(job.parent_session_id)}">Open parent session</button>` : ''}
      </div>
    </div>
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

async function openSession(sessionID, options = {}) {
  adoptSession(sessionID, true);
  state.sessionDetail = null;
  state.optimisticMessages = [];
  state.nextSendInterrupt = false;
  state.liveEvents = [];
  if (options.switchToChat !== false) {
    switchView('chat');
  }
  await refreshCurrentSession();
  queueOverviewRefresh(60);
}

function collectRecentToolEntries(messages) {
  const out = [];
  maybeArray(messages).forEach((message) => {
    maybeArray(message.tool_calls).forEach((call) => {
      out.push({
        kind: 'call',
        name: call.name,
        arguments: call.arguments,
        created_at: message.created_at
      });
    });
    maybeArray(message.tool_results).forEach((result) => {
      out.push({
        kind: 'result',
        name: result.name,
        display_output: result.display_output,
        is_error: result.is_error,
        created_at: message.created_at
      });
    });
  });
  return out;
}

function describeTimelineItem(item) {
  if (item.kind === 'message') {
    if (item.role === 'tool') {
      return {
        icon: 'wrench',
        title: 'Tool results appended',
        copy: truncateText(item.text || 'Tool output recorded.', 180),
        meta: item.message_id ? shortId(item.message_id) : '',
        tone: 'live',
        data: ''
      };
    }
    return {
      icon: item.role === 'user' ? 'user' : item.role === 'system' ? 'terminal' : 'message-square',
      title: `${humanizeStatus(item.role || 'message')} message`,
      copy: truncateText(item.text || '(empty message)', 180),
      meta: item.message_id ? shortId(item.message_id) : '',
      tone: item.role === 'system' ? 'danger' : 'neutral',
      data: ''
    };
  }

  return describeEventDescriptor(item.event_type, item.data, item.phase, item.event_id);
}

function describeEventDescriptor(eventType, data, phase, eventID) {
  const toolName = data?.tool_name || data?.tool || '';
  switch (eventType) {
    case 'provider.call':
      return {
        icon: 'radio',
        title: 'Provider call started',
        copy: 'The runner is waiting on the model provider.',
        meta: phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'provider.request.prepared':
      return {
        icon: 'package-search',
        title: 'Provider request prepared',
        copy: 'The durable turn request has been assembled.',
        meta: phaseHeadline(phase),
        tone: 'neutral',
        data: ''
      };
    case 'assistant.message':
      return {
        icon: 'sparkles',
        title: 'Assistant output persisted',
        copy: truncateText(data?.text || 'Assistant text recorded.', 180),
        meta: phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'tool.before':
      return {
        icon: 'wrench',
        title: `Tool started: ${toolName || 'tool'}`,
        copy: 'The runtime is executing a tool call.',
        meta: phaseHeadline(phase),
        tone: 'live',
        data: truncateText(data?.arguments || '', 600)
      };
    case 'tool.after':
      return {
        icon: 'check-circle-2',
        title: `Tool finished: ${toolName || 'tool'}`,
        copy: truncateText(data?.display_output || 'Tool output recorded.', 180),
        meta: phaseHeadline(phase),
        tone: data?.is_error ? 'danger' : 'live',
        data: data?.metadata ? prettyJSON(data.metadata) : ''
      };
    case 'tool.blocked':
      return {
        icon: 'shield-alert',
        title: `Tool blocked: ${toolName || 'tool'}`,
        copy: data?.reason || 'The runtime guard blocked this tool call.',
        meta: phaseHeadline(phase),
        tone: 'danger',
        data: ''
      };
    case 'tool.interrupted':
      return {
        icon: 'hand',
        title: `Tool interrupted: ${toolName || 'tool'}`,
        copy: 'The tool was interrupted before it could complete.',
        meta: phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'session.child.queued':
      return {
        icon: 'git-branch-plus',
        title: 'Child job queued',
        copy: agentLabel(data?.agent_name, data?.agent_role) || 'Background child agent queued.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'session.child.spawned':
      return {
        icon: 'git-branch',
        title: 'Child session spawned',
        copy: agentLabel(data?.agent_name, data?.agent_role) || 'Child session created.',
        meta: data?.session_id ? shortId(data.session_id) : phaseHeadline(phase),
        tone: 'live',
        data: ''
      };
    case 'queue.job.claimed':
      return {
        icon: 'list-todo',
        title: 'Queue job claimed',
        copy: 'A worker picked up a queued background job.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'queue.job.completed':
    case 'queue.job.failed':
      return {
        icon: eventType === 'queue.job.failed' ? 'x-circle' : 'check-check',
        title: eventType === 'queue.job.failed' ? 'Background job failed' : 'Background job completed',
        copy: data?.agent_role ? `Role: ${data.agent_role}` : 'Background queue state changed.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: eventType === 'queue.job.failed' ? 'danger' : 'live',
        data: ''
      };
    case 'queue.job.notified':
      return {
        icon: 'inbox',
        title: 'Background result notified',
        copy: 'Parent session received a background notification payload.',
        meta: data?.job_id ? shortId(data.job_id) : phaseHeadline(phase),
        tone: 'queued',
        data: ''
      };
    case 'session.steer.requested':
    case 'session.steer.queued':
    case 'session.steer.accepted':
    case 'session.steer.deferred':
    case 'session.steer.interrupt_requested':
      return {
        icon: 'corner-down-left',
        title: humanizeEventType(eventType),
        copy: 'Steer state changed for the current session.',
        meta: phaseHeadline(phase),
        tone: eventType.includes('deferred') ? 'queued' : 'live',
        data: data ? prettyJSON(data) : ''
      };
    case 'provider.cancelled':
      return {
        icon: 'ban',
        title: 'Provider request cancelled',
        copy: data?.reason || 'The in-flight provider call was cancelled.',
        meta: phaseHeadline(phase),
        tone: 'danger',
        data: ''
      };
    default:
      return {
        icon: 'dot',
        title: humanizeEventType(eventType || 'event'),
        copy: 'Durable runtime event recorded.',
        meta: [phaseHeadline(phase), eventID ? shortId(eventID) : ''].filter(Boolean).join(' · '),
        tone: 'neutral',
        data: data ? truncateText(prettyJSON(data), 600) : ''
      };
  }
}

function updateLiveActivityFromEvent(event) {
  const descriptor = describeEventDescriptor(event.type, event.data, event.phase, event.id);
  state.liveActivity = {
    title: descriptor.title,
    copy: descriptor.copy,
    tone: descriptor.tone || 'neutral'
  };
}

function shouldRefreshAfterEvent(type) {
  return [
    'assistant.message',
    'tool.after',
    'tool.blocked',
    'tool.interrupted',
    'session.steer.accepted',
    'session.background.accepted',
    'session.child.spawned'
  ].includes(type);
}

function needsOverviewRefresh(type) {
  return type.startsWith('session.child') || type.startsWith('queue.');
}

function phaseHeadline(phase) {
  const normalized = humanizeToken(phase || 'prepare');
  return normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

function toneForStatus(status) {
  switch ((status || '').toLowerCase()) {
    case 'running':
    case 'completed':
    case 'accepted':
      return 'live';
    case 'queued':
    case 'paused':
    case 'awaiting_input':
    case 'pending':
    case 'deferred':
      return 'queued';
    case 'failed':
    case 'rejected':
    case 'cancelled':
    case 'error':
      return 'danger';
    default:
      return 'neutral';
  }
}

function humanizeStatus(status) {
  if (!status) {
    return 'Unknown';
  }
  const normalized = humanizeToken(status);
  return normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

function humanizeEventType(value) {
  return humanizeStatus(String(value || '').replaceAll('.', ' '));
}

function humanizeToken(value) {
  return String(value || '')
    .replaceAll('_', ' ')
    .replaceAll('-', ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function iconForRole(role) {
  switch (role) {
    case 'user':
      return 'user';
    case 'system':
      return 'terminal';
    case 'tool':
      return 'wrench';
    default:
      return 'sparkles';
  }
}

function actorNameForMessage(message) {
  if (message.role === 'user') {
    return message.meta?.source === 'steer' ? 'You · steer' : 'You';
  }
  if (message.role === 'system') {
    return 'System';
  }
  if (message.role === 'tool') {
    return 'Tool lane';
  }
  if (message.meta?.source === 'harness_reminder') {
    return 'Harness';
  }
  return agentLabel(state.sessionDetail?.metadata?.agent_name, state.sessionDetail?.metadata?.agent_role) || 'Agent';
}

function agentLabel(name, role) {
  if (name && role) {
    return `${name} · ${role}`;
  }
  return name || role || '';
}

function isMultiAgentTool(name) {
  return ['agent_spawn', 'agent_status', 'agent_list'].includes(name);
}

function maybeArray(value) {
  return Array.isArray(value) ? value : [];
}

function appendOptimisticMessage(role, text, meta = {}) {
  const id = `optimistic_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`;
  state.optimisticMessages.push({
    id,
    role,
    text,
    meta,
    created_at: new Date().toISOString(),
    pending: true
  });
  return id;
}

function removeOptimisticMessage(id) {
  state.optimisticMessages = state.optimisticMessages.filter((message) => message.id !== id);
  renderCurrentSession();
}

function reconcileOptimisticMessages(detail) {
  const durableTexts = new Set(
    maybeArray(detail.messages)
      .filter((item) => item.role === 'user' || item.role === 'assistant')
      .map((item) => normalizeText(item.text))
  );
  const steerTexts = new Set(
    maybeArray(detail.steer_requests)
      .map((item) => normalizeText(item.text))
  );
  state.optimisticMessages = state.optimisticMessages.filter((message) => {
    const text = normalizeText(message.text);
    return !durableTexts.has(text) && !steerTexts.has(text);
  });
}

function normalizeText(value) {
  return String(value || '')
    .trim()
    .replace(/\s+/g, ' ');
}

function safeMarkdown(text) {
  try {
    return window.marked.parse(String(text || ''));
  } catch {
    return `<p>${escapeHTML(String(text || ''))}</p>`;
  }
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

async function requestJSON(url, options = {}) {
  const response = await fetch(url, options);
  let payload = null;
  try {
    payload = await response.json();
  } catch {
    payload = null;
  }
  if (!response.ok) {
    const message = payload?.error || payload?.message || response.statusText || `Request failed: ${response.status}`;
    throw new Error(message);
  }
  return payload;
}

function showToast(message, tone = 'info') {
  const id = `toast-${++state.toastCounter}`;
  const toast = document.createElement('div');
  toast.className = `toast toast-${tone}`;
  toast.id = id;
  toast.textContent = message;
  nodes.toastRack.appendChild(toast);
  window.setTimeout(() => {
    document.getElementById(id)?.remove();
  }, 3200);
}

async function fetchHistory(page = state.historyPage) {
  if (state.refreshingHistory) {
    return;
  }
  const container = nodes.views.history;
  state.refreshingHistory = true;
  state.historyPage = Math.max(1, Number(page) || 1);
  container.innerHTML = '<div class="view-loading">Loading durable history…</div>';
  try {
    const data = await requestJSON(`/api/history?page=${encodeURIComponent(state.historyPage)}&page_size=${encodeURIComponent(state.historyPageSize)}`);
    state.historyData = data;
    renderHistory(data);
    refreshOverview().catch((err) => {
      console.error('overview refresh error', err);
    });
  } catch (err) {
    console.error('history error', err);
    container.innerHTML = '<div class="empty-panel">Failed to load recent activity.</div>';
    showToast('Failed to load recent activity.', 'error');
  } finally {
    state.refreshingHistory = false;
  }
}

function renderHistory(data) {
  const container = nodes.views.history;
  const history = data || state.historyData;
  if (!history) {
    container.innerHTML = '<div class="empty-panel">No history data available yet.</div>';
    return;
  }
  const items = maybeArray(history.items);
  const total = Number(history.total || 0);
  const page = Number(history.page || 1);
  const pageSize = Number(history.page_size || state.historyPageSize || 8);
  const totalPages = Number(history.total_pages || 0);
  const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const rangeEnd = total === 0 ? 0 : rangeStart + items.length - 1;
  container.innerHTML = `
    <div class="view-header history-header">
      <div>
        <h2 class="view-title">History</h2>
        <p class="view-subtitle">Durable sessions only. Open one to inspect or continue it.</p>
      </div>
      <div class="history-toolbar">
        <span class="surface-chip">${escapeHTML(String(total))} total</span>
        <button class="ghost-action-btn danger" type="button" data-history-clear ${total === 0 ? 'disabled' : ''}>
          <i data-lucide="trash-2"></i>
          <span>Clear history</span>
        </button>
      </div>
    </div>

    <section class="panel-card history-panel">
      <div class="panel-card-header history-panel-header">
        <div>
          <h3 class="view-title compact-title">Sessions</h3>
          <p class="view-subtitle">${total ? `${rangeStart}-${rangeEnd} of ${total}` : 'No saved sessions yet.'}</p>
        </div>
        <div class="history-pager">
          <button class="ghost-action-btn" type="button" data-history-page="prev" ${page <= 1 ? 'disabled' : ''}>
            <i data-lucide="chevron-left"></i>
            <span>Prev</span>
          </button>
          <div>
            <span class="history-page-label">Page ${escapeHTML(String(page))}${totalPages ? ` / ${escapeHTML(String(totalPages))}` : ''}</span>
          </div>
          <button class="ghost-action-btn" type="button" data-history-page="next" ${totalPages !== 0 && page >= totalPages ? 'disabled' : ''}>
            <span>Next</span>
            <i data-lucide="chevron-right"></i>
          </button>
        </div>
      </div>
      <div class="panel-card-body">
        ${items.length ? `<div class="history-session-list">${items.map((item) => renderHistorySessionCard(item)).join('')}</div>` : '<div class="empty-panel">No history yet.</div>'}
      </div>
    </section>
  `;
  lucide.createIcons();
}

function renderHistorySessionCard(item) {
  return `
    <div class="history-session-row">
      <div class="history-session-main">
        <div class="history-session-top">
          <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
          <span class="tiny-code-chip">${escapeHTML(shortId(item.id))}</span>
          <span class="history-session-time">${escapeHTML(formatTimestamp(item.updated_at || item.created_at))}</span>
        </div>
        <div class="history-session-title">${escapeHTML(agentLabel(item.agent_name, item.agent_role) || 'Master session')}</div>
        <div class="history-session-meta">${escapeHTML(item.model || item.provider || 'n/a')} · ${escapeHTML(phaseHeadline(item.phase || 'prepare'))}</div>
      </div>
      <div class="history-row-actions">
        <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.id)}">Open session</button>
        <button class="mini-link-btn danger" type="button" data-delete-session="${escapeAttr(item.id)}">Delete</button>
      </div>
    </div>
  `;
}

async function deleteHistorySession(sessionID) {
  if (!window.confirm(`Delete history for session ${sessionID}?`)) {
    return;
  }
  try {
    await requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}`, {
      method: 'DELETE'
    });
    const activeMeta = state.sessionDetail?.metadata || {};
    if (state.sessionId === sessionID || activeMeta.parent_session_id === sessionID || activeMeta.root_session_id === sessionID) {
      resetChatSession({ notifyBackend: false });
    }
    showToast('History entry deleted.', 'success');
    await fetchHistory(state.historyPage);
    if ((state.historyData?.items || []).length === 0 && state.historyPage > 1) {
      await fetchHistory(state.historyPage - 1);
    }
    refreshOverview().catch(() => {});
  } catch (err) {
    showToast(err.message || 'Failed to delete history entry.', 'error');
  }
}

async function clearHistory() {
  if (!window.confirm('Clear all session history? This will remove saved sessions and queue history.')) {
    return;
  }
  try {
    await requestJSON('/api/sessions/clear', {
      method: 'POST'
    });
    resetChatSession({ notifyBackend: false });
    state.historyData = null;
    state.historyPage = 1;
    showToast('History cleared.', 'success');
    await fetchHistory(1);
    refreshOverview().catch(() => {});
  } catch (err) {
    showToast(err.message || 'Failed to clear history.', 'error');
  }
}

function renderOverviewFeedItem(item) {
  const descriptor = item.kind === 'session_summary'
    ? {
        icon: 'message-circle',
        title: truncateText(item.text || 'Session update', 120),
        copy: `${item.event_type || 'session'} · ${item.data?.model || item.data?.agent_name || 'n/a'}`,
        tone: 'live'
      }
    : {
        icon: 'play',
        title: truncateText(item.text || 'Queue update', 120),
        copy: `${item.event_type || 'event'} · ${item.data?.model || item.data?.agent_name || 'n/a'}`,
        tone: item.event_type?.includes('failed') ? 'danger' : 'queued'
      };
  return `
    <div class="timeline-card">
      <div class="timeline-icon ${descriptor.tone === 'danger' ? 'danger' : descriptor.tone === 'queued' ? 'warning' : ''}">
        <i data-lucide="${escapeAttr(descriptor.icon)}" style="width:16px;height:16px;"></i>
      </div>
      <div class="timeline-card-copy">
        <div class="timeline-card-top">
          <div class="timeline-card-title">${escapeHTML(descriptor.title)}</div>
          <span class="timeline-card-meta">${escapeHTML(formatTimestamp(item.time))}</span>
        </div>
        <div class="timeline-card-text">${escapeHTML(descriptor.copy)}</div>
      </div>
    </div>
  `;
}

async function fetchSkills() {
  try {
    nodes.skillsGrid.innerHTML = '<div class="view-loading">Loading local skills…</div>';
    const skills = await requestJSON('/api/skills');
    state.skills = skills;
    renderSkills(skills);
  } catch (err) {
    console.error('skills error', err);
    nodes.skillsGrid.innerHTML = '<div class="empty-panel">Failed to load local skills.</div>';
    showToast('Failed to load local skills.', 'error');
  }
}

function renderSkills(skills) {
  if (!skills.length) {
    nodes.skillsGrid.innerHTML = `
      <div class="empty-panel">
        <strong>No local skills found.</strong>
        <span>Upload a .zip skill to add one to this console.</span>
      </div>
    `;
    return;
  }
  nodes.skillsGrid.innerHTML = skills.map((skill) => `
    <div class="skill-card">
      <div class="skill-icon">
        <i data-lucide="${skill.icon || 'box'}"></i>
      </div>
      <h3 class="skill-name">${escapeHTML(skill.name)}</h3>
      <p class="skill-author">by ${escapeHTML(skill.author)}</p>
      <p class="skill-desc">${escapeHTML(skill.description)}</p>
      <div class="skill-footer">
        <span style="font-size:11px; color:var(--text-muted)">${escapeHTML((skill.downloads / 1000).toFixed(1))}k downloads</span>
        <button class="skill-btn ${skill.installed ? 'uninstall' : 'install'}" onclick="handleSkillAction('${escapeAttr(skill.id)}', ${skill.installed}, this)">
          ${skill.installed ? 'Uninstall' : 'Install'}
        </button>
      </div>
    </div>
  `).join('');
  lucide.createIcons();
}

async function handleSkillAction(id, isInstalled, button) {
  if (!isInstalled) {
    showToast('Marketplace install is not supported here yet. Upload a .zip skill instead.', 'info');
    return;
  }
  button.disabled = true;
  button.innerText = 'Uninstalling...';
  try {
    await fetch(`/api/skills/${id}/uninstall`, { method: 'POST' });
    await fetchSkills();
    showToast('Skill removed from the local catalog.', 'success');
  } catch (err) {
    showToast('Failed to uninstall skill.', 'error');
    button.disabled = false;
    button.innerText = 'Uninstall';
  }
}

window.handleSkillAction = handleSkillAction;

async function renderSettings() {
  const container = nodes.views.settings;
  container.innerHTML = '<div class="view-loading">Loading backend settings…</div>';
  try {
    const configData = await requestJSON('/api/config');
    const providers = configData.providers || {};
    const defaultProvider = configData.default_provider || '';
    const options = Object.keys(providers).map((providerName) => `
      <option value="${escapeAttr(providerName)}" ${providerName === defaultProvider ? 'selected' : ''}>${escapeHTML(providerName)}</option>
    `).join('');

    container.innerHTML = `
      <div class="view-header">
        <h2 class="view-title">Settings</h2>
        <p class="view-subtitle">Configure provider defaults and local API credentials.</p>
      </div>
      <div class="skill-card" style="max-width:680px;">
        <div style="display:flex; flex-direction:column; gap:20px;">
          <div class="field">
            <label style="display:block; font-weight:600; margin-bottom:8px;">API Provider</label>
            <select id="settings-provider" class="input-container" style="width:100%; padding:10px; border:1px solid var(--border); border-radius:12px;">
              ${options}
            </select>
          </div>
          <div class="field">
            <label style="display:block; font-weight:600; margin-bottom:8px;">Base URL</label>
            <input id="settings-baseurl" type="text" style="width:100%; padding:12px; border:1px solid var(--border); border-radius:12px; background:var(--background);">
          </div>
          <div class="field">
            <label style="display:block; font-weight:600; margin-bottom:8px;">Model Name</label>
            <input id="settings-model" type="text" style="width:100%; padding:12px; border:1px solid var(--border); border-radius:12px; background:var(--background);">
          </div>
          <div class="field">
            <label style="display:block; font-weight:600; margin-bottom:8px;">API Key</label>
            <input id="settings-apikey" type="password" placeholder="Leave blank to keep existing…" style="width:100%; padding:12px; border:1px solid var(--border); border-radius:12px; background:var(--background);">
          </div>
          <button id="settings-save-btn" class="skill-btn install" style="padding:12px;">Save Changes</button>
        </div>
      </div>
    `;
    lucide.createIcons();

    const providerSelect = document.getElementById('settings-provider');
    const baseURLInput = document.getElementById('settings-baseurl');
    const modelInput = document.getElementById('settings-model');
    const apiKeyInput = document.getElementById('settings-apikey');
    const saveButton = document.getElementById('settings-save-btn');

    const syncProviderFields = () => {
      const provider = providers[providerSelect.value];
      if (!provider) {
        return;
      }
      baseURLInput.value = provider.base_url || '';
      modelInput.value = provider.model || '';
      apiKeyInput.value = provider.has_key ? '••••••••••••••••' : '';
    };

    providerSelect.addEventListener('change', syncProviderFields);
    syncProviderFields();

    saveButton.addEventListener('click', async () => {
      saveButton.innerText = 'Saving...';
      saveButton.disabled = true;
      try {
        await requestJSON('/api/config', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            provider: providerSelect.value,
            base_url: baseURLInput.value,
            model: modelInput.value,
            api_key: apiKeyInput.value
          })
        });
        showToast('Settings saved.', 'success');
        await renderSettings();
      } catch (err) {
        showToast(err.message || 'Failed to save configuration.', 'error');
      }
    });
  } catch (err) {
    container.innerHTML = '<div class="empty-panel">Failed to load backend settings.</div>';
    showToast('Failed to load backend settings.', 'error');
  }
}

async function fetchWorkspace() {
  try {
    nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
    nodes.editorFilename.innerText = 'Workspace';
    nodes.editorContent.innerText = 'Choose a file or directory to inspect.';
    const tree = await requestJSON('/api/files?path=.');
    state.fileTree = tree;
    renderFileTree(tree);
  } catch (err) {
    console.error('workspace error', err);
    nodes.fileTree.innerHTML = '<div class="empty-panel">Failed to load workspace.</div>';
    nodes.editorFilename.innerText = 'Workspace';
    nodes.editorContent.innerText = 'Failed to load workspace.';
    showToast('Failed to load workspace.', 'error');
  }
}

function renderFileTree(tree, container = nodes.fileTree, level = 0) {
  if (level === 0) {
    container.innerHTML = '';
  }
  if (!Array.isArray(tree)) {
    return;
  }
  if (level === 0 && tree.length === 0) {
    container.innerHTML = '<div class="empty-panel">This workspace is empty.</div>';
    return;
  }

  tree.forEach((node) => {
    const itemWrapper = document.createElement('div');
    const button = document.createElement('button');
    button.className = 'tree-node';
    button.style.paddingLeft = `${level * 16 + 8}px`;
    const icon = node.type === 'directory' ? 'folder' : 'file-code';
    button.innerHTML = `<i data-lucide="${icon}" style="width:14px;height:14px;"></i><span>${escapeHTML(node.name)}</span>`;
    const childrenContainer = document.createElement('div');
    if (node.type === 'directory') {
      childrenContainer.style.display = 'none';
    }

    button.onclick = async () => {
      if (node.type === 'file') {
        await loadFile(node.path);
        document.querySelectorAll('.tree-node').forEach((treeNode) => treeNode.classList.remove('active'));
        button.classList.add('active');
        return;
      }
      const hidden = childrenContainer.style.display === 'none';
      if (hidden && !node.childrenLoaded) {
        button.disabled = true;
        button.classList.add('is-loading');
        try {
          node.children = await requestJSON(`/api/files?path=${encodeURIComponent(node.path)}`);
          node.childrenLoaded = true;
          renderFileTree(node.children, childrenContainer, level + 1);
        } catch (err) {
          nodes.editorFilename.innerText = node.path;
          nodes.editorContent.innerText = 'Error loading directory.';
          showToast(`Failed to load directory: ${node.path}`, 'error');
        } finally {
          button.disabled = false;
          button.classList.remove('is-loading');
        }
      }
      childrenContainer.style.display = hidden ? 'block' : 'none';
      button.innerHTML = `<i data-lucide="${hidden ? 'folder-open' : 'folder'}" style="width:14px;height:14px;"></i><span>${escapeHTML(node.name)}</span>`;
      lucide.createIcons();
    };

    itemWrapper.appendChild(button);
    itemWrapper.appendChild(childrenContainer);
    container.appendChild(itemWrapper);
    if (node.type === 'directory' && node.children) {
      renderFileTree(node.children, childrenContainer, level + 1);
    }
  });

  if (level === 0) {
    lucide.createIcons();
  }
}

async function loadFile(path) {
  nodes.editorFilename.innerText = path;
  nodes.editorContent.innerText = 'Loading...';
  try {
    const data = await requestJSON(`/api/file/read?path=${encodeURIComponent(path)}`);
    nodes.editorContent.innerText = data.content;
  } catch (err) {
    nodes.editorContent.innerText = 'Error loading file.';
    showToast(`Failed to load file: ${path}`, 'error');
  }
}

document.addEventListener('DOMContentLoaded', init);
