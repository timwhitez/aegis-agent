/**
 * Go CLI Agent Webconsole
 */

const POLL_INTERVAL_MS = 5000;
const POLL_INTERVAL_ACTIVE_MS = 1600;
const WS_RECONNECT_BASE_MS = 1000;
const WS_RECONNECT_MAX_MS = 30000;
const MAX_LIVE_EVENTS = 80;
const UI_STATE_STORAGE_KEY = 'go-cli-agent.webconsole.ui-state.v1';
const STOP_FALLBACK_STEER_MESSAGE = 'Stop this run without finishing so a later continue can close the task. Preserve partial output and wait for continue.';
const DEFAULT_LIVE_ACTIVITY = {
  title: 'Ready for a new session',
  copy: 'Send a prompt to start a durable session. Tool activity will appear here as it runs.',
  tone: 'neutral'
};

const SHORTCUTS = {
  'escape': 'stop',
  '/': 'command',
  'ctrl+n': 'new_session',
  'ctrl+,': 'settings',
  '?': 'help'
};

const state = {
  currentView: 'chat',
  isGenerating: false,
  launchInFlight: false,
  isConnected: false,
  meta: null,
  sessionId: nextEphemeralSessionId(),
  sessionBacked: false,
  sessionDetail: null,
  overview: null,
  overviewError: '',
  historyData: null,
  historyPage: 1,
  historyPageSize: 8,
  skills: [],
  fileTree: [],
  workspacePath: '',
  optimisticMessages: [],
  liveEvents: [],
  hasMoreMessages: false,
  oldestMessageId: '',
  loadingEarlier: false,
  loadedAllEarlierMessages: false,
  messageGapAnchorId: '',
  preserveScrollAfterRender: null
};

const renderState = {
  chatCache: createEmptyChatRenderCache()
};

const runtimeHandles = {
  ws: null,
  pollHandle: null,
  pollIntervalMs: POLL_INTERVAL_MS,
  wsReconnectAttempts: 0,
  wsReconnectTimer: null,
  pendingSessionRefresh: null,
  pendingOverviewRefresh: null,
  layoutObserver: null
};

const skillsViewState = {
  requestSeq: 0,
  uploadInFlight: false
};

const overviewViewState = {
  requestSeq: 0,
  refreshing: false,
  needsRefresh: false
};

const historyViewState = {
  requestSeq: 0,
  refreshing: false,
  needsRefresh: false,
  pendingRefreshOptions: null
};

const sessionViewState = {
  refreshing: false,
  needsRefresh: false
};

const queueJobViewState = {
  selectedJobId: '',
  selectedJobDetail: null
};

const inspectorViewState = {
  tab: 'tasks'
};

const historyExpansionViewState = {
  parentIds: new Set()
};

const messagePagingViewState = {
  requestSeq: 0
};

const toastViewState = {
  counter: 0
};

const stopActionViewState = {
  sessionIds: new Set()
};

const floatingPanelViewState = {
  todo: true,
  files: true,
  subAgents: true
};

const planInputViewState = {
  selections: {}
};

const helpViewState = {
  visible: false
};

const composerViewState = {
  inputEmpty: true
};

const composerControlViewState = {
  nextSendInterrupt: false,
  mode: ''
};

const activityViewState = {
  liveActivity: { ...DEFAULT_LIVE_ACTIVITY }
};

const pageLifecycleViewState = {
  visibilityHidden: false
};

function currentLiveActivity() {
  return activityViewState.liveActivity || DEFAULT_LIVE_ACTIVITY;
}

function setLiveActivity(activity) {
  const source = activity && typeof activity === 'object' ? activity : DEFAULT_LIVE_ACTIVITY;
  activityViewState.liveActivity = {
    title: String(source.title || DEFAULT_LIVE_ACTIVITY.title),
    copy: String(source.copy || ''),
    tone: String(source.tone || 'neutral')
  };
}

function isHelpVisible() {
  return helpViewState.visible;
}

function setHelpVisible(visible) {
  helpViewState.visible = Boolean(visible);
}

function isComposerInputEmpty() {
  return composerViewState.inputEmpty;
}

function setComposerInputEmpty(empty) {
  composerViewState.inputEmpty = Boolean(empty);
}

function syncComposerInputEmpty() {
  if (nodes.chatInput) {
    setComposerInputEmpty(!nodes.chatInput.value.trim());
  }
}

function isNextSendInterruptArmed() {
  return composerControlViewState.nextSendInterrupt;
}

function setNextSendInterruptArmed(armed) {
  composerControlViewState.nextSendInterrupt = Boolean(armed);
}

function composerMode() {
  return composerControlViewState.mode || '';
}

function isGoalComposerEnabled() {
  return composerMode() === 'goal';
}

function isPlanModeComposerEnabled() {
  return composerMode() === 'plan';
}

function setComposerMode(mode) {
  composerControlViewState.mode = mode === 'goal' || mode === 'plan' ? mode : '';
}

function isPageVisibilityHidden() {
  return pageLifecycleViewState.visibilityHidden;
}

function setPageVisibilityHidden(hidden) {
  pageLifecycleViewState.visibilityHidden = Boolean(hidden);
}

function syncPageVisibilityHidden() {
  if (typeof document !== 'undefined') {
    setPageVisibilityHidden(document.visibilityState === 'hidden');
  }
}

function isFloatingPanelExpanded(panel) {
  return floatingPanelViewState[panel] !== false;
}

function setFloatingPanelExpanded(panel, expanded) {
  if (Object.prototype.hasOwnProperty.call(floatingPanelViewState, panel)) {
    floatingPanelViewState[panel] = expanded !== false;
  }
}

function isSkillUploadInFlight() {
  return skillsViewState.uploadInFlight;
}

function setSkillUploadInFlight(inFlight) {
  skillsViewState.uploadInFlight = Boolean(inFlight);
}

function selectedQueueJobId() {
  return queueJobViewState.selectedJobId || '';
}

function selectedQueueJobDetail() {
  return queueJobViewState.selectedJobDetail || null;
}

function setSelectedQueueJob(id, detail = null) {
  queueJobViewState.selectedJobId = String(id || '');
  queueJobViewState.selectedJobDetail = detail || null;
}

function setSelectedQueueJobDetail(detail) {
  queueJobViewState.selectedJobDetail = detail || null;
}

function activeInspectorTab() {
  return inspectorViewState.tab || 'tasks';
}

function setInspectorTab(tab) {
  inspectorViewState.tab = String(tab || 'tasks');
}

function createEmptyChatRenderCache() {
  return {
    activity: '',
    flow: '',
    body: '',
    pending: '',
    rail: '',
    inspector: '',
    todoFloat: ''
  };
}

function resetChatRenderCache() {
  renderState.chatCache = createEmptyChatRenderCache();
}

function chatRenderCacheValue(key) {
  return renderState.chatCache[key] || '';
}

function updateChatRenderCache(key, markup) {
  renderState.chatCache[key] = markup || '';
}

function invalidateChatRenderSlot(key) {
  if (Object.prototype.hasOwnProperty.call(renderState.chatCache, key)) {
    renderState.chatCache[key] = '';
  }
}

