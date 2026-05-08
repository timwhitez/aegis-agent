/**
 * Go CLI Agent Webconsole
 */

const POLL_INTERVAL_MS = 1600;
const MAX_LIVE_EVENTS = 80;
const UI_STATE_STORAGE_KEY = 'go-cli-agent.webconsole.ui-state.v1';

const SHORTCUTS = {
  'escape': 'stop',
  '/': 'command',
  'ctrl+k': 'search',
  'ctrl+n': 'new_session',
  'ctrl+,': 'settings',
  '?': 'help'
};

const state = {
  currentView: 'chat',
  isGenerating: false,
  isConnected: false,
  ws: null,
  meta: null,
  sessionId: nextEphemeralSessionId(),
  sessionBacked: false,
  sessionDetail: null,
  overview: null,
  historyData: null,
  historyPage: 1,
  historyPageSize: 8,
  queueData: null,
  selectedQueueJobId: '',
  inspectorTab: 'tasks',
  refreshingHistory: false,
  refreshingQueue: false,
  toastCounter: 0,
  skills: [],
  fileTree: [],
  workspacePath: '',
  optimisticMessages: [],
  liveEvents: [],
  liveActivity: {
    title: 'Ready for a new session',
    copy: 'Send a prompt to start a durable session. Tool activity will appear here as it runs.',
    tone: 'neutral'
  },
  chatRenderCache: {
    activity: '',
    flow: '',
    body: '',
    pending: '',
    rail: '',
    inspector: '',
    todoFloat: ''
  },
  nextSendInterrupt: false,
  pollHandle: null,
  refreshingOverview: false,
  refreshingSession: false,
  pendingSessionRefresh: null,
  pendingOverviewRefresh: null,
  lastInputWasEmpty: true,
  layoutObserver: null,
  showHelp: false,
  todoFloatExpanded: true,
  fileChangesExpanded: true,
  subAgentExpanded: true,
  expandedHistoryParents: new Set()
};

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
    queue: document.getElementById('queue-view'),
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
  setupWebSocket();
  setupEventListeners();
  setupLayoutObservers();
  if (hasDurableSession()) {
    state.sessionDetail = null;
    state.optimisticMessages = [];
    state.liveEvents = [];
    state.nextSendInterrupt = false;
    state.isGenerating = false;
    state.liveActivity = {
      title: 'Restoring session',
      copy: 'Loading the previously selected durable session.',
      tone: 'neutral'
    };
    updateSessionId();
  } else {
    resetChatSession({ notifyBackend: false });
  }
  startPolling();
  refreshMeta().catch(() => {});
  refreshOverview();
  if (hasDurableSession()) {
    try {
      await refreshCurrentSession();
    } catch (err) {
      console.error('session restore error', err);
      state.liveActivity = {
        title: 'Error restoring session',
        copy: err.message || 'The session data could not be loaded.',
        tone: 'danger'
      };
    }
  }
  switchView(state.currentView, { skipPersist: true });
  renderCurrentSession();

  if (state.currentView === 'chat' && nodes.chatInput) {
    state.lastInputWasEmpty = !nodes.chatInput.value.trim();
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

function setupWebSocket() {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${window.location.host}/ws`;
  const ws = new WebSocket(wsUrl);
  state.ws = ws;

  ws.onopen = () => {
    state.isConnected = true;
    updateConnectionStatus();
    updateUI();
    queueOverviewRefresh(120);
  };

  ws.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      handleServerEvent(data);
    } catch (err) {
      console.warn('ignored malformed websocket payload', err);
      showToast('Ignored malformed websocket payload from local server.', 'error');
    }
  };

  ws.onclose = () => {
    state.isConnected = false;
    state.ws = null;
    updateConnectionStatus();
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
  nodes.skillUploadBtn?.addEventListener('click', () => nodes.skillUpload?.click());
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
    const wasEmpty = state.lastInputWasEmpty;
    if (isNowEmpty !== wasEmpty || state.nextSendInterrupt) {
      state.lastInputWasEmpty = isNowEmpty;
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

    const historyExpandToggle = event.target.closest('[data-history-toggle-children]');
    if (historyExpandToggle) {
      const parentID = historyExpandToggle.getAttribute('data-history-toggle-children');
      if (parentID) {
        if (state.expandedHistoryParents.has(parentID)) {
          state.expandedHistoryParents.delete(parentID);
        } else {
          state.expandedHistoryParents.add(parentID);
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
        if (confirm(hint)) {
          await openSession(sessionID, { switchToChat: true });
        }
      }
      return;
    }

    const inspectorTab = event.target.closest('[data-inspector-tab], [data-focus-inspector-tab]');
    if (inspectorTab) {
      state.inspectorTab = inspectorTab.getAttribute('data-inspector-tab') || inspectorTab.getAttribute('data-focus-inspector-tab') || 'tasks';
      renderCurrentSession();
      return;
    }

    const queueJobButton = event.target.closest('[data-open-job]');
    if (queueJobButton) {
      state.selectedQueueJobId = queueJobButton.getAttribute('data-open-job') || '';
      switchView('queue');
      return;
    }

    const queueRefresh = event.target.closest('[data-queue-refresh]');
    if (queueRefresh) {
      await fetchQueue();
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

    const todoFloatToggle = event.target.closest('[data-todo-float-toggle]');
    if (todoFloatToggle) {
      state.todoFloatExpanded = !state.todoFloatExpanded;
      persistUIState();
      state.chatRenderCache.todoFloat = '';
      renderCurrentSession();
      return;
    }

    const filesFloatToggle = event.target.closest('[data-files-float-toggle]');
    if (filesFloatToggle) {
      state.fileChangesExpanded = !state.fileChangesExpanded;
      persistUIState();
      state.chatRenderCache.todoFloat = '';
      renderCurrentSession();
      return;
    }

    const subAgentToggle = event.target.closest('[data-sub-agent-toggle]');
    if (subAgentToggle) {
      state.subAgentExpanded = !state.subAgentExpanded;
      persistUIState();
      state.chatRenderCache.todoFloat = '';
      renderCurrentSession();
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
      await requestFormJSON('/api/skills/upload', formData, {
        method: 'POST',
      });
      showToast('Skill uploaded and extracted successfully.', 'success');
      if (state.currentView === 'skills') {
        await fetchSkills();
      }
    } catch (err) {
      showToast(err.message || 'Failed to upload skill zip.', 'error');
    }
    event.target.value = '';
  });

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
      case 'submit':
        if (nodes.chatInput && nodes.chatInput.value.trim()) {
          sendMessage();
        }
        break;
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
      case 'search':
        break;
      case 'new_session':
        const wasGenerating = state.isGenerating;
        resetChatSession({ notifyBackend: true });
        showToast(
          wasGenerating
            ? 'Started a new session. The previous run may still settle in the background.'
            : 'Started a new session.',
          'info'
        );
        break;
      case 'settings':
        switchView('settings');
        break;
      case 'help':
        state.showHelp = !state.showHelp;
        renderShortcutHelp();
        break;
    }
  });
}

function setupLayoutObservers() {
  updateDynamicLayoutMetrics();
  window.addEventListener('resize', updateDynamicLayoutMetrics);
  if (window.ResizeObserver && nodes.inputArea) {
    state.layoutObserver = new ResizeObserver(updateDynamicLayoutMetrics);
    state.layoutObserver.observe(nodes.inputArea);
    if (nodes.chatInput) {
      state.layoutObserver.observe(nodes.chatInput);
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
  queue: 'Background Jobs — Agent Console',
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
      state.lastInputWasEmpty = !nodes.chatInput.value.trim();
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
  if (viewName === 'queue') {
    fetchQueue();
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

  const optimisticID = appendOptimisticMessage('user', text, {
    source: state.isGenerating ? 'steer' : 'user',
    interrupt: state.nextSendInterrupt && state.isGenerating && hasDurableSession()
  });

  nodes.chatInput.value = '';
  nodes.chatInput.style.height = 'auto';
  state.lastInputWasEmpty = true;
  updateUI();
  renderCurrentSession();

  if (state.isGenerating && hasDurableSession()) {
    try {
      await steerSession(state.sessionId, {
        message: text,
        interrupt: state.nextSendInterrupt
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

  const currentStatus = state.sessionDetail?.state?.status || '';
  if (hasDurableSession() && ['awaiting_input', 'paused', 'failed'].includes(currentStatus)) {
    try {
      await requestContinueSession(state.sessionId, text, { silentToast: true });
      setGenerating(true, {
        title: 'Continuing session',
        copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
        tone: 'live'
      });
      showToast('Session continued.', 'success');
      queueSessionRefresh(60);
      queueOverviewRefresh(220);
    } catch (err) {
      removeOptimisticMessage(optimisticID);
      showToast(err.message || 'Failed to continue session.', 'error');
      updateUI();
      renderCurrentSession();
    }
    return;
  }

  if (!hasDurableSession() || currentStatus === 'completed') {
    try {
      const resp = await startSession({
        prompt: text,
        workdir: selectedWorkspaceWorkdir()
      });
      adoptSession(resp.session_id, true);
      setGenerating(true, {
        title: 'Launching session',
        copy: 'Bootstrapping a new turn. Tool calls, queue activity, and children will appear as durable events arrive.',
        tone: 'live'
      });
      queueSessionRefresh(60);
      queueOverviewRefresh(220);
    } catch (err) {
      removeOptimisticMessage(optimisticID);
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
  state.nextSendInterrupt = !state.nextSendInterrupt;
  updateUI();
}

async function requestInterrupt() {
  if (!state.isGenerating || !hasDurableSession()) {
    showToast('No running session is available for interrupt.', 'info');
    return;
  }
  try {
    await interruptSession(state.sessionId);
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

async function requestStop() {
  if (!state.isGenerating || !hasDurableSession()) {
    showToast('No running session is available to stop.', 'info');
    return;
  }
  try {
    await stopSession(state.sessionId);
    state.liveActivity = {
      title: 'Stopping run',
      copy: 'The current run is being stopped. Partial output and tool results will remain visible.',
      tone: 'danger'
    };
    showToast('Stop requested.', 'success');
    queueSessionRefresh(120);
  } catch (err) {
    showToast(err.message || 'Failed to stop the session.', 'error');
  }
  renderCurrentSession();
}

async function requestContinueSession(sessionID, message = '', options = {}) {
  try {
    await continueSession(sessionID, { message });
    if (!options.silentToast) {
      showToast('Session continued.', 'success');
    }
    queueSessionRefresh(120);
    queueOverviewRefresh(180);
    if (state.currentView === 'queue') {
      await fetchQueue();
    }
  } catch (err) {
    if (!options.silentToast) {
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
  state.sessionId = sessionID;
  state.sessionBacked = backed;
  updateSessionId();
  persistUIState();
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
  state.lastInputWasEmpty = !nodes.chatInput.value.trim();
  updateSessionId();
  persistUIState();
  void notifyBackend;
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
  nodes.sendBtn.disabled = !hasDraft;
  nodes.sendBtn.classList.toggle('is-loading', state.isGenerating && hasDraft);
  nodes.sendBtn.classList.toggle('is-interrupt', state.nextSendInterrupt && state.isGenerating && hasDurableSession());
  nodes.inputContainer.classList.toggle('is-busy', state.isGenerating);
  nodes.inputContainer.classList.toggle('is-offline', !state.isConnected);
  nodes.newSessionBtn?.classList.toggle('is-busy', state.isGenerating);
  nodes.stopSessionBtn?.classList.toggle('is-visible', state.isGenerating);
  nodes.interruptSessionBtn?.classList.toggle('is-visible', state.isGenerating);
  nodes.interruptToggleBtn?.classList.toggle('is-visible', state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.classList.toggle('is-armed', state.nextSendInterrupt && state.isGenerating && hasDurableSession());
  nodes.interruptToggleBtn?.setAttribute('aria-pressed', state.nextSendInterrupt ? 'true' : 'false');
  if (nodes.stopSessionBtn) {
    nodes.stopSessionBtn.disabled = !state.isGenerating || !hasDurableSession();
  }
  if (nodes.interruptSessionBtn) {
    nodes.interruptSessionBtn.disabled = !state.isGenerating || !hasDurableSession();
  }
  nodes.chatInput.placeholder = state.isGenerating && hasDurableSession()
    ? state.nextSendInterrupt
      ? 'Send an interrupt steer message to the running session...'
      : 'Send a steer message into the running session...'
    : 'Ask anything...';

  nodes.inputStatusText.textContent = inputActionLabel();

  if (!state.isConnected) {
    nodes.connectionDot.className = 'dot';
    return;
  }
  nodes.connectionDot.className = state.isGenerating ? 'dot busy' : 'dot online';
}

function inputActionLabel() {
  if (!state.isConnected) {
    return 'Live event relay reconnecting; REST session actions remain available.';
  }
  const status = state.sessionDetail?.state?.status || '';
  if (state.isGenerating && hasDurableSession()) {
    return state.nextSendInterrupt
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

function startPolling() {
  stopPolling();
  
  const pollStep = () => {
    let nextInterval = POLL_INTERVAL_MS;

    if (state.currentView === 'history') {
      fetchHistory(state.historyPage, { showLoading: false, silentError: true });
    } else {
      if (shouldPollChatOverview()) {
        refreshOverview();
      }
      if (shouldPollCurrentSession()) {
        refreshCurrentSession();
      }
      
      // If we are actively generating or disconnected, poll faster. Otherwise relax the interval.
      if (!state.isGenerating && state.isConnected) {
        nextInterval = Math.min(POLL_INTERVAL_MS * 3, 5000); // 3-5 seconds when idle
      }
    }
    
    state.pollHandle = window.setTimeout(pollStep, nextInterval);
  };
  
  state.pollHandle = window.setTimeout(pollStep, POLL_INTERVAL_MS);
}

function stopPolling() {
  if (state.pollHandle) {
    window.clearTimeout(state.pollHandle);
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

function shouldPollChatOverview() {
  if (state.currentView !== 'chat') {
    return false;
  }
  if (!state.overview) {
    return true;
  }
  return state.isGenerating || !hasDurableSession();
}

function shouldPollCurrentSession() {
  if (state.currentView !== 'chat' || !hasDurableSession()) {
    return false;
  }
  return state.isGenerating || !state.sessionDetail;
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

async function refreshOverview() {
  if (state.refreshingOverview) {
    return;
  }
  state.refreshingOverview = true;
  try {
    state.overview = await requestJSON('/api/overview');
    if (state.currentView === 'chat') {
      renderCurrentSession();
    }
  } catch (err) {
    console.error('overview error', err);
  } finally {
    state.refreshingOverview = false;
  }
}

async function refreshCurrentSession() {
  if (!hasDurableSession() || state.refreshingSession || isEphemeralSessionId(state.sessionId)) {
    return;
  }
  state.refreshingSession = true;
  try {
    const detail = await requestJSON(`/api/sessions/${encodeURIComponent(state.sessionId)}?limit=80`);
    state.sessionDetail = detail;
    updateSessionId();
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
        state.liveActivity = sessionActivityForState(detail?.state);
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
  if (nodes.sessionRail) {
    mutated = patchAuxSlot(nodes.sessionRail, 'rail', renderSessionRail()) || mutated;
  }
  if (nodes.inspectorPanel) {
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
    const sessions = maybeArray(parsed.sessions);
    const jobs = maybeArray(parsed.jobs);
    const sessionIds = new Set(sessions.map((s) => s.id));
    const orphanJobs = jobs.filter((job) => !sessionIds.has(job.session_id));
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
  return `
    <section class="panel-section">
      <div class="summary-grid wide">
        ${renderMetricCard('Child sessions', String(children.length), 'durable sessions')}
        ${renderMetricCard('Background jobs', String(jobs.length), 'queued or completed')}
        ${renderMetricCard('Notifications', String(notifications.length), 'background results')}
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
    </div>
  `;
}

function renderSubAgentJobRow(job) {
  const statusTone = toneForStatus(job.status);
  const label = agentLabel(job.agent_name, job.agent_role) || shortId(job.id);
  const targetAttr = job.session_id
    ? `data-sub-agent-open="${escapeAttr(job.session_id)}" title="Click to open child session"`
    : `data-open-job="${escapeAttr(job.id)}" title="Click to open queue job"`;
  return `
    <div class="sa-tree-row orphan" ${targetAttr}>
      <span class="sa-tree-dot ${statusTone}"></span>
      <span class="sa-tree-label">${escapeHTML(label)}</span>
      <span class="status-badge ${statusTone}">${escapeHTML(humanizeStatus(job.status))}</span>
      <span class="sa-tree-meta">${escapeHTML(job.mode || '')} · ${escapeHTML(shortId(job.id))}</span>
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
    <div class="job-card ${job.id === state.selectedQueueJobId ? 'active' : ''}" data-queue-job-id="${escapeAttr(job.id)}">
      <div class="job-card-top">
        <div class="job-card-title">${escapeHTML(agentLabel(job.agent_name, job.agent_role) || shortId(job.id))}</div>
        <span class="status-badge ${toneForStatus(job.status)}">${escapeHTML(humanizeStatus(job.status))}</span>
      </div>
      <div class="job-card-copy">${escapeHTML(truncateText(job.prompt || '(no prompt)', 180))}</div>
      <div class="job-card-meta">${escapeHTML(shortId(job.id))} · ${escapeHTML(job.mode || 'exec')}${job.session_id ? ` · child ${escapeHTML(shortId(job.session_id))}` : ''}</div>
      ${renderVisiblePaths(job.visible_paths)}
      <div class="card-actions">
        <button class="mini-link-btn" type="button" data-open-job="${escapeAttr(job.id)}">Open job</button>
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
  state.isGenerating = false;
  state.liveActivity = {
    title: 'Loading session',
    copy: 'Loading durable session detail and tool activity.',
    tone: 'neutral'
  };
  if (options.switchToChat !== false) {
    switchView('chat');
  }
  try {
    await refreshCurrentSession();
  } catch (err) {
    console.error('Failed to open session:', err);
    state.liveActivity = {
      title: 'Error loading session',
      copy: err.message || 'The session data could not be loaded.',
      tone: 'danger'
    };
    showToast('Error loading session.', 'error');
    renderCurrentSession();
  }
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

async function fetchQueue() {
  const container = nodes.views.queue;
  if (!container || state.refreshingQueue) return;
  state.refreshingQueue = true;
  // Save form state before potential innerHTML replacement
  const savedPrompt = document.getElementById('queue-prompt-input')?.value || '';
  const savedParent = document.getElementById('queue-parent-input')?.value || '';
  const savedAgent = document.getElementById('queue-agent-input')?.value || '';
  const savedModel = document.getElementById('queue-model-input')?.value || '';
  if (!state.queueData) {
    container.innerHTML = '<div class="view-loading">Loading queue...</div>';
  }
  try {
    state.queueData = await requestJSON('/api/queue/jobs?limit=80');
    renderQueueView({ savedPrompt, savedParent, savedAgent, savedModel });
  } catch (err) {
    container.innerHTML = `<div class="empty-panel">Failed to load queue. ${escapeHTML(err.message || '')}</div>`;
    showToast(err.message || 'Failed to load queue.', 'error');
  } finally {
    state.refreshingQueue = false;
  }
}

function renderQueueView(opts = {}) {
  const container = nodes.views.queue;
  if (!container) return;
  const jobs = maybeArray(state.queueData?.items || state.queueData);
  const fetchedCounters = jobs.reduce((acc, job) => {
    const status = job.status || 'queued';
    acc[status] = (acc[status] || 0) + 1;
    return acc;
  }, {});
  const queueCounters = state.queueData ? fetchedCounters : (state.overview?.queue_counters || {});
  const savedPrompt = opts.savedPrompt || '';
  const savedParent = opts.savedParent || '';
  const savedAgent = opts.savedAgent || '';
  const savedModel = opts.savedModel || '';

  const recentSessions = maybeArray(state.overview?.recent_sessions).slice(0, 8);
  const parentOptions = recentSessions.map((s) =>
    `<option value="${escapeAttr(s.id)}" ${s.id === savedParent ? 'selected' : ''}>${escapeHTML(shortId(s.id))} · ${escapeHTML(agentLabel(s.agent_name, s.agent_role) || 'master')} (${escapeHTML(s.status || '?')})</option>`
  ).join('');

  container.innerHTML = `
    <div class="view-header history-header">
      <div>
        <h2 class="view-title">Background Jobs</h2>
        <p class="view-subtitle">Submit agent tasks that run asynchronously and report results when complete.</p>
      </div>
      <button class="ghost-action-btn" type="button" data-queue-refresh>Refresh</button>
    </div>
    <section class="queue-primer">
      <span class="surface-chip">Queued ${escapeHTML(String(queueCounters.queued || 0))}</span>
      <span class="surface-chip">Running ${escapeHTML(String(queueCounters.running || 0))}</span>
      <span class="surface-chip">Failed ${escapeHTML(String(queueCounters.failed || 0))}</span>
      <span class="surface-chip">Completed ${escapeHTML(String(queueCounters.completed || 0))}</span>
    </section>
    <section class="panel-card queue-submit-panel">
      <div class="panel-card-header"><h3 class="view-title compact-title">Submit Job</h3></div>
      <div class="panel-card-body">
        <div class="settings-form">
          <div class="field">
            <label class="field-label">Prompt</label>
            <textarea id="queue-prompt-input" class="settings-input" placeholder="Describe the task for the background agent..." rows="3">${escapeHTML(savedPrompt)}</textarea>
          </div>
          <div class="field">
            <label class="field-label">Model override (optional)</label>
            <input id="queue-model-input" class="settings-input" type="text" placeholder="e.g. gpt-5.4 or claude-sonnet-4-6" value="${escapeAttr(savedModel)}">
          </div>
          <div class="field">
            <label class="field-label">Parent Session (optional)</label>
            <select id="queue-parent-input" class="settings-input">
              <option value="" ${!savedParent ? 'selected' : ''}>None (standalone job)</option>
              ${parentOptions}
              ${savedParent && !recentSessions.some((s) => s.id === savedParent) ? `<option value="${escapeAttr(savedParent)}" selected>${escapeHTML(shortId(savedParent))} (manual)</option>` : ''}
            </select>
          </div>
          <div class="field">
            <label class="field-label">Agent Role (optional)</label>
            <select id="queue-agent-input" class="settings-input">
              <option value="" ${!savedAgent ? 'selected' : ''}>Auto / none</option>
              <option value="planner" ${savedAgent === 'planner' ? 'selected' : ''}>planner</option>
              <option value="generator" ${savedAgent === 'generator' ? 'selected' : ''}>generator</option>
              <option value="evaluator" ${savedAgent === 'evaluator' ? 'selected' : ''}>evaluator</option>
            </select>
          </div>
          <button id="queue-submit-btn" class="skill-btn install queue-submit-btn">Submit Job</button>
        </div>
      </div>
    </section>
  `;
  if (window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: container });
  }

  const submitBtn = document.getElementById('queue-submit-btn');
  if (submitBtn) {
    submitBtn.addEventListener('click', async () => {
      const promptInput = document.getElementById('queue-prompt-input');
      const parentInput = document.getElementById('queue-parent-input');
      const modelInput = document.getElementById('queue-model-input');
      const agentInput = document.getElementById('queue-agent-input');
      const prompt = promptInput.value.trim();

      if (!prompt) {
        showToast('Prompt is required to submit a job.', 'error');
        return;
      }

      submitBtn.disabled = true;
      submitBtn.innerText = 'Submitting...';

      try {
        await submitQueueJob({
          prompt,
          parentSessionID: parentInput.value.trim(),
          model: modelInput.value.trim(),
          agentRole: agentInput.value.trim(),
          workdir: selectedWorkspaceWorkdir()
        });
        showToast('Background job submitted.', 'success');
        promptInput.value = '';
        await fetchQueue();
      } catch (err) {
        showToast(err.message || 'Failed to submit job.', 'error');
      } finally {
        if (document.body.contains(submitBtn)) {
          submitBtn.disabled = false;
          submitBtn.innerText = 'Submit Job';
        }
      }
    });
  }
}

