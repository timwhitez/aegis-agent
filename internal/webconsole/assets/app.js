/**
 * Aegis Agent Webconsole
 */

const POLL_INTERVAL_MS = 5000;
const POLL_INTERVAL_ACTIVE_MS = 1600;
const WS_RECONNECT_BASE_MS = 1000;
const WS_RECONNECT_MAX_MS = 30000;
const MAX_LIVE_EVENTS = 80;
const STOP_REQUEST_HOLD_MS = 15000;
const UI_STATE_STORAGE_KEY = 'aegis-agent.webconsole.ui-state.v1';
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
  meta: null,
  sessionId: nextEphemeralSessionId(),
  sessionBacked: false,
  sessionDetail: null,
  overview: null
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
  catalog: [],
  requestSeq: 0,
  renderSeq: 0,
  uploadInFlight: false
};

const overviewViewState = {
  requestSeq: 0,
  refreshing: false,
  needsRefresh: false,
  error: ''
};

const historyViewState = {
  data: null,
  page: 1,
  pageSize: 8,
  requestSeq: 0,
  renderSeq: 0,
  refreshing: false,
  needsRefresh: false,
  pendingRefreshOptions: null
};

const sessionViewState = {
  refreshing: false,
  needsRefresh: false
};

const fileChangesViewState = {
  sessionID: '',
  loaded: false,
  loading: false,
  requestSeq: 0
};

const queueJobViewState = {
  selectedJobId: '',
  selectedJobDetail: null
};

const navigationViewState = {
  currentView: 'chat'
};

const inspectorViewState = {
  tab: 'tasks'
};

const contextReportViewState = {
  sessionID: '',
  report: null,
  loading: false,
  error: '',
  requestSeq: 0
};

const historyExpansionViewState = {
  parentIds: new Set()
};

const messagePagingViewState = {
  requestSeq: 0,
  loadingEarlier: false,
  preserveScrollAfterRender: null,
  hasMoreMessages: false,
  oldestMessageId: '',
  loadedAllEarlierMessages: false,
  messageGapAnchorId: ''
};

const toastViewState = {
  counter: 0
};