const nodes = {
  chatInput: document.getElementById('chat-input'),
  sendBtn: document.getElementById('send-btn'),
  chatMessages: document.getElementById('chat-messages'),
  chatContainer: document.getElementById('chat-container'),
  connectionDot: document.getElementById('connection-dot'),
  connectionStatus: document.getElementById('connection-status'),
  sessionIdDisplay: document.getElementById('session-id-display'),
  sessionRail: document.getElementById('session-rail'),
  inspectorPanel: document.getElementById('inspector-panel'),
  inspectorToggleBtn: document.getElementById('inspector-toggle-btn'),
  inspectorSlideOut: document.getElementById('inspector-slide-out'),
  inspectorBackdrop: document.getElementById('inspector-backdrop'),
  newSessionBtn: document.getElementById('new-session-btn'),
  stopSessionBtn: document.getElementById('stop-session-btn'),
  interruptSessionBtn: document.getElementById('interrupt-session-btn'),
  interruptToggleBtn: document.getElementById('interrupt-toggle-btn'),
  inputArea: document.querySelector('.input-area'),
  todoFloatPanel: document.getElementById('todo-float-panel'),
  inputContainer: document.getElementById('input-container'),
  goalToggleBtn: document.getElementById('goal-toggle-btn'),
  planToggleBtn: document.getElementById('plan-toggle-btn'),
  goalComposerPanel: document.getElementById('goal-composer-panel'),
  agentNameInput: document.getElementById('agent-name-input'),
  agentRoleSelect: document.getElementById('agent-role-select'),
  inputStatusText: document.getElementById('input-status-text'),
  toastRack: document.getElementById('toast-rack'),
  skillUploadBtn: document.getElementById('skill-upload-btn'),
  skillUpload: document.getElementById('skill-upload'),
  skillsGrid: document.getElementById('skills-grid'),
  fileTree: document.getElementById('file-tree'),
  workspaceSubtitle: document.getElementById('workspace-subtitle'),
  workspaceRootChip: document.getElementById('workspace-root-chip'),
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

async function init() {
  restoreUIState();
  if (window.lucide && lucide.createIcons) {
    lucide.createIcons();
  }
  setupVisibilityHandler();
  setupWebSocket();
  setupEventListeners();
  setupLayoutObservers();
  if (hasDurableSession()) {
    state.sessionDetail = null;
    state.optimisticMessages = [];
    state.liveEvents = [];
    setNextSendInterruptArmed(false);
    state.isGenerating = false;
    setLiveActivity({
      title: 'Restoring session',
      copy: 'Loading the previously selected durable session.',
      tone: 'neutral'
    });
    updateSessionId();
  } else {
    resetChatSession();
  }
  startPolling();
  refreshMeta().catch(() => {});
  refreshOverview();
  if (hasDurableSession()) {
    try {
      await refreshCurrentSession({ surfaceError: true });
    } catch (err) {
      console.error('session restore error', err);
      setLiveActivity({
        title: 'Error restoring session',
        copy: err.message || 'The session data could not be loaded.',
        tone: 'danger'
      });
    }
  }
  switchView(state.currentView, { skipPersist: true });
  renderCurrentSession();

  if (state.currentView === 'chat' && nodes.chatInput) {
    syncComposerInputEmpty();
    nodes.chatInput.focus();
  }
}

function nextEphemeralSessionId() {
  return '0x' + Math.random().toString(16).slice(2, 8).toUpperCase();
}

function isEphemeralSessionId(sessionID) {
  return /^0x[0-9a-f]+$/i.test(String(sessionID || '').trim());
}

function hasDurableSession() {
  return state.sessionBacked && Boolean(state.sessionId);
}

function currentClientSessionId() {
  return state.sessionId;
}

function shouldSubmitChatInput(event) {
  return event.key === 'Enter' &&
    !event.isComposing &&
    !event.shiftKey &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.altKey;
}

function shouldInsertChatNewline(event) {
  return event.key === 'Enter' &&
    !event.isComposing &&
    event.ctrlKey &&
    !event.altKey;
}

function insertChatInputNewline(textarea) {
  if (!textarea) {
    return;
  }
  const start = textarea.selectionStart ?? textarea.value.length;
  const end = textarea.selectionEnd ?? textarea.value.length;
  textarea.setRangeText('\n', start, end, 'end');
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
}

function isLiveWebSocket(ws) {
  return typeof WebSocket !== 'undefined' &&
    ws &&
    (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN);
}

function clearPendingRefreshes() {
  if (runtimeHandles.pendingSessionRefresh) {
    window.clearTimeout(runtimeHandles.pendingSessionRefresh);
    runtimeHandles.pendingSessionRefresh = null;
  }
  if (runtimeHandles.pendingOverviewRefresh) {
    window.clearTimeout(runtimeHandles.pendingOverviewRefresh);
    runtimeHandles.pendingOverviewRefresh = null;
  }
}

function setupWebSocket() {
  if (isPageVisibilityHidden() || typeof WebSocket === 'undefined') {
    return;
  }
  if (isLiveWebSocket(runtimeHandles.ws)) {
    return;
  }
  if (runtimeHandles.wsReconnectTimer) {
    window.clearTimeout(runtimeHandles.wsReconnectTimer);
    runtimeHandles.wsReconnectTimer = null;
  }
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${window.location.host}/ws`;
  const ws = new WebSocket(wsUrl);
  runtimeHandles.ws = ws;

  ws.onopen = () => {
    if (runtimeHandles.ws !== ws) {
      ws.close();
      return;
    }
    state.isConnected = true;
    runtimeHandles.wsReconnectAttempts = 0;
    // WS is currently a health/relay channel; keep REST polling only while a run needs snapshots.
    clearPendingRefreshes();
    updateConnectionStatus();
    updateUI();
    queueOverviewRefresh(120);
    if (hasDurableSession()) {
      queueSessionRefresh(120);
    }
    syncPollingForState();
  };

  ws.onmessage = (event) => {
    if (runtimeHandles.ws !== ws) {
      return;
    }
    try {
      const data = JSON.parse(event.data);
      handleServerEvent(data);
    } catch (err) {
      console.warn('ignored malformed websocket payload', err);
      showToast('Ignored malformed websocket payload from local server.', 'error');
    }
  };

  ws.onerror = () => {
    if (runtimeHandles.ws !== ws) {
      return;
    }
    // onclose will fire after onerror; let it own the reconnect logic.
  };

  ws.onclose = () => {
    if (runtimeHandles.ws !== ws) {
      return;
    }
    state.isConnected = false;
    runtimeHandles.ws = null;
    updateConnectionStatus();
    if (state.isGenerating) {
      setLiveActivity({
        title: 'Disconnected from the local agent',
        copy: 'The webconsole will retry automatically. Durable session data remains on disk.',
        tone: 'danger'
      });
    }
    updateUI();
    renderCurrentSession();
    // Fallback poller covers the disconnected window.
    syncPollingForState();
    scheduleWebSocketReconnect();
  };
}

function scheduleWebSocketReconnect() {
  if (isPageVisibilityHidden() || runtimeHandles.wsReconnectTimer || isLiveWebSocket(runtimeHandles.ws)) {
    return;
  }
  const attempt = runtimeHandles.wsReconnectAttempts++;
  const base = Math.min(WS_RECONNECT_BASE_MS * Math.pow(2, attempt), WS_RECONNECT_MAX_MS);
  const jitter = base * 0.2 * (Math.random() * 2 - 1);
  const delay = Math.max(WS_RECONNECT_BASE_MS, Math.round(base + jitter));
  runtimeHandles.wsReconnectTimer = window.setTimeout(() => {
    runtimeHandles.wsReconnectTimer = null;
    if (isPageVisibilityHidden()) {
      // Visibility handler reconnects immediately when the tab becomes active again.
      return;
    }
    setupWebSocket();
  }, delay);
}

function setupVisibilityHandler() {
  if (typeof document === 'undefined' || !document.addEventListener) {
    return;
  }
  syncPageVisibilityHidden();
  document.addEventListener('visibilitychange', () => {
    syncPageVisibilityHidden();
    if (isPageVisibilityHidden()) {
      stopPolling();
      clearPendingRefreshes();
      if (runtimeHandles.wsReconnectTimer) {
        window.clearTimeout(runtimeHandles.wsReconnectTimer);
        runtimeHandles.wsReconnectTimer = null;
      }
      return;
    }
    syncPollingForState();
    if (!state.isConnected) {
      // Tab visible again: restart fallback polling and try a faster reconnect.
      if (runtimeHandles.wsReconnectTimer) {
        window.clearTimeout(runtimeHandles.wsReconnectTimer);
        runtimeHandles.wsReconnectTimer = null;
      }
      setupWebSocket();
    }
  });
}

function handleServerEvent(data) {
  switch (data.type) {
    case 'session': {
      const clientSessionId = data.payload?.clientSessionId;
      if (clientSessionId && clientSessionId !== currentClientSessionId()) {
        return;
      }
      if (data.payload?.sessionId) {
        const durable = !isEphemeralSessionId(data.payload.sessionId);
        adoptSession(data.payload.sessionId, durable);
        if (!durable) {
          break;
        }
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
        setGenerating(false, sessionActivityForState({
          status: data.payload?.status || 'idle',
          phase: data.payload?.phase || '',
          pause_reason: data.payload?.pauseReason || data.payload?.pause_reason || '',
          last_error: '',
          last_assistant_excerpt: ''
        }));
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
  nodes.stopSessionBtn?.addEventListener('click', requestStop);
  nodes.interruptSessionBtn?.addEventListener('click', requestInterrupt);
  nodes.interruptToggleBtn?.addEventListener('click', toggleInterruptArm);
  nodes.goalToggleBtn?.addEventListener('click', toggleGoalMode);
  nodes.planToggleBtn?.addEventListener('click', togglePlanMode);
  nodes.skillUploadBtn?.addEventListener('click', () => openSkillUploadPicker());
  nodes.newSessionBtn?.addEventListener('click', () => {
    const wasGenerating = state.isGenerating;
    resetChatSession();
    showToast(
      wasGenerating
        ? 'Started a new session. The previous run may still settle in the background.'
        : 'Started a new session.',
      'info'
    );
  });

  nodes.inspectorToggleBtn?.addEventListener('click', toggleInspectorSlideOut);
  nodes.inspectorBackdrop?.addEventListener('click', closeInspectorSlideOut);

  nodes.chatInput.addEventListener('keydown', (event) => {
    if (shouldInsertChatNewline(event)) {
      event.preventDefault();
      event.stopPropagation();
      insertChatInputNewline(nodes.chatInput);
      return;
    }
    if (shouldSubmitChatInput(event)) {
      event.preventDefault();
      event.stopPropagation();
      sendMessage();
    }
  });

  let lastScrollHeight = 0;
  nodes.chatInput.addEventListener('input', function onInput() {
    this.style.height = 'auto';
    if (this.scrollHeight !== lastScrollHeight) {
      this.style.height = `${this.scrollHeight}px`;
      lastScrollHeight = this.scrollHeight;
      updateDynamicLayoutMetrics();
    } else {
      this.style.height = `${lastScrollHeight}px`;
    }

    // Only update UI if we really need to (e.g. for empty vs non-empty state)
    const isNowEmpty = !this.value.trim();
    const wasEmpty = isComposerInputEmpty();
    if (isNowEmpty !== wasEmpty || isNextSendInterruptArmed()) {
      setComposerInputEmpty(isNowEmpty);
      updateUI();
    }
  });

  let scrollThrottle = null;
  nodes.chatContainer.addEventListener('scroll', function onScroll() {
    if (scrollThrottle) {
      return;
    }
    scrollThrottle = setTimeout(() => {
      scrollThrottle = null;
    }, 16);

    updateDynamicLayoutMetrics();
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

    const loadEarlierButton = event.target.closest('[data-load-earlier]');
    if (loadEarlierButton) {
      await loadEarlierMessages();
      return;
    }

    const viewShortcut = event.target.closest('[data-view-shortcut]');
    if (viewShortcut) {
      switchView(viewShortcut.getAttribute('data-view-shortcut'));
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

    const stopInlineSessionButton = event.target.closest('[data-stop-session-id]');
    if (stopInlineSessionButton) {
      event.preventDefault();
      event.stopPropagation();
      const sessionID = stopInlineSessionButton.getAttribute('data-stop-session-id');
      if (sessionID) {
        await requestStopSession(sessionID, { button: stopInlineSessionButton });
      }
      return;
    }

    const historyExpandToggle = event.target.closest('[data-history-toggle-children]');
    if (historyExpandToggle) {
      const parentID = historyExpandToggle.getAttribute('data-history-toggle-children');
      if (parentID) {
        if (historyExpansionViewState.parentIds.has(parentID)) {
          historyExpansionViewState.parentIds.delete(parentID);
        } else {
          historyExpansionViewState.parentIds.add(parentID);
        }
        renderHistory();
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

    const subAgentOpenButton = event.target.closest('[data-sub-agent-open]');
    if (subAgentOpenButton) {
      const sessionID = subAgentOpenButton.getAttribute('data-sub-agent-open');
      if (sessionID) {
        const label = (subAgentOpenButton.querySelector('.sa-tree-label')?.textContent || '').trim();
        const hint = label ? `Open child session "${label}"?` : 'Open child session?';
        if (await confirmLocalAction({
          title: 'Open child session',
          message: hint,
          confirmLabel: 'Open'
        })) {
          await openSession(sessionID, { switchToChat: true });
        }
      }
      return;
    }

    const inspectorTab = event.target.closest('[data-inspector-tab], [data-focus-inspector-tab]');
    if (inspectorTab) {
      setInspectorTab(inspectorTab.getAttribute('data-inspector-tab') || inspectorTab.getAttribute('data-focus-inspector-tab') || 'tasks');
      renderCurrentSession();
      return;
    }

    const queueJobButton = event.target.closest('[data-open-job]');
    if (queueJobButton) {
      setSelectedQueueJob(queueJobButton.getAttribute('data-open-job') || '');
      setInspectorTab('agents');
      await refreshSelectedQueueJobDetail();
      renderCurrentSession();
      return;
    }

    const continueBtn = event.target.closest('[data-continue-session]');
    if (continueBtn) {
      const sessionID = continueBtn.getAttribute('data-continue-session');
      if (sessionID) {
        continueBtn.disabled = true;
        try {
          await requestContinueSession(sessionID);
        } finally {
          if (document.body.contains(continueBtn)) {
            continueBtn.disabled = false;
          }
        }
      }
      return;
    }

    const skillActionBtn = event.target.closest('[data-skill-action]');
    if (skillActionBtn) {
      const id = skillActionBtn.getAttribute('data-skill-action');
      const isInstalled = skillActionBtn.getAttribute('data-skill-installed') === '1';
      handleSkillAction(id, isInstalled, skillActionBtn);
      return;
    }

    const goalAction = event.target.closest('[data-goal-action]');
    if (goalAction) {
      await handleGoalAction(goalAction);
      return;
    }

    const planAction = event.target.closest('[data-plan-action]');
    if (planAction) {
      await handlePlanModeAction(planAction);
      return;
    }

    const planInputAction = event.target.closest('[data-plan-input-action]');
    if (planInputAction) {
      await handlePlanInputAction(planInputAction);
      return;
    }

    const todoFloatToggle = event.target.closest('[data-todo-float-toggle]');
    if (todoFloatToggle) {
      setFloatingPanelExpanded('todo', !isFloatingPanelExpanded('todo'));
      persistUIState();
      invalidateChatRenderSlot('todoFloat');
      renderCurrentSession();
      return;
    }

    const filesFloatToggle = event.target.closest('[data-files-float-toggle]');
    if (filesFloatToggle) {
      setFloatingPanelExpanded('files', !isFloatingPanelExpanded('files'));
      persistUIState();
      invalidateChatRenderSlot('todoFloat');
      renderCurrentSession();
      return;
    }

    const subAgentToggle = event.target.closest('[data-sub-agent-toggle]');
    if (subAgentToggle) {
      setFloatingPanelExpanded('subAgents', !isFloatingPanelExpanded('subAgents'));
      persistUIState();
      invalidateChatRenderSlot('todoFloat');
      renderCurrentSession();
      return;
    }

  });

  document.addEventListener('change', handleSkillUploadChange);

  document.addEventListener('keydown', (event) => {
    const isInput = ['INPUT', 'TEXTAREA'].includes(event.target.tagName);
    
    // Always handle Escape even if we are in an input field (to stop generating)
    if (event.key === 'Escape' || event.key === 'Esc') {
      if (state.isGenerating && hasDurableSession()) {
        event.preventDefault();
        requestStop();
      }
      return;
    }
    
    if (isInput) {
      return;
    }

    const parts = [];
    if (event.ctrlKey) parts.push('ctrl');
    if (event.altKey) parts.push('alt');
    if (event.shiftKey) parts.push('shift');
    parts.push(event.key.toLowerCase());
    const shortcut = parts.join('+');

    const action = SHORTCUTS[shortcut];
    if (!action) return;

    event.preventDefault();

    switch (action) {
      case 'stop':
        if (state.isGenerating && hasDurableSession()) {
          requestStop();
        }
        break;
      case 'command':
        if (nodes.chatInput) {
          nodes.chatInput.focus();
          if (!nodes.chatInput.value.startsWith('/')) {
            nodes.chatInput.value = '/' + nodes.chatInput.value;
            nodes.chatInput.dispatchEvent(new Event('input'));
          }
        }
        break;
      case 'new_session': {
        const wasGenerating = state.isGenerating;
        resetChatSession();
        showToast(
          wasGenerating
            ? 'Started a new session. The previous run may still settle in the background.'
            : 'Started a new session.',
          'info'
        );
        break;
      }
      case 'settings':
        switchView('settings');
        break;
      case 'help':
        setHelpVisible(!isHelpVisible());
        renderShortcutHelp();
        break;
    }
  });
}

function setupLayoutObservers() {
  updateDynamicLayoutMetrics();
  window.addEventListener('resize', updateDynamicLayoutMetrics);
  if (window.ResizeObserver && nodes.inputArea) {
    runtimeHandles.layoutObserver = new ResizeObserver(updateDynamicLayoutMetrics);
    runtimeHandles.layoutObserver.observe(nodes.inputArea);
    if (nodes.chatInput) {
      runtimeHandles.layoutObserver.observe(nodes.chatInput);
    }
  }
}

function updateDynamicLayoutMetrics() {
  const inputHeight = Math.ceil(nodes.inputArea?.getBoundingClientRect?.().height || 132);
  const clearance = Math.max(112, inputHeight + 28);
  document.documentElement.style.setProperty('--chat-input-clearance', `${clearance}px`);
  document.documentElement.style.setProperty('--toast-bottom-clearance', `${clearance + 10}px`);
}

const VIEW_TITLES = {
  chat: 'Session — Agent Console',
  history: 'Sessions — Agent Console',
  skills: 'Skills — Agent Console',
  workspace: 'Workspace — Agent Console',
  settings: 'Settings — Agent Console'
};

function applyViewVisibility(viewName) {
  if (!nodes.views[viewName]) {
    return;
  }
  Object.values(nodes.views).forEach((view) => {
    view.classList.add('is-hidden');
  });
  nodes.navItems.forEach((item) => item.classList.remove('active'));
  nodes.views[viewName].classList.remove('is-hidden');
  const activeNav = Array.from(nodes.navItems).find((item) => item.getAttribute('data-view') === viewName);
  if (activeNav) {
    activeNav.classList.add('active');
  }
  state.currentView = viewName;
  document.title = VIEW_TITLES[viewName] || 'Agent Console';
}

function switchView(viewName, options = {}) {
  if (!nodes.views[viewName]) {
    return;
  }
  applyViewVisibility(viewName);
  if (!options.skipPersist) {
    persistUIState();
  }

  if (viewName === 'chat') {
    renderCurrentSession();
    if (nodes.chatInput) {
      syncComposerInputEmpty();
      nodes.chatInput.focus();
    }
    if (shouldPollCurrentSession()) {
      queueSessionRefresh(60);
    }
    if (shouldPollChatOverview()) {
      queueOverviewRefresh(60);
    }
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
  syncPollingForState();
}

async function sendMessage() {
  const text = nodes.chatInput.value.trim();
  if (!text) {
    return;
  }
  if (state.launchInFlight && !hasDurableSession()) {
    showToast('Session launch is already in progress.', 'info');
    return;
  }

  const optimisticID = appendOptimisticMessage('user', text, {
    source: state.isGenerating ? 'steer' : 'user',
    interrupt: isNextSendInterruptArmed() && state.isGenerating && hasDurableSession()
  });

  nodes.chatInput.value = '';
  nodes.chatInput.style.height = 'auto';
  setComposerInputEmpty(true);
  updateUI();
  renderCurrentSession();

  if (state.isGenerating && hasDurableSession()) {
    const sessionID = state.sessionId;
    const requestedInterrupt = isNextSendInterruptArmed();
    try {
      await steerSession(sessionID, {
        message: text,
        interrupt: requestedInterrupt
      });
      if (state.sessionId !== sessionID) {
        return;
      }
      setNextSendInterruptArmed(false);
      setLiveActivity({
        title: requestedInterrupt ? 'Interrupt steer requested' : 'Steer queued',
        copy: requestedInterrupt
          ? 'The runtime will interrupt at the nearest safe boundary and merge your new instruction.'
          : 'Your follow-up was queued for the current run without starting a new session.',
        tone: requestedInterrupt ? 'queued' : 'live'
      });
      showToast(requestedInterrupt ? 'Interrupt steer sent.' : 'Steer queued for the running session.', 'success');
      queueSessionRefresh(120);
      queueOverviewRefresh(220);
    } catch (err) {
      if (state.sessionId === sessionID) {
        removeOptimisticMessage(optimisticID);
        showToast(err.message || 'Failed to queue steer input.', 'error');
      }
    }
    if (state.sessionId === sessionID) {
      updateUI();
      renderCurrentSession();
    }
    return;
  }

  const currentStatus = state.sessionDetail?.state?.status || '';
  if (hasDurableSession() && ['awaiting_input', 'paused', 'failed'].includes(currentStatus)) {
    const sessionID = state.sessionId;
    try {
      const planMode = currentPlanMode();
      if (planMode?.status === 'awaiting_approval') {
        await revisePlanMode(sessionID, text);
        if (state.sessionId !== sessionID) {
          return;
        }
        showToast('Plan revision sent.', 'success');
      } else {
        const planDraft = collectPlanModeDraft(text);
        if (planDraft?.error) {
          removeOptimisticMessage(optimisticID);
          showToast(planDraft.error, 'error');
          updateUI();
          renderCurrentSession();
          return;
        }
        setGenerating(true, {
          title: 'Continuing session',
          copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
          tone: 'live'
        });
        await continueSession(sessionID, {
          message: text,
          planMode: planDraft || undefined
        });
        if (state.sessionId !== sessionID) {
          return;
        }
        if (planDraft) {
          setComposerMode(null);
        }
        showToast(planDraft ? 'Plan Mode started.' : 'Session continued.', 'success');
      }
      setGenerating(true, {
        title: 'Continuing session',
        copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
        tone: 'live'
      });
      queueSessionRefresh(60);
      queueOverviewRefresh(220);
    } catch (err) {
      if (state.sessionId === sessionID) {
        removeOptimisticMessage(optimisticID);
        state.isGenerating = false;
        showToast(err.message || 'Failed to continue session.', 'error');
        updateUI();
        renderCurrentSession();
      }
    }
    return;
  }

  if (!hasDurableSession() || currentStatus === 'completed') {
    const goalDraft = collectGoalDraft(text);
    if (goalDraft?.error) {
      removeOptimisticMessage(optimisticID);
      showToast(goalDraft.error, 'error');
      updateUI();
      renderCurrentSession();
      return;
    }
    const planDraft = collectPlanModeDraft(text);
    if (planDraft?.error) {
      removeOptimisticMessage(optimisticID);
      showToast(planDraft.error, 'error');
      updateUI();
      renderCurrentSession();
      return;
    }
    const agentDraft = collectAgentDraft();
    const launchClientSessionID = state.sessionId;
    try {
      state.launchInFlight = true;
      setGenerating(true, {
        title: 'Launching session',
        copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
        tone: 'live'
      });
      queueOverviewRefresh(220);
      const resp = await startSession({
        prompt: text,
        agentName: agentDraft.agentName || undefined,
        agentRole: agentDraft.agentRole || undefined,
        workdir: selectedWorkspaceWorkdir(),
        goal: goalDraft || undefined,
        planMode: planDraft || undefined
      });
      if (state.sessionId !== launchClientSessionID || state.sessionBacked) {
        return;
      }
      setComposerMode(null);
      adoptSession(resp.session_id, true);
      state.launchInFlight = false;
      setGenerating(true, {
        title: 'Launching session',
        copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
        tone: 'live'
      });
      queueSessionRefresh(60);
      queueOverviewRefresh(220);
    } catch (err) {
      if (state.sessionId !== launchClientSessionID || state.sessionBacked) {
        return;
      }
      removeOptimisticMessage(optimisticID);
      state.isGenerating = false;
      state.launchInFlight = false;
      showToast(err.message || 'Failed to start session.', 'error');
      updateUI();
      renderCurrentSession();
    }
    return;
  }

  removeOptimisticMessage(optimisticID);
  showToast('This session is not ready to continue. Refresh the session state and try again.', 'error');
  updateUI();
  renderCurrentSession();
}

function toggleInterruptArm() {
  if (!state.isGenerating || !hasDurableSession()) {
    return;
  }
  setNextSendInterruptArmed(!isNextSendInterruptArmed());
  updateUI();
}

async function requestInterrupt() {
  if (!state.isGenerating || !hasDurableSession()) {
    showToast('No running session is available for interrupt.', 'info');
    return;
  }
  const sessionID = state.sessionId;
  try {
    await interruptSession(sessionID);
    if (state.sessionId !== sessionID) {
      return;
    }
    setLiveActivity({
      title: 'Interrupt requested',
      copy: 'The runner will stop at the nearest safe boundary and surface the session state.',
      tone: 'queued'
    });
    showToast('Interrupt requested.', 'success');
    queueSessionRefresh(120);
  } catch (err) {
    if (state.sessionId === sessionID) {
      showToast(err.message || 'Failed to request interrupt.', 'error');
    }
  }
  if (state.sessionId === sessionID) {
    renderCurrentSession();
  }
}

async function requestStop() {
  if (!state.isGenerating || !hasDurableSession()) {
    showToast('No running session is available to stop.', 'info');
    return;
  }
  await requestStopSession(state.sessionId);
}

async function requestStopSession(sessionID, options = {}) {
  if (!sessionID) {
    showToast('No session is available to stop.', 'info');
    return;
  }
  if (isStoppingSession(sessionID)) {
    return;
  }
  const selectedSessionID = state.sessionId || '';
  const refreshSelectedSession = state.currentView === 'chat' &&
    selectedSessionID &&
    selectedSessionID !== sessionID &&
    currentSessionReferencesSession(sessionID);
  const selectedContextStillCurrent = () => state.sessionId === selectedSessionID && hasDurableSession();
  stopActionViewState.sessionIds.add(sessionID);
  const button = options.button || null;
  if (button) {
    button.disabled = true;
  }
  try {
    const result = await requestStopViaBestAvailablePath(sessionID);
    if (state.sessionId === sessionID) {
      setLiveActivity({
        title: 'Stopping run',
        copy: result.via === 'steer'
          ? 'This run is being stopped through an interrupt steer request because no local handle is owned by this page.'
          : 'The current run is being stopped. Partial output and tool results will remain visible.',
        tone: 'danger'
      });
      showToast(result.via === 'steer' ? 'Stop requested through interrupt steer.' : 'Stop requested.', 'success');
      queueSessionRefresh(120);
      queueOverviewRefresh(180);
    } else if (refreshSelectedSession && selectedContextStillCurrent()) {
      showToast(result.via === 'steer' ? 'Stop requested through interrupt steer.' : 'Stop requested.', 'success');
      queueSessionRefresh(120);
      queueOverviewRefresh(180);
    }
    if (state.currentView === 'history') {
      await fetchHistory(state.historyPage, { showLoading: false, silentError: true });
    }
  } catch (err) {
    if (state.sessionId === sessionID) {
      showToast(err.message || 'Failed to stop the session.', 'error');
    } else if (refreshSelectedSession && selectedContextStillCurrent()) {
      showToast(err.message || 'Failed to stop the session.', 'error');
    }
  } finally {
    stopActionViewState.sessionIds.delete(sessionID);
    if (button && document.body.contains(button)) {
      button.disabled = false;
    }
    if (state.currentView === 'history') {
      renderHistory();
    }
    if (state.sessionId === sessionID || (refreshSelectedSession && selectedContextStillCurrent())) {
      renderCurrentSession();
    }
  }
}

async function requestStopViaBestAvailablePath(sessionID) {
  try {
    await stopSession(sessionID);
    return { via: 'handle' };
  } catch (err) {
    if (err?.code !== 'ACTIVE_HANDLE_NOT_OWNED') {
      throw err;
    }
    await steerSession(sessionID, {
      message: STOP_FALLBACK_STEER_MESSAGE,
      interrupt: true
    });
    return { via: 'steer' };
  }
}

async function requestContinueSession(sessionID, message = '', options = {}) {
  const isStillSelected = () => state.sessionId === sessionID;
  try {
    await continueSession(sessionID, {
      message,
      planMode: options.planMode
    });
    if (!isStillSelected()) {
      return;
    }
    if (!options.silentToast) {
      showToast('Session continued.', 'success');
    }
    queueSessionRefresh(120);
    queueOverviewRefresh(180);
  } catch (err) {
    if (!options.silentToast && isStillSelected()) {
      showToast(err.message || 'Failed to continue session.', 'error');
    }
    if (options.silentToast) {
      throw err;
    }
  }
}

function isCompactLayout() {
  return window.innerWidth < 1100;
}

function toggleInspectorSlideOut() {
  const slideOut = nodes.inspectorSlideOut;
  const backdrop = nodes.inspectorBackdrop;
  if (!slideOut || !backdrop) return;
  const isOpen = slideOut.classList.contains('is-open');
  if (isOpen) {
    closeInspectorSlideOut();
  } else {
    slideOut.classList.add('is-open');
    backdrop.classList.add('is-open');
  }
}

function closeInspectorSlideOut() {
  nodes.inspectorSlideOut?.classList.remove('is-open');
  nodes.inspectorBackdrop?.classList.remove('is-open');
}

function adoptSession(sessionID, backed) {
  if (!sessionID) {
    return;
  }
  if (sessionID !== state.sessionId) {
    setSelectedQueueJob('');
    clearPlanInputSelections();
    state.hasMoreMessages = false;
    state.oldestMessageId = '';
    state.loadingEarlier = false;
    state.loadedAllEarlierMessages = false;
    state.messageGapAnchorId = '';
    state.preserveScrollAfterRender = null;
    if (typeof clearMarkdownCache === 'function') {
      clearMarkdownCache();
    }
  }
  state.sessionId = sessionID;
  state.sessionBacked = backed;
  if (backed) {
    state.launchInFlight = false;
  }
  updateSessionId();
  persistUIState();
}

function resetChatSession() {
  state.sessionId = nextEphemeralSessionId();
  state.sessionBacked = false;
  state.sessionDetail = null;
  state.optimisticMessages = [];
  state.liveEvents = [];
  setNextSendInterruptArmed(false);
  setSelectedQueueJob('');
  clearPlanInputSelections();
  setComposerMode(null);
  state.hasMoreMessages = false;
  state.oldestMessageId = '';
  state.loadingEarlier = false;
  state.loadedAllEarlierMessages = false;
  state.messageGapAnchorId = '';
  state.preserveScrollAfterRender = null;
  if (typeof clearMarkdownCache === 'function') {
    clearMarkdownCache();
  }
  setLiveActivity({
    title: 'Ready for a new session',
    copy: 'Send a prompt to create a durable session. Answers, tool calls, and running flow will appear here.',
    tone: 'neutral'
  });
  state.isGenerating = false;
  state.launchInFlight = false;
  syncComposerInputEmpty();
  updateSessionId();
  persistUIState();
  renderCurrentSession();
  updateUI();
  syncPollingForState();
}

function setGenerating(value, activity) {
  state.isGenerating = value;
  if (!value) {
    setNextSendInterruptArmed(false);
  }
  if (activity) {
    setLiveActivity(activity);
  }
  updateUI();
  renderCurrentSession();
  syncPollingForState();
}

function updateConnectionStatus() {
  if (!nodes.connectionStatus) {
    return;
  }
  if (!state.isConnected) {
    nodes.connectionStatus.innerText = 'Disconnected';
    return;
  }
  nodes.connectionStatus.innerText = 'Agent Connected';
}

function updateSessionId() {
  const detail = state.sessionDetail;
  if (detail?.metadata) {
    nodes.sessionIdDisplay.innerText = `${humanizeStatus(detail.state?.status || 'loaded')} · ${detail.metadata.id} · ${detail.metadata.provider || 'provider'}/${detail.metadata.model || 'model'} · ${workdirBase(detail.metadata.workdir)}`;
  } else {
    nodes.sessionIdDisplay.innerText = `ID: ${state.sessionId}`;
  }
  updateConnectionStatus();
}

function updateUI() {
  const hasDraft = nodes.chatInput.value.trim().length > 0;
  const launchPendingWithoutSession = state.launchInFlight && !hasDurableSession();
  nodes.sendBtn.disabled = !hasDraft || launchPendingWithoutSession;
  nodes.sendBtn.classList.toggle('is-loading', (state.isGenerating || state.launchInFlight) && hasDraft);
  nodes.sendBtn.classList.toggle('is-interrupt', isNextSendInterruptArmed() && state.isGenerating && hasDurableSession());
  nodes.inputContainer.classList.toggle('is-busy', state.isGenerating || state.launchInFlight);
  nodes.inputContainer.classList.toggle('is-offline', !state.isConnected);
  nodes.newSessionBtn?.classList.toggle('is-busy', state.isGenerating);
  const directSessionControlAvailable = canUseDirectSessionControl();
  nodes.stopSessionBtn?.classList.toggle('is-visible', directSessionControlAvailable);
  nodes.interruptSessionBtn?.classList.toggle('is-visible', directSessionControlAvailable);
  nodes.interruptToggleBtn?.classList.toggle('is-visible', state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.classList.toggle('is-armed', isNextSendInterruptArmed() && state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.setAttribute('aria-pressed', isNextSendInterruptArmed() ? 'true' : 'false');
  if (nodes.stopSessionBtn) {
    nodes.stopSessionBtn.disabled = !directSessionControlAvailable;
  }
  if (nodes.interruptSessionBtn) {
    nodes.interruptSessionBtn.disabled = !directSessionControlAvailable;
  }
  nodes.chatInput.placeholder = chatInputPlaceholder();

  nodes.inputStatusText.textContent = inputActionLabel();
  renderGoalComposer();

  if (!state.isConnected) {
    nodes.connectionDot.className = 'dot';
    return;
  }
  nodes.connectionDot.className = state.isGenerating ? 'dot busy' : 'dot online';
}

function chatInputPlaceholder() {
  const planMode = currentPlanMode();
  if (state.isGenerating && hasDurableSession()) {
    return isNextSendInterruptArmed()
      ? 'Send an interrupt steer message to the running session...'
      : 'Send a steer message into the running session...';
  }
  if (planMode?.status === 'awaiting_approval') {
    return 'Ask for changes to the submitted plan...';
  }
  if (isPlanModeComposerEnabled()) {
    return 'Describe the objective to plan before execution...';
  }
  return 'Ask anything...';
}

function inputActionLabel() {
  if (!state.isConnected) {
    return 'Live event relay reconnecting; REST session actions remain available.';
  }
  const status = state.sessionDetail?.state?.status || '';
  const planMode = currentPlanMode();
  if (planMode?.status === 'awaiting_approval') {
    return 'Plan Mode awaiting approval: next send requests changes; use Approve & Run to execute.';
  }
  if (planMode?.status === 'awaiting_user_input') {
    return 'Plan Mode is waiting for your answer in the Plan inspector.';
  }
  if (isPlanModeComposerEnabled()) {
    return 'Plan Mode enabled: next send starts a planning gate before execution.';
  }
  if (state.isGenerating && hasDurableSession()) {
    return isNextSendInterruptArmed()
      ? 'Interrupt armed: next send requests preemption, then merges your steer prompt.'
      : 'Steer running session: next send queues guidance into the current run.';
  }
  if (hasDurableSession() && (status === 'awaiting_input' || status === 'paused' || status === 'failed')) {
    return `Continue ${humanizeStatus(status)} session: next send resumes this durable session.`;
  }
  if (hasDurableSession() && status === 'completed') {
    return 'Completed session loaded: next send starts a new session unless you open another session from the Sessions view.';
  }
  return 'Start new session: Enter sends, Shift+Enter / Ctrl+Enter inserts a line.';
}

function canShowGoalComposer() {
  const status = state.sessionDetail?.state?.status || '';
  return !state.isGenerating && (!hasDurableSession() || status === 'completed');
}

function canShowPlanComposer() {
  const status = state.sessionDetail?.state?.status || '';
  const planMode = currentPlanMode();
  return !state.isGenerating &&
    !isPendingPlanMode(planMode?.status) &&
    (!hasDurableSession() || ['completed', 'awaiting_input', 'paused', 'failed'].includes(status));
}

function toggleGoalMode() {
  if (!canShowGoalComposer()) {
    return;
  }
  setComposerMode(isGoalComposerEnabled() ? null : 'goal');
  renderGoalComposer();
  updateDynamicLayoutMetrics();
}

function togglePlanMode() {
  if (!canShowPlanComposer()) {
    return;
  }
  setComposerMode(isPlanModeComposerEnabled() ? null : 'plan');
  renderGoalComposer();
  updateDynamicLayoutMetrics();
}

function normalizeComposerMode(goalVisible, planVisible) {
  const mode = composerMode();
  if (mode === 'goal' && !goalVisible) {
    setComposerMode(null);
  }
  if (mode === 'plan' && !planVisible) {
    setComposerMode(null);
  }
}

function renderGoalComposer() {
  if (!nodes.goalToggleBtn || !nodes.planToggleBtn || !nodes.goalComposerPanel) {
    return;
  }
  const goalVisible = canShowGoalComposer();
  const planVisible = canShowPlanComposer();
  normalizeComposerMode(goalVisible, planVisible);
  nodes.goalToggleBtn.hidden = !goalVisible;
  nodes.goalToggleBtn.classList.toggle('is-active', isGoalComposerEnabled() && goalVisible);
  nodes.goalToggleBtn.setAttribute('aria-pressed', isGoalComposerEnabled() && goalVisible ? 'true' : 'false');
  nodes.planToggleBtn.hidden = !planVisible;
  nodes.planToggleBtn.classList.toggle('is-active', isPlanModeComposerEnabled() && planVisible);
  nodes.planToggleBtn.setAttribute('aria-pressed', isPlanModeComposerEnabled() && planVisible ? 'true' : 'false');
  if (nodes.goalComposerPanel) {
    nodes.goalComposerPanel.hidden = true;
    nodes.goalComposerPanel.innerHTML = '';
  }
}

function collectGoalDraft(promptText) {
  if (!isGoalComposerEnabled()) {
    return null;
  }
  const objective = String(promptText || '').trim();
  if (!objective) {
    return { error: 'Goal objective is required.' };
  }
  return {
    enabled: true,
    mode: 'goal',
    objective
  };
}

function collectPlanModeDraft(promptText) {
  if (!isPlanModeComposerEnabled()) {
    return null;
  }
  const objective = String(promptText || '').trim();
  if (!objective) {
    return { error: 'Plan Mode objective is required.' };
  }
  return {
    enabled: true,
    objective
  };
}

function collectAgentDraft() {
  return {
    agentName: String(nodes.agentNameInput?.value || '').trim(),
    agentRole: String(nodes.agentRoleSelect?.value || '').trim()
  };
}

function currentPlanMode() {
  return state.sessionDetail?.plan_mode || null;
}

function clearPlanInputSelections() {
  planInputViewState.selections = {};
}

function getPlanInputSelections(request) {
  const requestID = String(request?.request_id || '').trim();
  if (!requestID) {
    return {};
  }
  if (!planInputViewState.selections || typeof planInputViewState.selections !== 'object') {
    clearPlanInputSelections();
  }
  Object.keys(planInputViewState.selections).forEach((key) => {
    if (key !== requestID) {
      delete planInputViewState.selections[key];
    }
  });
  const selections = planInputViewState.selections[requestID] || {};
  const validQuestionIDs = new Set(maybeArray(request?.questions).map((question) => String(question?.id || '').trim()).filter(Boolean));
  Object.keys(selections).forEach((questionID) => {
    if (!validQuestionIDs.has(questionID)) {
      delete selections[questionID];
    }
  });
  planInputViewState.selections[requestID] = selections;
  return selections;
}

function setPlanInputSelection(request, answer) {
  const requestID = String(request?.request_id || '').trim();
  const questionID = String(answer?.question_id || '').trim();
  if (!requestID || !questionID) {
    return;
  }
  const selections = getPlanInputSelections(request);
  selections[questionID] = {
    question_id: questionID,
    label: String(answer.label || '').trim(),
    value: String(answer.value || '').trim(),
    is_other: Boolean(answer.is_other)
  };
}

function isPendingPlanMode(status) {
  return ['planning', 'awaiting_user_input', 'awaiting_approval'].includes(status || '');
}

function isCoverageApprovalBlock(err) {
  const haystack = [
    err?.message,
    err?.detail,
    err?.action,
    err?.code
  ].filter(Boolean).join(' ').toLowerCase();
  return err?.status === 409 && haystack.includes('coverage') && (
    haystack.includes('blocks approval') ||
    haystack.includes('approval block') ||
    haystack.includes('validation coverage')
  );
}

async function confirmCoverageOverride() {
  return confirmLocalAction({
    title: 'Override validation coverage',
    message: 'Validation coverage blocks approval. Continue only if you accept the uncovered validation risk for this local session.',
    confirmLabel: 'Override',
    tone: 'danger'
  });
}

async function confirmGoalClear() {
  return confirmLocalAction({
    title: 'Clear goal',
    message: 'Clear the durable goal for this local session? This removes goal state and history links from the current session view.',
    confirmLabel: 'Clear',
    tone: 'danger'
  });
}

async function confirmSkillUninstall(id) {
  return confirmLocalAction({
    title: 'Uninstall skill',
    message: `Uninstall skill ${id} from the local catalog?`,
    confirmLabel: 'Uninstall',
    tone: 'danger'
  });
}

async function handlePlanModeAction(button) {
  if (!hasDurableSession()) {
    showToast('No durable session is loaded.', 'info');
    return;
  }
  const sessionID = state.sessionId;
  const action = button.getAttribute('data-plan-action');
  button.disabled = true;
  try {
    if (action === 'approve') {
      try {
        await approvePlanMode(sessionID);
      } catch (err) {
        if (state.sessionId !== sessionID) {
          return;
        }
        if (!isCoverageApprovalBlock(err)) {
          throw err;
        }
        if (!await confirmCoverageOverride()) {
          showToast('Plan approval was not overridden.', 'info');
          return;
        }
        await approvePlanMode(sessionID, { override_coverage: true });
      }
      if (state.sessionId !== sessionID) {
        return;
      }
      setGenerating(true, {
        title: 'Executing approved plan',
        copy: 'The approved Plan Mode plan is now running as the next durable turn.',
        tone: 'live'
      });
      showToast('Plan approved and execution started.', 'success');
    } else if (action === 'cancel') {
      await cancelPlanMode(sessionID);
      if (state.sessionId !== sessionID) {
        return;
      }
      showToast('Plan Mode cancelled.', 'success');
    } else if (action === 'revise') {
      nodes.chatInput?.focus();
      showToast('Type the requested plan change and send it.', 'info');
    }
    if (state.sessionId === sessionID) {
      queueSessionRefresh(80);
      queueOverviewRefresh(180);
    }
  } catch (err) {
    if (state.sessionId === sessionID) {
      showToast(err.message || 'Plan Mode action failed.', 'error');
    }
  } finally {
    if (document.body.contains(button)) {
      button.disabled = false;
    }
    if (state.sessionId === sessionID) {
      renderCurrentSession();
      updateUI();
    }
  }
}

async function handlePlanInputAction(button) {
  if (!hasDurableSession()) {
    showToast('No durable session is loaded.', 'info');
    return;
  }
  const sessionID = state.sessionId;
  const action = button.getAttribute('data-plan-input-action') || 'select';
  const requestID = button.getAttribute('data-request-id') || '';
  const planMode = currentPlanMode();
  const request = planMode?.pending_request;
  if (!request || !requestID || request.request_id !== requestID) {
    showToast('Plan input request is no longer pending.', 'error');
    return;
  }

  if (action === 'select') {
    const questionID = button.getAttribute('data-question-id') || '';
    const label = button.getAttribute('data-label') || '';
    const isOther = button.getAttribute('data-other') === '1';
    let value = button.getAttribute('data-value') || label;
    if (isOther) {
      value = window.prompt('Enter a custom answer for this Plan Mode question:', '') || '';
      if (!value.trim()) {
        return;
      }
    }
    setPlanInputSelection(request, {
      question_id: questionID,
      label,
      value,
      is_other: isOther
    });
    renderCurrentSession();
    return;
  }

  if (action !== 'submit') {
    return;
  }

  const answers = collectPlanInputAnswers(request, getPlanInputSelections(request));
  if (answers.length !== maybeArray(request.questions).length) {
    showToast('Answer every Plan Mode question before submitting.', 'error');
    renderCurrentSession();
    return;
  }
  button.disabled = true;
  try {
    await answerPlanModeInput(sessionID, { requestID, answers });
    if (state.sessionId !== sessionID) {
      return;
    }
    delete planInputViewState.selections[requestID];
    showToast('Plan input answered.', 'success');
    queueSessionRefresh(80);
    queueOverviewRefresh(180);
  } catch (err) {
    if (state.sessionId === sessionID) {
      showToast(err.message || 'Failed to answer Plan Mode input.', 'error');
    }
  } finally {
    if (document.body.contains(button)) {
      button.disabled = false;
    }
    if (state.sessionId === sessionID) {
      renderCurrentSession();
    }
  }
}

async function handleGoalAction(button) {
  if (!hasDurableSession()) {
    showToast('No durable session is loaded.', 'info');
    return;
  }
  const sessionID = state.sessionId;
  const action = button.getAttribute('data-goal-action');
  button.disabled = true;
  try {
    if (action === 'pause') {
      await pauseGoal(sessionID);
      if (state.sessionId !== sessionID) {
        return;
      }
      showToast('Goal paused.', 'success');
    } else if (action === 'resume') {
      await resumeGoal(sessionID);
      if (state.sessionId !== sessionID) {
        return;
      }
      showToast('Goal resumed.', 'success');
    } else if (action === 'complete') {
      await completeGoal(sessionID);
      if (state.sessionId !== sessionID) {
        return;
      }
      showToast('Goal marked complete.', 'success');
    } else if (action === 'clear') {
      if (!await confirmGoalClear()) {
        if (state.sessionId === sessionID) {
          showToast('Goal clear cancelled.', 'info');
        }
        return;
      }
      if (state.sessionId !== sessionID) {
        return;
      }
      await deleteGoal(sessionID);
      if (state.sessionId !== sessionID) {
        return;
      }
      showToast('Goal cleared.', 'success');
    } else if (action === 'approve-plan') {
      let response = null;
      try {
        response = await approveMissionPlan(sessionID);
      } catch (err) {
        if (state.sessionId !== sessionID) {
          return;
        }
        if (!isCoverageApprovalBlock(err)) {
          throw err;
        }
        if (!await confirmCoverageOverride()) {
          showToast('Goal plan approval was not overridden.', 'info');
          return;
        }
        if (state.sessionId !== sessionID) {
          return;
        }
        response = await approveMissionPlan(sessionID, { override_coverage: true });
      }
      if (state.sessionId !== sessionID) {
        return;
      }
      if (isAcceptedLaunchResponse(response)) {
        setGenerating(true, {
          title: 'Executing approved plan',
          copy: 'The linked Plan Mode plan is now running as the next durable turn.',
          tone: 'live'
        });
      }
      showToast('Goal plan approved.', 'success');
    }
    if (state.sessionId === sessionID) {
      await refreshCurrentSession();
      queueOverviewRefresh(160);
    }
  } catch (err) {
    if (state.sessionId === sessionID) {
      showToast(err.message || 'Goal action failed.', 'error');
    }
  } finally {
    if (document.body.contains(button)) {
      button.disabled = false;
    }
    if (state.sessionId === sessionID) {
      renderCurrentSession();
    }
  }
}

function startPolling() {
  stopPolling();
  if (!shouldRunPollingLoop()) {
    return;
  }

  const pollStep = () => {
    if (state.currentView === 'history') {
      fetchHistory(state.historyPage, { showLoading: false, silentError: true });
    } else {
      if (shouldPollChatOverview()) {
        refreshOverview();
      }
      if (shouldPollCurrentSession()) {
        refreshCurrentSession();
      }

    }

    if (!shouldRunPollingLoop()) {
      runtimeHandles.pollHandle = null;
      return;
    }

    runtimeHandles.pollIntervalMs = pollingIntervalForState();
    runtimeHandles.pollHandle = window.setTimeout(pollStep, runtimeHandles.pollIntervalMs);
  };

  runtimeHandles.pollIntervalMs = pollingIntervalForState();
  runtimeHandles.pollHandle = window.setTimeout(pollStep, runtimeHandles.pollIntervalMs);
}

function shouldRunPollingLoop() {
  if (isPageVisibilityHidden()) {
    return false;
  }
  if (state.currentView === 'history') {
    return true;
  }
  if (state.currentView !== 'chat') {
    return false;
  }
  if (!state.isConnected) {
    return true;
  }
  if (state.isGenerating) {
    return true;
  }
  return hasDurableSession() && (!state.sessionDetail || sessionDetailHasActiveDescendants(state.sessionDetail));
}

function pollingIntervalForState() {
  if (state.currentView === 'history') {
    return POLL_INTERVAL_MS;
  }
  if (!state.isConnected || state.isGenerating || sessionDetailHasActiveDescendants(state.sessionDetail)) {
    return POLL_INTERVAL_ACTIVE_MS;
  }
  return POLL_INTERVAL_MS;
}

function syncPollingForState() {
  if (shouldRunPollingLoop()) {
    if (!runtimeHandles.pollHandle) {
      startPolling();
    }
    return;
  }
  stopPolling();
}

function stopPolling() {
  if (runtimeHandles.pollHandle) {
    window.clearTimeout(runtimeHandles.pollHandle);
    runtimeHandles.pollHandle = null;
  }
}

function queueSessionRefresh(delay = 120) {
  if (!hasDurableSession()) {
    return;
  }
  window.clearTimeout(runtimeHandles.pendingSessionRefresh);
  runtimeHandles.pendingSessionRefresh = window.setTimeout(() => {
    refreshCurrentSession();
  }, delay);
}

function queueOverviewRefresh(delay = 180) {
  window.clearTimeout(runtimeHandles.pendingOverviewRefresh);
  runtimeHandles.pendingOverviewRefresh = window.setTimeout(() => {
    refreshOverview();
  }, delay);
}

function shouldPollChatOverview() {
  if (state.currentView !== 'chat') {
    return false;
  }
  if (!state.overview) {
    return true;
  }
  return state.isGenerating || !hasDurableSession() || sessionDetailHasActiveDescendants(state.sessionDetail);
}

function shouldPollCurrentSession() {
  if (state.currentView !== 'chat' || !hasDurableSession()) {
    return false;
  }
  return state.isGenerating || !state.sessionDetail || sessionDetailHasActiveDescendants(state.sessionDetail);
}

function sessionDetailHasActiveDescendants(detail) {
  if (!detail) {
    return false;
  }
  return selectedQueueJobIsActiveForSession(detail) ||
    maybeArray(detail.children?.sessions).some((item) => isActiveRuntimeStatus(item.status)) ||
    maybeArray(detail.children?.jobs).some((item) => isActiveRuntimeStatus(item.status) || isActiveRuntimeStatus(item.session_status));
}

function selectedQueueJobIsActiveForSession(detail) {
  const job = selectedQueueJobDetail();
  const selectedID = String(selectedQueueJobId() || '');
  const sessionID = String(detail?.metadata?.id || '');
  if (!job || !selectedID || !sessionID) {
    return false;
  }
  if (String(job.id || '') !== selectedID) {
    return false;
  }
  if (String(job.parent_session_id || '') !== sessionID) {
    return false;
  }
  return isActiveRuntimeStatus(job.status) || isActiveRuntimeStatus(job.session_status);
}

function isActiveRuntimeStatus(status) {
  return ['queued', 'pending', 'running'].includes(String(status || '').toLowerCase());
}

function isStoppableSessionStatus(status) {
  return String(status || '').toLowerCase() === 'running';
}

function canUseDirectSessionControl() {
  return state.isGenerating &&
    hasDurableSession() &&
    state.sessionDetail?.active_handle === true &&
    state.sessionDetail?.active_handle_owner?.owned_by_current_process === true;
}

function queueJobItems(data = state.sessionDetail?.children?.jobs) {
  return maybeArray(data?.items || data);
}

function queueJobByID(jobID, data = state.sessionDetail?.children?.jobs) {
  const id = String(jobID || '');
  if (!id) {
    return null;
  }
  return queueJobItems(data).find((job) => String(job?.id || '') === id) || null;
}

async function refreshSelectedQueueJobDetail(jobs = queueJobItems(), options = {}) {
  const isCurrent = typeof options.isCurrent === 'function' ? options.isCurrent : () => true;
  const jobID = String(selectedQueueJobId() || '');
  if (!jobID) {
    setSelectedQueueJobDetail(null);
    return;
  }
  const listedJob = jobs.find((job) => String(job?.id || '') === jobID);
  if (listedJob) {
    if (isCurrent()) {
      setSelectedQueueJobDetail(listedJob);
    }
    return;
  }
  try {
    const detail = await requestJSON(`/api/queue/jobs/${encodeURIComponent(jobID)}`);
    if (String(selectedQueueJobId() || '') !== jobID || !isCurrent()) {
      return;
    }
    setSelectedQueueJobDetail(detail);
  } catch (err) {
    if (String(selectedQueueJobId() || '') !== jobID || !isCurrent()) {
      return;
    }
    setSelectedQueueJobDetail({
      id: jobID,
      status: 'unavailable',
      last_error: err.message || 'Job detail is unavailable.'
    });
  }
}

function isStoppingSession(sessionID) {
  return stopActionViewState.sessionIds.has(sessionID);
}

function currentSessionReferencesSession(sessionID) {
  if (!sessionID) {
    return false;
  }
  const detail = state.sessionDetail || {};
  if (detail.metadata?.id === sessionID) {
    return true;
  }
  const children = detail.children || {};
  if (maybeArray(children.sessions).some((item) => item?.id === sessionID)) {
    return true;
  }
  if (maybeArray(children.jobs).some((item) => item?.session_id === sessionID)) {
    return true;
  }
  if (maybeArray(detail.background_notifications).some((item) => item?.session_id === sessionID)) {
    return true;
  }
  return selectedQueueJobDetail()?.session_id === sessionID;
}

function sessionActivityForState(sessionState = {}) {
  const status = sessionState?.status || 'idle';
  const pauseReason = sessionState?.pause_reason || sessionState?.pauseReason || '';
  if (status === 'completed') {
    return {
      title: 'Completed',
      copy: 'The run finished. Answers and tool results remain visible below.',
      tone: 'live'
    };
  }
  if (status === 'awaiting_input') {
    return {
      title: 'Awaiting input',
      copy: 'The session is waiting for the next message before it can continue.',
      tone: 'queued'
    };
  }
  if (status === 'paused' && pauseReason === 'manual_stop') {
    return {
      title: 'Stopped',
      copy: 'The run was stopped. Partial output remains visible and you can continue later if needed.',
      tone: 'danger'
    };
  }
  if (status === 'paused' && pauseReason === 'manual_interrupt') {
    return {
      title: 'Interrupted',
      copy: 'The run stopped at a safe boundary. Review the partial results or continue later.',
      tone: 'queued'
    };
  }
  if (status === 'paused') {
    return {
      title: 'Paused',
      copy: 'The session paused and can be continued when you are ready.',
      tone: 'queued'
    };
  }
  if (status === 'failed') {
    return {
      title: 'Failed',
      copy: sessionState?.last_error || 'The run failed. Review the last tool result or continue after adjusting the prompt.',
      tone: 'danger'
    };
  }
  return {
    title: humanizeStatus(status),
    copy: sessionState?.last_error || 'Durable session data is loaded.',
    tone: toneForStatus(status)
  };
}

function overviewErrorMessage(err, fallback = 'Failed to load session overview.') {
  return err?.message || fallback;
}

async function refreshOverview() {
  if (overviewViewState.refreshing) {
    overviewViewState.needsRefresh = true;
    return;
  }
  const requestSeq = ++overviewViewState.requestSeq;
  overviewViewState.refreshing = true;
  overviewViewState.needsRefresh = false;
  try {
    const overview = await requestJSON('/api/overview');
    if (overviewViewState.requestSeq !== requestSeq || overviewViewState.needsRefresh) {
      return;
    }
    state.overview = overview;
    state.overviewError = '';
    if (state.currentView === 'chat') {
      renderCurrentSession();
    }
  } catch (err) {
    if (overviewViewState.requestSeq !== requestSeq || overviewViewState.needsRefresh) {
      return;
    }
    console.error('overview error', err);
    if (!state.overview) {
      const message = overviewErrorMessage(err);
      state.overviewError = message;
      if (state.currentView === 'chat') {
        showToast(message, 'error');
        renderCurrentSession();
      }
    }
  } finally {
    overviewViewState.refreshing = false;
    if (overviewViewState.needsRefresh) {
      overviewViewState.needsRefresh = false;
      refreshOverview().catch((err) => {
        console.error('queued overview refresh error', err);
      });
    }
  }
}

async function refreshCurrentSession(options = {}) {
  if (!hasDurableSession() || isEphemeralSessionId(state.sessionId)) {
    return;
  }
  const sessionID = state.sessionId;
  if (sessionViewState.refreshing) {
    sessionViewState.needsRefresh = true;
    return;
  }
  sessionViewState.refreshing = true;
  sessionViewState.needsRefresh = false;
  try {
    const detail = await requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}?limit=40`);
    if (state.sessionId !== sessionID || sessionViewState.needsRefresh) {
      return;
    }
    mergeLoadedMessagesIntoDetail(detail);
    mergeMessageTimelineEntries(detail);
    state.sessionDetail = detail;
    await refreshSelectedQueueJobDetail(queueJobItems(detail?.children?.jobs), {
      isCurrent: () => state.sessionId === sessionID && !sessionViewState.needsRefresh
    });
    if (state.sessionId !== sessionID || sessionViewState.needsRefresh) {
      return;
    }
    updateSessionId();
    reconcileOptimisticMessages(detail);
    state.hasMoreMessages = state.loadedAllEarlierMessages ? false : detail?.has_more_messages === true;
    const msgs = maybeArray(detail?.messages);
    state.oldestMessageId = msgs.length > 0 ? msgs[0].id : '';
    if (detail?.state?.status === 'running') {
      state.isGenerating = true;
      if (!state.liveEvents.length) {
        setLiveActivity({
          title: phaseHeadline(detail.state.phase),
          copy: 'The runner is active. Tool calls and child-agent transitions will stream into this panel as durable events.',
          tone: 'live'
        });
      }
    } else {
      state.isGenerating = false;
      if (!state.liveEvents.length || toneForStatus(detail?.state?.status) !== 'live') {
        setLiveActivity(sessionActivityForState(detail?.state));
      }
      setNextSendInterruptArmed(false);
    }
    renderCurrentSession();
    updateUI();
    syncPollingForState();
  } catch (err) {
    if (state.sessionId !== sessionID || sessionViewState.needsRefresh) {
      return;
    }
    console.error('session detail error', err);
    if (options.surfaceError) {
      if (state.sessionId === sessionID) {
        showSessionLoadError(err, { toast: options.toastError !== false });
      }
    }
  } finally {
    sessionViewState.refreshing = false;
    if (sessionViewState.needsRefresh) {
      sessionViewState.needsRefresh = false;
      queueSessionRefresh(80);
    }
  }
}