async function fetchHistory(page = state.historyPage, options = {}) {
  if (state.refreshingHistory) {
    return;
  }
  const container = nodes.views.history;
  const showLoading = options.showLoading ?? !state.historyData;
  const silentError = options.silentError ?? false;
  state.refreshingHistory = true;
  state.historyPage = Math.max(1, Number(page) || 1);
  persistUIState();
  if (showLoading) {
    container.innerHTML = '<div class="view-loading">Loading sessions...</div>';
  }
  try {
    const data = await requestJSON(`/api/history?page=${encodeURIComponent(state.historyPage)}&page_size=${encodeURIComponent(state.historyPageSize)}`);
    state.historyData = data;
    renderHistory(data);
    refreshOverview().catch((err) => {
      console.error('overview refresh error', err);
    });
  } catch (err) {
    console.error('history error', err);
    if (!state.historyData) {
      container.innerHTML = '<div class="empty-panel">Failed to load recent activity.</div>';
    }
    if (!silentError) {
      showToast('Failed to load recent activity.', 'error');
    }
  } finally {
    state.refreshingHistory = false;
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
      const isExpanded = state.expandedHistoryParents.has(item.id);
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
      todoFloatExpanded: state.todoFloatExpanded,
      fileChangesExpanded: state.fileChangesExpanded,
      subAgentExpanded: state.subAgentExpanded
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
    state.todoFloatExpanded = persisted.todoFloatExpanded;
  }
  if (typeof persisted.fileChangesExpanded === 'boolean') {
    state.fileChangesExpanded = persisted.fileChangesExpanded;
  }
  if (typeof persisted.subAgentExpanded === 'boolean') {
    state.subAgentExpanded = persisted.subAgentExpanded;
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
        <button class="mini-link-btn" type="button" data-open-session="${escapeAttr(item.id)}">Open session</button>
        <button class="mini-link-btn danger" type="button" data-delete-session="${escapeAttr(item.id)}">Delete</button>
      </div>
    </div>
  `;
}

async function deleteHistorySession(sessionID) {
  if (!window.confirm(`Delete session ${sessionID}?`)) {
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
  if (!window.confirm('Clear all saved sessions? This will remove sessions and queue history.')) {
    return;
  }
  try {
    await requestJSON('/api/sessions/clear', {
      method: 'POST'
    });
    resetChatSession({ notifyBackend: false });
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
      emptyBtn.addEventListener('click', () => {
        document.getElementById('skill-upload')?.click();
      });
    }
    return;
  }
  nodes.skillsGrid.innerHTML = skills.map((skill) => {
    const isReadOnly = !!skill.read_only;
    const trustLine = skill.trust
      ? `<p class="skill-author">trust: ${escapeHTML(skill.trust)}${skill.disabled_reason ? ` · ${escapeHTML(skill.disabled_reason)}` : ''}</p>`
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
}

async function handleSkillAction(id, isInstalled, button) {
  if (!isInstalled) {
    const uploadInput = document.getElementById('skill-upload');
    if (uploadInput) {
      uploadInput.click();
    } else {
      showToast('Upload input not available.', 'error');
    }
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

async function renderSettings() {
  const container = nodes.views.settings;
  container.innerHTML = '<div class="view-loading">Loading backend settings…</div>';
  try {
    const configData = await requestJSON('/api/config');
    const providers = configData.providers || {};
    const defaultProvider = configData.default_provider || '';
    const guardrailsMode = configData.guardrails_mode || 'yolo';
    const disableHardTurnLimit = Boolean(configData.disable_hard_turn_limit);
    const maxTurnsHard = Number(configData.max_turns_hard || 0);
    const options = Object.keys(providers).map((providerName) => `
      <option value="${escapeAttr(providerName)}" ${providerName === defaultProvider ? 'selected' : ''}>${escapeHTML(providerName)}</option>
    `).join('');

    container.innerHTML = `
      <div class="view-header">
        <h2 class="view-title">Settings</h2>
        <p class="view-subtitle">Configure provider defaults, local API credentials, and guardrails mode. API keys are persisted to the local env file for future restarts.</p>
      </div>
      <div class="skill-card settings-card">
        <div class="settings-form">
          <div class="field">
            <label class="field-label">Guardrails Mode</label>
            <select id="settings-guardrails" class="settings-input">
              <option value="yolo" ${guardrailsMode === 'yolo' ? 'selected' : ''}>YOLO (default)</option>
              <option value="standard" ${guardrailsMode === 'standard' ? 'selected' : ''}>Standard</option>
            </select>
            <p class="view-subtitle settings-help">
              YOLO disables runtime retrieval, project-memory, and review-artifact guardrails for new or resumed turns.
            </p>
          </div>
          <div class="field">
            <label class="field-label">Hard Max Turns</label>
            <input id="settings-max-turns-hard" class="settings-input" type="number" min="1" step="1">
            <label class="check-row">
              <input id="settings-disable-hard-turn-limit" type="checkbox">
              <span>Disable hard turn limit</span>
            </label>
            <p class="view-subtitle settings-help">
              When disabled, the runtime will no longer fail a session with <code>max_turns_hard_exceeded</code>.
            </p>
          </div>
          <div class="field">
            <label class="field-label">API Provider</label>
            <select id="settings-provider" class="settings-input">
              ${options}
            </select>
          </div>
          <div class="field">
            <label class="field-label">Base URL</label>
            <input id="settings-baseurl" class="settings-input" type="text">
          </div>
          <div class="field">
            <label class="field-label">Model Name</label>
            <input id="settings-model" class="settings-input" type="text">
          </div>
          <div class="field">
            <label class="field-label">API Key</label>
            <input id="settings-apikey" class="settings-input" type="password" placeholder="Leave blank to keep existing persisted key...">
          </div>
          <button id="settings-save-btn" class="skill-btn install settings-save-btn">Save Changes</button>
        </div>
      </div>
    `;
    if (window.lucide && lucide.createIcons) {
      lucide.createIcons({ root: container });
    }

    const providerSelect = document.getElementById('settings-provider');
    const guardrailsSelect = document.getElementById('settings-guardrails');
    const maxTurnsHardInput = document.getElementById('settings-max-turns-hard');
    const disableHardTurnLimitInput = document.getElementById('settings-disable-hard-turn-limit');
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
      apiKeyInput.dataset.originalHasKey = provider.has_key ? 'true' : 'false';
    };

    providerSelect.addEventListener('change', syncProviderFields);
    syncProviderFields();
    maxTurnsHardInput.value = maxTurnsHard > 0 ? String(maxTurnsHard) : '40';
    disableHardTurnLimitInput.checked = disableHardTurnLimit;
    maxTurnsHardInput.disabled = disableHardTurnLimit;
    disableHardTurnLimitInput.addEventListener('change', () => {
      maxTurnsHardInput.disabled = disableHardTurnLimitInput.checked;
    });

    saveButton.addEventListener('click', async () => {
      saveButton.innerText = 'Saving...';
      saveButton.disabled = true;
      try {
        if (!disableHardTurnLimitInput.checked) {
          const parsed = Number.parseInt(maxTurnsHardInput.value, 10);
          if (!Number.isFinite(parsed) || parsed <= 0) {
            throw new Error('Hard max turns must be a positive integer, or disable the hard limit.');
          }
        }
        await saveConfig({
          guardrailsMode: guardrailsSelect.value,
          maxTurnsHard: Number.parseInt(maxTurnsHardInput.value || '0', 10),
          disableHardTurnLimit: disableHardTurnLimitInput.checked,
          provider: providerSelect.value,
          baseURL: baseURLInput.value,
          model: modelInput.value,
          apiKey: apiKeyInput.value === '••••••••••••••••' && apiKeyInput.dataset.originalHasKey === 'true' ? '' : apiKeyInput.value
        });
        showToast('Settings saved.', 'success');
        // Optimistic update: reflect saved state without full re-render
        saveButton.innerText = 'Saved';
        if (apiKeyInput.value !== '••••••••••••••••') {
          apiKeyInput.value = '••••••••••••••••';
          apiKeyInput.dataset.originalHasKey = 'true';
        }
        setTimeout(() => {
          if (document.body.contains(saveButton)) {
            saveButton.innerText = 'Save Changes';
          }
        }, 1500);
      } catch (err) {
        showToast(err.message || 'Failed to save configuration.', 'error');
      } finally {
        if (document.body.contains(saveButton)) {
          saveButton.innerText = 'Save Changes';
          saveButton.disabled = false;
        }
      }
    });
  } catch (err) {
    container.innerHTML = '<div class="empty-panel">Failed to load backend settings.</div>';
    showToast('Failed to load backend settings.', 'error');
  }
}

async function fetchWorkspace() {
  try {
    if (!state.meta) {
      await refreshMeta().catch(() => {});
    }
    updateWorkspaceMeta();
    nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
    nodes.editorFilename.innerText = workspaceDisplayName();
    nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
    await loadWorkspaceDirectory(state.workspacePath || '');
  } catch (err) {
    console.error('workspace error', err);
    nodes.fileTree.innerHTML = '<div class="empty-panel">Failed to load workspace.</div>';
    nodes.editorFilename.innerText = 'Workspace';
    nodes.editorContent.innerText = 'Failed to load workspace.';
    showToast('Failed to load workspace.', 'error');
  }
}

async function refreshMeta() {
  state.meta = await requestJSON('/api/meta');
  updateWorkspaceMeta();
}

function updateWorkspaceMeta() {
  if (nodes.workspaceSubtitle) {
    if (state.meta?.workspace_switch_supported) {
      nodes.workspaceSubtitle.textContent = 'Browse the active workspace and switch roots when needed.';
    } else {
      nodes.workspaceSubtitle.textContent = 'Browsing the selected workspace path. Use .. to move to the parent directory.';
    }
  }
  if (nodes.workspaceRootChip) {
    const root = String(state.meta?.workspace_root || '').trim();
    const selected = selectedWorkspaceWorkdir();
    nodes.workspaceRootChip.textContent = selected || root || 'current cwd';
    nodes.workspaceRootChip.title = selected || root || 'current cwd';
  }
}

async function loadWorkspaceDirectory(path = '') {
  const normalized = normalizeWorkspacePath(path);
  const queryPath = normalized || '.';
  nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
  const tree = await requestJSON(`/api/files?path=${encodeURIComponent(queryPath)}`);
  state.workspacePath = normalized;
  state.fileTree = tree;
  renderFileTree(tree);
  updateWorkspaceMeta();
  nodes.editorFilename.innerText = workspaceDisplayName();
  nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
}

function normalizeWorkspacePath(path = '') {
  const normalized = String(path || '')
    .replace(/\\/g, '/')
    .replace(/^\.\//, '')
    .replace(/^\/+|\/+$/g, '');
  return normalized === '.' ? '' : normalized;
}

function workspaceDisplayName() {
  return state.workspacePath ? `Workspace / ${state.workspacePath}` : 'Workspace';
}

function selectedWorkspaceWorkdir() {
  const root = String(state.meta?.workspace_root || '').trim();
  if (!root) {
    return '';
  }
  const rel = normalizeWorkspacePath(state.workspacePath);
  return rel ? `${root.replace(/\/+$/g, '')}/${rel}` : root;
}

function renderFileTree(tree, container = nodes.fileTree, level = 0) {
  if (level === 0) {
    container.innerHTML = '';
  }
  if (!Array.isArray(tree)) {
    return;
  }
  if (level === 0 && tree.length === 0) {
    container.innerHTML = state.workspacePath
      ? '<div class="empty-panel">This directory is empty.</div>'
      : '<div class="empty-panel">This workspace is empty.</div>';
    return;
  }

  tree.forEach((node) => {
    const itemWrapper = document.createElement('div');
    const button = document.createElement('button');
    button.className = 'tree-node';
    button.style.paddingLeft = `${16 + level * 16}px`;
    const icon = node.navigation === 'parent' ? 'corner-up-left' : node.type === 'directory' ? 'folder' : 'file-code';
    button.innerHTML = `<i data-lucide="${icon}" class="icon-small"></i><span>${escapeHTML(node.name)}</span>`;
    const childrenContainer = document.createElement('div');
    childrenContainer.className = 'tree-node-children';
    if (node.type === 'directory') {
      childrenContainer.classList.add('is-collapsed');
    }

    button.addEventListener('click', async () => {
      if (node.navigation === 'parent') {
        button.disabled = true;
        button.classList.add('is-loading');
        try {
          await loadWorkspaceDirectory(node.path || '');
        } catch (err) {
          showToast('Failed to load parent directory.', 'error');
        } finally {
          button.disabled = false;
          button.classList.remove('is-loading');
        }
        return;
      }
      if (node.type === 'file') {
        await loadFile(node.path);
        document.querySelectorAll('.tree-node').forEach((treeNode) => treeNode.classList.remove('active'));
        button.classList.add('active');
        return;
      }
      if (node.type === 'directory') {
        button.disabled = true;
        button.classList.add('is-loading');
        try {
          await loadWorkspaceDirectory(node.path);
        } catch (err) {
          nodes.editorFilename.innerText = node.path;
          nodes.editorContent.innerText = 'Error loading directory.';
          showToast(`Failed to load directory: ${node.path}`, 'error');
        } finally {
          button.disabled = false;
          button.classList.remove('is-loading');
        }
      }
    });

    itemWrapper.appendChild(button);
    itemWrapper.appendChild(childrenContainer);
    container.appendChild(itemWrapper);
    if (node.type === 'directory' && node.children) {
      renderFileTree(node.children, childrenContainer, level + 1);
    }
  });

  if (level === 0 && window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: nodes.fileTree });
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