const stopActionViewState = {
  sessionIds: new Set(),
  requestedAtBySessionId: new Map()
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

const liveEventsViewState = {
  events: []
};

const optimisticMessagesViewState = {
  messages: []
};

const pageLifecycleViewState = {
  visibilityHidden: false
};

const connectionViewState = {
  liveRelayConnected: false
};

const launchViewState = {
  inFlight: false
};

const runViewState = {
  generating: false
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

function currentLiveEvents() {
  return liveEventsViewState.events;
}

function hasLiveEvents() {
  return liveEventsViewState.events.length > 0;
}

function resetLiveEvents() {
  liveEventsViewState.events = [];
}

function currentOptimisticMessages() {
  return optimisticMessagesViewState.messages;
}

function resetOptimisticMessages() {
  optimisticMessagesViewState.messages = [];
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

function isLiveRelayConnected() {
  return connectionViewState.liveRelayConnected;
}

function setLiveRelayConnected(connected) {
  connectionViewState.liveRelayConnected = Boolean(connected);
}

function isLaunchInFlight() {
  return launchViewState.inFlight;
}

function setLaunchInFlight(inFlight) {
  launchViewState.inFlight = Boolean(inFlight);
}

function isGenerating() {
  return runViewState.generating;
}

function setGeneratingViewState(generating) {
  runViewState.generating = Boolean(generating);
}

function isLoadingEarlierMessages() {
  return messagePagingViewState.loadingEarlier;
}

function setLoadingEarlierMessages(loading) {
  messagePagingViewState.loadingEarlier = Boolean(loading);
}

function preserveScrollAfterRenderHeight() {
  return messagePagingViewState.preserveScrollAfterRender;
}

function setPreserveScrollAfterRenderHeight(height) {
  if (height === null || height === undefined) {
    messagePagingViewState.preserveScrollAfterRender = null;
    return;
  }
  const numeric = Number(height);
  messagePagingViewState.preserveScrollAfterRender = Number.isFinite(numeric) ? numeric : null;
}

function resetMessagePagingRenderState() {
  setLoadingEarlierMessages(false);
  setPreserveScrollAfterRenderHeight(null);
}

function hasMoreMessagesToLoad() {
  return messagePagingViewState.hasMoreMessages === true;
}

function setHasMoreMessagesToLoad(hasMore) {
  messagePagingViewState.hasMoreMessages = Boolean(hasMore);
}

function oldestLoadedMessageId() {
  return messagePagingViewState.oldestMessageId || '';
}

function setOldestLoadedMessageId(messageID) {
  messagePagingViewState.oldestMessageId = String(messageID || '');
}

function loadedAllEarlierMessages() {
  return messagePagingViewState.loadedAllEarlierMessages === true;
}

function setLoadedAllEarlierMessages(loadedAll) {
  messagePagingViewState.loadedAllEarlierMessages = Boolean(loadedAll);
}

function messageGapAnchorId() {
  return messagePagingViewState.messageGapAnchorId || '';
}

function setMessageGapAnchorId(messageID) {
  messagePagingViewState.messageGapAnchorId = String(messageID || '');
}

function resetMessagePagingWindowState() {
  messagePagingViewState.requestSeq += 1;
  setHasMoreMessagesToLoad(false);
  setOldestLoadedMessageId('');
  setLoadedAllEarlierMessages(false);
  setMessageGapAnchorId('');
  resetMessagePagingRenderState();
}

function currentOverviewError() {
  return overviewViewState.error || '';
}

function setOverviewError(message) {
  overviewViewState.error = String(message || '');
}

function currentHistoryData() {
  return historyViewState.data || null;
}

function setHistoryData(data) {
  historyViewState.data = data || null;
}

function currentHistoryPage() {
  const page = Number(historyViewState.page);
  return Number.isFinite(page) && page >= 1 ? Math.floor(page) : 1;
}

function setCurrentHistoryPage(page) {
  const nextPage = Number(page);
  historyViewState.page = Number.isFinite(nextPage) && nextPage >= 1 ? Math.floor(nextPage) : 1;
}

function currentHistoryPageSize() {
  const pageSize = Number(historyViewState.pageSize);
  return Number.isFinite(pageSize) && pageSize > 0 ? Math.floor(pageSize) : 8;
}

function resetHistoryViewData() {
  setHistoryData(null);
  setCurrentHistoryPage(1);
  invalidateHistoryRenderSeq();
}

function invalidateHistoryRenderSeq() {
  historyViewState.renderSeq += 1;
}

function currentHistoryRenderSeq() {
  return historyViewState.renderSeq || 0;
}

function isCurrentHistoryRenderSeq(renderSeq) {
  return currentHistoryRenderSeq() === renderSeq;
}

function currentViewName() {
  return navigationViewState.currentView || 'chat';
}

function setCurrentViewName(viewName) {
  navigationViewState.currentView = nodes.views[viewName] ? viewName : 'chat';
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

function invalidateSkillRenderSeq() {
  skillsViewState.renderSeq += 1;
}

function currentSkillRenderSeq() {
  return skillsViewState.renderSeq || 0;
}

function isCurrentSkillRenderSeq(renderSeq) {
  return currentSkillRenderSeq() === renderSeq;
}

function currentSkills() {
  return Array.isArray(skillsViewState.catalog) ? skillsViewState.catalog : [];
}

function setCurrentSkills(skills) {
  skillsViewState.catalog = Array.isArray(skills) ? skills : [];
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

function resetSessionContextReportState() {
  contextReportViewState.sessionID = '';
  contextReportViewState.report = null;
  contextReportViewState.loading = false;
  contextReportViewState.error = '';
  contextReportViewState.requestSeq += 1;
}

async function loadSessionContextReport(options = {}) {
  const sessionID = String(state.sessionDetail?.metadata?.id || state.sessionId || '').trim();
  if (!sessionID || isEphemeralSessionId(sessionID)) {
    return;
  }
  if (!options.force && contextReportViewState.sessionID === sessionID && contextReportViewState.report) {
    return;
  }
  const requestSeq = contextReportViewState.requestSeq + 1;
  contextReportViewState.requestSeq = requestSeq;
  contextReportViewState.sessionID = sessionID;
  contextReportViewState.loading = true;
  contextReportViewState.error = '';
  renderCurrentSession();
  try {
    const report = await requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/context`);
    if (contextReportViewState.requestSeq !== requestSeq || state.sessionId !== sessionID) {
      return;
    }
    contextReportViewState.report = report;
  } catch (err) {
    if (contextReportViewState.requestSeq !== requestSeq || state.sessionId !== sessionID) {
      return;
    }
    contextReportViewState.report = null;
    contextReportViewState.error = err?.message || 'Unable to load context telemetry.';
  } finally {
    if (contextReportViewState.requestSeq === requestSeq && state.sessionId === sessionID) {
      contextReportViewState.loading = false;
      renderCurrentSession();
    }
  }
}

function createEmptyChatRenderCache() {
  return {
    activity: null,
    flow: null,
    body: null,
    pending: null,
    rail: null,
    inspector: null,
    inspectorSlideOut: null,
    todoFloat: null,
    planModeInputActions: null
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
    renderState.chatCache[key] = null;
  }
}

function patchCachedMarkup(node, key, html, options = {}) {
  if (!node) {
    return false;
  }
  const markup = html || '';
  if (options.hideWhenEmpty) {
    node.hidden = markup === '';
  }
  if (renderState.chatCache[key] === markup) {
    return false;
  }
  node.innerHTML = markup;
  updateChatRenderCache(key, markup);
  if (markup && options.renderIcons !== false && window.lucide?.createIcons) {
    window.lucide.createIcons({ root: node });
  }
  return true;
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
  inputStatusText: document.getElementById('input-status-text'),
  planModeInputActions: document.getElementById('plan-mode-input-actions'),
  toastRack: document.getElementById('toast-rack'),
  skillUploadBtn: document.getElementById('skill-upload-btn'),
  skillUpload: document.getElementById('skill-upload'),
  skillsGrid: document.getElementById('skills-grid'),
  fileTree: document.getElementById('file-tree'),
  workspaceSubtitle: document.getElementById('workspace-subtitle'),
  workspaceRootChip: document.getElementById('workspace-root-chip'),
  workspaceNewFolderBtn: document.getElementById('workspace-new-folder-btn'),
  workspaceUploadBtn: document.getElementById('workspace-upload-btn'),
  workspaceUploadInput: document.getElementById('workspace-upload-input'),
  workspaceRefreshBtn: document.getElementById('workspace-refresh-btn'),
  workspaceDeleteDirBtn: document.getElementById('workspace-delete-dir-btn'),
  workspaceSelectionActions: document.getElementById('workspace-selection-actions'),
  workspaceSelectedChip: document.getElementById('workspace-selected-chip'),
  workspaceDownloadBtn: document.getElementById('workspace-download-btn'),
  workspaceRenameBtn: document.getElementById('workspace-rename-btn'),
  workspaceDeleteFileBtn: document.getElementById('workspace-delete-file-btn'),
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
    resetOptimisticMessages();
    resetLiveEvents();
    setNextSendInterruptArmed(false);
    setGeneratingViewState(false);
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
  switchView(currentViewName(), { skipPersist: true });
  renderCurrentSession();

  if (currentViewName() === 'chat' && nodes.chatInput) {
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
    setLiveRelayConnected(true);
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
    setLiveRelayConnected(false);
    runtimeHandles.ws = null;
    updateConnectionStatus();
    if (isGenerating()) {
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
    if (!isLiveRelayConnected()) {
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
  liveEventsViewState.events.push(event);
  if (liveEventsViewState.events.length > MAX_LIVE_EVENTS) {
    liveEventsViewState.events = liveEventsViewState.events.slice(-MAX_LIVE_EVENTS);
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
    const wasGenerating = isGenerating();
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

  // Event delegation map: data-attribute selector -> handler(element, event)
  const clickHandlers = [
    { selector: '[data-history-page]', handler: async (el) => {
      const direction = el.getAttribute('data-history-page');
      if (direction === 'prev') {
        await fetchHistory(Math.max(1, currentHistoryPage() - 1));
      } else if (direction === 'next') {
        const nextPage = currentHistoryData()?.total_pages
          ? Math.min(currentHistoryData().total_pages, currentHistoryPage() + 1)
          : currentHistoryPage() + 1;
        await fetchHistory(nextPage);
      }
    }},
    { selector: '[data-history-clear]', handler: async () => { await clearHistory(); } },
    { selector: '[data-load-earlier]', handler: async () => { await loadEarlierMessages(); } },
    { selector: '[data-view-shortcut]', handler: (el) => { switchView(el.getAttribute('data-view-shortcut')); } },
    { selector: '[data-delete-session]', handler: async (el) => {
      const sessionID = el.getAttribute('data-delete-session');
      if (sessionID) await deleteHistorySession(sessionID);
    }},
    { selector: '[data-stop-session-id]', handler: async (el, event) => {
      event.preventDefault();
      event.stopPropagation();
      const sessionID = el.getAttribute('data-stop-session-id');
      if (sessionID) {
        await requestStopSession(sessionID, {
          button: el,
          historyRenderSeq: currentViewName() === 'history' ? currentHistoryRenderSeq() : 0
        });
      }
    }},
    { selector: '[data-history-toggle-children]', handler: (el) => {
      const parentID = el.getAttribute('data-history-toggle-children');
      if (parentID) {
        if (historyExpansionViewState.parentIds.has(parentID)) {
          historyExpansionViewState.parentIds.delete(parentID);
        } else {
          historyExpansionViewState.parentIds.add(parentID);
        }
        renderHistory();
      }
    }},
    { selector: '[data-open-session]', handler: async (el) => {
      const sessionID = el.getAttribute('data-open-session');
      if (sessionID) await openSession(sessionID, { switchToChat: true });
    }},
    { selector: '[data-open-parent-session]', handler: async (el) => {
      const parentSessionID = el.getAttribute('data-open-parent-session');
      if (parentSessionID) await openSession(parentSessionID, { switchToChat: true });
    }},
    { selector: '[data-sub-agent-open]', handler: async (el) => {
      const sessionID = el.getAttribute('data-sub-agent-open');
      if (sessionID) {
        const label = (el.querySelector('.sa-tree-label')?.textContent || '').trim();
        const hint = label ? `Open child session "${label}"?` : 'Open child session?';
        if (await confirmLocalAction({
          title: 'Open child session',
          message: hint,
          confirmLabel: 'Open'
        })) {
          await openSession(sessionID, { switchToChat: true });
        }
      }
    }},
    { selector: '[data-inspector-tab], [data-focus-inspector-tab]', handler: async (el) => {
      const tab = el.getAttribute('data-inspector-tab') || el.getAttribute('data-focus-inspector-tab') || 'tasks';
      setInspectorTab(tab);
      renderCurrentSession();
      if (tab === 'context') {
        await loadSessionContextReport();
      }
    }},
    { selector: '[data-context-report-refresh]', handler: async (el) => {
      el.disabled = true;
      await loadSessionContextReport({ force: true });
    }},
    { selector: '[data-continue-session]', handler: async (el) => {
      const sessionID = el.getAttribute('data-continue-session');
      if (sessionID) {
        el.disabled = true;
        try {
          await requestContinueSession(sessionID);
        } finally {
          if (document.body.contains(el)) {
            el.disabled = false;
          }
        }
      }
    }},
    { selector: '[data-skill-action]', handler: (el) => {
      const id = el.getAttribute('data-skill-action');
      const isInstalled = el.getAttribute('data-skill-installed') === '1';
      handleSkillAction(id, isInstalled, el);
    }},
    { selector: '[data-goal-action]', handler: async (el) => { await handleGoalAction(el); } },
    { selector: '[data-plan-action]', handler: async (el) => { await handlePlanModeAction(el); } },
    { selector: '[data-plan-input-action]', handler: async (el) => { await handlePlanInputAction(el); } },
    { selector: '[data-todo-float-toggle]', handler: () => {
      setFloatingPanelExpanded('todo', !isFloatingPanelExpanded('todo'));
      persistUIState();
      invalidateChatRenderSlot('todoFloat');
      renderCurrentSession();
    }},
    { selector: '[data-files-float-toggle]', handler: () => {
      setFloatingPanelExpanded('files', !isFloatingPanelExpanded('files'));
      persistUIState();
      invalidateChatRenderSlot('todoFloat');
      renderCurrentSession();
    }},
    { selector: '[data-sub-agent-toggle]', handler: () => {
      setFloatingPanelExpanded('subAgents', !isFloatingPanelExpanded('subAgents'));
      persistUIState();
      invalidateChatRenderSlot('todoFloat');
      renderCurrentSession();
    }}
  ];

  document.addEventListener('click', async (event) => {
    for (const { selector, handler } of clickHandlers) {
      const el = event.target.closest(selector);
      if (el) {
        await handler(el, event);
        return;
      }
    }
  });

  document.addEventListener('change', handleSkillUploadChange);

  document.addEventListener('keydown', (event) => {
    const isInput = ['INPUT', 'TEXTAREA'].includes(event.target.tagName);
    
    // Always handle Escape even if we are in an input field (to stop generating)
    if (event.key === 'Escape' || event.key === 'Esc') {
      if (isGenerating() && hasDurableSession()) {
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
        if (isGenerating() && hasDurableSession()) {
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
        const wasGenerating = isGenerating();
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
  setCurrentViewName(viewName);
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
  if (isLaunchInFlight() && !hasDurableSession()) {
    showToast('Session launch is already in progress.', 'info');
    return;
  }

  const optimisticID = appendOptimisticMessage('user', text, {
    source: isGenerating() ? 'steer' : 'user',
    interrupt: isNextSendInterruptArmed() && isGenerating() && hasDurableSession()
  });

  nodes.chatInput.value = '';
  nodes.chatInput.style.height = 'auto';
  setComposerInputEmpty(true);
  updateUI();
  renderCurrentSession();

  if (isGenerating() && hasDurableSession()) {
    const sessionID = state.sessionId;
    const requestedInterrupt = isNextSendInterruptArmed();
    const actionSteerIdentity = currentSteerActionIdentity();
    try {
      await steerSession(sessionID, {
        message: text,
        interrupt: requestedInterrupt
      });
      if (state.sessionId !== sessionID || !isCurrentSteerActionIdentity(actionSteerIdentity)) {
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
      if (state.sessionId === sessionID && isCurrentSteerActionIdentity(actionSteerIdentity)) {
        removeOptimisticMessage(optimisticID);
        showToast(err.message || 'Failed to queue steer input.', 'error');
      }
    }
    if (state.sessionId === sessionID && isCurrentSteerActionIdentity(actionSteerIdentity)) {
      updateUI();
      renderCurrentSession();
    }
    return;
  }

  const currentStatus = state.sessionDetail?.state?.status || '';
  if (hasDurableSession() && ['awaiting_input', 'paused', 'failed', 'completed'].includes(currentStatus)) {
    const sessionID = state.sessionId;
    const actionPlanModeIdentity = currentPlanModeActionIdentity();
    let revisingPlanMode = false;
    let continuingSession = false;
    let actionContinueIdentity = '';
    try {
      const planMode = currentPlanMode();
      if (planMode?.status === 'awaiting_approval') {
        revisingPlanMode = true;
        await revisePlanMode(sessionID, text);
        if (state.sessionId !== sessionID || !isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
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
        continuingSession = true;
        actionContinueIdentity = currentContinueActionIdentity();
        setGenerating(true, {
          title: 'Continuing session',
          copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
          tone: 'live'
        });
        await continueSession(sessionID, {
          message: text,
          planMode: planDraft || undefined
        });
        if (state.sessionId !== sessionID || !isCurrentContinueActionIdentity(actionContinueIdentity)) {
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
      if (
        state.sessionId === sessionID &&
        (!revisingPlanMode || isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) &&
        (!continuingSession || isCurrentContinueActionIdentity(actionContinueIdentity))
      ) {
        removeOptimisticMessage(optimisticID);
        setGeneratingViewState(false);
        showToast(err.message || 'Failed to continue session.', 'error');
        updateUI();
        renderCurrentSession();
      }
    }
    return;
  }

  if (!hasDurableSession()) {
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
    const launchClientSessionID = state.sessionId;
    try {
      setLaunchInFlight(true);
      setGenerating(true, {
        title: 'Launching session',
        copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
        tone: 'live'
      });
      queueOverviewRefresh(220);
      const resp = await startSession({
        prompt: text,
        workdir: selectedWorkspaceWorkdir(),
        goal: goalDraft || undefined,
        planMode: planDraft || undefined
      });
      if (state.sessionId !== launchClientSessionID || state.sessionBacked) {
        return;
      }
      setComposerMode(null);
      adoptSession(resp.session_id, true);
      setLaunchInFlight(false);
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
      setGeneratingViewState(false);
      setLaunchInFlight(false);
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
  if (!isGenerating() || !hasDurableSession()) {
    return;
  }
  setNextSendInterruptArmed(!isNextSendInterruptArmed());
  updateUI();
}

async function requestInterrupt() {
  if (!isGenerating() || !hasDurableSession()) {
    showToast('No running session is available for interrupt.', 'info');
    return;
  }
  const sessionID = state.sessionId;
  const actionInterruptIdentity = currentInterruptActionIdentity();
  try {
    await interruptSession(sessionID);
    if (state.sessionId !== sessionID || !isCurrentInterruptActionIdentity(actionInterruptIdentity)) {
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
    if (state.sessionId === sessionID && isCurrentInterruptActionIdentity(actionInterruptIdentity)) {
      showToast(err.message || 'Failed to request interrupt.', 'error');
    }
  }
  if (state.sessionId === sessionID && isCurrentInterruptActionIdentity(actionInterruptIdentity)) {
    renderCurrentSession();
  }
}

async function requestStop() {
  if (!isGenerating() || !hasDurableSession()) {
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
    showToast('Stop already requested for this session.', 'info');
    return;
  }
  const selectedSessionID = state.sessionId || '';
  const stoppingSelectedSession = selectedSessionID === sessionID;
  const actionStopIdentity = stoppingSelectedSession ? currentStopActionIdentity() : '';
  const refreshSelectedSession = currentViewName() === 'chat' &&
    selectedSessionID &&
    selectedSessionID !== sessionID &&
    currentSessionReferencesSession(sessionID);
  const selectedReferenceIdentity = refreshSelectedSession ? currentReferencedSessionActionIdentity(sessionID) : '';
  const selectedStoppedSessionStillCurrent = () => (
    state.sessionId === sessionID &&
    (!stoppingSelectedSession || isCurrentStopActionIdentity(actionStopIdentity))
  );
  const selectedContextStillCurrent = () => (
    state.sessionId === selectedSessionID &&
    hasDurableSession() &&
    (!refreshSelectedSession || isCurrentReferencedSessionActionIdentity(selectedReferenceIdentity, sessionID))
  );
  const historyRenderSeq = Number(options.historyRenderSeq || 0);
  const historyContextStillCurrent = () => (
    !historyRenderSeq ||
    (currentViewName() === 'history' && isCurrentHistoryRenderSeq(historyRenderSeq))
  );
  const stopFallbackStillCurrent = () => (
    historyContextStillCurrent() &&
    (
      selectedStoppedSessionStillCurrent() ||
      (refreshSelectedSession && selectedContextStillCurrent()) ||
      (!stoppingSelectedSession && !refreshSelectedSession)
    )
  );
  stopActionViewState.sessionIds.add(sessionID);
  const button = options.button || null;
  if (button) {
    button.disabled = true;
  }
  try {
    const result = await requestStopViaBestAvailablePath(sessionID, {
      shouldFallback: stopFallbackStillCurrent
    });
    markStopRequested(sessionID);
    const stopRequestedViaSteer = result.via === 'steer';
    const stopRecoveredStaleRun = result.via === 'recovered';
    const stopCopy = stopRequestedViaSteer
      ? 'This run is being stopped through an interrupt steer request because no local handle is owned by this page.'
      : stopRecoveredStaleRun
        ? 'The stale running state was recovered after the previous web console process exited.'
        : 'The current run is being stopped. Partial output and tool results will remain visible.';
    const stopToast = stopRequestedViaSteer
      ? 'Stop requested through interrupt steer.'
      : stopRecoveredStaleRun
        ? 'Stale run recovered and paused.'
        : 'Stop requested.';
    if (selectedStoppedSessionStillCurrent()) {
      setLiveActivity({
        title: 'Stopping run',
        copy: stopCopy,
        tone: 'danger'
      });
      showToast(stopToast, 'success');
      queueSessionRefresh(120);
      queueOverviewRefresh(180);
    } else if (refreshSelectedSession && selectedContextStillCurrent()) {
      showToast(stopToast, 'success');
      queueSessionRefresh(120);
      queueOverviewRefresh(180);
    }
    if (currentViewName() === 'history' && historyContextStillCurrent()) {
      await fetchHistory(currentHistoryPage(), { showLoading: false, silentError: true });
    }
  } catch (err) {
    if (selectedStoppedSessionStillCurrent()) {
      showToast(err.message || 'Failed to stop the session.', 'error');
    } else if (refreshSelectedSession && selectedContextStillCurrent()) {
      showToast(err.message || 'Failed to stop the session.', 'error');
    }
  } finally {
    stopActionViewState.sessionIds.delete(sessionID);
    if (button && document.body.contains(button)) {
      button.disabled = false;
    }
    if (currentViewName() === 'history' && historyContextStillCurrent()) {
      renderHistory();
    }
    if (selectedStoppedSessionStillCurrent() || (refreshSelectedSession && selectedContextStillCurrent())) {
      renderCurrentSession();
    }
  }
}

async function requestStopViaBestAvailablePath(sessionID, options = {}) {
  try {
    const response = await stopSession(sessionID);
    return { via: response?.reconciled ? 'recovered' : 'handle' };
  } catch (err) {
    if (err?.code !== 'ACTIVE_HANDLE_NOT_OWNED') {
      throw err;
    }
    if (typeof options.shouldFallback === 'function' && !options.shouldFallback()) {
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
  const actionContinueIdentity = currentContinueActionIdentity();
  const isStillCurrent = () => state.sessionId === sessionID && isCurrentContinueActionIdentity(actionContinueIdentity);
  try {
    await continueSession(sessionID, {
      message,
      planMode: options.planMode
    });
    if (!isStillCurrent()) {
      return;
    }
    if (!options.silentToast) {
      showToast('Session continued.', 'success');
    }
    queueSessionRefresh(120);
    queueOverviewRefresh(180);
  } catch (err) {
    if (!options.silentToast && isStillCurrent()) {
      showToast(err.message || 'Failed to continue session.', 'error');
    }
    if (options.silentToast) {
      throw err;
    }
  }
}

function toggleInspectorSlideOut() {
  const slideOut = nodes.inspectorSlideOut;
  const backdrop = nodes.inspectorBackdrop;
  if (!slideOut || !backdrop) return;
  const isOpen = slideOut.classList.contains('is-open');
  if (isOpen) {
    closeInspectorSlideOut();
  } else {
    renderCurrentSession();
    slideOut.classList.add('is-open');
    backdrop.classList.add('is-open');
    slideOut.setAttribute('aria-hidden', 'false');
    backdrop.setAttribute('aria-hidden', 'false');
    nodes.inspectorToggleBtn?.setAttribute('aria-expanded', 'true');
  }
}

function closeInspectorSlideOut() {
  nodes.inspectorSlideOut?.classList.remove('is-open');
  nodes.inspectorBackdrop?.classList.remove('is-open');
  nodes.inspectorSlideOut?.setAttribute('aria-hidden', 'true');
  nodes.inspectorBackdrop?.setAttribute('aria-hidden', 'true');
  nodes.inspectorToggleBtn?.setAttribute('aria-expanded', 'false');
}

function adoptSession(sessionID, backed) {
  if (!sessionID) {
    return;
  }
  if (sessionID !== state.sessionId) {
    setSelectedQueueJob('');
    clearPlanInputSelections();
    resetMessagePagingWindowState();
    resetWorkspaceSessionSync();
    resetSessionFileChangesState();
    resetSessionContextReportState();
    if (typeof clearMarkdownCache === 'function') {
      clearMarkdownCache();
    }
  }
  state.sessionId = sessionID;
  state.sessionBacked = backed;
  if (backed) {
    setLaunchInFlight(false);
  }
  updateSessionId();
  persistUIState();
}

function resetChatSession() {
  state.sessionId = nextEphemeralSessionId();
  state.sessionBacked = false;
  state.sessionDetail = null;
  resetSessionContextReportState();
  resetOptimisticMessages();
  resetLiveEvents();
  setNextSendInterruptArmed(false);
  setSelectedQueueJob('');
  clearPlanInputSelections();
  setComposerMode(null);
  resetMessagePagingWindowState();
  resetWorkspaceSessionSync();
  syncWorkspaceToCurrentSession({ refresh: false });
  resetSessionFileChangesState();
  if (typeof clearMarkdownCache === 'function') {
    clearMarkdownCache();
  }
  setLiveActivity({
    title: 'Ready for a new session',
    copy: 'Send a prompt to create a durable session. Answers, tool calls, and running flow will appear here.',
    tone: 'neutral'
  });
  setGeneratingViewState(false);
  setLaunchInFlight(false);
  syncComposerInputEmpty();
  updateSessionId();
  persistUIState();
  renderCurrentSession();
  updateUI();
  syncPollingForState();
}

function setGenerating(value, activity) {
  setGeneratingViewState(value);
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
  if (!isLiveRelayConnected()) {
    nodes.connectionStatus.innerText = 'Disconnected';
    return;
  }
  nodes.connectionStatus.innerText = 'Agent Connected';
}

function updateSessionId() {
  const detail = state.sessionDetail;
  if (detail?.metadata) {
    nodes.sessionIdDisplay.innerText = `${humanizeStatus(sessionDetailDisplayStatus(detail) || 'loaded')} · ${detail.metadata.id} · ${detail.metadata.provider || 'provider'}/${detail.metadata.model || 'model'} · ${workdirBase(detail.metadata.workdir)}`;
  } else {
    nodes.sessionIdDisplay.innerText = `ID: ${state.sessionId}`;
  }
  updateConnectionStatus();
}

function updateUI() {
  const hasDraft = nodes.chatInput.value.trim().length > 0;
  const launchPending = isLaunchInFlight();
  const launchPendingWithoutSession = launchPending && !hasDurableSession();
  nodes.sendBtn.disabled = !hasDraft || launchPendingWithoutSession;
  nodes.sendBtn.classList.toggle('is-loading', (isGenerating() || launchPending) && hasDraft);
  nodes.sendBtn.classList.toggle('is-interrupt', isNextSendInterruptArmed() && isGenerating() && hasDurableSession());
  nodes.inputContainer.classList.toggle('is-busy', isGenerating() || launchPending);
  nodes.inputContainer.classList.toggle('is-offline', !isLiveRelayConnected());
  const directSessionControlAvailable = canUseDirectSessionControl();
  const stopSessionControlAvailable = canUseStopSessionControl();
  nodes.stopSessionBtn?.classList.toggle('is-visible', stopSessionControlAvailable);
  nodes.interruptSessionBtn?.classList.toggle('is-visible', directSessionControlAvailable);
  nodes.interruptToggleBtn?.classList.toggle('is-visible', isGenerating() && hasDurableSession());
  nodes.interruptToggleBtn?.classList.toggle('is-armed', isNextSendInterruptArmed() && isGenerating() && hasDurableSession());
  nodes.interruptToggleBtn?.setAttribute('aria-pressed', isNextSendInterruptArmed() ? 'true' : 'false');
  if (nodes.stopSessionBtn) {
    nodes.stopSessionBtn.disabled = !stopSessionControlAvailable || isStoppingSession(state.sessionId);
    nodes.stopSessionBtn.classList.toggle('is-loading', isStoppingSession(state.sessionId));
  }
  if (nodes.interruptSessionBtn) {
    nodes.interruptSessionBtn.disabled = !directSessionControlAvailable;
  }
  nodes.chatInput.placeholder = chatInputPlaceholder();

  nodes.inputStatusText.textContent = inputActionLabel();
  renderPlanModeInputActions();
  renderGoalComposer();

  if (!isLiveRelayConnected()) {
    nodes.connectionDot.className = 'dot';
    return;
  }
  nodes.connectionDot.className = isGenerating() ? 'dot busy' : 'dot online';
}

function chatInputPlaceholder() {
  const planMode = currentPlanMode();
  if (isGenerating() && hasDurableSession()) {
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
  const status = state.sessionDetail?.state?.status || '';
  if (hasDurableSession() && status === 'completed') {
    return 'Add a follow-up to continue this completed session...';
  }
  return 'Ask anything...';
}

function inputActionLabel() {
  if (!isLiveRelayConnected()) {
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
  if (isGenerating() && hasDurableSession()) {
    return isNextSendInterruptArmed()
      ? 'Interrupt armed: next send requests preemption, then merges your steer prompt.'
      : 'Steer running session: next send queues guidance into the current run.';
  }
  if (hasDurableSession() && (status === 'awaiting_input' || status === 'paused' || status === 'failed')) {
    return `Continue ${humanizeStatus(status)} session: next send resumes this durable session.`;
  }
  if (hasDurableSession() && status === 'completed') {
    return 'Completed session loaded: next send adds a follow-up and continues this session with its existing context.';
  }
  return 'Start new session: Enter sends, Shift+Enter / Ctrl+Enter inserts a line.';
}

function renderPlanModeInputActions() {
  if (!nodes.planModeInputActions) {
    return;
  }
  const planMode = currentPlanMode();
  const status = String(planMode?.status || '').toLowerCase();
  let html = '';
  if (status === 'awaiting_approval') {
    html = `
      <div class="plan-action-group" role="group" aria-label="Plan Mode approval actions">
        <button class="plan-action-btn primary" type="button" data-plan-action="approve" title="Approve the submitted plan and start execution">
          <i data-lucide="check-circle-2"></i>
          <span>Approve & Run</span>
        </button>
        <button class="plan-action-btn" type="button" data-plan-action="revise" title="Send your input as a plan revision request">
          <i data-lucide="corner-down-left"></i>
          <span>Ask for Changes</span>
        </button>
        <button class="plan-action-btn danger" type="button" data-plan-action="cancel" title="Cancel Plan Mode for this session">
          <i data-lucide="x-circle"></i>
          <span>Cancel</span>
        </button>
      </div>
    `;
  } else if (status === 'awaiting_user_input') {
    html = `
      <div class="plan-action-group" role="group" aria-label="Plan Mode input actions">
        <button class="plan-action-btn primary" type="button" data-focus-inspector-tab="plan" title="Open the pending Plan Mode question">
          <i data-lucide="corner-down-left"></i>
          <span>Open Plan Input</span>
        </button>
        <button class="plan-action-btn danger" type="button" data-plan-action="cancel" title="Cancel Plan Mode for this session">
          <i data-lucide="x-circle"></i>
          <span>Cancel</span>
        </button>
      </div>
    `;
  }
  patchCachedMarkup(nodes.planModeInputActions, 'planModeInputActions', html, { hideWhenEmpty: true });
}

function canShowGoalComposer() {
  const status = state.sessionDetail?.state?.status || '';
  return !isGenerating() && (!hasDurableSession() || status === 'completed');
}

function canShowPlanComposer() {
  const status = state.sessionDetail?.state?.status || '';
  const planMode = currentPlanMode();
  return !isGenerating() &&
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

function currentGoalActionIdentity() {
  const goal = state.sessionDetail?.goal;
  if (!goal || typeof goal !== 'object') {
    return '';
  }
  return [
    goal.goal_id || goal.id || '',
    goal.updated_at || '',
    goal.status || '',
    goal.objective || ''
  ].map((part) => String(part || '')).join('\n');
}

function isCurrentGoalActionIdentity(identity) {
  return currentGoalActionIdentity() === identity;
}

function currentPlanModeActionIdentity() {
  const planMode = currentPlanMode();
  if (!planMode || typeof planMode !== 'object') {
    return '';
  }
  return [
    planMode.plan_mode_id || planMode.id || '',
    planMode.updated_at || '',
    planMode.status || '',
    planMode.objective || '',
    planMode.plan_id || '',
    planMode.plan_version || '',
    planMode.approved_version || '',
    planMode.linked_goal_id || ''
  ].map((part) => String(part || '')).join('\n');
}

function isCurrentPlanModeActionIdentity(identity) {
  return currentPlanModeActionIdentity() === identity;
}

function currentPlanInputActionIdentity(requestID) {
  const planMode = currentPlanMode();
  const request = planMode?.pending_request;
  const currentRequestID = String(request?.request_id || '').trim();
  if (!request || !currentRequestID || currentRequestID !== String(requestID || '').trim()) {
    return '';
  }
  return [
    currentPlanModeActionIdentity(),
    currentRequestID
  ].join('\n');
}

function isCurrentPlanInputActionIdentity(identity, requestID) {
  return currentPlanInputActionIdentity(requestID) === identity;
}

function currentContinueActionIdentity() {
  const detail = state.sessionDetail;
  if (!detail || typeof detail !== 'object') {
    return '';
  }
  const metadata = detail.metadata || {};
  const sessionState = detail.state || {};
  return [
    state.sessionId || '',
    metadata.id || '',
    metadata.updated_at || '',
    sessionState.status || '',
    sessionState.updated_at || '',
    currentPlanModeActionIdentity()
  ].map((part) => String(part || '')).join('\n');
}

function isCurrentContinueActionIdentity(identity) {
  return currentContinueActionIdentity() === identity;
}

function currentSteerActionIdentity() {
  return currentContinueActionIdentity();
}

function isCurrentSteerActionIdentity(identity) {
  return currentSteerActionIdentity() === identity;
}

function currentInterruptActionIdentity() {
  return currentContinueActionIdentity();
}

function isCurrentInterruptActionIdentity(identity) {
  return currentInterruptActionIdentity() === identity;
}

function currentStopActionIdentity() {
  return currentContinueActionIdentity();
}

function isCurrentStopActionIdentity(identity) {
  return currentStopActionIdentity() === identity;
}

function currentReferencedSessionActionIdentity(sessionID) {
  const targetID = String(sessionID || '');
  if (!targetID || !currentSessionReferencesSession(targetID)) {
    return '';
  }
  return [
    currentContinueActionIdentity(),
    targetID,
    referencedSessionFactsIdentity(targetID)
  ].join('\n');
}

function isCurrentReferencedSessionActionIdentity(identity, sessionID) {
  return Boolean(identity) && currentReferencedSessionActionIdentity(sessionID) === identity;
}

function referencedSessionFactsIdentity(sessionID) {
  const targetID = String(sessionID || '');
  const detail = state.sessionDetail || {};
  const children = detail.children || {};
  const references = [];
  const addReference = (kind, parts) => {
    references.push([kind, ...parts].map((part) => String(part || '')).join('\t'));
  };

  if (detail.metadata?.id === targetID) {
    const sessionState = detail.state || {};
    addReference('detail', [
      detail.metadata.id,
      detail.metadata.updated_at,
      sessionState.status,
      sessionState.updated_at
    ]);
  }
  maybeArray(children.sessions).forEach((item) => {
    if (item?.id === targetID) {
      addReference('child-session', [
        item.id,
        item.status,
        item.updated_at,
        item.last_error
      ]);
    }
  });
  maybeArray(children.jobs).forEach((item) => {
    if (item?.session_id === targetID) {
      addReference('child-job', [
        item.id,
        item.session_id,
        item.status,
        item.updated_at,
        item.last_error
      ]);
    }
  });
  maybeArray(detail.background_notifications).forEach((item) => {
    if (item?.session_id === targetID) {
      addReference('background-notification', [
        item.id,
        item.session_id,
        item.queue_job_id,
        item.status,
        item.updated_at,
        item.last_error
      ]);
    }
  });
  const selectedJob = selectedQueueJobDetail();
  if (selectedJob?.session_id === targetID) {
    addReference('selected-queue-job', [
      selectedQueueJobId(),
      selectedJob.id,
      selectedJob.session_id,
      selectedJob.status,
      selectedJob.updated_at,
      selectedJob.last_error
    ]);
  }
  return references.join('\n');
}

async function handlePlanModeAction(button) {
  if (!hasDurableSession()) {
    showToast('No durable session is loaded.', 'info');
    return;
  }
  const sessionID = state.sessionId;
  const actionPlanModeIdentity = currentPlanModeActionIdentity();
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
        if (!isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
          return;
        }
        if (!await confirmCoverageOverride()) {
          if (state.sessionId === sessionID && isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
            showToast('Plan approval was not overridden.', 'info');
          }
          return;
        }
        if (state.sessionId !== sessionID || !isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
          return;
        }
        await approvePlanMode(sessionID, { override_coverage: true });
      }
      if (state.sessionId !== sessionID || !isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
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
      if (state.sessionId !== sessionID || !isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
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
    if (state.sessionId === sessionID && isCurrentPlanModeActionIdentity(actionPlanModeIdentity)) {
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
  const actionPlanInputIdentity = currentPlanInputActionIdentity(requestID);

  if (action === 'select') {
    const questionID = button.getAttribute('data-question-id') || '';
    const label = button.getAttribute('data-label') || '';
    const isOther = button.getAttribute('data-other') === '1';
    let value = button.getAttribute('data-value') || label;
    if (isOther) {
      value = await promptLocalAction({
        title: 'Custom Plan Mode Answer',
        message: 'Enter a custom answer for this Plan Mode question.',
        inputLabel: 'Answer',
        confirmLabel: 'Use Answer'
      }) || '';
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
    if (state.sessionId !== sessionID || !isCurrentPlanInputActionIdentity(actionPlanInputIdentity, requestID)) {
      return;
    }
    delete planInputViewState.selections[requestID];
    showToast('Plan input answered.', 'success');
    queueSessionRefresh(80);
    queueOverviewRefresh(180);
  } catch (err) {
    if (state.sessionId === sessionID && isCurrentPlanInputActionIdentity(actionPlanInputIdentity, requestID)) {
      showToast(err.message || 'Failed to answer Plan Mode input.', 'error');
    }
  } finally {
    if (document.body.contains(button)) {
      button.disabled = false;
    }
    if (state.sessionId === sessionID && isCurrentPlanInputActionIdentity(actionPlanInputIdentity, requestID)) {
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
  const actionGoalIdentity = currentGoalActionIdentity();
  const action = button.getAttribute('data-goal-action');
  button.disabled = true;
  try {
    if (action === 'pause') {
      await pauseGoal(sessionID);
      if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
        return;
      }
      showToast('Goal paused.', 'success');
    } else if (action === 'resume') {
      await resumeGoal(sessionID);
      if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
        return;
      }
      showToast('Goal resumed.', 'success');
    } else if (action === 'complete') {
      await completeGoal(sessionID);
      if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
        return;
      }
      showToast('Goal marked complete.', 'success');
    } else if (action === 'clear') {
      if (!await confirmGoalClear()) {
        if (state.sessionId === sessionID && isCurrentGoalActionIdentity(actionGoalIdentity)) {
          showToast('Goal clear cancelled.', 'info');
        }
        return;
      }
      if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
        return;
      }
      await deleteGoal(sessionID);
      if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
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
        if (!isCurrentGoalActionIdentity(actionGoalIdentity)) {
          return;
        }
        if (!await confirmCoverageOverride()) {
          if (state.sessionId === sessionID && isCurrentGoalActionIdentity(actionGoalIdentity)) {
            showToast('Goal plan approval was not overridden.', 'info');
          }
          return;
        }
        if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
          return;
        }
        response = await approveMissionPlan(sessionID, { override_coverage: true });
      }
      if (state.sessionId !== sessionID || !isCurrentGoalActionIdentity(actionGoalIdentity)) {
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
    if (state.sessionId === sessionID && isCurrentGoalActionIdentity(actionGoalIdentity)) {
      await refreshCurrentSession();
      queueOverviewRefresh(160);
    }
  } catch (err) {
    if (state.sessionId === sessionID && isCurrentGoalActionIdentity(actionGoalIdentity)) {
      showToast(err.message || 'Goal action failed.', 'error');
    }
  } finally {
    if (document.body.contains(button)) {
      button.disabled = false;
    }
    if (state.sessionId === sessionID && isCurrentGoalActionIdentity(actionGoalIdentity)) {
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
    if (currentViewName() === 'history') {
      fetchHistory(currentHistoryPage(), { showLoading: false, silentError: true });
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
  if (currentViewName() === 'history') {
    return true;
  }
  if (currentViewName() !== 'chat') {
    return false;
  }
  if (!isLiveRelayConnected()) {
    return true;
  }
  if (isGenerating()) {
    return true;
  }
  return hasDurableSession() && (
    !state.sessionDetail ||
    sessionDetailHasCurrentProcessHandle(state.sessionDetail) ||
    sessionDetailHasActiveDescendants(state.sessionDetail)
  );
}

function pollingIntervalForState() {
  if (currentViewName() === 'history') {
    return POLL_INTERVAL_MS;
  }
  if (!isLiveRelayConnected() ||
    isGenerating() ||
    sessionDetailHasCurrentProcessHandle(state.sessionDetail) ||
    sessionDetailHasActiveDescendants(state.sessionDetail)
  ) {
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
  if (currentViewName() !== 'chat') {
    return false;
  }
  if (!state.overview) {
    return true;
  }
  return isGenerating() ||
    !hasDurableSession() ||
    sessionDetailHasCurrentProcessHandle(state.sessionDetail) ||
    sessionDetailHasActiveDescendants(state.sessionDetail);
}

function shouldPollCurrentSession() {
  if (currentViewName() !== 'chat' || !hasDurableSession()) {
    return false;
  }
  return isGenerating() ||
    !state.sessionDetail ||
    sessionDetailHasCurrentProcessHandle(state.sessionDetail) ||
    sessionDetailHasActiveDescendants(state.sessionDetail);
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

function sessionDetailHasCurrentProcessHandle(detail) {
  return detail?.active_handle === true ||
    detail?.active_handle_owner?.owned_by_current_process === true;
}

function sessionDetailDisplayStatus(detail) {
  const status = detail?.state?.status || '';
  const planModeStatus = planModeActionDisplayStatus(detail?.plan_mode?.status);
  if (planModeStatus) {
    return planModeStatus;
  }
  if (sessionDetailHasCurrentProcessHandle(detail) && status !== 'completed') {
    return 'running';
  }
  return status;
}

function sessionSummaryDisplayStatus(item) {
  const planModeStatus = planModeActionDisplayStatus(item?.plan_mode_status);
  return planModeStatus || item?.status || 'unknown';
}

function planModeActionDisplayStatus(status) {
  switch (String(status || '').toLowerCase()) {
    case 'awaiting_approval':
      return 'awaiting_plan_approval';
    case 'awaiting_user_input':
      return 'awaiting_plan_input';
    default:
      return '';
  }
}

function isStoppableSessionStatus(status) {
  return String(status || '').toLowerCase() === 'running';
}

function canUseDirectSessionControl() {
  return isGenerating() &&
    hasDurableSession() &&
    state.sessionDetail?.active_handle === true &&
    state.sessionDetail?.active_handle_owner?.owned_by_current_process === true;
}

function canUseStopSessionControl() {
  return isGenerating() &&
    hasDurableSession() &&
    isStoppableSessionStatus(state.sessionDetail?.state?.status);
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
  const id = String(sessionID || '');
  if (!id) {
    return false;
  }
  pruneExpiredStopRequests();
  return stopActionViewState.sessionIds.has(id) || stopActionViewState.requestedAtBySessionId.has(id);
}

function markStopRequested(sessionID) {
  const id = String(sessionID || '');
  if (!id) {
    return;
  }
  stopActionViewState.requestedAtBySessionId.set(id, Date.now());
}

function clearStopRequested(sessionID) {
  const id = String(sessionID || '');
  if (!id) {
    return;
  }
  stopActionViewState.requestedAtBySessionId.delete(id);
}

function pruneExpiredStopRequests() {
  const now = Date.now();
  stopActionViewState.requestedAtBySessionId.forEach((requestedAt, sessionID) => {
    if (now - Number(requestedAt || 0) > STOP_REQUEST_HOLD_MS) {
      stopActionViewState.requestedAtBySessionId.delete(sessionID);
    }
  });
}

function clearSettledStopRequestsFromDetail(detail) {
  const clearIfSettled = (sessionID, ...statuses) => {
    const id = String(sessionID || '');
    if (!id) {
      return;
    }
    const active = statuses.some((status) => isStoppableSessionStatus(status));
    if (!active) {
      clearStopRequested(id);
    }
  };
  clearIfSettled(detail?.metadata?.id, detail?.state?.status);
  maybeArray(detail?.children?.sessions).forEach((item) => {
    clearIfSettled(item?.id, item?.status);
  });
  maybeArray(detail?.children?.jobs).forEach((item) => {
    clearIfSettled(item?.session_id, item?.session_status, item?.status);
  });
  maybeArray(detail?.background_notifications).forEach((item) => {
    clearIfSettled(item?.session_id, item?.session_status, item?.status);
  });
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
  if (status === 'cancelled') {
    return {
      title: 'Cancelled',
      copy: 'The child or queued run was explicitly cancelled. Durable events and partial tool results remain available for inspection.',
      tone: 'danger'
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

function sessionActivityForDetail(detail = {}) {
  const planModeStatus = String(detail?.plan_mode?.status || '').toLowerCase();
  if (planModeStatus === 'awaiting_approval') {
    return {
      title: 'Awaiting plan approval',
      copy: 'Review the submitted Plan Mode plan, then approve it to run, ask for changes, or cancel it.',
      tone: 'queued'
    };
  }
  if (planModeStatus === 'awaiting_user_input') {
    return {
      title: 'Awaiting plan input',
      copy: 'Answer the pending Plan Mode question before planning can continue.',
      tone: 'queued'
    };
  }
  return sessionActivityForState(detail?.state);
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
    setOverviewError('');
    if (currentViewName() === 'chat') {
      renderCurrentSession();
    }
  } catch (err) {
    if (overviewViewState.requestSeq !== requestSeq || overviewViewState.needsRefresh) {
      return;
    }
    console.error('overview error', err);
    if (!state.overview) {
      const message = overviewErrorMessage(err);
      setOverviewError(message);
      if (currentViewName() === 'chat') {
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
    clearSettledStopRequestsFromDetail(detail);
    syncWorkspaceToCurrentSession();
    await refreshSelectedQueueJobDetail(queueJobItems(detail?.children?.jobs), {
      isCurrent: () => state.sessionId === sessionID && !sessionViewState.needsRefresh
    });
    if (state.sessionId !== sessionID || sessionViewState.needsRefresh) {
      return;
    }
    updateSessionId();
    reconcileOptimisticMessages(detail);
    setHasMoreMessagesToLoad(loadedAllEarlierMessages() ? false : detail?.has_more_messages === true);
    const msgs = maybeArray(detail?.messages);
    setOldestLoadedMessageId(msgs.length > 0 ? msgs[0].id : '');
    if (detail?.state?.status === 'running' || sessionDetailHasCurrentProcessHandle(detail)) {
      setGeneratingViewState(true);
      if (!hasLiveEvents()) {
        setLiveActivity({
          title: detail?.state?.status === 'running' ? phaseHeadline(detail.state?.phase) : 'Starting turn',
          copy: 'The runner is active. Tool calls and child-agent transitions will stream into this panel as durable events.',
          tone: 'live'
        });
      }
    } else {
      setGeneratingViewState(false);
      if (!hasLiveEvents() || toneForStatus(detail?.state?.status) !== 'live') {
        setLiveActivity(sessionActivityForDetail(detail));
      }
      setNextSendInterruptArmed(false);
    }
    renderCurrentSession();
    updateUI();
    loadSessionFileChanges(sessionID).catch((err) => {
      console.error('session file changes error', err);
    });
    syncPollingForState();
  } catch (err) {
    if (state.sessionId !== sessionID || sessionViewState.needsRefresh) {
      return;
    }
    if (isMissingSessionError(err)) {
      handleMissingSelectedSession(sessionID, err);
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

function resetSessionFileChangesState() {
  fileChangesViewState.sessionID = '';
  fileChangesViewState.loaded = false;
  fileChangesViewState.loading = false;
  fileChangesViewState.requestSeq++;
}

async function loadSessionFileChanges(sessionID) {
  if (!sessionID || !hasDurableSession() || isEphemeralSessionId(sessionID)) {
    return;
  }
  if (fileChangesViewState.sessionID !== sessionID) {
    fileChangesViewState.sessionID = sessionID;
    fileChangesViewState.loaded = false;
    fileChangesViewState.loading = false;
  }
  if (fileChangesViewState.loaded || fileChangesViewState.loading) {
    return;
  }
  const requestSeq = ++fileChangesViewState.requestSeq;
  fileChangesViewState.loading = true;
  try {
    const resp = await requestJSON(`/api/sessions/${encodeURIComponent(sessionID)}/file-changes`);
    if (state.sessionId !== sessionID || fileChangesViewState.requestSeq !== requestSeq) {
      return;
    }
    if (state.sessionDetail?.metadata?.id === sessionID) {
      state.sessionDetail.file_changes = maybeArray(resp?.file_changes);
      renderCurrentSession();
    }
    fileChangesViewState.loaded = true;
  } finally {
    if (fileChangesViewState.requestSeq === requestSeq) {
      fileChangesViewState.loading = false;
    }
  }
}

function isMissingSessionError(err) {
  return err?.status === 404 &&
    (
      String(err?.code || '').toUpperCase() === 'NOT_FOUND' ||
      String(err?.message || '').includes('session.json') ||
      String(err?.detail || '').includes('session.json')
    );
}

function handleMissingSelectedSession(sessionID, err) {
  if (state.sessionId !== sessionID) {
    return;
  }
  resetChatSession();
  setLiveActivity({
    title: 'Session no longer available',
    copy: 'The previously selected session was removed locally. Start a new session or choose another entry from Sessions.',
    tone: 'queued'
  });
  showToast('Previously selected session is no longer available.', 'info');
  renderCurrentSession();
  updateUI();
  queueOverviewRefresh(120);
  syncPollingForState();
}

function showSessionLoadError(err, options = {}) {
  const message = err?.message || 'The session data could not be loaded.';
  setGeneratingViewState(false);
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
  if (isLoadingEarlierMessages() || !hasMoreMessagesToLoad() || !(messageGapAnchorId() || oldestLoadedMessageId())) {
    return;
  }
  const sessionID = state.sessionId;
  const fillingGap = Boolean(messageGapAnchorId());
  const beforeID = messageGapAnchorId() || oldestLoadedMessageId();
  const requestSeq = ++messagePagingViewState.requestSeq;
  setLoadingEarlierMessages(true);
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
      setOldestLoadedMessageId(mergedMessages.length > 0 ? mergedMessages[0].id : '');
      const activeGapAnchor = messageGapAnchorId();
      if (activeGapAnchor && activeGapAnchor !== beforeID) {
        setHasMoreMessagesToLoad(true);
        setLoadedAllEarlierMessages(false);
      } else if (fillingGap && resp?.has_more === true) {
        setMessageGapAnchorId(olderMessages[0]?.id || beforeID);
        setHasMoreMessagesToLoad(true);
        setLoadedAllEarlierMessages(false);
      } else {
        setMessageGapAnchorId('');
        setHasMoreMessagesToLoad(resp?.has_more === true);
        setLoadedAllEarlierMessages(resp?.has_more !== true);
      }
      mergeMessageTimelineEntries(state.sessionDetail);
      setPreserveScrollAfterRenderHeight(beforeScrollHeight);
    } else {
      setHasMoreMessagesToLoad(false);
      setLoadedAllEarlierMessages(true);
      setMessageGapAnchorId('');
    }
    setLoadingEarlierMessages(false);
    renderCurrentSession();
  } catch (err) {
    if (state.sessionId !== sessionID || messagePagingViewState.requestSeq !== requestSeq) {
      return;
    }
    console.error('load earlier messages error', err);
    const message = earlierMessagesErrorMessage(err);
    setLoadingEarlierMessages(false);
    showToast(message, 'error');
    renderCurrentSession();
  } finally {
    if (messagePagingViewState.requestSeq === requestSeq) {
      resetMessagePagingRenderState();
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
    setMessageGapAnchorId(merged.gapAnchorId);
    setLoadedAllEarlierMessages(false);
    return;
  }
  if (messageGapAnchorId() && merged.messages.some((message) => message?.id === messageGapAnchorId())) {
    setLoadedAllEarlierMessages(false);
    return;
  }
  setMessageGapAnchorId('');
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
  resetOptimisticMessages();
  setSelectedQueueJob('');
  resetMessagePagingWindowState();
  setNextSendInterruptArmed(false);
  resetLiveEvents();
  setGeneratingViewState(false);
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
    case 'awaiting_plan_approval':
    case 'awaiting_plan_input':
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
  optimisticMessagesViewState.messages.push({
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
  optimisticMessagesViewState.messages = optimisticMessagesViewState.messages.filter((message) => message.id !== id);
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
  optimisticMessagesViewState.messages = optimisticMessagesViewState.messages.filter((message) => {
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

async function fetchHistory(page = currentHistoryPage(), options = {}) {
  const requestedPage = Math.max(1, Number(page) || 1);
  if (historyViewState.refreshing) {
    setCurrentHistoryPage(requestedPage);
    historyViewState.needsRefresh = true;
    historyViewState.pendingRefreshOptions = options;
    persistUIState();
    return;
  }
  const container = nodes.views.history;
  const showLoading = options.showLoading ?? !currentHistoryData();
  const silentError = options.silentError ?? false;
  const pageSize = currentHistoryPageSize();
  const requestSeq = ++historyViewState.requestSeq;
  historyViewState.refreshing = true;
  historyViewState.needsRefresh = false;
  historyViewState.pendingRefreshOptions = null;
  setCurrentHistoryPage(requestedPage);
  invalidateHistoryRenderSeq();
  persistUIState();
  if (showLoading) {
    container.innerHTML = '<div class="view-loading">Loading sessions...</div>';
  }
  try {
    const data = await requestJSON(`/api/history?page=${encodeURIComponent(requestedPage)}&page_size=${encodeURIComponent(pageSize)}`);
    if (historyViewState.requestSeq !== requestSeq || currentHistoryPage() !== requestedPage) {
      return;
    }
    setHistoryData(data);
    renderHistory(data);
    refreshOverview().catch((err) => {
      console.error('overview refresh error', err);
    });
  } catch (err) {
    if (historyViewState.requestSeq !== requestSeq || currentHistoryPage() !== requestedPage) {
      return;
    }
    console.error('history error', err);
    const message = historyErrorMessage(err);
    if (!currentHistoryData()) {
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
      const nextPage = currentHistoryPage();
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
  invalidateHistoryRenderSeq();
  const container = nodes.views.history;
  const history = data || currentHistoryData();
  if (!history) {
    container.innerHTML = '<div class="empty-panel">No session data available yet.</div>';
    return;
  }
  const items = maybeArray(history.items);
  const total = Number(history.total || 0);
  const page = Number(history.page || 1);
  const pageSize = Number(history.page_size || currentHistoryPageSize() || 8);
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
      currentView: currentViewName(),
      historyPage: currentHistoryPage(),
      selectedSessionId,
      workspacePaths: workspacePathPreferences(),
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
  setCurrentViewName(nextView);
  if (Number.isFinite(nextHistoryPage) && nextHistoryPage >= 1) {
    setCurrentHistoryPage(nextHistoryPage);
  }
  if (typeof persisted.selectedSessionId === 'string' && persisted.selectedSessionId.trim()) {
    state.sessionId = persisted.selectedSessionId.trim();
    state.sessionBacked = true;
  }
  restoreWorkspacePathPreferences(persisted.workspacePaths);
  if (typeof persisted.todoFloatExpanded === 'boolean') {
    setFloatingPanelExpanded('todo', persisted.todoFloatExpanded);
  }
  if (typeof persisted.fileChangesExpanded === 'boolean') {
    setFloatingPanelExpanded('files', persisted.fileChangesExpanded);
  }
  if (typeof persisted.subAgentExpanded === 'boolean') {
    setFloatingPanelExpanded('subAgents', persisted.subAgentExpanded);
  }
  applyViewVisibility(currentViewName());
}

function renderHistorySessionCard(item, isChild, hasChildren, isExpanded, chevronSVG, childCount) {
  const displayStatus = sessionSummaryDisplayStatus(item);
  const metaText = item.last_error
    ? truncateText(item.last_error, 140)
    : item.plan_mode_status === 'awaiting_approval'
      ? `${item.model || item.provider || 'n/a'} · Plan approval needed`
      : item.plan_mode_status === 'awaiting_user_input'
        ? `${item.model || item.provider || 'n/a'} · Plan input needed`
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
          <span class="status-badge ${toneForStatus(displayStatus)}">${escapeHTML(humanizeStatus(displayStatus))}</span>
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
  const actionRenderSeq = currentHistoryRenderSeq();
  if (!await confirmLocalAction({
    title: 'Delete session',
    message: `Delete session ${sessionID}?`,
    confirmLabel: 'Delete',
    tone: 'danger'
  })) {
    return;
  }
  if (!isCurrentHistoryRenderSeq(actionRenderSeq)) {
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
    await fetchHistory(currentHistoryPage());
    if ((currentHistoryData()?.items || []).length === 0 && currentHistoryPage() > 1) {
      await fetchHistory(currentHistoryPage() - 1);
    }
    refreshOverview().catch(() => {});
  } catch (err) {
    showToast(err.message || 'Failed to delete session.', 'error');
  }
}

async function clearHistory() {
  const actionRenderSeq = currentHistoryRenderSeq();
  if (!await confirmLocalAction({
    title: 'Clear saved sessions',
    message: 'Clear all saved sessions? This will remove sessions and queue history.',
    confirmLabel: 'Clear all',
    tone: 'danger'
  })) {
    return;
  }
  if (!isCurrentHistoryRenderSeq(actionRenderSeq)) {
    return;
  }
  try {
    await requestJSON('/api/sessions/clear', {
      method: 'POST'
    });
    resetChatSession();
    resetHistoryViewData();
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
    invalidateSkillRenderSeq();
    nodes.skillsGrid.innerHTML = '<div class="view-loading">Loading local skills…</div>';
    const skills = await requestJSON('/api/skills');
    if (skillsViewState.requestSeq !== requestSeq) {
      return;
    }
    setCurrentSkills(skills);
    renderSkills(currentSkills());
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
  invalidateSkillRenderSeq();
  if (!skills.length) {
    nodes.skillsGrid.innerHTML = `
      <div class="empty-panel">
        <i data-lucide="package-open" class="empty-icon"></i>
        <strong>No local skills found.</strong>
        <span>Upload a .zip skill package to extend your agent's capabilities.</span>
        <button class="skill-btn install empty-upload-btn" type="button" id="empty-upload-btn">Upload .zip Skill</button>
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
    if (currentViewName() === 'skills') {
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
  const actionRenderSeq = currentSkillRenderSeq();
  if (!await confirmSkillUninstall(id)) {
    if (!isCurrentSkillRenderSeq(actionRenderSeq)) {
      return;
    }
    showToast('Skill uninstall cancelled.', 'info');
    return;
  }
  if (!isCurrentSkillRenderSeq(actionRenderSeq)) {
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
  syncWorkspaceToCurrentSession({ refresh: false });
  updateWorkspaceMeta();
}

document.addEventListener('DOMContentLoaded', init);