function showSessionLoadError(err, options = {}) {
  const message = err?.message || 'The session data could not be loaded.';
  state.isGenerating = false;
  setLiveActivity({
    title: 'Error loading session',
    copy: message,
    tone: 'danger'
  });
  if (options.toast) {
    showToast(message, 'error');
  }
  renderCurrentSession();
  updateUI();
}

function earlierMessagesErrorMessage(err, fallback = 'Failed to load earlier messages.') {
  return err?.message || fallback;
}

async function loadEarlierMessages() {
  if (state.loadingEarlier || !state.hasMoreMessages || !(state.messageGapAnchorId || state.oldestMessageId)) {
    return;
  }
  const sessionID = state.sessionId;
  const fillingGap = Boolean(state.messageGapAnchorId);
  const beforeID = state.messageGapAnchorId || state.oldestMessageId;
  const requestSeq = ++messagePagingViewState.requestSeq;
  state.loadingEarlier = true;
  renderCurrentSession();
  try {
    const beforeScrollHeight = nodes.chatContainer.scrollHeight;
    const resp = await requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/messages?before_id=${encodeURIComponent(beforeID)}&limit=40`);
    if (state.sessionId !== sessionID || messagePagingViewState.requestSeq !== requestSeq) {
      return;
    }
    const olderMessages = maybeArray(resp?.messages);
    if (olderMessages.length > 0) {
      const currentMessages = maybeArray(state.sessionDetail?.messages);
      state.sessionDetail.messages = mergeMessagesBeforeAnchor(currentMessages, olderMessages, beforeID);
      const mergedMessages = maybeArray(state.sessionDetail?.messages);
      state.oldestMessageId = mergedMessages.length > 0 ? mergedMessages[0].id : '';
      if (fillingGap && resp?.has_more === true) {
        state.messageGapAnchorId = olderMessages[0]?.id || beforeID;
        state.hasMoreMessages = true;
        state.loadedAllEarlierMessages = false;
      } else {
        state.messageGapAnchorId = '';
        state.hasMoreMessages = resp?.has_more === true;
        state.loadedAllEarlierMessages = resp?.has_more !== true;
      }
      mergeMessageTimelineEntries(state.sessionDetail);
      state.preserveScrollAfterRender = beforeScrollHeight;
    } else {
      state.hasMoreMessages = false;
      state.loadedAllEarlierMessages = true;
      state.messageGapAnchorId = '';
    }
    state.loadingEarlier = false;
    renderCurrentSession();
  } catch (err) {
    if (state.sessionId !== sessionID || messagePagingViewState.requestSeq !== requestSeq) {
      return;
    }
    console.error('load earlier messages error', err);
    const message = earlierMessagesErrorMessage(err);
    state.loadingEarlier = false;
    showToast(message, 'error');
    renderCurrentSession();
  } finally {
    if (messagePagingViewState.requestSeq === requestSeq) {
      state.loadingEarlier = false;
      state.preserveScrollAfterRender = null;
    }
  }
}

function mergeLoadedMessagesIntoDetail(detail) {
  const currentMessages = maybeArray(state.sessionDetail?.messages);
  const nextMessages = maybeArray(detail?.messages);
  if (!detail || !currentMessages.length) {
    return;
  }
  const merged = mergeMessageWindows(currentMessages, nextMessages);
  detail.messages = merged.messages;
  if (merged.hasGap) {
    state.messageGapAnchorId = merged.gapAnchorId;
    state.loadedAllEarlierMessages = false;
    return;
  }
  if (state.messageGapAnchorId && merged.messages.some((message) => message?.id === state.messageGapAnchorId)) {
    state.loadedAllEarlierMessages = false;
    return;
  }
  state.messageGapAnchorId = '';
}

function mergeMessageTimelineEntries(detail) {
  if (!detail) {
    return;
  }
  const timeline = maybeArray(detail.timeline);
  const existing = new Set(timeline
    .filter((item) => item?.kind === 'message' && item?.message_id)
    .map((item) => item.message_id));
  const additions = maybeArray(detail.messages)
    .filter((message) => message?.id && !existing.has(message.id))
    .map((message) => ({
      time: message.created_at || '',
      kind: 'message',
      message_id: message.id,
      role: message.role || '',
      text: messageTimelineText(message),
      data: message.meta || null
    }));
  const merged = timeline.concat(additions).sort((left, right) => String(right.time || '').localeCompare(String(left.time || '')));
  const seen = new Set();
  detail.timeline = merged.filter((item) => {
    if (item?.kind !== 'message' || !item?.message_id) {
      return true;
    }
    if (seen.has(item.message_id)) {
      return false;
    }
    seen.add(item.message_id);
    return true;
  });
}

function messageTimelineText(message) {
  const text = String(message?.text || '').trim();
  if (text) {
    return text;
  }
  const toolResults = maybeArray(message?.tool_results);
  if (toolResults.length > 0) {
    return toolResults[0]?.display_output || toolResults[0]?.llm_output || '';
  }
  return '';
}

// Session workspace rendering helpers live in session-view.js.

async function openSession(sessionID, options = {}) {
  adoptSession(sessionID, true);
  state.sessionDetail = null;
  state.optimisticMessages = [];
  setSelectedQueueJob('');
  state.hasMoreMessages = false;
  state.oldestMessageId = '';
  state.loadingEarlier = false;
  state.loadedAllEarlierMessages = false;
  state.messageGapAnchorId = '';
  state.preserveScrollAfterRender = null;
  setNextSendInterruptArmed(false);
  state.liveEvents = [];
  state.isGenerating = false;
  setLiveActivity({
    title: 'Loading session',
    copy: 'Loading durable session detail and tool activity.',
    tone: 'neutral'
  });
  if (options.switchToChat !== false) {
    switchView('chat');
  }
  await refreshCurrentSession({ surfaceError: true });
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
  if (isBackgroundResultsMessage(message)) {
    return 'Background agents';
  }
  const source = messageSource(message);
  if (message.role === 'user') {
    return source === 'steer' ? 'You · steer' : 'You';
  }
  if (message.role === 'system') {
    return 'System';
  }
  if (message.role === 'tool') {
    return 'Tool lane';
  }
  if (source === 'harness_reminder') {
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

function showToast(message, tone = 'info') {
  const id = `toast-${++toastViewState.counter}`;
  const toast = document.createElement('div');
  toast.className = `toast toast-${tone}`;
  toast.id = id;
  toast.textContent = message;
  nodes.toastRack.appendChild(toast);
  window.setTimeout(() => {
    document.getElementById(id)?.remove();
  }, 3200);
}

function historyErrorMessage(err, fallback = 'Failed to load recent activity.') {
  return err?.message || fallback;
}

async function fetchHistory(page = state.historyPage, options = {}) {
  const requestedPage = Math.max(1, Number(page) || 1);
  if (historyViewState.refreshing) {
    state.historyPage = requestedPage;
    historyViewState.needsRefresh = true;
    historyViewState.pendingRefreshOptions = options;
    persistUIState();
    return;
  }
  const container = nodes.views.history;
  const showLoading = options.showLoading ?? !state.historyData;
  const silentError = options.silentError ?? false;
  const pageSize = state.historyPageSize;
  const requestSeq = ++historyViewState.requestSeq;
  historyViewState.refreshing = true;
  historyViewState.needsRefresh = false;
  historyViewState.pendingRefreshOptions = null;
  state.historyPage = requestedPage;
  persistUIState();
  if (showLoading) {
    container.innerHTML = '<div class="view-loading">Loading sessions...</div>';
  }
  try {
    const data = await requestJSON(`/api/history?page=${encodeURIComponent(requestedPage)}&page_size=${encodeURIComponent(pageSize)}`);
    if (historyViewState.requestSeq !== requestSeq || historyViewState.needsRefresh || state.historyPage !== requestedPage) {
      return;
    }
    state.historyData = data;
    renderHistory(data);
    refreshOverview().catch((err) => {
      console.error('overview refresh error', err);
    });
  } catch (err) {
    if (historyViewState.requestSeq !== requestSeq || historyViewState.needsRefresh || state.historyPage !== requestedPage) {
      return;
    }
    console.error('history error', err);
    const message = historyErrorMessage(err);
    if (!state.historyData) {
      const panel = document.createElement('div');
      panel.className = 'empty-panel';
      panel.textContent = message;
      container.replaceChildren(panel);
    }
    if (!silentError) {
      showToast(message, 'error');
    }
  } finally {
    historyViewState.refreshing = false;
    if (historyViewState.needsRefresh) {
      const nextPage = state.historyPage;
      const nextOptions = historyViewState.pendingRefreshOptions || {};
      historyViewState.needsRefresh = false;
      historyViewState.pendingRefreshOptions = null;
      fetchHistory(nextPage, nextOptions).catch((err) => {
        console.error('queued history refresh error', err);
      });
    }
  }
}

function renderHistory(data) {
  const container = nodes.views.history;
  const history = data || state.historyData;
  if (!history) {
    container.innerHTML = '<div class="empty-panel">No session data available yet.</div>';
    return;
  }
  const items = maybeArray(history.items);
  const total = Number(history.total || 0);
  const page = Number(history.page || 1);
  const pageSize = Number(history.page_size || state.historyPageSize || 8);
  const totalPages = Number(history.total_pages || 0);
  const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const rangeEnd = total === 0 ? 0 : rangeStart + items.length - 1;

  // Build tree: group children under their parent
  const itemMap = {};
  const rootItems = [];
  const childrenByParent = {};
  items.forEach((item) => {
    itemMap[item.id] = item;
  });
  items.forEach((item) => {
    if (item.parent_session_id && itemMap[item.parent_session_id]) {
      if (!childrenByParent[item.parent_session_id]) {
        childrenByParent[item.parent_session_id] = [];
      }
      childrenByParent[item.parent_session_id].push(item);
    } else {
      rootItems.push(item);
    }
  });

  const chevronDown = '<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"></polyline></svg>';

  function renderTreeRows(list) {
    return list.map((item) => {
      const children = childrenByParent[item.id] || [];
      const hasChildren = children.length > 0;
      const isExpanded = historyExpansionViewState.parentIds.has(item.id);
      let html = renderHistorySessionCard(item, false, hasChildren, isExpanded, chevronDown, children.length);
      if (hasChildren) {
        html += `<div class="history-tree-children${isExpanded ? ' is-expanded' : ''}">`;
        html += children.map((child) => renderHistorySessionCard(child, true, false, false, '', 0)).join('');
        html += '</div>';
      }
      return html;
    }).join('');
  }

  container.innerHTML = `
    <div class="view-header history-header">
      <div>
        <h2 class="view-title">Sessions</h2>
        <p class="view-subtitle">All durable sessions with parent-child hierarchy. Open one to inspect or continue it.</p>
      </div>
      <div class="history-toolbar">
        <span class="surface-chip">${escapeHTML(String(total))} total</span>
        <button class="ghost-action-btn danger" type="button" data-history-clear ${total === 0 ? 'disabled' : ''}>
          <i data-lucide="trash-2"></i>
          <span>Clear sessions</span>
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
          <button class="ghost-action-btn" type="button" data-history-page="next" ${totalPages === 0 || page >= totalPages ? 'disabled' : ''}>
            <span>Next</span>
            <i data-lucide="chevron-right"></i>
          </button>
        </div>
      </div>
      <div class="panel-card-body">
        ${items.length ? `<div class="history-session-list">${renderTreeRows(rootItems)}</div>` : '<div class="empty-panel">No sessions yet.</div>'}
      </div>
    </section>
  `;
  if (window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: container });
  }
}

function loadPersistedUIState() {
  try {
    const raw = window.localStorage?.getItem(UI_STATE_STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function persistUIState() {
  try {
    const selectedSessionId = state.sessionBacked && !isEphemeralSessionId(state.sessionId)
      ? state.sessionId
      : '';
    window.localStorage?.setItem(UI_STATE_STORAGE_KEY, JSON.stringify({
      currentView: state.currentView,
      historyPage: state.historyPage,
      selectedSessionId,
      todoFloatExpanded: isFloatingPanelExpanded('todo'),
      fileChangesExpanded: isFloatingPanelExpanded('files'),
      subAgentExpanded: isFloatingPanelExpanded('subAgents')
    }));
  } catch {
    // Ignore storage failures and continue with in-memory state.
  }
}

function restoreUIState() {
  const persisted = loadPersistedUIState();
  const nextView = typeof persisted.currentView === 'string' && nodes.views[persisted.currentView]
    ? persisted.currentView
    : 'chat';
  const nextHistoryPage = Number(persisted.historyPage);
  state.currentView = nextView;
  if (Number.isFinite(nextHistoryPage) && nextHistoryPage >= 1) {
    state.historyPage = Math.floor(nextHistoryPage);
  }
  if (typeof persisted.selectedSessionId === 'string' && persisted.selectedSessionId.trim()) {
    state.sessionId = persisted.selectedSessionId.trim();
    state.sessionBacked = true;
  }
  if (typeof persisted.todoFloatExpanded === 'boolean') {
    setFloatingPanelExpanded('todo', persisted.todoFloatExpanded);
  }
  if (typeof persisted.fileChangesExpanded === 'boolean') {
    setFloatingPanelExpanded('files', persisted.fileChangesExpanded);
  }
  if (typeof persisted.subAgentExpanded === 'boolean') {
    setFloatingPanelExpanded('subAgents', persisted.subAgentExpanded);
  }
  applyViewVisibility(state.currentView);
}

function renderHistorySessionCard(item, isChild, hasChildren, isExpanded, chevronSVG, childCount) {
  const metaText = item.last_error
    ? truncateText(item.last_error, 140)
    : `${item.model || item.provider || 'n/a'} · ${phaseHeadline(item.phase || 'prepare')}`;
  const rowClass = isChild ? 'child-session' : (hasChildren ? 'parent-session' : '');
  const expandToggle = hasChildren
    ? `<button class="history-expand-toggle${isExpanded ? ' is-expanded' : ''}" type="button" data-history-toggle-children="${escapeAttr(item.id)}" title="${isExpanded ? 'Collapse' : 'Expand'} children">${chevronSVG}<span>${isExpanded ? 'Collapse' : 'Expand'}</span></button>`
    : '';
  const childrenBadge = hasChildren
    ? `<span class="history-children-count">${childCount} child${childCount !== 1 ? 'ren' : ''}</span>`
    : '';
  return `
    <div class="history-session-row ${rowClass}">
      <div class="history-session-main">
        <div class="history-session-top">
          <span class="status-badge ${toneForStatus(item.status)}">${escapeHTML(humanizeStatus(item.status))}</span>
          <span class="tiny-code-chip">${escapeHTML(shortId(item.id))}</span>
          ${childrenBadge}
          <span class="history-session-time" title="${escapeAttr(formatTimestamp(item.updated_at || item.created_at))}">${escapeHTML(formatRelativeTime(item.updated_at || item.created_at))}</span>
        </div>
        <div class="history-session-title">${escapeHTML(agentLabel(item.agent_name, item.agent_role) || 'Master session')}</div>
        <div class="history-session-meta">${escapeHTML(metaText)}</div>
      </div>
      <div class="history-row-actions">
        ${expandToggle}
        ${renderSessionStopButton(item.id, item.status)}
        <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.id)}">
          <i data-lucide="external-link"></i>
          <span>Open</span>
        </button>
        <button class="mini-link-btn danger" type="button" data-delete-session="${escapeAttr(item.id)}">
          <i data-lucide="trash-2"></i>
          <span>Delete</span>
        </button>
      </div>
    </div>
  `;
}

async function deleteHistorySession(sessionID) {
  if (!await confirmLocalAction({
    title: 'Delete session',
    message: `Delete session ${sessionID}?`,
    confirmLabel: 'Delete',
    tone: 'danger'
  })) {
    return;
  }
  try {
    await requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}`, {
      method: 'DELETE'
    });
    const activeMeta = state.sessionDetail?.metadata || {};
    if (state.sessionId === sessionID || activeMeta.parent_session_id === sessionID || activeMeta.root_session_id === sessionID) {
      resetChatSession();
    }
    showToast('Session deleted.', 'success');
    await fetchHistory(state.historyPage);
    if ((state.historyData?.items || []).length === 0 && state.historyPage > 1) {
      await fetchHistory(state.historyPage - 1);
    }
    refreshOverview().catch(() => {});
  } catch (err) {
    showToast(err.message || 'Failed to delete session.', 'error');
  }
}

async function clearHistory() {
  if (!await confirmLocalAction({
    title: 'Clear saved sessions',
    message: 'Clear all saved sessions? This will remove sessions and queue history.',
    confirmLabel: 'Clear all',
    tone: 'danger'
  })) {
    return;
  }
  try {
    await requestJSON('/api/sessions/clear', {
      method: 'POST'
    });
    resetChatSession();
    state.historyData = null;
    state.historyPage = 1;
    showToast('Sessions cleared.', 'success');
    await fetchHistory(1);
    refreshOverview().catch(() => {});
  } catch (err) {
    showToast(err.message || 'Failed to clear sessions.', 'error');
  }
}


async function fetchSkills() {
  const requestSeq = ++skillsViewState.requestSeq;
  try {
    nodes.skillsGrid.innerHTML = '<div class="view-loading">Loading local skills…</div>';
    const skills = await requestJSON('/api/skills');
    if (skillsViewState.requestSeq !== requestSeq) {
      return;
    }
    state.skills = skills;
    renderSkills(skills);
  } catch (err) {
    if (skillsViewState.requestSeq !== requestSeq) {
      return;
    }
    console.error('skills error', err);
    const message = err?.message || 'Failed to load local skills.';
    const panel = document.createElement('div');
    panel.className = 'empty-panel';
    panel.textContent = message;
    nodes.skillsGrid.replaceChildren(panel);
    showToast(message, 'error');
  }
}

function renderSkills(skills) {
  if (!skills.length) {
    nodes.skillsGrid.innerHTML = `
      <div class="empty-panel">
        <i data-lucide="package-open" class="empty-icon"></i>
        <strong>No local skills found.</strong>
        <span>Upload a .zip skill package to extend your agent's capabilities.</span>
        <button class="skill-btn install" type="button" id="empty-upload-btn" style="margin-top:12px">Upload .zip Skill</button>
      </div>
    `;
    if (window.lucide && lucide.createIcons) {
      lucide.createIcons({ root: nodes.skillsGrid });
    }
    const emptyBtn = document.getElementById('empty-upload-btn');
    if (emptyBtn) {
      emptyBtn.addEventListener('click', () => openSkillUploadPicker());
      setSkillUploadPending(document, isSkillUploadInFlight());
    }
    return;
  }
  nodes.skillsGrid.innerHTML = skills.map((skill) => {
    const isReadOnly = !!skill.read_only;
    const trustLine = skill.trust
      ? `<p class="skill-author">trust: ${escapeHTML(skill.trust)}${skill.disabled_reason ? ` · ${escapeHTML(skill.disabled_reason)}` : ''}</p>`
      : '';
    const disabledReasonLine = !skill.trust && skill.disabled_reason
      ? `<p class="skill-author">${escapeHTML(skill.disabled_reason)}</p>`
      : '';
    const pathLine = skill.extension_path
      ? `<p class="skill-desc"><code>${escapeHTML(skill.extension_path)}</code></p>`
      : '';
    const button = isReadOnly
      ? `<button class="skill-btn uninstall" type="button" disabled>Disabled</button>`
      : `<button class="skill-btn ${skill.installed ? 'uninstall' : 'install'}" data-skill-action="${escapeAttr(skill.id)}" data-skill-installed="${skill.installed ? '1' : '0'}">
          ${skill.installed ? 'Uninstall' : 'Upload to Install'}
        </button>`;
    return `
      <div class="skill-card">
        <div class="skill-icon">
          <i data-lucide="${skill.icon || 'box'}"></i>
        </div>
        <h3 class="skill-name">${escapeHTML(skill.name)}</h3>
        <p class="skill-author">by ${escapeHTML(skill.author)}</p>
        ${trustLine}
        ${disabledReasonLine}
        <p class="skill-desc">${escapeHTML(skill.description)}</p>
        ${pathLine}
        <div class="skill-footer">
          ${button}
        </div>
      </div>
    `;
  }).join('');
  if (window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: nodes.skillsGrid });
  }
  setSkillUploadPending(document, isSkillUploadInFlight());
}

function openSkillUploadPicker() {
  if (isSkillUploadInFlight()) {
    showToast('Skill upload is already in progress.', 'info');
    return;
  }
  const uploadInput = document.getElementById('skill-upload');
  if (uploadInput) {
    uploadInput.click();
  } else {
    showToast('Upload input not available.', 'error');
  }
}

async function handleSkillUploadChange(event) {
  if (!event.target || event.target.id !== 'skill-upload') {
    return;
  }
  const file = event.target.files?.[0];
  if (!file) {
    return;
  }
  if (isSkillUploadInFlight()) {
    showToast('Skill upload is already in progress.', 'info');
    event.target.value = '';
    return;
  }
  const formData = new FormData();
  formData.append('file', file);
  setSkillUploadInFlight(true);
  setSkillUploadPending(document, true);
  try {
    await requestFormJSON('/api/skills/upload', formData, {
      method: 'POST',
    });
    showToast('Skill uploaded and extracted successfully.', 'success');
    if (state.currentView === 'skills') {
      await fetchSkills();
    }
  } catch (err) {
    showToast(err.message || 'Failed to upload skill zip.', 'error');
  } finally {
    setSkillUploadInFlight(false);
    setSkillUploadPending(document, false);
    event.target.value = '';
  }
}

async function handleSkillAction(id, isInstalled, button) {
  if (!isInstalled) {
    openSkillUploadPicker();
    return;
  }
  if (!await confirmSkillUninstall(id)) {
    showToast('Skill uninstall cancelled.', 'info');
    return;
  }
  button.disabled = true;
  button.innerText = 'Uninstalling...';
  try {
    await requestJSON(`/api/skills/${id}/uninstall`, { method: 'POST' });
    await fetchSkills();
    showToast('Skill removed from the local catalog.', 'success');
  } catch (err) {
    showToast(err.message || 'Failed to uninstall skill.', 'error');
    button.disabled = false;
    button.innerText = 'Uninstall';
  }
}

async function refreshMeta() {
  state.meta = await requestJSON('/api/meta');
  updateWorkspaceMeta();
}

document.addEventListener('DOMContentLoaded', init);
