import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const utilsSource = readFileSync(new URL('../../internal/webconsole/assets/utils.js', import.meta.url), 'utf8');
const i18nSource = readFileSync(new URL('../../internal/webconsole/assets/i18n.js', import.meta.url), 'utf8');
const sessionViewSource = readFileSync(new URL('../../internal/webconsole/assets/session-view.js', import.meta.url), 'utf8');
const settingsViewSource = readFileSync(new URL('../../internal/webconsole/assets/settings-view.js', import.meta.url), 'utf8');
const workspaceViewSource = readFileSync(new URL('../../internal/webconsole/assets/workspace-view.js', import.meta.url), 'utf8');
const appSource = readFileSync(new URL('../../internal/webconsole/assets/app.js', import.meta.url), 'utf8');
const stylesSource = readFileSync(new URL('../../internal/webconsole/assets/styles.css', import.meta.url), 'utf8');
const context = {
  console: {
    warn() {}
  },
  window: {
    getComputedStyle() {
      return { display: 'block' };
    }
  },
  document: {
    getElementById() {
      return null;
    },
    createElement() {
      return {
        appendChild() {},
        addEventListener() {},
        remove() {},
        classList: { contains() { return false; } },
        innerHTML: ''
      };
    },
    body: {
      appendChild() {}
    }
  },
  localStorage: {
    getItem() {
      return null;
    },
    setItem() {},
    removeItem() {}
  },
  setTimeout() {
    return 0;
  },
  clearTimeout() {},
  setInterval() {
    return 0;
  },
  clearInterval() {},
  fetch() {
    throw new Error('fetch is not available in renderer tests');
  }
};

vm.createContext(context);
vm.runInContext(utilsSource, context, { filename: 'utils.js' });
vm.runInContext(`
  const state = {};
  const optimisticMessagesViewState = { messages: [] };
  const queueJobViewState = { selectedJobId: '', selectedJobDetail: null };
  const inspectorViewState = { tab: 'tasks' };
  const contextReportViewState = { sessionID: '', report: null, loading: false, error: '', requestSeq: 0 };
  const stopActionViewState = { sessionIds: new Set() };
  const messagePagingViewState = { loadingEarlier: false, preserveScrollAfterRender: null, hasMoreMessages: false };
  function selectedQueueJobId() {
    return queueJobViewState.selectedJobId || '';
  }
  function currentOptimisticMessages() {
    return optimisticMessagesViewState.messages;
  }
  function selectedQueueJobDetail() {
    return queueJobViewState.selectedJobDetail || null;
  }
  function setSelectedQueueJob(id, detail = null) {
    queueJobViewState.selectedJobId = String(id || '');
    queueJobViewState.selectedJobDetail = detail || null;
  }
  function activeInspectorTab() {
    return inspectorViewState.tab || 'tasks';
  }
  function setInspectorTab(tab) {
    inspectorViewState.tab = String(tab || 'tasks');
  }
  function agentLabel(name, role) {
    if (name && role) return name + ' · ' + role;
    return name || role || '';
  }
  function toneForStatus(status) {
    switch (String(status || '').toLowerCase()) {
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
    if (!status) return 'Unknown';
    const normalized = String(status).replaceAll('_', ' ').replaceAll('-', ' ').replace(/\\s+/g, ' ').trim();
    return normalized.charAt(0).toUpperCase() + normalized.slice(1);
  }
  function isStoppableSessionStatus(status) {
    const normalized = String(status || '').toLowerCase();
    return normalized === 'running' || normalized === 'awaiting_input' || normalized === 'paused';
  }
  function isStoppingSession(sessionID) {
    return stopActionViewState.sessionIds.has(sessionID);
  }
  function currentOverviewError() {
    return '';
  }
  function isLoadingEarlierMessages() {
    return messagePagingViewState.loadingEarlier;
  }
  function hasMoreMessagesToLoad() {
    return messagePagingViewState.hasMoreMessages === true;
  }
  function oldestLoadedMessageId() {
    return messagePagingViewState.oldestMessageId || '';
  }
  function loadedAllEarlierMessages() {
    return messagePagingViewState.loadedAllEarlierMessages === true;
  }
  function messageGapAnchorId() {
    return messagePagingViewState.messageGapAnchorId || '';
  }
  function preserveScrollAfterRenderHeight() {
    return messagePagingViewState.preserveScrollAfterRender;
  }
  function isFloatingPanelExpanded() {
    return true;
  }
  function isNextSendInterruptArmed() {
    return false;
  }
  function collectRecentToolEntries() {
    return [];
  }
  function phaseHeadline(phase) {
    return humanizeStatus(phase);
  }
`, context, { filename: 'renderer-test-stubs.js' });
vm.runInContext(sessionViewSource, context, { filename: 'session-view.js' });
vm.runInContext(settingsViewSource, context, { filename: 'settings-view.js' });

function sameRealm(value) {
  return JSON.parse(JSON.stringify(value));
}

function fakeAppElement(initial = {}) {
  const classNames = new Set();
  const attrs = {};
  const children = [];
  const element = {
    value: initial.value || '',
    checked: Boolean(initial.checked),
    disabled: false,
    dataset: {},
    listeners: {},
    style: {},
    classList: {
      add(...names) {
        names.forEach((name) => classNames.add(name));
      },
      remove(...names) {
        names.forEach((name) => classNames.delete(name));
      },
      toggle(name, force) {
        if (force === true) {
          classNames.add(name);
          return true;
        }
        if (force === false) {
          classNames.delete(name);
          return false;
        }
        if (classNames.has(name)) {
          classNames.delete(name);
          return false;
        }
        classNames.add(name);
        return true;
      },
      contains(name) {
        return classNames.has(name) || String(element.className || '').split(/\s+/).includes(name);
      }
    },
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
    innerHTML: '',
    innerText: '',
    textContent: '',
    hidden: false,
    offsetParent: {},
    addEventListener(event, callback) {
      this.listeners[event] = callback;
    },
    appendChild(child) {
      children.push(child);
      child.parentNode = this;
      return child;
    },
    replaceChildren(...newChildren) {
      children.splice(0, children.length);
      newChildren.forEach((child) => this.appendChild(child));
      const text = newChildren.map((child) => String(child?.innerText ?? child?.textContent ?? '')).join('');
      this.innerText = text;
      this.textContent = text;
    },
    remove() {},
    focus() {
      this.focused = true;
    },
    click() {},
    dispatchEvent() {},
    querySelector(selector) {
      return this.querySelectorAll(selector)[0] || null;
    },
    querySelectorAll(selector) {
      const results = [];
      const matches = (node) => {
        if (selector === '.tree-node') {
          return String(node.className || '').split(/\s+/).includes('tree-node') || node.classList?.contains?.('tree-node');
        }
        if (selector === '.tree-node.active') {
          return (String(node.className || '').split(/\s+/).includes('tree-node') || node.classList?.contains?.('tree-node')) &&
            node.classList?.contains?.('active');
        }
        return false;
      };
      const walk = (node) => {
        if (matches(node)) {
          results.push(node);
        }
        const nodeChildren = node.__children || [];
        nodeChildren.forEach(walk);
      };
      children.forEach(walk);
      return results;
    },
    getAttribute(name) {
      return attrs[name] || null;
    },
    setAttribute(name, value) {
      attrs[name] = String(value);
    },
    removeAttribute(name) {
      delete attrs[name];
    },
    scrollTo() {}
  };
  element.__children = children;
  element.__attrs = attrs;
  return element;
}

function fakeActionButton(attrs = {}) {
  return {
    ...fakeAppElement(),
    getAttribute(name) {
      return attrs[name] || null;
    }
  };
}

function collectFakeElementsByClass(root, className) {
  const results = [];
  const walk = (node) => {
    if (!node || typeof node !== 'object') {
      return;
    }
    if (node.classList?.contains?.(className)) {
      results.push(node);
    }
    for (const child of node.__children || []) {
      walk(child);
    }
  };
  walk(root);
  return results;
}

function createAppHarnessContext(initialStorage = {}) {
  const pendingRequests = [];
  const storage = new Map(Object.entries(initialStorage));
  const localStorage = {
    getItem(key) {
      return storage.has(key) ? storage.get(key) : null;
    },
    setItem(key, value) {
      storage.set(key, String(value));
    },
    removeItem(key) {
      storage.delete(key);
    }
  };
  const appContext = {
    console: {
      error() {},
      warn() {}
    },
    window: {
      location: {
        protocol: 'http:',
        host: '127.0.0.1:8080'
      },
      innerWidth: 1280,
      getComputedStyle() {
        return { display: 'block' };
      },
      matchMedia() {
        return { matches: false };
      },
      setTimeout() {
        return 0;
      },
      clearTimeout() {},
      setInterval() {
        return 0;
      },
      clearInterval() {},
      confirm() {
        return true;
      },
      localStorage
    },
    document: {
      listeners: {},
      getElementById() {
        return fakeAppElement();
      },
      createElement() {
        return fakeAppElement();
      },
      querySelector() {
        return fakeAppElement();
      },
      querySelectorAll() {
        return [];
      },
      addEventListener(event, callback) {
        this.listeners[event] = callback;
      },
      body: {
        contains() {
          return true;
        },
        appendChild() {}
      }
    },
    localStorage,
    setTimeout() {
      return 0;
    },
    clearTimeout() {},
    setInterval() {
      return 0;
    },
    clearInterval() {},
    renderCurrentSession() {},
    FormData: class FakeFormData {
      constructor() {
        this.entries = [];
      }
      append(name, value) {
        this.entries.push([name, value]);
      }
    },
    requestJSON(url, payload) {
      return new Promise((resolve, reject) => {
        pendingRequests.push({ url, payload, resolve, reject });
      });
    },
    requestFormJSON(url, formData, options = {}) {
      return new Promise((resolve, reject) => {
        pendingRequests.push({ url, formData, options, resolve, reject });
      });
    }
  };
  vm.createContext(appContext);
  vm.runInContext(utilsSource, appContext, { filename: 'utils.js' });
  vm.runInContext(workspaceViewSource, appContext, { filename: 'workspace-view.js' });
  vm.runInContext(`
    function workdirBase(path) {
      const text = String(path || '').replaceAll('\\\\', '/');
      if (!text) return 'workspace';
      return text.split('/').filter(Boolean).pop() || text;
    }
  `, appContext);
  vm.runInContext(appSource, appContext, { filename: 'app.js' });
  appContext.pendingRequests = pendingRequests;
  appContext.storage = storage;
  return appContext;
}

test('global new session navigates from Settings to the visible chat composer', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    setupEventListeners();
    applyViewVisibility('settings');
    nodes.newSessionBtn.listeners.click();
    return {
      view: currentViewName(),
      chatHidden: nodes.views.chat.classList.contains('is-hidden'),
      settingsHidden: nodes.views.settings.classList.contains('is-hidden'),
      composerFocused: nodes.chatInput.focused === true
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    view: 'chat',
    chatHidden: false,
    settingsHidden: true,
    composerFocused: true
  });
});

test('session inspector exposes a lazy Context tab and renders bounded lineage telemetry without raw content', () => {
  const rendered = vm.runInContext(`(() => {
    state.sessionId = 'context_session';
    state.sessionDetail = {
      metadata: { id: 'context_session' },
      state: { status: 'completed' },
      task_board: { todo: [], tasks: [] },
      children: { sessions: [], jobs: [] }
    };
    setInspectorTab('tasks');
    const ordinary = renderInspectorPanel();
    setInspectorTab('context');
    const lazy = renderInspectorPanel();
    contextReportViewState.sessionID = 'context_session';
    contextReportViewState.report = {
      schema_version: 1,
      root_session_id: 'context_session',
      aggregate: {
        root_peak_estimated_input_tokens: 900,
        child_peak_estimated_input_tokens: 800,
        root_aggregate_estimated_input_tokens: 950,
        child_aggregate_estimated_input_tokens: 1400,
        total_estimated_input_tokens: 2350,
        unknown_usage_request_count: 2,
        total_provider_usage: {
          input_tokens: 1400,
          output_tokens: 100,
          cache_read_input_tokens: 25,
          cache_creation_input_tokens: 5
        },
        session_count: 3,
        child_session_count: 2,
        total_request_count: 4,
        total_turn_count: 4,
        total_tool_call_count: 11,
        total_compaction_count: 1,
        wall_time_ms: 1200
      },
      sessions: [
        { session_id: 'context_session', metrics: { peak_estimated_input_tokens: 900, aggregate_estimated_input_tokens: 950, request_count: 2 } },
        { session_id: 'context_child', agent_role: 'explorer', metrics: { peak_estimated_input_tokens: 800, aggregate_estimated_input_tokens: 1400, request_count: 2 }, requests: [{ raw_prompt: 'WEB_CONTEXT_PROMPT_SENTINEL', tool_output: 'WEB_CONTEXT_TOOL_SENTINEL' }] }
      ],
      truncation: { truncated: true, omitted_session_count: 1, omitted_request_count: 7 }
    };
    const loaded = renderInspectorPanel();
    return { ordinary, lazy, loaded };
  })()`, context);

  assert.match(rendered.ordinary, /data-inspector-tab="context"/);
  assert.match(rendered.lazy, /loaded only when this inspector tab is opened/);
  assert.match(rendered.loaded, /Root peak/);
  assert.match(rendered.loaded, /<span>Context report<\/span> v<span translate="no" data-i18n-skip>1<\/span>/);
  assert.match(rendered.loaded, /<h4 translate="no" data-i18n-skip>contex…sion<\/h4>/);
  assert.match(rendered.loaded, />900</);
  assert.match(rendered.loaded, /Child aggregate/);
  assert.match(rendered.loaded, />1,400</);
  assert.match(rendered.loaded, /Total input/);
  assert.match(rendered.loaded, />2,350</);
  assert.match(rendered.loaded, /Unknown usage/);
  assert.match(rendered.loaded, />2</);
  assert.match(rendered.loaded, /Bounded view: 1 sessions and 7 requests omitted\. Aggregate totals are complete\./);
  assert.match(rendered.loaded, /<span translate="no" data-i18n-skip>explorer<\/span>/);
  assert.doesNotMatch(rendered.loaded, /WEB_CONTEXT_(PROMPT|TOOL)_SENTINEL/);
});

test('context endpoint is called only after opening Context or requesting Refresh', async () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });
  vm.runInContext(`
    setupEventListeners();
    renderCurrentSession = function() {};
    state.sessionId = 'context_lazy_session';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'context_lazy_session' },
      state: { status: 'completed' }
    };
  `, appContext);

  const delegatedClick = (selector, attributes = {}) => {
    const button = {
      disabled: false,
      focused: false,
      getAttribute(name) {
        return Object.prototype.hasOwnProperty.call(attributes, name) ? attributes[name] : null;
      },
      querySelector() {
        return null;
      },
      focus() {
        this.focused = true;
      }
    };
    return {
      button,
      event: {
        target: {
          closest(candidate) {
            return candidate === selector ? button : null;
          }
        }
      }
    };
  };

  const tasksClick = delegatedClick('[data-inspector-tab], [data-focus-inspector-tab]', { 'data-inspector-tab': 'tasks' });
  await appContext.document.listeners.click(tasksClick.event);
  assert.equal(appContext.pendingRequests.length, 0);

  const planFocusClick = delegatedClick('[data-inspector-tab], [data-focus-inspector-tab]', { 'data-focus-inspector-tab': 'plan' });
  appContext.document.activeElement = planFocusClick.button;
  await appContext.document.listeners.click(planFocusClick.event);
  assert.equal(vm.runInContext(`nodes.inspectorSlideOut.classList.contains('is-open')`, appContext), true);
  assert.equal(vm.runInContext(`nodes.inspectorSlideOut.getAttribute('aria-hidden')`, appContext), 'false');
  assert.equal(vm.runInContext(`nodes.inspectorToggleBtn.getAttribute('aria-expanded')`, appContext), 'true');
  assert.equal(vm.runInContext(`nodes.inspectorSlideOut.focused`, appContext), true);

  let tabPrevented = false;
  appContext.document.listeners.keydown({
    key: 'Tab',
    shiftKey: false,
    target: { tagName: 'DIV' },
    preventDefault() { tabPrevented = true; }
  });
  assert.equal(tabPrevented, true);

  let escapePrevented = false;
  appContext.document.listeners.keydown({
    key: 'Escape',
    target: { tagName: 'DIV' },
    preventDefault() { escapePrevented = true; }
  });
  assert.equal(escapePrevented, true);
  assert.equal(vm.runInContext(`nodes.inspectorSlideOut.classList.contains('is-open')`, appContext), false);
  assert.equal(appContext.pendingRequests.length, 0);
  assert.equal(planFocusClick.button.focused, true);

  planFocusClick.button.focused = false;
  appContext.document.activeElement = planFocusClick.button;
  await appContext.document.listeners.click(planFocusClick.event);

  const inspectorCloseClick = delegatedClick('[data-close-inspector]');
  await appContext.document.listeners.click(inspectorCloseClick.event);
  assert.equal(vm.runInContext(`nodes.inspectorSlideOut.classList.contains('is-open')`, appContext), false);
  assert.equal(vm.runInContext(`nodes.inspectorSlideOut.getAttribute('aria-hidden')`, appContext), 'true');
  assert.equal(vm.runInContext(`nodes.inspectorToggleBtn.getAttribute('aria-expanded')`, appContext), 'false');
  assert.equal(planFocusClick.button.focused, true);

  const contextClick = delegatedClick('[data-inspector-tab], [data-focus-inspector-tab]', { 'data-inspector-tab': 'context' });
  const firstLoad = appContext.document.listeners.click(contextClick.event);
  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/context_lazy_session/context');
  appContext.pendingRequests[0].resolve({ schema_version: 1, root_session_id: 'context_lazy_session', aggregate: {}, sessions: [] });
  await firstLoad;

  const refreshClick = delegatedClick('[data-context-report-refresh]');
  const refresh = appContext.document.listeners.click(refreshClick.event);
  assert.equal(refreshClick.button.disabled, true);
  assert.equal(appContext.pendingRequests.length, 2);
  assert.equal(appContext.pendingRequests[1].url, '/api/sessions/context_lazy_session/context');
  appContext.pendingRequests[1].resolve({ schema_version: 1, root_session_id: 'context_lazy_session', aggregate: {}, sessions: [] });
  await refresh;
});

test('confirmLocalAction resolves from local dialog controls without native confirm', async () => {
  const previousDocument = context.document;
  const previousConfirm = context.window.confirm;
  const appended = [];
  let nativeConfirmCalls = 0;

  context.window.confirm = () => {
    nativeConfirmCalls += 1;
    throw new Error('native confirm should not be used');
  };
  context.document = {
    activeElement: null,
    createElement() {
      return fakeAppElement();
    },
    addEventListener() {},
    removeEventListener() {},
    body: {
      appendChild(node) {
        appended.push(node);
      }
    }
  };

  try {
    const cancelled = context.confirmLocalAction({
      title: 'Delete session',
      message: 'Delete this session?',
      confirmLabel: 'Delete',
      tone: 'danger'
    });
    assert.equal(appended.length, 1);
    collectFakeElementsByClass(appended[0], 'confirm-dialog-cancel')[0].listeners.click();
    assert.equal(await cancelled, false);

    const confirmed = context.confirmLocalAction({
      title: 'Open child session',
      message: 'Open this child session?',
      confirmLabel: 'Open'
    });
    assert.equal(appended.length, 2);
    collectFakeElementsByClass(appended[1], 'confirm-dialog-confirm')[0].listeners.click();
    assert.equal(await confirmed, true);
    assert.equal(nativeConfirmCalls, 0);
  } finally {
    context.document = previousDocument;
    context.window.confirm = previousConfirm;
  }
});

test('promptLocalAction resolves from local dialog controls without native prompt', async () => {
  const previousDocument = context.document;
  const previousPrompt = context.window.prompt;
  const appended = [];
  let nativePromptCalls = 0;

  context.window.prompt = () => {
    nativePromptCalls += 1;
    throw new Error('native prompt should not be used');
  };
  context.document = {
    activeElement: null,
    createElement() {
      return fakeAppElement();
    },
    addEventListener() {},
    removeEventListener() {},
    body: {
      appendChild(node) {
        appended.push(node);
      }
    }
  };

  try {
    const submitted = context.promptLocalAction({
      title: 'Custom Plan Mode Answer',
      message: 'Enter a custom answer.',
      inputLabel: 'Answer',
      confirmLabel: 'Use Answer'
    });
    assert.equal(appended.length, 1);
    collectFakeElementsByClass(appended[0], 'prompt-dialog-input')[0].value = 'Browser smoke';
    collectFakeElementsByClass(appended[0], 'confirm-dialog-confirm')[0].listeners.click();
    assert.equal(await submitted, 'Browser smoke');

    const cancelled = context.promptLocalAction({
      title: 'Custom Plan Mode Answer'
    });
    assert.equal(appended.length, 2);
    collectFakeElementsByClass(appended[1], 'confirm-dialog-cancel')[0].listeners.click();
    assert.equal(await cancelled, null);
    assert.equal(nativePromptCalls, 0);
  } finally {
    context.document = previousDocument;
    context.window.prompt = previousPrompt;
  }
});

test('chat render cache is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const initial = vm.runInContext(`({
    stateHasChatCache: Object.prototype.hasOwnProperty.call(state, 'chatRenderCache'),
    renderCacheKeys: Object.keys(renderState.chatCache).sort(),
    emptyBody: chatRenderCacheValue('body')
  })`, appContext);

  assert.equal(initial.stateHasChatCache, false);
  assert.deepEqual(sameRealm(initial.renderCacheKeys), [
    'activity',
    'body',
    'flow',
    'inspector',
    'inspectorSlideOut',
    'pending',
    'planModeInputActions',
    'rail',
    'todoFloat'
  ]);
  assert.equal(initial.emptyBody, '');

  vm.runInContext(`
    updateChatRenderCache('todoFloat', '<div>cached</div>');
    invalidateChatRenderSlot('todoFloat');
  `, appContext);
  assert.equal(vm.runInContext(`chatRenderCacheValue('todoFloat')`, appContext), '');
});

test('cached markup patches DOM and icons only when rendered output changes', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    const node = {
      hidden: false,
      innerHTML: '',
      querySelectorAll() { return []; }
    };
    let iconPasses = 0;
    window.lucide = {
      createIcons() {
        iconPasses += 1;
      }
    };
    const first = patchCachedMarkup(node, 'planModeInputActions', '<button>Approve</button>', { hideWhenEmpty: true });
    const second = patchCachedMarkup(node, 'planModeInputActions', '<button>Approve</button>', { hideWhenEmpty: true });
    const cleared = patchCachedMarkup(node, 'planModeInputActions', '', { hideWhenEmpty: true });
    return {
      first,
      second,
      cleared,
      hidden: node.hidden,
      markup: node.innerHTML,
      iconPasses,
      cached: chatRenderCacheValue('planModeInputActions')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    first: true,
    second: false,
    cleared: true,
    hidden: true,
    markup: '',
    iconPasses: 1,
    cached: ''
  });
});

test('desktop and compact inspectors use independent render cache slots', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });
  const result = vm.runInContext(`(() => {
    const desktop = { innerHTML: '', querySelectorAll() { return []; } };
    const compact = { innerHTML: '', querySelectorAll() { return []; } };
    const html = '<section>Session inspector</section>';
    const desktopFirst = patchAuxSlot(desktop, 'inspector', html);
    const compactFirst = patchAuxSlot(compact, 'inspectorSlideOut', html);
    return {
      desktopFirst,
      compactFirst,
      desktopSecond: patchAuxSlot(desktop, 'inspector', html),
      compactSecond: patchAuxSlot(compact, 'inspectorSlideOut', html),
      desktopHTML: desktop.innerHTML,
      compactHTML: compact.innerHTML
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    desktopFirst: true,
    compactFirst: true,
    desktopSecond: false,
    compactSecond: false,
    desktopHTML: '<section>Session inspector</section>',
    compactHTML: '<section>Session inspector</section>'
  });
});

test('compact inspector patches preserve the current scroll position', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });
  const result = vm.runInContext(`(() => {
    const compact = { scrollTop: 180, innerHTML: '', querySelectorAll() { return []; } };
    const first = patchScrollableAuxSlot(compact, 'inspectorSlideOut', '<section>Tasks</section>');
    compact.scrollTop = 240;
    const second = patchScrollableAuxSlot(compact, 'inspectorSlideOut', '<section>Updated tasks</section>');
    return { first, second, scrollTop: compact.scrollTop };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    first: true,
    second: true,
    scrollTop: 240
  });
});

test('Plan Mode input actions reuse cached markup and hide after the gate clears', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    let assignments = 0;
    let markup = '';
    const actionNode = {
      hidden: true,
      querySelectorAll() { return []; },
      get innerHTML() { return markup; },
      set innerHTML(value) {
        assignments += 1;
        markup = value;
      }
    };
    nodes.planModeInputActions = actionNode;
    state.sessionDetail = { plan_mode: { status: 'awaiting_approval' } };
    renderPlanModeInputActions();
    renderPlanModeInputActions();
    const approvalVisible = !actionNode.hidden;
    state.sessionDetail.plan_mode.status = 'executing';
    renderPlanModeInputActions();
    return {
      assignments,
      approvalVisible,
      hiddenAfterClear: actionNode.hidden,
      markupAfterClear: actionNode.innerHTML
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    assignments: 2,
    approvalVisible: true,
    hiddenAfterClear: true,
    markupAfterClear: ''
  });
});

test('runtime handles are isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    const handleKeys = [
      'ws',
      'pollHandle',
      'pollIntervalMs',
      'wsReconnectAttempts',
      'wsReconnectTimer',
      'pendingSessionRefresh',
      'pendingOverviewRefresh',
      'layoutObserver',
      'inspectorPreviousFocus'
    ];
    runtimeHandles.pendingSessionRefresh = 101;
    runtimeHandles.pendingOverviewRefresh = 102;
    clearPendingRefreshes();
    return {
      stateHandleKeys: handleKeys.filter((key) => Object.prototype.hasOwnProperty.call(state, key)),
      runtimeHandleKeys: handleKeys.filter((key) => Object.prototype.hasOwnProperty.call(runtimeHandles, key)).sort(),
      pendingSessionRefresh: runtimeHandles.pendingSessionRefresh,
      pendingOverviewRefresh: runtimeHandles.pendingOverviewRefresh
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result.stateHandleKeys), []);
  assert.deepEqual(sameRealm(result.runtimeHandleKeys), [
    'inspectorPreviousFocus',
    'layoutObserver',
    'pendingOverviewRefresh',
    'pendingSessionRefresh',
    'pollHandle',
    'pollIntervalMs',
    'ws',
    'wsReconnectAttempts',
    'wsReconnectTimer'
  ]);
  assert.equal(result.pendingSessionRefresh, null);
  assert.equal(result.pendingOverviewRefresh, null);
});

test('page visibility tracking is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    setCurrentViewName('history');
    setPageVisibilityHidden(true);
    const hidden = {
      stateHasVisibilityHidden: Object.prototype.hasOwnProperty.call(state, 'visibilityHidden'),
      visibilityHidden: isPageVisibilityHidden(),
      runLoop: shouldRunPollingLoop()
    };
    setPageVisibilityHidden(false);
    return {
      hidden,
      visible: {
        stateHasVisibilityHidden: Object.prototype.hasOwnProperty.call(state, 'visibilityHidden'),
        visibilityHidden: isPageVisibilityHidden(),
        runLoop: shouldRunPollingLoop()
      }
    };
  })()`, appContext);

  assert.equal(result.hidden.stateHasVisibilityHidden, false);
  assert.equal(result.hidden.visibilityHidden, true);
  assert.equal(result.hidden.runLoop, false);
  assert.equal(result.visible.stateHasVisibilityHidden, false);
  assert.equal(result.visible.visibilityHidden, false);
  assert.equal(result.visible.runLoop, true);
});

test('current view selection is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    const persisted = [];
    window.localStorage = {
      stored: '',
      getItem() {
        return this.stored || null;
      },
      setItem(key, value) {
        persisted.push({ key, value: JSON.parse(value) });
        this.stored = value;
      }
    };
    switchView('history');
    const afterHistory = {
      current: currentViewName(),
      title: document.title,
      stateHasCurrentView: Object.prototype.hasOwnProperty.call(state, 'currentView'),
      historyHidden: nodes.views.history.classList.contains('is-hidden'),
      chatHidden: nodes.views.chat.classList.contains('is-hidden'),
      shouldRunPolling: shouldRunPollingLoop()
    };
    window.localStorage.stored = JSON.stringify({ currentView: 'skills', historyPage: 3 });
    restoreUIState();
    return {
      afterHistory,
      restored: {
        current: currentViewName(),
        title: document.title,
        historyPage: currentHistoryPage(),
        stateHasCurrentView: Object.prototype.hasOwnProperty.call(state, 'currentView')
      },
      persisted: persisted.map((entry) => entry.value.currentView)
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    afterHistory: {
      current: 'history',
      title: 'Sessions — Agent Console',
      stateHasCurrentView: false,
      historyHidden: false,
      chatHidden: true,
      shouldRunPolling: true
    },
    restored: {
      current: 'skills',
      title: 'Skills — Agent Console',
      historyPage: 3,
      stateHasCurrentView: false
    },
    persisted: ['history', 'history']
  });
});

test('live relay connection status is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    setCurrentViewName('chat');
    state.sessionId = 'session_connection_view';
    state.sessionBacked = true;
    state.overview = { sessions: [] };
    state.sessionDetail = {
      metadata: { id: 'session_connection_view' },
      state: { status: 'awaiting_input' },
      messages: []
    };
    nodes.chatInput.value = 'draft prompt';
    setPageVisibilityHidden(false);

    setLiveRelayConnected(false);
    updateConnectionStatus();
    updateUI();
    const disconnected = {
      connected: isLiveRelayConnected(),
      connectionStatus: nodes.connectionStatus.innerText,
      connectionDotClass: nodes.connectionDot.className,
      inputOffline: nodes.inputContainer.classList.contains('is-offline'),
      runLoop: shouldRunPollingLoop(),
      interval: pollingIntervalForState(),
      label: inputActionLabel()
    };

    setLiveRelayConnected(true);
    updateConnectionStatus();
    updateUI();
    const connected = {
      connected: isLiveRelayConnected(),
      connectionStatus: nodes.connectionStatus.innerText,
      connectionDotClass: nodes.connectionDot.className,
      inputOffline: nodes.inputContainer.classList.contains('is-offline'),
      runLoop: shouldRunPollingLoop(),
      interval: pollingIntervalForState(),
      label: inputActionLabel()
    };

    return {
      stateHasIsConnected: Object.prototype.hasOwnProperty.call(state, 'isConnected'),
      disconnected,
      connected
    };
  })()`, appContext);

  assert.equal(result.stateHasIsConnected, false);
  assert.deepEqual(sameRealm(result.disconnected), {
    connected: false,
    connectionStatus: 'Disconnected',
    connectionDotClass: 'dot',
    inputOffline: true,
    runLoop: true,
    interval: 1600,
    label: 'Live event relay reconnecting; REST session actions remain available.'
  });
  assert.deepEqual(sameRealm(result.connected), {
    connected: true,
    connectionStatus: 'Agent Connected',
    connectionDotClass: 'dot online',
    inputOffline: false,
    runLoop: false,
    interval: 5000,
    label: 'Continue Awaiting input session: next send resumes this durable session.'
  });
});

test('launch pending guard is isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveRelayConnected(true);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    nodes.chatInput.value = 'start a slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  const pending = vm.runInContext(`(() => {
    nodes.chatInput.value = 'duplicate launch';
    updateUI();
    const beforeDuplicate = {
      stateHasLaunchInFlight: Object.prototype.hasOwnProperty.call(state, 'launchInFlight'),
      launchInFlight: isLaunchInFlight(),
      sendDisabled: nodes.sendBtn.disabled,
      inputBusy: nodes.inputContainer.classList.contains('is-busy')
    };
    sendMessage();
    return beforeDuplicate;
  })()`, appContext);

  assert.deepEqual(sameRealm(pending), {
    stateHasLaunchInFlight: false,
    launchInFlight: true,
    sendDisabled: true,
    inputBusy: true
  });
  assert.equal(appContext.pendingRequests.length, 1);

  appContext.pendingRequests[0].resolve({ session_id: 'session_created_from_launch', status: 'accepted' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    stateHasLaunchInFlight: Object.prototype.hasOwnProperty.call(state, 'launchInFlight'),
    launchInFlight: isLaunchInFlight()
  })`, appContext)), {
    selected: 'session_created_from_launch',
    backed: true,
    stateHasLaunchInFlight: false,
    launchInFlight: false
  });
});

test('generating view state is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const running = vm.runInContext(`(() => {
    renderCurrentSession = function() {};
    state.sessionId = 'session_generating_view';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_generating_view' },
      state: { status: 'running', phase: 'provider_call' },
      active_handle: true,
      active_handle_owner: { owned_by_current_process: true },
      messages: []
    };
    setLiveRelayConnected(true);
    nodes.chatInput.value = 'steer while running';
    setGenerating(true, {
      title: 'Provider call',
      copy: 'The provider turn is running.',
      tone: 'live'
    });
    const stream = renderMessageStream();
    return {
      stateHasIsGenerating: Object.prototype.hasOwnProperty.call(state, 'isGenerating'),
      generating: isGenerating(),
      sendLoading: nodes.sendBtn.classList.contains('is-loading'),
      inputBusy: nodes.inputContainer.classList.contains('is-busy'),
      interruptVisible: nodes.interruptToggleBtn.classList.contains('is-visible'),
      controlVisible: nodes.stopSessionBtn.classList.contains('is-visible'),
      actionLabel: nodes.inputStatusText.textContent,
      pendingHasRunning: stream.pending.includes('Running'),
      activityHasRunningStatus: stream.activity.includes('Running')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(running), {
    stateHasIsGenerating: false,
    generating: true,
    sendLoading: true,
    inputBusy: true,
    interruptVisible: true,
    controlVisible: true,
    actionLabel: 'Steer running session: next send queues guidance into the current run.',
    pendingHasRunning: true,
    activityHasRunningStatus: true
  });

  const settled = vm.runInContext(`(() => {
    setNextSendInterruptArmed(true);
    setGenerating(false, {
      title: 'Awaiting input',
      copy: 'The session is ready for a follow-up.',
      tone: 'queued'
    });
    const stream = renderMessageStream();
    return {
      stateHasIsGenerating: Object.prototype.hasOwnProperty.call(state, 'isGenerating'),
      generating: isGenerating(),
      interruptArmed: isNextSendInterruptArmed(),
      inputBusy: nodes.inputContainer.classList.contains('is-busy'),
      interruptVisible: nodes.interruptToggleBtn.classList.contains('is-visible'),
      controlVisible: nodes.stopSessionBtn.classList.contains('is-visible'),
      pendingEmpty: stream.pending === ''
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(settled), {
    stateHasIsGenerating: false,
    generating: false,
    interruptArmed: false,
    inputBusy: false,
    interruptVisible: false,
    controlVisible: false,
    pendingEmpty: true
  });
});

test('toast id counter is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    nodes.toastRack.__children.splice(0, nodes.toastRack.__children.length);
    if (typeof toastViewState !== 'undefined') {
      toastViewState.counter = 0;
    }
    showToast('First toast', 'info');
    showToast('Second toast', 'error');
    return {
      stateHasToastCounter: Object.prototype.hasOwnProperty.call(state, 'toastCounter'),
      toastIDs: nodes.toastRack.__children.map((node) => node.id),
      toastClasses: nodes.toastRack.__children.map((node) => node.className),
      toastTexts: nodes.toastRack.__children.map((node) => node.textContent)
    };
  })()`, appContext);

  assert.equal(result.stateHasToastCounter, false);
  assert.deepEqual(sameRealm(result.toastIDs), ['toast-1', 'toast-2']);
  assert.deepEqual(sameRealm(result.toastClasses), ['toast toast-info', 'toast toast-error']);
  assert.deepEqual(sameRealm(result.toastTexts), ['First toast', 'Second toast']);
});

test('stop action pending sessions are isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const stop = vm.runInContext(`
    setCurrentViewName('chat');
    state.sessionId = 'session_stop_pending';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    state.sessionDetail = {
      metadata: { id: 'session_stop_pending' },
      state: { status: 'running' },
      messages: []
    };
    requestStopSession('session_stop_pending');
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  vm.runInContext(`requestStopSession('session_stop_pending');`, appContext);
  assert.equal(appContext.pendingRequests.length, 1);

  const pendingState = vm.runInContext(`({
    stateHasStoppingSessionIds: Object.prototype.hasOwnProperty.call(state, 'stoppingSessionIds'),
    isStopping: isStoppingSession('session_stop_pending')
  })`, appContext);
  assert.equal(pendingState.stateHasStoppingSessionIds, false);
  assert.equal(pendingState.isStopping, true);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await stop;

  const finalState = vm.runInContext(`({
    stateHasStoppingSessionIds: Object.prototype.hasOwnProperty.call(state, 'stoppingSessionIds'),
    inFlight: stopActionViewState.sessionIds.has('session_stop_pending'),
    requestedHold: stopActionViewState.requestedAtBySessionId.has('session_stop_pending'),
    isStopping: isStoppingSession('session_stop_pending')
  })`, appContext);
  assert.equal(finalState.stateHasStoppingSessionIds, false);
  assert.equal(finalState.inFlight, false);
  assert.equal(finalState.requestedHold, true);
  assert.equal(finalState.isStopping, true);

  vm.runInContext(`clearSettledStopRequestsFromDetail({
    metadata: { id: 'session_stop_pending' },
    state: { status: 'paused' }
  })`, appContext);
  assert.equal(vm.runInContext(`isStoppingSession('session_stop_pending')`, appContext), false);
});

test('history parent expansion is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    const history = {
      total: 2,
      page: 1,
      page_size: 8,
      total_pages: 1,
      items: [
        {
          id: 'parent_history_session',
          status: 'completed',
          model: 'gpt-test',
          phase: 'done',
          updated_at: '2026-05-29T00:00:00Z'
        },
        {
          id: 'child_history_session',
          parent_session_id: 'parent_history_session',
          status: 'completed',
          model: 'gpt-test',
          phase: 'done',
          updated_at: '2026-05-29T00:00:01Z'
        }
      ]
    };
    historyExpansionViewState.parentIds.add('parent_history_session');
    renderSessionStopButton = function() { return ''; };
    renderHistory(history);
    return {
      stateHasExpandedHistoryParents: Object.prototype.hasOwnProperty.call(state, 'expandedHistoryParents'),
      isExpanded: nodes.views.history.innerHTML.includes('history-tree-children is-expanded'),
      childVisible: nodes.views.history.innerHTML.includes('child_history_session')
    };
  })()`, appContext);

  assert.equal(result.stateHasExpandedHistoryParents, false);
  assert.equal(result.isExpanded, true);
  assert.equal(result.childVisible, true);
});

test('history session titles localize the fallback and preserve custom agent facts', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    window.AegisI18n = {
      t(value) { return value === 'Master session' ? '主会话' : value; }
    };
    return {
      fallback: renderHistorySessionTitle({}),
      custom: renderHistorySessionTitle({ agent_name: 'Ready', agent_role: 'evaluator' })
    };
  })()`, appContext);

  assert.match(result.fallback, /class="history-session-fallback">主会话<\/span>/);
  assert.doesNotMatch(result.fallback, /data-i18n-skip/);
  assert.match(result.custom, /class="history-session-agent-label" translate="no" data-i18n-skip>Ready · evaluator<\/span>/);
  assert.doesNotMatch(result.custom, /主会话/);
});

test('floating panel expansion preferences are isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    window.localStorage = {
      stored: '',
      key: '',
      getItem(key) {
        return this.stored || null;
      },
      setItem(key, value) {
        this.key = key;
        this.stored = value;
      },
      removeItem() {
        this.stored = '';
      }
    };
  `, appContext);
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    const stateKeys = [
      'todoFloatExpanded',
      'fileChangesExpanded',
      'subAgentExpanded'
    ];
    setFloatingPanelExpanded('todo', false);
    setFloatingPanelExpanded('files', false);
    setFloatingPanelExpanded('subAgents', true);
    persistUIState();
    const stored = JSON.parse(window.localStorage.stored);
    window.localStorage.stored = JSON.stringify({
      currentView: 'chat',
      historyPage: 2,
      todoFloatExpanded: true,
      fileChangesExpanded: false,
      subAgentExpanded: false
    });
    restoreUIState();
    const restored = {
      todo: isFloatingPanelExpanded('todo'),
      files: isFloatingPanelExpanded('files'),
      subAgents: isFloatingPanelExpanded('subAgents'),
      historyPage: currentHistoryPage()
    };
    state.sessionDetail = {
      task_board: {
        todo: [{ content: 'Review docs', status: 'pending' }],
        tasks: []
      }
    };
    const expandedTodo = renderTodoFloat();
    setFloatingPanelExpanded('todo', false);
    const collapsedTodo = renderTodoFloat();
    return {
      stateOwnedKeys: stateKeys.filter((key) => Object.prototype.hasOwnProperty.call(state, key)),
      stored,
      restored,
      expandedTodoHasBody: expandedTodo.includes('tf-body'),
      collapsedTodoHasBody: collapsedTodo.includes('tf-body')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result.stateOwnedKeys), []);
  assert.equal(result.stored.todoFloatExpanded, false);
  assert.equal(result.stored.fileChangesExpanded, false);
  assert.equal(result.stored.subAgentExpanded, true);
  assert.deepEqual(sameRealm(result.restored), {
    todo: true,
    files: false,
    subAgents: false,
    historyPage: 2
  });
  assert.equal(result.expandedTodoHasBody, true);
  assert.equal(result.collapsedTodoHasBody, false);
});

test('shortcut help overlay visibility is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const appended = [];
  let overlay = null;
  let removed = false;

  appContext.document = {
    getElementById(id) {
      return id === 'shortcut-help-overlay' ? overlay : fakeAppElement();
    },
    createElement() {
      const element = fakeAppElement();
      element.__listeners = {};
      element.addEventListener = function addEventListener(event, callback) {
        if (!this.__listeners[event]) {
          this.__listeners[event] = [];
        }
        this.__listeners[event].push(callback);
      };
      element.remove = function remove() {
        removed = true;
        overlay = null;
      };
      return element;
    },
    body: {
      appendChild(node) {
        overlay = node;
        appended.push(node);
      }
    }
  };
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const rendered = vm.runInContext(`(() => {
    setHelpVisible(true);
    renderShortcutHelp();
    const helpOverlay = document.getElementById('shortcut-help-overlay');
    return {
      stateHasShowHelp: Object.prototype.hasOwnProperty.call(state, 'showHelp'),
      visible: isHelpVisible(),
      listenerCount: helpOverlay.__listeners.click.length,
      html: helpOverlay.innerHTML
    };
  })()`, appContext);

  assert.equal(rendered.stateHasShowHelp, false);
  assert.equal(rendered.visible, true);
  assert.equal(rendered.listenerCount, 2);
  assert.equal(rendered.html.includes('Keyboard Shortcuts'), true);
  assert.equal(appended.length, 1);

  overlay.__listeners.click[0]({ target: overlay });

  const closed = vm.runInContext(`({
    stateHasShowHelp: Object.prototype.hasOwnProperty.call(state, 'showHelp'),
    visible: isHelpVisible()
  })`, appContext);
  assert.equal(closed.stateHasShowHelp, false);
  assert.equal(closed.visible, false);
  assert.equal(removed, true);
});

test('composer input empty flag is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    let updateCalls = 0;
    updateUI = function updateUIForComposerInputTest() {
      updateCalls += 1;
    };
    updateDynamicLayoutMetrics = function updateDynamicLayoutMetricsForComposerInputTest() {};
    nodes.chatInput.value = '';
    nodes.chatInput.scrollHeight = 24;
    setupEventListeners();
    const inputListener = nodes.chatInput.listeners.input;
    const before = {
      stateHasLastInputWasEmpty: Object.prototype.hasOwnProperty.call(state, 'lastInputWasEmpty'),
      empty: isComposerInputEmpty(),
      updateCalls
    };
    nodes.chatInput.value = 'draft instruction';
    nodes.chatInput.scrollHeight = 40;
    inputListener.call(nodes.chatInput);
    const afterDraft = {
      empty: isComposerInputEmpty(),
      updateCalls,
      height: nodes.chatInput.style.height
    };
    inputListener.call(nodes.chatInput);
    const afterSameNonEmpty = {
      empty: isComposerInputEmpty(),
      updateCalls
    };
    nodes.chatInput.value = '   ';
    inputListener.call(nodes.chatInput);
    return {
      before,
      afterDraft,
      afterSameNonEmpty,
      afterEmpty: {
        stateHasLastInputWasEmpty: Object.prototype.hasOwnProperty.call(state, 'lastInputWasEmpty'),
        empty: isComposerInputEmpty(),
        updateCalls
      }
    };
  })()`, appContext);

  assert.equal(result.before.stateHasLastInputWasEmpty, false);
  assert.equal(result.before.empty, true);
  assert.equal(result.before.updateCalls, 0);
  assert.equal(result.afterDraft.empty, false);
  assert.equal(result.afterDraft.updateCalls, 1);
  assert.equal(result.afterDraft.height, '40px');
  assert.equal(result.afterSameNonEmpty.empty, false);
  assert.equal(result.afterSameNonEmpty.updateCalls, 1);
  assert.equal(result.afterEmpty.stateHasLastInputWasEmpty, false);
  assert.equal(result.afterEmpty.empty, true);
  assert.equal(result.afterEmpty.updateCalls, 2);
});

test('composer interrupt steer intent is isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_interrupt_steer';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveRelayConnected(true);
    setLiveActivity({ title: 'Running session', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_interrupt_steer' },
      state: { status: 'running' },
      messages: []
    };
    setNextSendInterruptArmed(true);
    nodes.chatInput.value = 'interrupt with updated instructions';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/session_interrupt_steer/steer');
  assert.deepEqual(sameRealm(appContext.pendingRequests[0].payload.payload), {
    message: 'interrupt with updated instructions',
    interrupt: true
  });

  const armedBeforeResolve = vm.runInContext(`({
    stateHasNextSendInterrupt: Object.prototype.hasOwnProperty.call(state, 'nextSendInterrupt'),
    armed: isNextSendInterruptArmed()
  })`, appContext);
  assert.equal(armedBeforeResolve.stateHasNextSendInterrupt, false);
  assert.equal(armedBeforeResolve.armed, true);

  appContext.pendingRequests[0].resolve({ status: 'queued' });
  await send;

  const afterResolve = vm.runInContext(`({
    stateHasNextSendInterrupt: Object.prototype.hasOwnProperty.call(state, 'nextSendInterrupt'),
    armed: isNextSendInterruptArmed(),
    activityTitle: currentLiveActivity().title,
    inputLabel: inputActionLabel()
  })`, appContext);
  assert.equal(afterResolve.stateHasNextSendInterrupt, false);
  assert.equal(afterResolve.armed, false);
  assert.equal(afterResolve.activityTitle, 'Interrupt steer requested');
  assert.equal(afterResolve.inputLabel, 'Steer running session: next send queues guidance into the current run.');
});

test('composer Goal and Plan Mode toggles are isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const goalSend = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    setComposerMode('goal');
    nodes.chatInput.value = 'ship the full audit goal';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');
  assert.deepEqual(sameRealm(appContext.pendingRequests[0].payload.payload.goal), {
    enabled: true,
    mode: 'goal',
    objective: 'ship the full audit goal'
  });
  assert.equal(vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'goalEnabled')`, appContext), false);
  assert.equal(vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'planModeEnabled')`, appContext), false);

  appContext.pendingRequests[0].resolve({ session_id: 'session_goal_start', status: 'accepted' });
  await goalSend;
  assert.equal(vm.runInContext(`composerMode()`, appContext), '');

  const planSend = vm.runInContext(`
    state.sessionId = '0xB22CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    setComposerMode('plan');
    nodes.chatInput.value = 'plan before changing files';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 2);
  assert.equal(appContext.pendingRequests[1].url, '/api/sessions/start');
  assert.deepEqual(sameRealm(appContext.pendingRequests[1].payload.payload.planMode), {
    enabled: true,
    objective: 'plan before changing files'
  });
  assert.equal(vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'goalEnabled')`, appContext), false);
  assert.equal(vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'planModeEnabled')`, appContext), false);

  appContext.pendingRequests[1].resolve({ session_id: 'session_plan_start', status: 'accepted' });
  await planSend;
  assert.equal(vm.runInContext(`composerMode()`, appContext), '');
});

test('live activity is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    state.sessionId = 'session_activity_view';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    state.sessionDetail = {
      metadata: { id: 'session_activity_view' },
      state: { status: 'running' },
      messages: []
    };
    setLiveActivity({
      title: 'Tool is running',
      copy: 'Waiting for the current tool call.',
      tone: 'live'
    });
    const pending = renderPendingStageCard();
    return {
      stateHasLiveActivity: Object.prototype.hasOwnProperty.call(state, 'liveActivity'),
      current: currentLiveActivity(),
      pendingHasTitle: pending.includes('Tool is running'),
      pendingHasCopy: pending.includes('Waiting for the current tool call.')
    };
  })()`, appContext);

  assert.equal(result.stateHasLiveActivity, false);
  assert.deepEqual(sameRealm(result.current), {
    title: 'Tool is running',
    copy: 'Waiting for the current tool call.',
    tone: 'live'
  });
  assert.equal(result.pendingHasTitle, true);
  assert.equal(result.pendingHasCopy, true);
});

test('session activity card surfaces durable Goal runtime status', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    state.sessionId = 'session_goal_runtime';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_goal_runtime', mode: 'run' },
      state: { status: 'running', phase: 'tool_execute' },
      goal: {
        goal_id: 'goal_runtime_status',
        mode: 'goal',
        status: 'active',
        objective: 'Keep the Goal visible while the run is executing.',
        source: 'web',
        tokens_used: 42,
        time_used_seconds: 9,
        updated_at: '2026-06-12T06:14:43Z'
      },
      goal_facts: {
        latest_history: {
          type: 'goal.accounting.updated',
          created_at: '2026-06-12T06:14:43Z'
        },
        progress: []
      },
      messages: []
    };
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Tool execute', copy: 'The runner is active.', tone: 'live' });
    const html = renderSessionActivityCard();
    return {
      hasGoalStatus: html.includes('Goal Active'),
      hasGoalChipClass: html.includes('status-badge live'),
      hasPhase: html.includes('Tool execute') || html.includes('Tool Execute'),
      hasAccounting: html.includes('tokens 42') && html.includes('time 9s'),
      hasLatestHistory: html.includes('Goal accounting updated')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    hasGoalStatus: true,
    hasGoalChipClass: true,
    hasPhase: true,
    hasAccounting: true,
    hasLatestHistory: true
  });
});

test('session activity distinguishes cancelled from failed and paused', () => {
  const appContext = createAppHarnessContext();
  const { cancelled, failed, paused } = sameRealm(vm.runInContext(`({
    cancelled: sessionActivityForState({ status: 'cancelled' }),
    failed: sessionActivityForState({ status: 'failed', last_error: 'boom' }),
    paused: sessionActivityForState({ status: 'paused' })
  })`, appContext));
  assert.equal(cancelled.title, 'Cancelled');
  assert.equal(cancelled.tone, 'danger');
  assert.match(cancelled.copy, /explicitly cancelled/);
  assert.equal(failed.title, 'Failed');
  assert.equal(paused.title, 'Paused');
});

test('Goal inspector separates runtime facts from mission facts', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    const detail = {
      metadata: { id: 'session_goal_panel' },
      state: { status: 'running', phase: 'provider_call' },
      goal: {
        goal_id: 'goal_panel_status',
        mode: 'mission',
        status: 'budget_limited',
        objective: 'Build a safer Web-first pentest workflow.',
        source: 'web',
        tokens_used: 1000,
        token_budget: 1000,
        time_used_seconds: 60,
        updated_at: '2026-06-12T06:15:00Z',
        progress: [
          {
            id: 'progress_1',
            kind: 'budget_wrapup',
            summary: 'Recorded remaining work.',
            created_at: '2026-06-12T06:15:00Z'
          }
        ],
        mission: { plan_status: 'needs_approval', features: [], milestones: [], role_plan: [] }
      },
      goal_facts: {
        latest_history: { type: 'goal.budget_limited', created_at: '2026-06-12T06:15:00Z' },
        progress: [
          {
            id: 'progress_1',
            kind: 'budget_wrapup',
            summary: 'Recorded remaining work.',
            created_at: '2026-06-12T06:15:00Z'
          }
        ],
        coverage: {},
        evaluator_evidence_count: 0
      }
    };
    const html = renderGoalPanel(detail);
    return {
      hasGoalStatus: html.includes('Goal Budget limited'),
      hasRuntimeSection: html.includes('goal-runtime-card'),
      hasSessionPhase: html.includes('Provider call') || html.includes('Provider Call'),
      hasGoalFactsTitle: html.includes('Goal facts'),
      hasRecentProgress: html.includes('Recent progress'),
      hasProgressSummary: html.includes('Recorded remaining work.')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    hasGoalStatus: true,
    hasRuntimeSection: true,
    hasSessionPhase: true,
    hasGoalFactsTitle: true,
    hasRecentProgress: true,
    hasProgressSummary: true
  });
});

test('Goal inspector marks a completed Goal as historical during session follow-up', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    const detail = {
      metadata: { id: 'session_goal_historical' },
      state: { status: 'running', phase: 'provider_call' },
      goal: {
        goal_id: 'goal_historical',
        mode: 'goal',
        status: 'complete',
        objective: 'Completed objective',
        completion_audit: {
          status: 'complete',
          summary: 'Original objective completed.',
          completed_at: '2026-07-10T01:00:00Z'
        }
      },
      goal_facts: { coverage: {}, evaluator_evidence_count: 0 }
    };
    const html = renderGoalPanel(detail);
    return {
      historical: isHistoricalCompletedGoal(detail),
      hasHistoricalBadge: html.includes('>Historical<'),
      hasRunningFact: html.includes('session Running'),
      hasCompletionAudit: html.includes('Original objective completed.')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    historical: true,
    hasHistoricalBadge: true,
    hasRunningFact: true,
    hasCompletionAudit: true
  });
});

test('Goal tool lane renders compact status cards instead of full objective JSON', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    const objective = 'Sensitive objective should stay in the Goal panel, not the tool lane body.';
    const message = {
      role: 'assistant',
      tool_calls: [{
        id: 'call_goal',
        name: 'create_goal',
        arguments: {
          mode: 'goal',
          objective,
          token_budget: 100
        }
      }],
      tool_results: [{
        tool_call_id: 'call_goal',
        name: 'create_goal',
        display_output: JSON.stringify({
          goal_id: 'goal_tool_lane',
          mode: 'goal',
          status: 'active',
          objective,
          tokens_used: 3,
          time_used_seconds: 1,
          updated_at: '2026-06-12T06:16:00Z'
        }),
        metadata: { goal_id: 'goal_tool_lane', status: 'active' },
        is_error: false
      }]
    };
    const html = renderToolLane(message);
    return {
      hasGoalStatus: html.includes('Goal Active'),
      hasStoredObjectiveReference: html.includes('Objective stored in Goal panel'),
      leaksObjectiveText: html.includes(objective),
      hasAccounting: html.includes('tokens 3') && html.includes('time 1s'),
      hasGoalToolCard: html.includes('goal-tool-card')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    hasGoalStatus: true,
    hasStoredObjectiveReference: true,
    leaksObjectiveText: false,
    hasAccounting: true,
    hasGoalToolCard: true
  });
});

test('assistant tool-only messages do not render blank text bubbles', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    const html = renderMessage({
      id: 'msg_tool_only_blank_text',
      role: 'assistant',
      text: String.fromCharCode(10, 32, 32, 9),
      thinking: '  ',
      created_at: '2026-06-26T01:02:03Z',
      tool_calls: [{
        id: 'call_edit_blank_text',
        name: 'edit_file',
        arguments: { path: 'rancher/reports/progress.md' }
      }],
      tool_results: [{
        tool_call_id: 'call_edit_blank_text',
        name: 'edit_file',
        display_output: 'edited rancher/reports/progress.md',
        is_error: false
      }]
    });
    return {
      hasToolLane: html.includes('tool-lane') && html.includes('Tool Lane'),
      hasToolName: html.includes('edit_file'),
      hasMessageBubble: html.includes('class="message-bubble'),
      hasThinkingBlock: html.includes('thinking-block')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    hasToolLane: true,
    hasToolName: true,
    hasMessageBubble: false,
    hasThinkingBlock: false
  });
});

test('live event relay buffer is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();

  const result = vm.runInContext(`(() => {
    resetLiveEvents();
    for (let index = 0; index < MAX_LIVE_EVENTS + 2; index += 1) {
      pushLiveEvent({ type: 'tool.after', sequence: index });
    }
    const events = currentLiveEvents();
    const snapshot = {
      stateHasLiveEvents: Object.prototype.hasOwnProperty.call(state, 'liveEvents'),
      eventCount: events.length,
      firstSequence: events[0]?.sequence,
      lastSequence: events[events.length - 1]?.sequence
    };
    resetLiveEvents();
    snapshot.afterResetCount = currentLiveEvents().length;
    return snapshot;
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    stateHasLiveEvents: false,
    eventCount: 80,
    firstSequence: 2,
    lastSequence: 81,
    afterResetCount: 0
  });
});

test('optimistic message buffer is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    resetOptimisticMessages();
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(true);
    state.sessionDetail = null;
    appendOptimisticMessage('user', 'pending launch prompt', { source: 'user' });
    const rendered = renderMessageStream();
    const snapshot = {
      stateHasOptimisticMessages: Object.prototype.hasOwnProperty.call(state, 'optimisticMessages'),
      pendingCount: currentOptimisticMessages().length,
      bodyHasPendingPrompt: rendered.body.includes('pending launch prompt'),
      bodyHasOptimisticClass: rendered.body.includes('optimistic')
    };
    resetOptimisticMessages();
    snapshot.afterResetCount = currentOptimisticMessages().length;
    return snapshot;
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    stateHasOptimisticMessages: false,
    pendingCount: 1,
    bodyHasPendingPrompt: true,
    bodyHasOptimisticClass: true,
    afterResetCount: 0
  });
});

test('plan input selections are isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  appContext.planInputSubmitButton = fakeActionButton({
    'data-plan-input-action': 'submit',
    'data-request-id': 'request_plan_input'
  });

  const setup = vm.runInContext(`(() => {
    const request = {
      request_id: 'request_plan_input',
      questions: [
        { id: 'scope', options: [{ label: 'Small' }, { label: 'Large' }] }
      ]
    };
    state.sessionId = 'session_plan_input';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_plan_input' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        status: 'awaiting_user_input',
        pending_request: request
      }
    };
    planInputViewState.selections = {
      stale_request: {
        old: { label: 'Old', value: 'Old' }
      }
    };
    setPlanInputSelection(request, {
      question_id: 'scope',
      label: 'Large',
      value: 'Large'
    });
    return {
      stateHasPlanInputSelections: Object.prototype.hasOwnProperty.call(state, 'planInputSelections'),
      staleRequestPresent: Object.prototype.hasOwnProperty.call(planInputViewState.selections, 'stale_request'),
      selectedValue: getPlanInputSelections(request).scope.value
    };
  })()`, appContext);

  assert.equal(setup.stateHasPlanInputSelections, false);
  assert.equal(setup.staleRequestPresent, false);
  assert.equal(setup.selectedValue, 'Large');

  const action = vm.runInContext(`handlePlanInputAction(planInputSubmitButton)`, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/session_plan_input/planmode/input');
  assert.deepEqual(sameRealm(appContext.pendingRequests[0].payload.payload), {
    requestID: 'request_plan_input',
    answers: [
      { question_id: 'scope', label: 'Large', value: 'Large', is_other: false }
    ]
  });

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await action;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasPlanInputSelections: Object.prototype.hasOwnProperty.call(state, 'planInputSelections'),
    requestSelections: planInputViewState.selections.request_plan_input || null
  })`, appContext)), {
    stateHasPlanInputSelections: false,
    requestSelections: null
  });
});

test('plan input Other answer uses local prompt dialog', async () => {
  const appContext = createAppHarnessContext();
  const promptCalls = [];
  appContext.planInputOtherButton = fakeActionButton({
    'data-plan-input-action': 'select',
    'data-request-id': 'request_plan_input',
    'data-question-id': 'validation',
    'data-label': 'Other',
    'data-other': '1'
  });

  await vm.runInContext(`
    const request = {
      request_id: 'request_plan_input',
      questions: [
        { id: 'validation', options: [{ label: 'Unit tests' }, { label: 'Browser smoke' }] }
      ]
    };
    state.sessionId = 'session_plan_input_other';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_plan_input_other' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        status: 'awaiting_user_input',
        pending_request: request
      }
    };
    promptLocalAction = async function(options) {
      promptCallsRef.push(options);
      return 'Manual browser smoke';
    };
    window.prompt = function() {
      throw new Error('native prompt should not be used');
    };
    renderCurrentSession = function() {};
    handlePlanInputAction(planInputOtherButton);
  `, Object.assign(appContext, { promptCallsRef: promptCalls }));

  assert.deepEqual(sameRealm(promptCalls), [
    {
      title: 'Custom Plan Mode Answer',
      message: 'Enter a custom answer for this Plan Mode question.',
      inputLabel: 'Answer',
      confirmLabel: 'Use Answer'
    }
  ]);
  assert.deepEqual(sameRealm(vm.runInContext(`
    getPlanInputSelections(currentPlanMode().pending_request).validation
  `, appContext)), {
    question_id: 'validation',
    label: 'Other',
    value: 'Manual browser smoke',
    is_other: true
  });
});

test('deleteHistorySession cancellation uses local dialog and avoids delete request', async () => {
  const appContext = createAppHarnessContext();
  const action = vm.runInContext(`
    confirmCalls = [];
    confirmLocalAction = async function(options) {
      confirmCalls.push(options);
      return false;
    };
    window.confirm = function() {
      throw new Error('native confirm should not be used');
    };
    deleteHistorySession('session_delete_local_dialog');
  `, appContext);

  await action;

  assert.deepEqual(sameRealm(vm.runInContext(`confirmCalls`, appContext)), [
    {
      title: 'Delete session',
      message: 'Delete session session_delete_local_dialog?',
      confirmLabel: 'Delete',
      tone: 'danger'
    }
  ]);
  assert.equal(appContext.pendingRequests.length, 0);
});

test('deleteHistorySession ignores stale confirmation after history refresh', async () => {
  const appContext = createAppHarnessContext();
  const confirmResolvers = [];
  const toasts = [];
  const requestURLs = [];

  const action = vm.runInContext(`
    requestJSON = async function(url) {
      requestURLsRef.push(String(url));
      if (String(url).startsWith('/api/history')) {
        return {
          items: [{ id: 'session_current_b', status: 'completed' }],
          page: 1,
          page_size: 8,
          total: 1,
          total_pages: 1
        };
      }
      if (String(url) === '/api/overview') {
        return {};
      }
      return { deleted: true };
    };
    confirmLocalAction = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    deleteHistorySession('session_delete_stale_a');
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, toastsRef: toasts, requestURLsRef: requestURLs }));

  assert.equal(confirmResolvers.length, 1);
  assert.equal(appContext.pendingRequests.length, 0);

  vm.runInContext(`invalidateHistoryRenderSeq()`, appContext);

  confirmResolvers[0](true);
  await action;

  const deleteRequests = requestURLs.filter((url) => url.includes('/api/sessions/session_delete_stale_a'));
  assert.equal(deleteRequests.length, 0);
  assert.deepEqual(sameRealm(toasts), []);
});

test('clearHistory ignores stale confirmation after history refresh', async () => {
  const appContext = createAppHarnessContext();
  const confirmResolvers = [];
  const toasts = [];
  const requestURLs = [];

  const action = vm.runInContext(`
    requestJSON = async function(url) {
      requestURLsRef.push(String(url));
      if (String(url).startsWith('/api/history')) {
        return {
          items: [{ id: 'session_current_clear_b', status: 'completed' }],
          page: 1,
          page_size: 8,
          total: 1,
          total_pages: 1
        };
      }
      if (String(url) === '/api/overview') {
        return {};
      }
      return { cleared: true };
    };
    confirmLocalAction = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    clearHistory();
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, toastsRef: toasts, requestURLsRef: requestURLs }));

  assert.equal(confirmResolvers.length, 1);
  assert.equal(appContext.pendingRequests.length, 0);

  vm.runInContext(`invalidateHistoryRenderSeq()`, appContext);

  confirmResolvers[0](true);
  await action;

  const clearRequests = requestURLs.filter((url) => url === '/api/sessions/clear');
  assert.equal(clearRequests.length, 0);
  assert.deepEqual(sameRealm(toasts), []);
});

function installPlanModeAPITestWrappers(appContext) {
  vm.runInContext(`
    approvePlanMode = function(sessionID, payload = {}) {
      const suffix = payload.override_coverage ? '?override=1' : '';
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/planmode/approve' + suffix, { method: 'POST' });
    };
    answerPlanModeInput = function(sessionID, payload = {}) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/planmode/input', {
        method: 'POST',
        payload
      });
    };
  `, appContext);
}

function installGoalAPITestWrappers(appContext) {
  vm.runInContext(`
    pauseGoal = function(sessionID) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/goal/pause', { method: 'POST' });
    };
    resumeGoal = function(sessionID) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/goal/resume', { method: 'POST' });
    };
    completeGoal = function(sessionID) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/goal/complete', { method: 'POST' });
    };
    deleteGoal = function(sessionID) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/goal', { method: 'DELETE' });
    };
    approveMissionPlan = function(sessionID, payload = {}) {
      const suffix = payload.override_coverage ? '?override=1' : '';
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/mission/plan/approve' + suffix, { method: 'POST' });
    };
  `, appContext);
}

function installChatActionAPITestWrappers(appContext) {
  vm.runInContext(`
    steerSession = function(sessionID, payload = {}) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/steer', {
        method: 'POST',
        payload
      });
    };
    continueSession = function(sessionID, payload = {}) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/continue', {
        method: 'POST',
        payload
      });
    };
    startSession = function(payload = {}) {
      return requestJSON('/api/sessions/start', {
        method: 'POST',
        payload
      });
    };
    revisePlanMode = function(sessionID, message) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/planmode/revise', {
        method: 'POST',
        message
      });
    };
    interruptSession = function(sessionID) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/interrupt', {
        method: 'POST'
      });
    };
    stopSession = function(sessionID) {
      return requestJSON('/api/sessions/' + encodeURIComponent(sessionID) + '/stop', {
        method: 'POST'
      });
    };
  `, appContext);
}

function createWorkspaceHarnessContext() {
  const pendingRequests = [];
  const workspaceContext = {
    console: {
      error() {}
    },
    window: {},
    document: {
      createElement() {
        return fakeAppElement();
      },
      createTextNode(text) {
        return { textContent: String(text ?? ''), innerText: String(text ?? '') };
      }
    },
    CSS: {
      escape(value) {
        return String(value).replace(/"/g, '\\"');
      }
    },
    state: {
      meta: {
        workspace_root: '/tmp/workspace',
        workspace_switch_supported: false
      }
    },
    nodes: {
      fileTree: fakeAppElement(),
      editorFilename: fakeAppElement(),
      editorContent: fakeAppElement(),
      workspaceSubtitle: fakeAppElement(),
      workspaceRootChip: fakeAppElement()
    },
    requestJSON(url) {
      return new Promise((resolve, reject) => {
        pendingRequests.push({ url, resolve, reject });
      });
    },
    listWorkspaceFiles(path = '.') {
      return workspaceContext.requestJSON(`/api/files?path=${encodeURIComponent(path || '.')}`);
    },
    readWorkspaceFile(path, offset = 0, limit = 256 * 1024) {
      return workspaceContext.requestJSON(`/api/file/read?path=${encodeURIComponent(path)}&offset=${encodeURIComponent(String(offset || 0))}&limit=${encodeURIComponent(String(limit))}`);
    },
    createWorkspaceDirectory(path, name) {
      return workspaceContext.requestJSON('/api/files/mkdir', {
        method: 'POST',
        payload: { path: path || '.', name }
      });
    },
    uploadWorkspaceFile(path, file) {
      return workspaceContext.requestJSON('/api/files/upload', {
        method: 'POST',
        payload: { path: path || '.', file }
      });
    },
    renameWorkspaceFile(path, name) {
      return workspaceContext.requestJSON('/api/files/rename', {
        method: 'PATCH',
        payload: { path, name }
      });
    },
    deleteWorkspacePath(path) {
      return workspaceContext.requestJSON(`/api/files?path=${encodeURIComponent(path)}`, { method: 'DELETE' });
    },
    deleteWorkspacePaths(paths = []) {
      return workspaceContext.requestJSON('/api/files/delete', {
        method: 'POST',
        payload: { paths }
      });
    },
    workspaceDownloadURL(path) {
      return `/api/file/download?path=${encodeURIComponent(path)}`;
    },
    showToast() {}
  };
  vm.createContext(workspaceContext);
  vm.runInContext(utilsSource, workspaceContext, { filename: 'utils.js' });
  vm.runInContext(workspaceViewSource, workspaceContext, { filename: 'workspace-view.js' });
  vm.runInContext(`delete state.workspaceRequestSeq; delete state.selectedTreePath; delete state.workspaceFilePreview; workspaceViewState.requestSeq = 0;`, workspaceContext);
  vm.runInContext(`
    const realRenderFileTree = renderFileTree;
    renderFileTree = function(tree, container, level) {
      if (!container || container === nodes.fileTree || !level) {
        state.renderedTree = tree;
      }
      return realRenderFileTree(tree, container, level);
    };
  `, workspaceContext);
  workspaceContext.pendingRequests = pendingRequests;
  return workspaceContext;
}

test('safeMarkdown keeps language-tagged fences inside an open code block', () => {
  const html = context.safeMarkdown(['```markdown', '```go', 'fmt.Println("x")', '```'].join('\n'));

  assert.match(html, /<code class="language-markdown">```go\nfmt\.Println\(&quot;x&quot;\)<\/code>/);
  assert.doesNotMatch(html, /<p>fmt\.Println/);
});

test('safeMarkdown only closes nested fences when the fence is long enough', () => {
  const html = context.safeMarkdown(['````markdown', '```go', 'x', '```', '````'].join('\n'));

  assert.match(html, /<code class="language-markdown">```go\nx\n```<\/code>/);
});

test('safeMarkdown emits lazy images and noopener links', () => {
  const html = context.safeMarkdown('[docs](https://example.test) ![alt](./image.png)');

  assert.match(html, /rel="noopener noreferrer"/);
  assert.match(html, /class="md-img"/);
  assert.match(html, /loading="lazy"/);
});

test('safeMarkdown rejects protocol-relative links and images', () => {
  const html = context.safeMarkdown('[docs](//example.test/path) ![alt](//example.test/image.png)');

  assert.doesNotMatch(html, /href="\/\/example\.test/);
  assert.doesNotMatch(html, /src="\/\/example\.test/);
  assert.doesNotMatch(html, /<a\b/);
  assert.doesNotMatch(html, /<img\b/);
  assert.doesNotMatch(html, /class="md-img"/);
  assert.match(html, /<p>docs !\[alt\]\(\/\/example\.test\/image\.png\)<\/p>/);
});

test('renderMarkdownCached invalidates by content hash', () => {
  context.clearMarkdownCache();

  const first = context.renderMarkdownCached('same-message', '**first**');
  const second = context.renderMarkdownCached('same-message', '**second**');

  assert.notEqual(first, second);
  assert.match(second, /<strong>second<\/strong>/);
});

test('collectPlanInputAnswers requires explicit answers for every question', () => {
  const request = {
    questions: [
      { id: 'scope', options: [{ label: 'Small' }] },
      { id: 'validation', options: [{ label: 'Unit' }] }
    ]
  };

  const answers = context.collectPlanInputAnswers(request, {
    scope: { label: 'Small', value: 'Small' }
  });

  assert.deepEqual(sameRealm(answers), []);
});

test('collectPlanInputAnswers preserves selected multi-question answers', () => {
  const request = {
    questions: [
      { id: 'scope', options: [{ label: 'Small' }] },
      { id: 'validation', options: [{ label: 'Unit' }] }
    ]
  };

  const answers = context.collectPlanInputAnswers(request, {
    scope: { label: 'Small', value: 'Small' },
    validation: { label: 'Other', value: 'Browser smoke', is_other: true }
  });

  assert.deepEqual(sameRealm(answers), [
    { question_id: 'scope', label: 'Small', value: 'Small', is_other: false },
    { question_id: 'validation', label: 'Other', value: 'Browser smoke', is_other: true }
  ]);
});

test('collectPlanInputAnswers rejects non-other selections outside offered options', () => {
  const request = {
    questions: [
      { id: 'scope', options: [{ label: 'Small' }, { label: 'Large' }] }
    ]
  };

  const answers = context.collectPlanInputAnswers(request, {
    scope: { label: 'Surprise', value: 'Surprise' }
  });

  assert.deepEqual(sameRealm(answers), []);
});

test('collectPlanInputAnswers accepts offered option descriptions as non-other values', () => {
  const request = {
    questions: [
      { id: 'scope', options: [{ label: 'Small', description: 'Keep it focused.' }, { label: 'Large', description: 'Include cleanup.' }] }
    ]
  };

  const answers = context.collectPlanInputAnswers(request, {
    scope: { label: 'Small', value: 'Keep it focused.' }
  });

  assert.deepEqual(sameRealm(answers), [
    { question_id: 'scope', label: 'Small', value: 'Keep it focused.', is_other: false }
  ]);
});

test('isAcceptedLaunchResponse recognizes async launch responses only', () => {
  assert.equal(context.isAcceptedLaunchResponse({ session_id: 'session_123', status: 'accepted' }), true);
  assert.equal(context.isAcceptedLaunchResponse({ session_id: 'session_123', status: 'ACCEPTED' }), true);
  assert.equal(context.isAcceptedLaunchResponse({ session_id: 'session_123', status: 'completed' }), false);
  assert.equal(context.isAcceptedLaunchResponse({ goal_id: 'goal_123', status: 'active' }), false);
  assert.equal(context.isAcceptedLaunchResponse(null), false);
});

test('setSkillUploadPending disables and restores upload controls', () => {
  function fakeControl(text = '') {
    return {
      textContent: text,
      disabled: false,
      dataset: {},
      attrs: {},
      setAttribute(name, value) {
        this.attrs[name] = value;
      },
      removeAttribute(name) {
        delete this.attrs[name];
      },
      getAttribute(name) {
        return this.attrs[name] || null;
      }
    };
  }
  const mainLabel = fakeControl('上传 .zip');
  const mainButton = fakeControl('button shell with icon');
  mainButton.dataset.uploadDefaultLabel = 'Upload .zip';
  mainButton.querySelector = (selector) => selector === '[data-upload-label]' ? mainLabel : null;
  const emptyButton = fakeControl('Upload .zip Skill');
  const cardButton = fakeControl('Upload to Install');
  const freshCardButton = fakeControl('Upload to Install');
  const uploadInput = fakeControl('');
  const root = {
    getElementById(id) {
      return {
        'skill-upload-btn': mainButton,
        'empty-upload-btn': emptyButton,
        'skill-upload': uploadInput
      }[id] || null;
    },
    querySelectorAll(selector) {
      assert.equal(selector, '[data-skill-action][data-skill-installed="0"]');
      return [cardButton, freshCardButton];
    }
  };

  context.setSkillUploadPending(root, true);

  for (const control of [mainButton, emptyButton, cardButton]) {
    assert.equal(control.disabled, true);
    assert.equal(control.getAttribute('aria-busy'), 'true');
  }
  assert.equal(mainLabel.textContent, 'Uploading...');
  assert.equal(mainButton.textContent, 'button shell with icon');
  assert.equal(emptyButton.textContent, 'Uploading...');
  assert.equal(cardButton.textContent, 'Uploading...');
  assert.equal(uploadInput.disabled, true);

  context.setSkillUploadPending(root, false);

  assert.equal(mainButton.disabled, false);
  assert.equal(mainLabel.textContent, 'Upload .zip');
  assert.equal(mainButton.textContent, 'button shell with icon');
  assert.equal(emptyButton.disabled, false);
  assert.equal(emptyButton.textContent, 'Upload .zip Skill');
  assert.equal(cardButton.disabled, false);
  assert.equal(cardButton.textContent, 'Upload to Install');
  assert.equal(mainButton.getAttribute('aria-busy'), null);
  assert.equal(uploadInput.disabled, false);

  freshCardButton.textContent = 'Upload to Install';
  delete freshCardButton.dataset.uploadIdleLabel;
  context.setSkillUploadPending(root, false);
  assert.equal(freshCardButton.textContent, 'Upload to Install');
});

test('skill upload pending guard is isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  const uploadInput = fakeAppElement();
  uploadInput.id = 'skill-upload';
  uploadInput.files = [{ name: 'demo-skill.zip' }];
  uploadInput.value = 'C:\\fakepath\\demo-skill.zip';
  const uploadButton = fakeAppElement({ value: 'Upload .zip Skill' });
  uploadButton.textContent = 'Upload .zip Skill';
  const toasts = [];

  vm.runInContext(`
    nodes.skillUpload = uploadInputRef;
    nodes.skillUploadBtn = uploadButtonRef;
    nodes.skillsGrid = fakeAppElement();
    nodes.toastRack = fakeAppElement();
    document.getElementById = function(id) {
      if (id === 'skill-upload') return uploadInputRef;
      if (id === 'skill-upload-btn') return uploadButtonRef;
      return null;
    };
    document.querySelectorAll = function(selector) {
      if (selector === '[data-skill-action][data-skill-installed="0"]') return [];
      return [];
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
  `, Object.assign(appContext, { uploadInputRef: uploadInput, uploadButtonRef: uploadButton, toastsRef: toasts, fakeAppElement }));

  const uploadPromise = vm.runInContext(`
    handleSkillUploadChange({ target: uploadInputRef });
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/skills/upload');

  const pending = vm.runInContext(`({
    stateHasSkillUploadInFlight: Object.prototype.hasOwnProperty.call(state, 'skillUploadInFlight'),
    uploadInFlight: isSkillUploadInFlight(),
    buttonDisabled: uploadButtonRef.disabled,
    inputDisabled: uploadInputRef.disabled
  })`, appContext);
  assert.equal(pending.stateHasSkillUploadInFlight, false);
  assert.equal(pending.uploadInFlight, true);
  assert.equal(pending.buttonDisabled, true);
  assert.equal(pending.inputDisabled, true);

  const duplicateInput = fakeAppElement();
  duplicateInput.id = 'skill-upload';
  duplicateInput.files = [{ name: 'other-skill.zip' }];
  duplicateInput.value = 'C:\\fakepath\\other-skill.zip';
  await vm.runInContext(`handleSkillUploadChange({ target: duplicateInputRef })`, Object.assign(appContext, { duplicateInputRef: duplicateInput }));
  assert.equal(appContext.pendingRequests.length, 1);
  assert.deepEqual(sameRealm(toasts.at(-1)), { message: 'Skill upload is already in progress.', tone: 'info' });
  assert.equal(duplicateInput.value, '');

  appContext.pendingRequests[0].resolve({ installed: 1 });
  await uploadPromise;

  const restored = vm.runInContext(`({
    stateHasSkillUploadInFlight: Object.prototype.hasOwnProperty.call(state, 'skillUploadInFlight'),
    uploadInFlight: isSkillUploadInFlight(),
    buttonDisabled: uploadButtonRef.disabled,
    inputDisabled: uploadInputRef.disabled,
    inputValue: uploadInputRef.value
  })`, appContext);
  assert.equal(restored.stateHasSkillUploadInFlight, false);
  assert.equal(restored.uploadInFlight, false);
  assert.equal(restored.buttonDisabled, false);
  assert.equal(restored.inputDisabled, false);
  assert.equal(restored.inputValue, '');
});

test('mergeMessageWindows preserves older loaded messages when server tail overlaps', () => {
  const current = ['m1', 'm2', 'm3', 'm4'].map((id) => ({ id }));
  const next = ['m3', 'm4', 'm5'].map((id) => ({ id }));

  const merged = context.mergeMessageWindows(current, next);

  assert.deepEqual(sameRealm(merged.messages.map((message) => message.id)), ['m1', 'm2', 'm3', 'm4', 'm5']);
  assert.equal(merged.hasGap, false);
  assert.equal(merged.gapAnchorId, '');
});

test('mergeMessageWindows exposes a paging gap when server tail no longer overlaps loaded history', () => {
  const current = ['m1', 'm2', 'm3'].map((id) => ({ id }));
  const next = ['m8', 'm9'].map((id) => ({ id }));

  const merged = context.mergeMessageWindows(current, next);

  assert.deepEqual(sameRealm(merged.messages.map((message) => message.id)), ['m1', 'm2', 'm3', 'm8', 'm9']);
  assert.equal(merged.hasGap, true);
  assert.equal(merged.gapAnchorId, 'm8');
});

test('mergeMessagesBeforeAnchor fills middle pages before the server tail anchor', () => {
  const current = ['m1', 'm2', 'm8', 'm9'].map((id) => ({ id }));
  const older = ['m2', 'm3', 'm4', 'm5'].map((id) => ({ id }));

  const merged = context.mergeMessagesBeforeAnchor(current, older, 'm8');

  assert.deepEqual(sameRealm(merged.map((message) => message.id)), ['m1', 'm2', 'm3', 'm4', 'm5', 'm8', 'm9']);
});

test('background notification cards keep queue facts without frontend job actions', () => {
  const notification = {
    queue_job_id: 'job_notification_open',
    session_id: 'child_notification_open',
    agent_name: 'reviewer',
    agent_role: 'evaluator',
    status: 'completed',
    session_status: 'completed',
    final_text: 'done'
  };

  const full = context.renderNotificationCard(notification);
  const preview = context.renderBackgroundNotificationsPreview([notification]);

  assert.match(full, /job_no/);
  assert.doesNotMatch(full, /data-open-job=/);
  assert.doesNotMatch(full, />Open job<\/button>/);
  assert.match(full, /data-open-session="child_notification_open"/);
  assert.match(preview, /job_no/);
  assert.doesNotMatch(preview, /data-open-job=/);
  assert.doesNotMatch(preview, />Open job<\/button>/);
  assert.match(preview, /data-open-session="child_notification_open"/);
});

test('background notification cards prefer errors over final text for failed facts', () => {
  const notification = {
    queue_job_id: 'job_failed_handoff',
    session_id: 'child_failed_handoff',
    agent_name: 'reviewer',
    agent_role: 'evaluator',
    status: 'failed',
    session_status: 'completed',
    final_text: 'child reported success before handoff failed',
    last_error: 'load child session messages.jsonl for queue job job_failed_handoff: corrupt'
  };

  const full = context.renderNotificationCard(notification);
  const preview = context.renderBackgroundNotificationsPreview([notification]);

  assert.match(full, /messages\.jsonl/);
  assert.doesNotMatch(full, /child reported success before handoff failed/);
  assert.match(preview, /messages\.jsonl/);
  assert.doesNotMatch(preview, /child reported success before handoff failed/);
});

test('background notification renderer localizes fallbacks without rewriting durable facts', () => {
  const fallback = context.renderNotificationCard({
    id: 'notification_fallback',
    queue_job_id: 'job_fallback',
    agent_name: 'Settings',
    agent_role: 'evaluator',
    status: 'completed',
    session_status: 'completed',
    delivery_status: 'accepted'
  });
  const raw = context.renderNotificationCard({
    id: 'notification_raw',
    queue_job_id: 'job_raw',
    status: 'completed',
    session_status: 'completed',
    delivery_status: 'accepted',
    final_text: 'Settings'
  });

  assert.match(fallback, /class="job-card-title"><span translate="no" data-i18n-skip>Settings · evaluator<\/span>/);
  assert.match(fallback, /class="notification-copy">No final text recorded\.<\/div>/);
  assert.match(fallback, /<span translate="no" data-i18n-skip>job_fallback<\/span> · <span translate="no" data-i18n-skip>accepted<\/span> · <span>session<\/span> <span>Completed<\/span>/);
  assert.doesNotMatch(fallback, /class="notification-copy"[^>]+translate="no"/);
  assert.match(raw, /class="notification-copy" translate="no" data-i18n-skip>Settings<\/div>/);
});

test('queue job renderers prefer queue failure over completed child status', () => {
  const job = {
    id: 'job_failed_handoff',
    session_id: 'child_failed_handoff',
    parent_session_id: 'parent_failed_handoff',
    agent_name: 'reviewer',
    agent_role: 'evaluator',
    mode: 'exec',
    status: 'failed',
    session_status: 'completed',
    prompt: 'review handoff',
    final_text: 'child reported success before handoff failed',
    last_error: 'load child session messages.jsonl for queue job job_failed_handoff: corrupt'
  };

  const card = context.renderQueueJobCard(job);
  const subAgentRow = context.renderSubAgentJobRow(job);
  vm.runInContext(`
    setSelectedQueueJob('job_failed_handoff', ${JSON.stringify(job)});
  `, context);
  const selectedPanel = context.renderSelectedQueueJobPanel();

  for (const html of [card, subAgentRow, selectedPanel]) {
    assert.match(html, />Failed<\/span>/);
    assert.doesNotMatch(html, />Completed<\/span>/);
  }
});

test('sub-agent cards show queue final text for completed jobs', () => {
  const row = {
    session: {
      id: 'child_completed_with_final',
      status: 'completed',
      provider: 'openai',
      model: 'gpt-test',
      phase: 'complete',
      queue_job_id: 'job_completed_with_final',
      agent_name: 'reviewer',
      agent_role: 'evaluator'
    },
    job: {
      id: 'job_completed_with_final',
      session_id: 'child_completed_with_final',
      status: 'completed',
      session_status: 'completed',
      mode: 'exec',
      final_text: 'child completed final summary for parent handoff'
    }
  };

  const html = context.renderSubAgentCard(row);

  assert.match(html, /child completed final summary for parent handoff/);
});

test('sub-agent cards prefer blocked queue status over resumable child status', () => {
  const row = {
    session: {
      id: 'child_blocked_waiting',
      status: 'awaiting_input',
      provider: 'openai',
      model: 'gpt-test',
      phase: 'awaiting_input',
      queue_job_id: 'job_blocked_waiting',
      agent_name: 'reviewer',
      agent_role: 'evaluator'
    },
    job: {
      id: 'job_blocked_waiting',
      session_id: 'child_blocked_waiting',
      status: 'blocked',
      session_status: 'awaiting_input',
      mode: 'exec',
      last_error: 'child session is resumable: awaiting_input'
    }
  };

  const html = context.renderSubAgentCard(row);

  assert.match(html, />Blocked<\/span>/);
  assert.doesNotMatch(html, />Awaiting input<\/span>/);
  assert.match(html, /child session is resumable/);
});

test('sub-agent budget summary exposes attempt usage remaining deadline and reason', () => {
  const summary = context.effectiveBudgetSummary({
    status: 'exhausted',
    attempt: 2,
    max_turns_per_attempt: 5,
    used_turns: 4,
    remaining_turns: 1,
    max_active_runtime_ms: 60000,
    used_active_runtime_ms: 45000,
    remaining_active_runtime_ms: 15000,
    active_runtime_checkpoint_interval_ms: 1000,
    active_runtime_checkpoint_at: '2026-07-15T11:59:59Z',
    active_runtime_lease_open: true,
    active_runtime_last_recovery_ms: 1000,
    active_runtime_last_recovery_at: '2026-07-15T11:00:00Z',
    absolute_deadline_at: '2026-07-15T12:00:00Z',
    last_reason: 'child_budget_active_runtime_exceeded',
    source: 'runtime.child_budget'
  });
  assert.match(summary, /attempt 2/);
  assert.match(summary, /turns 4\/5, 1 left/);
  assert.match(summary, /active 45s\/60s, 15s left/);
  assert.match(summary, /checkpoint/);
  assert.match(summary, /lease open/);
  assert.match(summary, /crash recovery \+1s/);
  assert.match(summary, /deadline/);
  assert.match(summary, /child_budget_active_runtime_exceeded/);
  assert.match(summary, /runtime.child_budget/);
});

test('sub-agent cards distinguish cancelled queue outcomes and show parent actions', () => {
  const card = context.renderSubAgentCard({
    job: {
      id: 'job_cancelled_budget',
      status: 'cancelled',
      session_status: 'paused',
      stop_reason: 'agent_stop',
      effective_budget: {
        status: 'exhausted',
        attempt: 1,
        max_turns_per_attempt: 1,
        used_turns: 1,
        remaining_turns: 0,
        last_reason: 'child_budget_turns_exceeded',
        source: 'runtime.child_budget'
      }
    }
  });
  assert.match(card, /Cancelled/);
  assert.match(card, /child_budget_turns_exceeded/);

  const notification = context.renderNotificationCard({
    id: 'notification_budget',
    queue_job_id: 'job_budget',
    status: 'blocked',
    session_status: 'paused',
    delivery_status: 'pending',
    available_actions: ['extend_resume', 'cancel_settle', 'inspect'],
    effective_budget: {
      status: 'exhausted',
      attempt: 1,
      max_turns_per_attempt: 1,
      used_turns: 1,
      remaining_turns: 0,
      last_reason: 'child_budget_turns_exceeded',
      source: 'runtime.child_budget'
    }
  });
  assert.match(notification, /Parent actions:/);
  assert.match(notification, /Extend resume/);
  assert.match(notification, /Cancel settle/);
  assert.match(notification, /Inspect/);
});

test('sub-agent float session rows prefer blocked queue status over resumable child status', () => {
  const sessionItem = {
    id: 'child_float_blocked',
    status: 'awaiting_input',
    provider: 'openai',
    model: 'gpt-test',
    phase: 'awaiting_input',
    queue_job_id: 'job_float_blocked',
    agent_name: 'reviewer',
    agent_role: 'evaluator'
  };
  const job = {
    id: 'job_float_blocked',
    session_id: 'child_float_blocked',
    status: 'blocked',
    session_status: 'awaiting_input',
    mode: 'exec',
    last_error: 'child session is resumable: awaiting_input'
  };

  const html = context.renderSubAgentSessionRow(sessionItem, job);

  assert.match(html, />Blocked<\/span>/);
  assert.doesNotMatch(html, />Awaiting input<\/span>/);
});

test('refreshSelectedQueueJobDetail ignores stale async responses after selection changes', async () => {
  const appContext = createAppHarnessContext();
  const slowRefresh = vm.runInContext(`
    setSelectedQueueJob('job_slow_a');
    refreshSelectedQueueJobDetail();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /job_slow_a/);

  await vm.runInContext(`
    setSelectedQueueJob('job_fast_b');
    refreshSelectedQueueJobDetail([{ id: 'job_fast_b', prompt: 'fast selected' }]);
  `, appContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasSelectedQueueJobId: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobId'),
    stateHasSelectedQueueJobDetail: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobDetail'),
    selected: selectedQueueJobId(),
    detailID: selectedQueueJobDetail()?.id,
    prompt: selectedQueueJobDetail()?.prompt
  })`, appContext)), {
    stateHasSelectedQueueJobId: false,
    stateHasSelectedQueueJobDetail: false,
    selected: 'job_fast_b',
    detailID: 'job_fast_b',
    prompt: 'fast selected'
  });

  appContext.pendingRequests[0].resolve({ id: 'job_slow_a', prompt: 'stale slow' });
  await slowRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasSelectedQueueJobId: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobId'),
    stateHasSelectedQueueJobDetail: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobDetail'),
    selected: selectedQueueJobId(),
    detailID: selectedQueueJobDetail()?.id,
    prompt: selectedQueueJobDetail()?.prompt
  })`, appContext)), {
    stateHasSelectedQueueJobId: false,
    stateHasSelectedQueueJobDetail: false,
    selected: 'job_fast_b',
    detailID: 'job_fast_b',
    prompt: 'fast selected'
  });
});

test('selected current-session queue job detail keeps chat polling active while job runs', () => {
  const appContext = createAppHarnessContext();

  const result = vm.runInContext(`(() => {
    setCurrentViewName('chat');
    setPageVisibilityHidden(false);
    setLiveRelayConnected(true);
    setGeneratingViewState(false);
    state.sessionId = 'parent_polling';
    state.sessionBacked = true;
    state.overview = { sessions: [] };
    state.sessionDetail = {
      metadata: { id: 'parent_polling' },
      state: { status: 'awaiting_input' },
      children: { sessions: [], jobs: [] },
      messages: []
    };
    setSelectedQueueJob('job_outside_window', {
      id: 'job_outside_window',
      status: 'running',
      parent_session_id: 'parent_polling'
    });
    return {
      stateHasSelectedQueueJobId: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobId'),
      stateHasSelectedQueueJobDetail: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobDetail'),
      runLoop: shouldRunPollingLoop(),
      overview: shouldPollChatOverview(),
      current: shouldPollCurrentSession(),
      interval: pollingIntervalForState()
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    stateHasSelectedQueueJobId: false,
    stateHasSelectedQueueJobDetail: false,
    runLoop: true,
    overview: true,
    current: true,
    interval: 1600
  });
});

test('selected queue job detail from another parent does not keep chat polling active', () => {
  const appContext = createAppHarnessContext();

  const result = vm.runInContext(`(() => {
    setCurrentViewName('chat');
    setPageVisibilityHidden(false);
    setLiveRelayConnected(true);
    setGeneratingViewState(false);
    state.sessionId = 'parent_current';
    state.sessionBacked = true;
    state.overview = { sessions: [] };
    state.sessionDetail = {
      metadata: { id: 'parent_current' },
      state: { status: 'awaiting_input' },
      children: { sessions: [], jobs: [] },
      messages: []
    };
    setSelectedQueueJob('job_other_parent', {
      id: 'job_other_parent',
      status: 'running',
      parent_session_id: 'parent_other'
    });
    return {
      stateHasSelectedQueueJobId: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobId'),
      stateHasSelectedQueueJobDetail: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobDetail'),
      runLoop: shouldRunPollingLoop(),
      overview: shouldPollChatOverview(),
      current: shouldPollCurrentSession(),
      interval: pollingIntervalForState()
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    stateHasSelectedQueueJobId: false,
    stateHasSelectedQueueJobDetail: false,
    runLoop: false,
    overview: false,
    current: false,
    interval: 5000
  });
});

test('inspector tab selection is isolated from durable app state', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(sessionViewSource, appContext, { filename: 'session-view.js' });

  const result = vm.runInContext(`(() => {
    state.sessionId = 'session_inspector_tab';
    state.sessionBacked = true;
    setInspectorTab('timeline');
    state.sessionDetail = {
      metadata: { id: 'session_inspector_tab' },
      state: { status: 'running' },
      timeline: [
        { kind: 'event', event_type: 'session.started', event_id: 'evt_inspector', time: '2026-05-29T00:00:00Z' }
      ]
    };
    renderTimelineItem = function renderTimelineItem(item) {
      return '<span data-event="' + item.event_id + '">' + item.event_id + '</span>';
    };
    const timelinePanel = renderInspectorPanel();
    setInspectorTab('invalid');
    state.sessionDetail = {
      metadata: { id: 'session_inspector_tab' },
      state: { status: 'running' },
      task_board: {
        todo: [{ content: 'Review tab state', status: 'pending' }],
        tasks: []
      }
    };
    const fallbackPanel = renderInspectorPanel();
    return {
      stateHasInspectorTab: Object.prototype.hasOwnProperty.call(state, 'inspectorTab'),
      activeTab: activeInspectorTab(),
      timelineActive: timelinePanel.includes('data-inspector-tab="timeline"') && timelinePanel.includes('inspector-tab active'),
      timelineRendered: timelinePanel.includes('evt_inspector'),
      fallbackRenderedTasks: fallbackPanel.includes('Review tab state')
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    stateHasInspectorTab: false,
    activeTab: 'invalid',
    timelineActive: true,
    timelineRendered: true,
    fallbackRenderedTasks: true
  });
});

test('flow lane renders the latest compact timeline events from newest-first detail timelines', () => {
  const previousIsCompactFlowEvent = context.isCompactFlowEvent;
  const previousRenderTimelineItem = context.renderTimelineItem;
  context.isCompactFlowEvent = () => true;
  context.renderTimelineItem = (item) => `<span data-event="${item.event_id}">${item.event_id}</span>`;
  try {
    const html = vm.runInContext(`(() => {
      state.sessionDetail = {
        timeline: [
          { kind: 'event', event_type: 'tool.after', event_id: 'evt_5_newest', time: '2026-05-29T00:00:05Z' },
          { kind: 'event', event_type: 'tool.before', event_id: 'evt_4', time: '2026-05-29T00:00:04Z' },
          { kind: 'event', event_type: 'provider.call', event_id: 'evt_3', time: '2026-05-29T00:00:03Z' },
          { kind: 'event', event_type: 'session.started', event_id: 'evt_2', time: '2026-05-29T00:00:02Z' },
          { kind: 'event', event_type: 'session.created', event_id: 'evt_1_oldest', time: '2026-05-29T00:00:01Z' }
        ]
      };
      return renderFlowLane();
    })()`, context);

    assert.doesNotMatch(html, /evt_1_oldest/);
    assert.doesNotMatch(html, /evt_2/);
    assert.match(html, /evt_3/);
    assert.match(html, /evt_4/);
    assert.match(html, /evt_5_newest/);
    assert.ok(html.indexOf('evt_3') < html.indexOf('evt_4'));
    assert.ok(html.indexOf('evt_4') < html.indexOf('evt_5_newest'));
  } finally {
    context.isCompactFlowEvent = previousIsCompactFlowEvent;
    context.renderTimelineItem = previousRenderTimelineItem;
  }
});

test('timeline inspector renders detail timeline entries chronologically', () => {
  const previousRenderTimelineItem = context.renderTimelineItem;
  context.renderTimelineItem = (item) => `<span data-event="${item.event_id}">${item.event_id}</span>`;
  try {
    const html = vm.runInContext(`(() => renderTimelinePanel({
      timeline: [
        { kind: 'event', event_type: 'tool.after', event_id: 'evt_newest', time: '2026-05-29T00:00:03Z' },
        { kind: 'event', event_type: 'provider.call', event_id: 'evt_middle', time: '2026-05-29T00:00:02Z' },
        { kind: 'event', event_type: 'session.started', event_id: 'evt_oldest', time: '2026-05-29T00:00:01Z' }
      ]
    }))()`, context);

    assert.ok(html.indexOf('evt_oldest') < html.indexOf('evt_middle'));
    assert.ok(html.indexOf('evt_middle') < html.indexOf('evt_newest'));
  } finally {
    context.renderTimelineItem = previousRenderTimelineItem;
  }
});

test('refreshCurrentSession ignores stale session detail responses after selection changes', async () => {
  const appContext = createAppHarnessContext();
  const slowRefresh = vm.runInContext(`
    state.sessionId = 'session_slow_a';
    state.sessionBacked = true;
    refreshCurrentSession();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_slow_a/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({
    metadata: { id: 'session_slow_a' },
    state: { status: 'failed', last_error: 'stale detail' },
    messages: [{ id: 'm_slow', role: 'assistant', text: 'stale' }]
  });
  await slowRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    detailID: state.sessionDetail?.metadata?.id,
    status: state.sessionDetail?.state?.status,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id)
  })`, appContext)), {
    selected: 'session_fast_b',
    detailID: 'session_fast_b',
    status: 'completed',
    messageIDs: []
  });
});

test('refreshCurrentSession rechecks selected session after queue detail enrichment', async () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    updateSessionId = function() {};
    reconcileOptimisticMessages = function() {};
    renderCurrentSession = function() {
      state.renderCount = (state.renderCount || 0) + 1;
    };
    updateUI = function() {};
    syncPollingForState = function() {};
  `, appContext);
  const slowRefresh = vm.runInContext(`
    state.sessionId = 'session_slow_queue_a';
    state.sessionBacked = true;
    setInspectorTab('agents');
    setSelectedQueueJob('job_slow_queue_a');
    resetLiveEvents();
    refreshCurrentSession();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_slow_queue_a/);

  appContext.pendingRequests[0].resolve({
    metadata: { id: 'session_slow_queue_a' },
    state: { status: 'running' },
    children: { jobs: [] },
    messages: [{ id: 'm_slow_queue', role: 'assistant', text: 'slow queue detail' }],
    timeline: []
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(appContext.pendingRequests.length, 2);
  assert.match(appContext.pendingRequests[1].url, /job_slow_queue_a/);

  vm.runInContext(`
    state.sessionId = 'session_fast_queue_b';
    state.sessionBacked = true;
    setSelectedQueueJob('');
    state.sessionDetail = {
      metadata: { id: 'session_fast_queue_b' },
      state: { status: 'completed' },
      messages: []
    };
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Fast selected', copy: '', tone: 'neutral' });
  `, appContext);

  appContext.pendingRequests[1].resolve({ id: 'job_slow_queue_a', prompt: 'stale queue detail' });
  await slowRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    detailID: state.sessionDetail?.metadata?.id,
    status: state.sessionDetail?.state?.status,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title,
    renderCount: state.renderCount || 0,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id)
  })`, appContext)), {
    selected: 'session_fast_queue_b',
    detailID: 'session_fast_queue_b',
    status: 'completed',
    generating: false,
    activityTitle: 'Fast selected',
    renderCount: 0,
    messageIDs: []
  });
});

test('refreshCurrentSession skips stale queue detail when a newer same-session refresh is queued', async () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    updateSessionId = function() {};
    reconcileOptimisticMessages = function() {};
    renderCurrentSession = function() {
      state.renderCount = (state.renderCount || 0) + 1;
    };
    updateUI = function() {};
    syncPollingForState = function() {};
  `, appContext);
  const firstRefresh = vm.runInContext(`
    state.sessionId = 'session_same_enrich';
    state.sessionBacked = true;
    setInspectorTab('agents');
    setSelectedQueueJob('job_same_enrich');
    resetLiveEvents();
    refreshCurrentSession();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_same_enrich/);

  appContext.pendingRequests[0].resolve({
    metadata: { id: 'session_same_enrich' },
    state: { status: 'running' },
    children: { jobs: [] },
    messages: [{ id: 'm_same_enrich_old', role: 'assistant', text: 'old detail' }],
    timeline: []
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(appContext.pendingRequests.length, 2);
  assert.match(appContext.pendingRequests[1].url, /job_same_enrich/);

  await vm.runInContext(`refreshCurrentSession()`, appContext);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasRefreshingSession: Object.prototype.hasOwnProperty.call(state, 'refreshingSession'),
    stateHasNeedsSessionRefresh: Object.prototype.hasOwnProperty.call(state, 'needsSessionRefresh'),
    refreshing: sessionViewState.refreshing,
    needsRefresh: sessionViewState.needsRefresh
  })`, appContext)), {
    stateHasRefreshingSession: false,
    stateHasNeedsSessionRefresh: false,
    refreshing: true,
    needsRefresh: true
  });

  appContext.pendingRequests[1].resolve({ id: 'job_same_enrich', prompt: 'stale enriched detail' });
  await firstRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    stateHasSelectedQueueJobId: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobId'),
    stateHasSelectedQueueJobDetail: Object.prototype.hasOwnProperty.call(state, 'selectedQueueJobDetail'),
    selectedJob: selectedQueueJobId(),
    selectedJobDetail: selectedQueueJobDetail(),
    renderCount: state.renderCount || 0
  })`, appContext)), {
    selected: 'session_same_enrich',
    stateHasSelectedQueueJobId: false,
    stateHasSelectedQueueJobDetail: false,
    selectedJob: 'job_same_enrich',
    selectedJobDetail: null,
    renderCount: 0
  });
});

test('refreshCurrentSession skips stale same-session detail when a newer refresh is queued', async () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    queueSessionRefresh = function() {
      refreshCurrentSession();
    };
  `, appContext);

  const firstRefresh = vm.runInContext(`
    state.sessionId = 'session_same_refresh';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    state.sessionDetail = {
      metadata: { id: 'session_same_refresh' },
      state: { status: 'running' },
      messages: [{ id: 'm_current', role: 'assistant', text: 'current detail' }],
      timeline: []
    };
    refreshCurrentSession();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_same_refresh/);

  await vm.runInContext(`refreshCurrentSession()`, appContext);

  appContext.pendingRequests[0].resolve({
    metadata: { id: 'session_same_refresh' },
    state: { status: 'completed' },
    messages: [{ id: 'm_stale', role: 'assistant', text: 'stale detail' }],
    timeline: []
  });
  await firstRefresh;

  assert.equal(appContext.pendingRequests.length, 2);
  assert.match(appContext.pendingRequests[1].url, /session_same_refresh/);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasRefreshingSession: Object.prototype.hasOwnProperty.call(state, 'refreshingSession'),
    stateHasNeedsSessionRefresh: Object.prototype.hasOwnProperty.call(state, 'needsSessionRefresh'),
    refreshing: sessionViewState.refreshing,
    needsRefresh: sessionViewState.needsRefresh,
    status: state.sessionDetail?.state?.status,
    generating: isGenerating(),
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id)
  })`, appContext)), {
    stateHasRefreshingSession: false,
    stateHasNeedsSessionRefresh: false,
    refreshing: true,
    needsRefresh: false,
    status: 'running',
    generating: true,
    messageIDs: ['m_current']
  });

  appContext.pendingRequests[1].resolve({
    metadata: { id: 'session_same_refresh' },
    state: { status: 'running' },
    messages: [{ id: 'm_updated', role: 'assistant', text: 'updated detail' }],
    timeline: []
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasRefreshingSession: Object.prototype.hasOwnProperty.call(state, 'refreshingSession'),
    stateHasNeedsSessionRefresh: Object.prototype.hasOwnProperty.call(state, 'needsSessionRefresh'),
    refreshing: sessionViewState.refreshing,
    needsRefresh: sessionViewState.needsRefresh,
    status: state.sessionDetail?.state?.status,
    generating: isGenerating(),
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id)
  })`, appContext)), {
    stateHasRefreshingSession: false,
    stateHasNeedsSessionRefresh: false,
    refreshing: false,
    needsRefresh: false,
    status: 'running',
    generating: true,
    messageIDs: ['m_current', 'm_updated']
  });
});

test('loadEarlierMessages ignores stale page responses after session changes', async () => {
  const appContext = createAppHarnessContext();
  const slowLoad = vm.runInContext(`
    state.sessionId = 'session_slow_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_slow_a' },
      messages: [{ id: 'm8', role: 'assistant', text: 'tail' }],
      timeline: []
    };
    setHasMoreMessagesToLoad(true);
    setOldestLoadedMessageId('m8');
    loadEarlierMessages();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_slow_a/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      messages: [{ id: 'b1', role: 'assistant', text: 'current' }],
      timeline: []
    };
    setHasMoreMessagesToLoad(false);
    setOldestLoadedMessageId('b1');
  `, appContext);

  appContext.pendingRequests[0].resolve({
    messages: [
      { id: 'm6', role: 'assistant', text: 'stale older' },
      { id: 'm7', role: 'assistant', text: 'stale newer' }
    ],
    has_more: false
  });
  await slowLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasMessagePageRequestSeq: Object.prototype.hasOwnProperty.call(state, 'messagePageRequestSeq'),
    stateHasLoadingEarlier: Object.prototype.hasOwnProperty.call(state, 'loadingEarlier'),
    stateHasPreserveScrollAfterRender: Object.prototype.hasOwnProperty.call(state, 'preserveScrollAfterRender'),
    stateHasHasMoreMessages: Object.prototype.hasOwnProperty.call(state, 'hasMoreMessages'),
    stateHasOldestMessageId: Object.prototype.hasOwnProperty.call(state, 'oldestMessageId'),
    stateHasLoadedAllEarlierMessages: Object.prototype.hasOwnProperty.call(state, 'loadedAllEarlierMessages'),
    stateHasMessageGapAnchorId: Object.prototype.hasOwnProperty.call(state, 'messageGapAnchorId'),
    selected: state.sessionId,
    detailID: state.sessionDetail?.metadata?.id,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id),
    hasMore: hasMoreMessagesToLoad(),
    oldest: oldestLoadedMessageId(),
    loadingEarlier: isLoadingEarlierMessages(),
    preserveScrollAfterRender: preserveScrollAfterRenderHeight()
  })`, appContext)), {
    stateHasMessagePageRequestSeq: false,
    stateHasLoadingEarlier: false,
    stateHasPreserveScrollAfterRender: false,
    stateHasHasMoreMessages: false,
    stateHasOldestMessageId: false,
    stateHasLoadedAllEarlierMessages: false,
    stateHasMessageGapAnchorId: false,
    selected: 'session_fast_b',
    detailID: 'session_fast_b',
    messageIDs: ['b1'],
    hasMore: false,
    oldest: 'b1',
    loadingEarlier: false,
    preserveScrollAfterRender: null
  });
});

test('loadEarlierMessages ignores stale page responses after same-session window reset', async () => {
  const appContext = createAppHarnessContext();
  const slowLoad = vm.runInContext(`
    state.sessionId = 'session_same_reset';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_same_reset' },
      messages: [{ id: 'm8', role: 'assistant', text: 'old tail' }],
      timeline: []
    };
    setHasMoreMessagesToLoad(true);
    setOldestLoadedMessageId('m8');
    loadEarlierMessages();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_same_reset/);

  vm.runInContext(`
    resetMessagePagingWindowState();
    state.sessionId = 'session_same_reset';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_same_reset' },
      messages: [{ id: 'fresh1', role: 'assistant', text: 'fresh tail' }],
      timeline: []
    };
    setHasMoreMessagesToLoad(false);
    setOldestLoadedMessageId('fresh1');
  `, appContext);

  appContext.pendingRequests[0].resolve({
    messages: [
      { id: 'm6', role: 'assistant', text: 'stale older' },
      { id: 'm7', role: 'assistant', text: 'stale newer' }
    ],
    has_more: false
  });
  await slowLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    detailID: state.sessionDetail?.metadata?.id,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id),
    hasMore: hasMoreMessagesToLoad(),
    oldest: oldestLoadedMessageId(),
    loadingEarlier: isLoadingEarlierMessages(),
    preserveScrollAfterRender: preserveScrollAfterRenderHeight()
  })`, appContext)), {
    selected: 'session_same_reset',
    detailID: 'session_same_reset',
    messageIDs: ['fresh1'],
    hasMore: false,
    oldest: 'fresh1',
    loadingEarlier: false,
    preserveScrollAfterRender: null
  });
});

test('loadEarlierMessages preserves a polling-created gap while an older page is in flight', async () => {
  const appContext = createAppHarnessContext();
  const pageLoad = vm.runInContext(`
    state.sessionId = 'session_gap_race';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_gap_race' },
      messages: [
        { id: 'm41', role: 'assistant', text: 'old tail 41' },
        { id: 'm42', role: 'assistant', text: 'old tail 42' }
      ],
      timeline: []
    };
    setHasMoreMessagesToLoad(true);
    setOldestLoadedMessageId('m41');
    setLoadedAllEarlierMessages(false);
    setMessageGapAnchorId('');
    loadEarlierMessages();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /before_id=m41/);

  vm.runInContext(`
    state.sessionDetail.messages = [
      { id: 'm41', role: 'assistant', text: 'old tail 41' },
      { id: 'm42', role: 'assistant', text: 'old tail 42' },
      { id: 'm50', role: 'assistant', text: 'new tail 50' },
      { id: 'm51', role: 'assistant', text: 'new tail 51' }
    ];
    setMessageGapAnchorId('m50');
    setHasMoreMessagesToLoad(true);
    setLoadedAllEarlierMessages(false);
  `, appContext);

  appContext.pendingRequests[0].resolve({
    messages: [
      { id: 'm39', role: 'assistant', text: 'older 39' },
      { id: 'm40', role: 'assistant', text: 'older 40' }
    ],
    has_more: false
  });
  await pageLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id),
    hasMore: hasMoreMessagesToLoad(),
    loadedAll: loadedAllEarlierMessages(),
    gapAnchor: messageGapAnchorId()
  })`, appContext)), {
    messageIDs: ['m39', 'm40', 'm41', 'm42', 'm50', 'm51'],
    hasMore: true,
    loadedAll: false,
    gapAnchor: 'm50'
  });
});

test('message paging in-flight view state is isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  const pageLoad = vm.runInContext(`
    const renderSnapshots = [];
    renderCurrentSession = () => {
      renderSnapshots.push({
        loading: isLoadingEarlierMessages(),
        preserveScrollAfterRender: preserveScrollAfterRenderHeight()
      });
    };
    nodes.chatContainer.scrollHeight = 240;
    state.sessionId = 'session_page_state';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_page_state' },
      messages: [{ id: 'm8', role: 'assistant', text: 'tail' }],
      timeline: []
    };
    setHasMoreMessagesToLoad(true);
    setOldestLoadedMessageId('m8');
    loadEarlierMessages();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_page_state/);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasMessagePageRequestSeq: Object.prototype.hasOwnProperty.call(state, 'messagePageRequestSeq'),
    stateHasLoadingEarlier: Object.prototype.hasOwnProperty.call(state, 'loadingEarlier'),
    stateHasPreserveScrollAfterRender: Object.prototype.hasOwnProperty.call(state, 'preserveScrollAfterRender'),
    stateHasHasMoreMessages: Object.prototype.hasOwnProperty.call(state, 'hasMoreMessages'),
    stateHasOldestMessageId: Object.prototype.hasOwnProperty.call(state, 'oldestMessageId'),
    stateHasLoadedAllEarlierMessages: Object.prototype.hasOwnProperty.call(state, 'loadedAllEarlierMessages'),
    stateHasMessageGapAnchorId: Object.prototype.hasOwnProperty.call(state, 'messageGapAnchorId'),
    helperLoading: isLoadingEarlierMessages(),
    helperPreserveScrollAfterRender: preserveScrollAfterRenderHeight(),
    messagePagingFacts: {
      hasMoreMessages: hasMoreMessagesToLoad(),
      oldestMessageId: oldestLoadedMessageId(),
      loadedAllEarlierMessages: loadedAllEarlierMessages(),
      messageGapAnchorId: messageGapAnchorId()
    }
  })`, appContext)), {
    stateHasMessagePageRequestSeq: false,
    stateHasLoadingEarlier: false,
    stateHasPreserveScrollAfterRender: false,
    stateHasHasMoreMessages: false,
    stateHasOldestMessageId: false,
    stateHasLoadedAllEarlierMessages: false,
    stateHasMessageGapAnchorId: false,
    helperLoading: true,
    helperPreserveScrollAfterRender: null,
    messagePagingFacts: {
      hasMoreMessages: true,
      oldestMessageId: 'm8',
      loadedAllEarlierMessages: false,
      messageGapAnchorId: ''
    }
  });

  appContext.pendingRequests[0].resolve({
    messages: [
      { id: 'm6', role: 'assistant', text: 'older' },
      { id: 'm7', role: 'assistant', text: 'newer older' }
    ],
    has_more: false
  });
  await pageLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasLoadingEarlier: Object.prototype.hasOwnProperty.call(state, 'loadingEarlier'),
    stateHasPreserveScrollAfterRender: Object.prototype.hasOwnProperty.call(state, 'preserveScrollAfterRender'),
    messagePagingKeys: Object.keys(messagePagingViewState).sort(),
    helperLoading: isLoadingEarlierMessages(),
    helperPreserveScrollAfterRender: preserveScrollAfterRenderHeight(),
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id),
    stateOwnedPagingKeys: ['hasMoreMessages', 'oldestMessageId', 'loadedAllEarlierMessages', 'messageGapAnchorId']
      .filter((key) => Object.prototype.hasOwnProperty.call(state, key)),
    hasMoreMessages: hasMoreMessagesToLoad(),
    oldestMessageId: oldestLoadedMessageId(),
    loadedAllEarlierMessages: loadedAllEarlierMessages(),
    messageGapAnchorId: messageGapAnchorId(),
    renderSnapshots
  })`, appContext)), {
    stateHasLoadingEarlier: false,
    stateHasPreserveScrollAfterRender: false,
    messagePagingKeys: ['hasMoreMessages', 'loadedAllEarlierMessages', 'loadingEarlier', 'messageGapAnchorId', 'oldestMessageId', 'preserveScrollAfterRender', 'requestSeq'],
    helperLoading: false,
    helperPreserveScrollAfterRender: null,
    messageIDs: ['m6', 'm7', 'm8'],
    stateOwnedPagingKeys: [],
    hasMoreMessages: false,
    oldestMessageId: 'm6',
    loadedAllEarlierMessages: true,
    messageGapAnchorId: '',
    renderSnapshots: [
      { loading: true, preserveScrollAfterRender: null },
      { loading: false, preserveScrollAfterRender: 240 }
    ]
  });
});

test('Plan Mode approval does not mark a newly selected session as generating', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  appContext.planApproveButton = fakeActionButton({ 'data-plan-action': 'approve' });

  const approval = vm.runInContext(`
    state.sessionId = 'session_plan_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded plan A', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_plan_slow_a' },
      state: { status: 'awaiting_input' },
      plan_mode: { status: 'awaiting_approval' }
    };
    handlePlanModeAction(planApproveButton);
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_plan_slow_a\/planmode\/approve/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      plan_mode: null
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_plan_slow_a', status: 'accepted' });
  await approval;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('Plan Mode revision action closes modal inspector and focuses the composer', async () => {
  const appContext = createAppHarnessContext();
  appContext.planReviseButton = fakeActionButton({ 'data-plan-action': 'revise' });

  await vm.runInContext(`
    renderCurrentSession = function() {};
    updateUI = function() {};
    state.sessionId = 'session_plan_revision_focus';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_plan_revision_focus' },
      state: { status: 'awaiting_input' },
      plan_mode: { status: 'awaiting_approval', plan_mode_id: 'plan_focus', plan_version: 1 }
    };
    openInspectorSlideOut();
    handlePlanModeAction(planReviseButton);
  `, appContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    open: nodes.inspectorSlideOut.classList.contains('is-open'),
    hidden: nodes.inspectorSlideOut.getAttribute('aria-hidden'),
    expanded: nodes.inspectorToggleBtn.getAttribute('aria-expanded'),
    composerFocused: nodes.chatInput.focused === true
  })`, appContext)), {
    open: false,
    hidden: 'true',
    expanded: 'false',
    composerFocused: true
  });
  assert.equal(appContext.pendingRequests.length, 0);
});

test('modal inspector resets scroll only when reopening from closed state', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    renderCurrentSession = function() {};
    nodes.inspectorSlideOut.scrollTop = 240;
    openInspectorSlideOut();
  `, appContext);
  assert.equal(vm.runInContext('nodes.inspectorSlideOut.scrollTop', appContext), 0);
  vm.runInContext(`
    nodes.inspectorSlideOut.scrollTop = 180;
    openInspectorSlideOut();
  `, appContext);
  assert.equal(vm.runInContext('nodes.inspectorSlideOut.scrollTop', appContext), 180);
});

test('Plan Mode approval override ignores stale confirmation after session changes', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  const confirmResolvers = [];
  const toasts = [];
  appContext.planApproveButton = fakeActionButton({ 'data-plan-action': 'approve' });

  const approval = vm.runInContext(`
    confirmCoverageOverride = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_plan_override_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_plan_override_a' },
      state: { status: 'awaiting_input' },
      plan_mode: { status: 'awaiting_approval' }
    };
    handlePlanModeAction(planApproveButton);
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_plan_override_a\/planmode\/approve$/);
  appContext.pendingRequests[0].reject({
    status: 409,
    message: 'validation coverage blocks approval'
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(confirmResolvers.length, 1);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      plan_mode: null
    };
  `, appContext);

  confirmResolvers[0](true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({ session_id: 'session_plan_override_a', status: 'accepted' });
  }
  await approval;

  assert.equal(appContext.pendingRequests.length, 1);
  assert.deepEqual(sameRealm(toasts), []);
});

test('Plan Mode approval override ignores stale confirmation after same-session refresh', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  const confirmResolvers = [];
  const toasts = [];
  appContext.planApproveButton = fakeActionButton({ 'data-plan-action': 'approve' });

  const approval = vm.runInContext(`
    confirmCoverageOverride = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_plan_override_same_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_plan_override_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_old',
        status: 'awaiting_approval',
        objective: 'old plan',
        plan_version: 1,
        updated_at: '2026-05-30T01:00:00Z'
      }
    };
    handlePlanModeAction(planApproveButton);
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_plan_override_same_a\/planmode\/approve$/);
  appContext.pendingRequests[0].reject({
    status: 409,
    message: 'validation coverage blocks approval'
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(confirmResolvers.length, 1);

  vm.runInContext(`
    state.sessionDetail = {
      metadata: { id: 'session_plan_override_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_new',
        status: 'awaiting_approval',
        objective: 'new plan',
        plan_version: 2,
        updated_at: '2026-05-30T01:01:00Z'
      }
    };
  `, appContext);

  confirmResolvers[0](true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({ session_id: 'session_plan_override_same_a', status: 'accepted' });
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (appContext.pendingRequests[2]) {
    appContext.pendingRequests[2].resolve({
      metadata: { id: 'session_plan_override_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_new',
        status: 'awaiting_approval',
        objective: 'new plan',
        plan_version: 2,
        updated_at: '2026-05-30T01:01:00Z'
      },
      messages: [],
      timeline: []
    });
  }
  await approval;

  assert.equal(appContext.pendingRequests.length, 1);
  assert.deepEqual(sameRealm(toasts), []);
});

test('Plan input answer does not refresh a newly selected session after stale completion', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  appContext.planInputSubmitButton = fakeActionButton({
    'data-plan-input-action': 'submit',
    'data-request-id': 'request_slow_a'
  });
  appContext.window.setTimeout = function(callback) {
    callback();
    return 0;
  };

  const action = vm.runInContext(`
    state.sessionId = 'session_input_slow_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_input_slow_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        status: 'awaiting_user_input',
        pending_request: {
          request_id: 'request_slow_a',
          questions: [
            { id: 'scope', options: [{ label: 'Small' }] }
          ]
        }
      }
    };
    planInputViewState.selections = {
      request_slow_a: {
        scope: { label: 'Small', value: 'Small' }
      }
    };
    handlePlanInputAction(planInputSubmitButton);
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_input_slow_a\/planmode\/input/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      plan_mode: null,
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await new Promise((resolve) => setImmediate(resolve));
  const requestURLs = appContext.pendingRequests.map((request) => request.url);
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: [],
      timeline: []
    });
  }
  await action;

  assert.deepEqual(sameRealm(requestURLs), [
    '/api/sessions/session_input_slow_a/planmode/input'
  ]);
});

test('Plan input answer ignores stale completion after same-session request refresh', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  const toasts = [];
  appContext.planInputSubmitButton = fakeActionButton({
    'data-plan-input-action': 'submit',
    'data-request-id': 'request_old'
  });
  appContext.window.setTimeout = function(callback) {
    callback();
    return 0;
  };

  const action = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_input_same_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_input_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_input_gate',
        status: 'awaiting_user_input',
        objective: 'old input',
        updated_at: '2026-05-30T03:00:00Z',
        pending_request: {
          request_id: 'request_old',
          questions: [
            { id: 'scope', options: [{ label: 'Small' }] }
          ]
        }
      }
    };
    planInputViewState.selections = {
      request_old: {
        scope: { label: 'Small', value: 'Small' }
      }
    };
    handlePlanInputAction(planInputSubmitButton);
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_input_same_a\/planmode\/input/);

  vm.runInContext(`
    state.sessionDetail = {
      metadata: { id: 'session_input_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_input_gate',
        status: 'awaiting_user_input',
        objective: 'new input',
        updated_at: '2026-05-30T03:01:00Z',
        pending_request: {
          request_id: 'request_new',
          questions: [
            { id: 'scope', options: [{ label: 'Large' }] }
          ]
        }
      }
    };
    planInputViewState.selections = {
      request_old: {
        scope: { label: 'Small', value: 'Small' }
      },
      request_new: {
        scope: { label: 'Large', value: 'Large' }
      }
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await new Promise((resolve) => setImmediate(resolve));
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({
      metadata: { id: 'session_input_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_input_gate',
        status: 'awaiting_user_input',
        objective: 'new input',
        updated_at: '2026-05-30T03:01:00Z',
        pending_request: {
          request_id: 'request_new',
          questions: [
            { id: 'scope', options: [{ label: 'Large' }] }
          ]
        }
      },
      messages: [],
      timeline: []
    });
  }
  await action;

  assert.equal(appContext.pendingRequests.length, 1);
  assert.deepEqual(sameRealm(toasts), []);
  assert.deepEqual(sameRealm(vm.runInContext(`Object.keys(planInputViewState.selections).sort()`, appContext)), [
    'request_new',
    'request_old'
  ]);
});

test('Goal actions do not refresh a newly selected session after stale completion', async () => {
  const appContext = createAppHarnessContext();
  installGoalAPITestWrappers(appContext);
  appContext.goalPauseButton = fakeActionButton({ 'data-goal-action': 'pause' });

  const action = vm.runInContext(`
    state.sessionId = 'session_goal_slow_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_goal_slow_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_a', status: 'active' }
    };
    handleGoalAction(goalPauseButton);
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_goal_slow_a\/goal\/pause/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      goal: { goal_id: 'goal_b', status: 'active' }
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ goal_id: 'goal_a', status: 'paused' });
  await new Promise((resolve) => setImmediate(resolve));
  const requestURLs = appContext.pendingRequests.map((request) => request.url);
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: [],
      timeline: []
    });
  }
  await action;

  assert.deepEqual(sameRealm(requestURLs), [
    '/api/sessions/session_goal_slow_a/goal/pause'
  ]);
});

test('Goal plan approval does not mark a refreshed same-session goal as generating', async () => {
  const appContext = createAppHarnessContext();
  installGoalAPITestWrappers(appContext);
  appContext.goalApprovePlanButton = fakeActionButton({ 'data-goal-action': 'approve-plan' });

  const approval = vm.runInContext(`
    state.sessionId = 'session_goal_approve_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded old goal', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_goal_approve_same_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_old', status: 'active', objective: 'old mission', updated_at: '2026-05-30T02:00:00Z' }
    };
    handleGoalAction(goalApprovePlanButton);
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_goal_approve_same_a\/mission\/plan\/approve$/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded new goal', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_goal_approve_same_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_new', status: 'active', objective: 'new mission', updated_at: '2026-05-30T02:01:00Z' }
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_goal_approve_same_a', status: 'accepted' });
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({
      metadata: { id: 'session_goal_approve_same_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_new', status: 'active', objective: 'new mission', updated_at: '2026-05-30T02:01:00Z' },
      messages: [],
      timeline: []
    });
  }
  await approval;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_goal_approve_same_a',
    generating: false,
    activityTitle: 'Loaded new goal'
  });
});

test('Goal clear ignores stale confirmation after same-session refresh', async () => {
  const appContext = createAppHarnessContext();
  installGoalAPITestWrappers(appContext);
  const confirmResolvers = [];
  const requestURLs = [];
  const toasts = [];
  appContext.goalClearButton = fakeActionButton({ 'data-goal-action': 'clear' });

  const action = vm.runInContext(`
    requestJSON = async function(url) {
      requestURLsRef.push(String(url));
      return {
        metadata: { id: 'session_goal_clear_stale_a' },
        state: { status: 'awaiting_input' },
        goal: null,
        messages: [],
        timeline: []
      };
    };
    confirmGoalClear = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_goal_clear_stale_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_goal_clear_stale_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_old', status: 'active', objective: 'old objective' }
    };
    handleGoalAction(goalClearButton);
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, requestURLsRef: requestURLs, toastsRef: toasts }));

  assert.equal(confirmResolvers.length, 1);
  assert.deepEqual(sameRealm(requestURLs), []);

  vm.runInContext(`
    state.sessionDetail = {
      metadata: { id: 'session_goal_clear_stale_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_new', status: 'active', objective: 'new objective' }
    };
  `, appContext);

  confirmResolvers[0](true);
  await action;

  const deleteRequests = requestURLs.filter((url) => url === '/api/sessions/session_goal_clear_stale_a/goal');
  assert.equal(deleteRequests.length, 0);
  assert.deepEqual(sameRealm(toasts), []);
});

test('Goal plan approval override ignores stale confirmation after session changes', async () => {
  const appContext = createAppHarnessContext();
  installGoalAPITestWrappers(appContext);
  const confirmResolvers = [];
  const toasts = [];
  appContext.goalApprovePlanButton = fakeActionButton({ 'data-goal-action': 'approve-plan' });

  const approval = vm.runInContext(`
    confirmCoverageOverride = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_goal_override_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_goal_override_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_a', status: 'active' }
    };
    handleGoalAction(goalApprovePlanButton);
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_goal_override_a\/mission\/plan\/approve$/);
  appContext.pendingRequests[0].reject({
    status: 409,
    message: 'validation coverage blocks approval'
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(confirmResolvers.length, 1);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      goal: { goal_id: 'goal_b', status: 'active' }
    };
  `, appContext);

  confirmResolvers[0](true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({ session_id: 'session_goal_override_a', status: 'accepted' });
  }
  await approval;

  assert.equal(appContext.pendingRequests.length, 1);
  assert.deepEqual(sameRealm(toasts), []);
});

test('Goal plan approval override ignores stale confirmation after same-session refresh', async () => {
  const appContext = createAppHarnessContext();
  installGoalAPITestWrappers(appContext);
  const confirmResolvers = [];
  const toasts = [];
  appContext.goalApprovePlanButton = fakeActionButton({ 'data-goal-action': 'approve-plan' });

  const approval = vm.runInContext(`
    confirmCoverageOverride = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_goal_override_same_a';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_goal_override_same_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_old', status: 'active', objective: 'old mission', updated_at: '2026-05-30T01:00:00Z' }
    };
    handleGoalAction(goalApprovePlanButton);
  `, Object.assign(appContext, { confirmResolversRef: confirmResolvers, toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_goal_override_same_a\/mission\/plan\/approve$/);
  appContext.pendingRequests[0].reject({
    status: 409,
    message: 'validation coverage blocks approval'
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(confirmResolvers.length, 1);

  vm.runInContext(`
    state.sessionDetail = {
      metadata: { id: 'session_goal_override_same_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_new', status: 'active', objective: 'new mission', updated_at: '2026-05-30T01:01:00Z' }
    };
  `, appContext);

  confirmResolvers[0](true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({ session_id: 'session_goal_override_same_a', status: 'accepted' });
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (appContext.pendingRequests[2]) {
    appContext.pendingRequests[2].resolve({
      metadata: { id: 'session_goal_override_same_a' },
      state: { status: 'awaiting_input' },
      goal: { goal_id: 'goal_new', status: 'active', objective: 'new mission', updated_at: '2026-05-30T01:01:00Z' },
      messages: [],
      timeline: []
    });
  }
  await approval;

  assert.equal(appContext.pendingRequests.length, 1);
  assert.deepEqual(sameRealm(toasts), []);
});

test('running-session steer completion does not mark a newly selected session as queued', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_steer_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setNextSendInterruptArmed(true);
    setLiveActivity({ title: 'Running A', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_steer_slow_a' },
      state: { status: 'running' },
      messages: []
    };
    nodes.chatInput.value = 'adjust the running task';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_steer_slow_a\/steer/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setNextSendInterruptArmed(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'queued' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    interruptArmed: isNextSendInterruptArmed(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    interruptArmed: false,
    activityTitle: 'Loaded session B'
  });
});

test('running-session steer completion ignores refreshed same-session state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];

  const send = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_steer_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setNextSendInterruptArmed(true);
    setLiveActivity({ title: 'Running old state', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_steer_same_a', updated_at: '2026-05-30T06:00:00Z' },
      state: { status: 'running', updated_at: '2026-05-30T06:00:00Z' },
      messages: []
    };
    nodes.chatInput.value = 'adjust the running task';
    sendMessage();
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_steer_same_a\/steer/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setNextSendInterruptArmed(true);
    setLiveActivity({ title: 'Loaded completed session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_steer_same_a', updated_at: '2026-05-30T06:01:00Z' },
      state: { status: 'completed', updated_at: '2026-05-30T06:01:00Z' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'queued' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    interruptArmed: isNextSendInterruptArmed(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_steer_same_a',
    generating: false,
    interruptArmed: true,
    activityTitle: 'Loaded completed session'
  });
  assert.deepEqual(sameRealm(toasts), []);
});

test('continue completion does not mark a newly selected session as generating', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_continue_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded A', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_continue_slow_a' },
      state: { status: 'paused' },
      plan_mode: null,
      messages: []
    };
    nodes.chatInput.value = 'continue this session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_continue_slow_a\/continue/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      plan_mode: null,
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_continue_slow_a', status: 'accepted' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('durable awaiting-input state routes to continue during active-handle teardown', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_continue_teardown';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Starting turn', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_continue_teardown' },
      state: { status: 'awaiting_input' },
      active_handle: true,
      active_handle_owner: { owned_by_current_process: true },
      plan_mode: null,
      messages: []
    };
    nodes.chatInput.value = 'continue after teardown';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_continue_teardown\/continue/);
  assert.doesNotMatch(appContext.pendingRequests[0].url, /\/steer$/);
  appContext.pendingRequests[0].resolve({ session_id: 'session_continue_teardown', status: 'accepted' });
  await send;
});

test('continue completion ignores refreshed same-session state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];

  const send = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_continue_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded paused session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_continue_same_a', updated_at: '2026-05-30T05:00:00Z' },
      state: { status: 'paused', updated_at: '2026-05-30T05:00:00Z' },
      plan_mode: null,
      messages: []
    };
    nodes.chatInput.value = 'continue this session';
    sendMessage();
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_continue_same_a\/continue/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded completed session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_continue_same_a', updated_at: '2026-05-30T05:01:00Z' },
      state: { status: 'completed', updated_at: '2026-05-30T05:01:00Z' },
      plan_mode: null,
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_continue_same_a', status: 'accepted' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_continue_same_a',
    generating: false,
    activityTitle: 'Loaded completed session'
  });
  assert.deepEqual(sameRealm(toasts), []);
});

test('refresh with current active handle keeps continued awaiting-input session live', async () => {
  const appContext = createAppHarnessContext();
  const refresh = vm.runInContext(`
    syncWorkspaceToCurrentSession = function() {};
    state.sessionId = 'session_continue_bootstrap';
    state.sessionBacked = true;
    state.overview = { sessions: [] };
    setCurrentViewName('chat');
    setLiveRelayConnected(true);
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Continuing session', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: {
        id: 'session_continue_bootstrap',
        provider: 'openai-compatible',
        model: 'gpt-5.5',
        workdir: '/workspace'
      },
      state: { status: 'awaiting_input', phase: 'awaiting_input' },
      messages: [],
      children: { sessions: [], jobs: [] }
    };
    appendOptimisticMessage('user', 'authorization_source: confirmed', { source: 'user' });
    refreshCurrentSession();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_continue_bootstrap/);

  appContext.pendingRequests[0].resolve({
    metadata: {
      id: 'session_continue_bootstrap',
      provider: 'openai-compatible',
      model: 'gpt-5.5',
      workdir: '/workspace'
    },
    state: { status: 'awaiting_input', phase: 'awaiting_input' },
    messages: [
      {
        id: 'msg_confirmed_scope',
        role: 'user',
        text: 'authorization_source: confirmed',
        created_at: '2026-06-16T04:17:48.17523378Z'
      }
    ],
    has_more_messages: false,
    children: { sessions: [], jobs: [] },
    background_notifications: [],
    steer_requests: [],
    events: [],
    timeline: [],
    active_handle: true,
    active_handle_owner: {
      state: 'current_process',
      owned_by_current_process: true
    }
  });
  await refresh;
  await new Promise((resolve) => setImmediate(resolve));

  const result = vm.runInContext(`({
    generating: isGenerating(),
    displayStatus: sessionDetailDisplayStatus(state.sessionDetail),
    runLoop: shouldRunPollingLoop(),
    pollSession: shouldPollCurrentSession(),
    interval: pollingIntervalForState(),
    optimisticCount: currentOptimisticMessages().length,
    needsRefresh: sessionViewState.needsRefresh,
    refreshing: sessionViewState.refreshing,
    detailMessageCount: maybeArray(state.sessionDetail?.messages).length,
    sessionTitle: nodes.sessionIdDisplay.innerText,
    activityTitle: currentLiveActivity().title
  })`, appContext);

  assert.deepEqual(sameRealm(result), {
    generating: true,
    displayStatus: 'running',
    runLoop: true,
    pollSession: true,
    interval: 1600,
    optimisticCount: 0,
    needsRefresh: false,
    refreshing: false,
    detailMessageCount: 1,
    sessionTitle: 'Running · session_continue_bootstrap · openai-compatible/gpt-5.5 · workspace',
    activityTitle: 'Starting turn'
  });
});

test('start completion does not replace a session selected while launch is pending', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    nodes.chatInput.value = 'start a slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_created_slow_a', status: 'accepted' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    generating: isGenerating(),
    launchInFlight: isLaunchInFlight(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    backed: true,
    generating: false,
    launchInFlight: false,
    activityTitle: 'Loaded session B'
  });
});

test('new session start omits default agent identity fields', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    nodes.chatInput.value = 'start a default session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');
  assert.equal(Object.prototype.hasOwnProperty.call(appContext.pendingRequests[0].payload.payload, 'agentName'), false);
  assert.equal(Object.prototype.hasOwnProperty.call(appContext.pendingRequests[0].payload.payload, 'agentRole'), false);
  appContext.pendingRequests[0].resolve({ session_id: 'session_default_start', status: 'accepted' });
  await send;
});

test('missing selected session resets to new composer and clears persisted selection', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];
  vm.runInContext(`
    window.localStorage = {
      stored: '',
      setItem(key, value) {
        this.stored = value;
      },
      getItem() {
        return this.stored || null;
      },
      removeItem() {
        this.stored = '';
      }
    };
    showToast = function(message, tone) {
      toasts.push({ message, tone });
    };
  `, appContext);
  appContext.toasts = toasts;

  vm.runInContext(`
    state.sessionId = 'missing_session_123';
    state.sessionBacked = true;
    state.sessionDetail = null;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setComposerMode('goal');
    persistUIState();
    requestJSON = async function(url) {
      const err = new Error('open /tmp/sessions/missing_session_123/session.json: file does not exist');
      err.name = 'APIError';
      err.status = 404;
      err.code = 'NOT_FOUND';
      throw err;
    };
  `, appContext);

  await vm.runInContext(`refreshCurrentSession({ surfaceError: true })`, appContext);

  const result = vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    detail: state.sessionDetail,
    generating: isGenerating(),
    launchInFlight: isLaunchInFlight(),
    composerMode: composerMode(),
    stored: JSON.parse(window.localStorage.stored),
    activityTitle: currentLiveActivity().title
  })`, appContext);

  assert.match(result.selected, /^0x/i);
  assert.equal(result.backed, false);
  assert.equal(result.detail, null);
  assert.equal(result.generating, false);
  assert.equal(result.launchInFlight, false);
  assert.equal(result.composerMode, '');
  assert.equal(result.stored.selectedSessionId, '');
  assert.equal(result.activityTitle, 'Session no longer available');
  assert.deepEqual(sameRealm(toasts), [
    { message: 'Previously selected session is no longer available.', tone: 'info' }
  ]);
});

test('start completion does not clear a newer pending launch', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const firstSend = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    nodes.chatInput.value = 'start first slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  vm.runInContext(`
    state.sessionId = '0xB22CE0';
    state.sessionBacked = false;
    setGeneratingViewState(true);
    setLaunchInFlight(true);
    setLiveActivity({ title: 'Launching second session', copy: '', tone: 'live' });
    state.sessionDetail = null;
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_created_slow_a', status: 'accepted' });
  await firstSend;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    generating: isGenerating(),
    launchInFlight: isLaunchInFlight(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: '0xB22CE0',
    backed: false,
    generating: true,
    launchInFlight: true,
    activityTitle: 'Launching second session'
  });
});

test('openSession clears pending launch state for the newly selected durable session', async () => {
  const appContext = createAppHarnessContext();

  const open = vm.runInContext(`
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(true);
    setLaunchInFlight(true);
    setLiveActivity({ title: 'Launching session', copy: '', tone: 'live' });
    openSession('session_fast_b', { switchToChat: false });
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_fast_b/);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    generating: isGenerating(),
    launchInFlight: isLaunchInFlight(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    backed: true,
    generating: false,
    launchInFlight: false,
    activityTitle: 'Loading session'
  });

  appContext.pendingRequests[0].resolve({
    metadata: { id: 'session_fast_b' },
    state: { status: 'completed' },
    messages: []
  });
  await open;
});

test('start failure does not clear generating state after another session is selected', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    setGeneratingViewState(false);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Ready', copy: '', tone: 'neutral' });
    state.sessionDetail = null;
    nodes.chatInput.value = 'start a slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLaunchInFlight(false);
    setLiveActivity({ title: 'Session B running', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'running' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].reject(new Error('slow launch failed'));
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    generating: isGenerating(),
    launchInFlight: isLaunchInFlight(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    backed: true,
    generating: true,
    launchInFlight: false,
    activityTitle: 'Session B running'
  });
});

test('inline continue action does not refresh a newly selected session after stale completion', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  vm.runInContext(`
    window.setTimeout = function(callback) {
      callback();
      return 0;
    };
  `, appContext);

  const action = vm.runInContext(`
    state.sessionId = 'session_inline_continue_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded A', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_inline_continue_slow_a' },
      state: { status: 'awaiting_input' },
      messages: []
    };
    requestContinueSession('session_inline_continue_slow_a');
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_inline_continue_slow_a\/continue/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_inline_continue_slow_a', status: 'accepted' });
  await action;

  const requestURLs = appContext.pendingRequests.map((request) => request.url);
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve({
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    });
  }

  assert.deepEqual(sameRealm(requestURLs), [
    '/api/sessions/session_inline_continue_slow_a/continue'
  ]);
});

test('inline continue action ignores refreshed same-session state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];
  vm.runInContext(`
    window.setTimeout = function(callback) {
      callback();
      return 0;
    };
  `, appContext);

  const action = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_inline_continue_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded old inline state', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_inline_continue_same_a', updated_at: '2026-05-30T08:00:00Z' },
      state: { status: 'awaiting_input', updated_at: '2026-05-30T08:00:00Z' },
      messages: []
    };
    requestContinueSession('session_inline_continue_same_a');
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_inline_continue_same_a\/continue/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded completed session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_inline_continue_same_a', updated_at: '2026-05-30T08:01:00Z' },
      state: { status: 'completed', updated_at: '2026-05-30T08:01:00Z' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_inline_continue_same_a', status: 'accepted' });
  await action;

  assert.deepEqual(sameRealm(appContext.pendingRequests.map((request) => request.url)), [
    '/api/sessions/session_inline_continue_same_a/continue'
  ]);
  assert.deepEqual(sameRealm(toasts), []);
});

test('fetchHistory queues the latest requested page and ignores stale in-flight history', async () => {
  const appContext = createAppHarnessContext();

  const firstLoad = vm.runInContext(`fetchHistory(1)`, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /\/api\/history\?page=1&page_size=8/);

  await vm.runInContext(`fetchHistory(3)`, appContext);

  appContext.pendingRequests[0].resolve({
    items: [{ id: 'session_page_1_stale', status: 'completed' }],
    page: 1,
    page_size: 8,
    total: 1,
    total_pages: 3
  });
  await firstLoad;

  assert.equal(appContext.pendingRequests.length, 2);
  assert.match(appContext.pendingRequests[1].url, /\/api\/history\?page=3&page_size=8/);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    hasHistoryPageHelper: typeof currentHistoryPage === 'function',
    hasHistoryDataHelper: typeof currentHistoryData === 'function',
    stateHasHistoryRequestSeq: Object.prototype.hasOwnProperty.call(state, 'historyRequestSeq'),
    stateHasRefreshingHistory: Object.prototype.hasOwnProperty.call(state, 'refreshingHistory'),
    stateHasNeedsHistoryRefresh: Object.prototype.hasOwnProperty.call(state, 'needsHistoryRefresh'),
    stateHasPendingHistoryRefreshOptions: Object.prototype.hasOwnProperty.call(state, 'pendingHistoryRefreshOptions'),
    stateHasHistoryData: Object.prototype.hasOwnProperty.call(state, 'historyData'),
    stateHasHistoryPage: Object.prototype.hasOwnProperty.call(state, 'historyPage'),
    stateHasHistoryPageSize: Object.prototype.hasOwnProperty.call(state, 'historyPageSize'),
    refreshing: historyViewState.refreshing,
    needsRefresh: historyViewState.needsRefresh,
    page: currentHistoryPage(),
    historyIDs: maybeArray(currentHistoryData()?.items).map((item) => item.id)
  })`, appContext)), {
    hasHistoryPageHelper: true,
    hasHistoryDataHelper: true,
    stateHasHistoryRequestSeq: false,
    stateHasRefreshingHistory: false,
    stateHasNeedsHistoryRefresh: false,
    stateHasPendingHistoryRefreshOptions: false,
    stateHasHistoryData: false,
    stateHasHistoryPage: false,
    stateHasHistoryPageSize: false,
    refreshing: true,
    needsRefresh: false,
    page: 3,
    historyIDs: []
  });

  appContext.pendingRequests[1].resolve({
    items: [{ id: 'session_page_3_current', status: 'completed' }],
    page: 3,
    page_size: 8,
    total: 17,
    total_pages: 3
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(sameRealm(vm.runInContext(`({
    page: currentHistoryPage(),
    dataPage: currentHistoryData()?.page,
    pageSize: currentHistoryPageSize(),
    stateHasHistoryData: Object.prototype.hasOwnProperty.call(state, 'historyData'),
    stateHasHistoryPage: Object.prototype.hasOwnProperty.call(state, 'historyPage'),
    stateHasHistoryPageSize: Object.prototype.hasOwnProperty.call(state, 'historyPageSize'),
    historyIDs: maybeArray(currentHistoryData()?.items).map((item) => item.id)
  })`, appContext)), {
    page: 3,
    dataPage: 3,
    pageSize: 8,
    stateHasHistoryData: false,
    stateHasHistoryPage: false,
    stateHasHistoryPageSize: false,
    historyIDs: ['session_page_3_current']
  });
});

test('refreshOverview queues the latest refresh and ignores stale in-flight overview', async () => {
  const appContext = createAppHarnessContext();

  const firstRefresh = vm.runInContext(`refreshOverview()`, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/overview');

  await vm.runInContext(`refreshOverview()`, appContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasRefreshingOverview: Object.prototype.hasOwnProperty.call(state, 'refreshingOverview'),
    stateHasNeedsOverviewRefresh: Object.prototype.hasOwnProperty.call(state, 'needsOverviewRefresh'),
    refreshing: overviewViewState.refreshing,
    needsRefresh: overviewViewState.needsRefresh
  })`, appContext)), {
    stateHasRefreshingOverview: false,
    stateHasNeedsOverviewRefresh: false,
    refreshing: true,
    needsRefresh: true
  });

  appContext.pendingRequests[0].resolve({
    recent_sessions: [{ id: 'session_stale_overview' }],
    queue_counters: { queued: 1 }
  });
  await firstRefresh;

  assert.equal(appContext.pendingRequests.length, 2);
  assert.equal(appContext.pendingRequests[1].url, '/api/overview');
  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasOverviewRequestSeq: Object.prototype.hasOwnProperty.call(state, 'overviewRequestSeq'),
    overviewIDs: maybeArray(state.overview?.recent_sessions).map((item) => item.id),
    queued: state.overview?.queue_counters?.queued ?? null
  })`, appContext)), {
    stateHasOverviewRequestSeq: false,
    overviewIDs: [],
    queued: null
  });

  appContext.pendingRequests[1].resolve({
    recent_sessions: [{ id: 'session_current_overview' }],
    queue_counters: { queued: 0 }
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(sameRealm(vm.runInContext(`({
    overviewIDs: maybeArray(state.overview?.recent_sessions).map((item) => item.id),
    queued: state.overview?.queue_counters?.queued ?? null,
    stateHasRefreshingOverview: Object.prototype.hasOwnProperty.call(state, 'refreshingOverview'),
    stateHasNeedsOverviewRefresh: Object.prototype.hasOwnProperty.call(state, 'needsOverviewRefresh'),
    refreshing: overviewViewState.refreshing,
    needsRefresh: overviewViewState.needsRefresh
  })`, appContext)), {
    overviewIDs: ['session_current_overview'],
    queued: 0,
    stateHasRefreshingOverview: false,
    stateHasNeedsOverviewRefresh: false,
    refreshing: false,
    needsRefresh: false
  });
});

test('overview load error view state is isolated from durable app state', async () => {
  const appContext = createAppHarnessContext();
  const renderCalls = vm.runInContext(`
    const renderCalls = [];
    setCurrentViewName('chat');
    renderCurrentSession = () => {
      renderCalls.push({
        overviewError: currentOverviewError(),
        overviewPresent: Boolean(state.overview)
      });
    };
    renderCalls;
  `, appContext);

  const firstRefresh = vm.runInContext(`refreshOverview()`, appContext);
  assert.equal(appContext.pendingRequests.length, 1);
  appContext.pendingRequests[0].reject(new Error('overview store unavailable'));
  await firstRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasOverviewError: Object.prototype.hasOwnProperty.call(state, 'overviewError'),
    overviewError: currentOverviewError(),
    overviewPresent: Boolean(state.overview),
    refreshing: overviewViewState.refreshing,
    needsRefresh: overviewViewState.needsRefresh
  })`, appContext)), {
    stateHasOverviewError: false,
    overviewError: 'overview store unavailable',
    overviewPresent: false,
    refreshing: false,
    needsRefresh: false
  });
  assert.deepEqual(sameRealm(renderCalls), [
    { overviewError: 'overview store unavailable', overviewPresent: false }
  ]);

  const secondRefresh = vm.runInContext(`refreshOverview()`, appContext);
  assert.equal(appContext.pendingRequests.length, 2);
  appContext.pendingRequests[1].resolve({
    recent_sessions: [{ id: 'session_after_error' }],
    queue_counters: { queued: 0 }
  });
  await secondRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stateHasOverviewError: Object.prototype.hasOwnProperty.call(state, 'overviewError'),
    overviewError: currentOverviewError(),
    overviewIDs: maybeArray(state.overview?.recent_sessions).map((item) => item.id)
  })`, appContext)), {
    stateHasOverviewError: false,
    overviewError: '',
    overviewIDs: ['session_after_error']
  });
});

test('fetchSkills ignores stale skill catalog responses', async () => {
  const appContext = createAppHarnessContext();

  const firstLoad = vm.runInContext(`fetchSkills()`, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/skills');

  const secondLoad = vm.runInContext(`fetchSkills()`, appContext);

  assert.equal(appContext.pendingRequests.length, 2);
  assert.equal(appContext.pendingRequests[1].url, '/api/skills');

  appContext.pendingRequests[1].resolve([
    { id: 'skill_current', name: 'Current Skill', author: 'agent', description: 'current catalog', installed: true }
  ]);
  await secondLoad;

  const currentState = sameRealm(vm.runInContext(`({
    stateHasSkillsRequestSeq: Object.prototype.hasOwnProperty.call(state, 'skillsRequestSeq'),
    stateHasSkills: Object.prototype.hasOwnProperty.call(state, 'skills'),
    hasCurrentSkillsHelper: typeof currentSkills === 'function',
    skillIDs: maybeArray(currentSkills()).map((skill) => skill.id),
    grid: nodes.skillsGrid.innerHTML
  })`, appContext));
  assert.equal(currentState.stateHasSkillsRequestSeq, false);
  assert.equal(currentState.stateHasSkills, false);
  assert.equal(currentState.hasCurrentSkillsHelper, true);
  assert.deepEqual(currentState.skillIDs, ['skill_current']);
  assert.match(currentState.grid, /Current Skill/);

  appContext.pendingRequests[0].resolve([
    { id: 'skill_stale', name: 'Stale Skill', author: 'agent', description: 'stale catalog', installed: true }
  ]);
  await firstLoad;

  const finalState = sameRealm(vm.runInContext(`({
    stateHasSkills: Object.prototype.hasOwnProperty.call(state, 'skills'),
    skillIDs: maybeArray(currentSkills()).map((skill) => skill.id),
    grid: nodes.skillsGrid.innerHTML
  })`, appContext));
  assert.equal(finalState.stateHasSkills, false);
  assert.deepEqual(finalState.skillIDs, ['skill_current']);
  assert.match(finalState.grid, /Current Skill/);
  assert.doesNotMatch(finalState.grid, /Stale Skill/);
});

test('skill uninstall ignores stale confirmation after catalog refresh', async () => {
  const appContext = createAppHarnessContext();
  const confirmResolvers = [];
  const toasts = [];
  const button = fakeAppElement();
  button.innerText = 'Uninstall';

  vm.runInContext(`
    nodes.skillsGrid = fakeAppElement();
    nodes.toastRack = fakeAppElement();
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    confirmSkillUninstall = function() {
      return new Promise((resolve) => {
        confirmResolversRef.push(resolve);
      });
    };
    renderSkills([{
      id: 'skill_slow',
      name: 'Slow Skill',
      author: 'agent',
      description: 'old catalog entry',
      installed: true
    }]);
  `, Object.assign(appContext, { fakeAppElement, confirmResolversRef: confirmResolvers, toastsRef: toasts }));

  const action = vm.runInContext(`handleSkillAction('skill_slow', true, buttonRef)`, Object.assign(appContext, { buttonRef: button }));
  assert.equal(confirmResolvers.length, 1);
  assert.equal(appContext.pendingRequests.length, 0);

  vm.runInContext(`
    renderSkills([{
      id: 'skill_current',
      name: 'Current Skill',
      author: 'agent',
      description: 'new catalog entry',
      installed: true
    }]);
  `, appContext);

  confirmResolvers[0](true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (appContext.pendingRequests[0]) {
    appContext.pendingRequests[0].resolve({ status: 'ok' });
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (appContext.pendingRequests[1]) {
    appContext.pendingRequests[1].resolve([
      { id: 'skill_current', name: 'Current Skill', author: 'agent', description: 'new catalog entry', installed: true }
    ]);
  }
  await action;

  const uninstallRequests = appContext.pendingRequests.filter((request) => request.url.includes('/uninstall'));
  assert.equal(uninstallRequests.length, 0);
  assert.deepEqual(sameRealm(toasts), []);
});

test('renderSkills shows disabled reasons for read-only local skills', () => {
  const appContext = createAppHarnessContext();

  vm.runInContext(`
    renderSkills([{
      id: 'external-skill',
      name: 'External Skill',
      author: 'Local',
      description: 'External discovery directory skill.',
      icon: 'Box',
      installed: true,
      read_only: true,
      disabled_reason: 'Only the first configured skill directory is managed by this WebConsole.'
    }]);
  `, appContext);

  const html = vm.runInContext('nodes.skillsGrid.innerHTML', appContext);

  assert.match(html, /Only the first configured skill directory is managed by this WebConsole\./);
  assert.match(html, /Disabled/);
});

test('renderSkills isolates the author fact from its localizable prefix', () => {
  const appContext = createAppHarnessContext();

  vm.runInContext(`
    renderSkills([{
      id: 'collision-skill',
      name: 'Settings',
      author: 'Local',
      description: 'Settings',
      installed: true
    }]);
  `, appContext);

  const html = vm.runInContext('nodes.skillsGrid.innerHTML', appContext);
  assert.match(html, /class="skill-name" translate="no" data-i18n-skip>Settings<\/h3>/);
  assert.match(html, /class="skill-author"><span>by<\/span> <span translate="no" data-i18n-skip>Local<\/span><\/p>/);
  assert.match(html, /class="skill-desc" translate="no" data-i18n-skip>Settings<\/p>/);
});

test('plan revision completion does not mark a newly selected session as generating', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_revision_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded plan A', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_revision_slow_a' },
      state: { status: 'awaiting_input' },
      plan_mode: { status: 'awaiting_approval' },
      messages: []
    };
    nodes.chatInput.value = 'revise the plan';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_revision_slow_a\/planmode\/revise/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      plan_mode: null,
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_revision_slow_a', status: 'accepted' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('plan revision completion ignores refreshed same-session Plan Mode', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];

  const send = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_revision_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded old plan', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_revision_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_old',
        status: 'awaiting_approval',
        objective: 'old plan',
        plan_version: 1,
        updated_at: '2026-05-30T04:00:00Z'
      },
      messages: []
    };
    nodes.chatInput.value = 'revise the plan';
    sendMessage();
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_revision_same_a\/planmode\/revise/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded new plan', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_revision_same_a' },
      state: { status: 'awaiting_input' },
      plan_mode: {
        plan_mode_id: 'plan_new',
        status: 'awaiting_approval',
        objective: 'new plan',
        plan_version: 2,
        updated_at: '2026-05-30T04:01:00Z'
      },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_revision_same_a', status: 'accepted' });
  await send;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_revision_same_a',
    generating: false,
    activityTitle: 'Loaded new plan'
  });
  assert.deepEqual(sameRealm(toasts), []);
});

test('interrupt completion does not update a newly selected session', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const interrupt = vm.runInContext(`
    state.sessionId = 'session_interrupt_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Running A', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_interrupt_slow_a' },
      state: { status: 'running' },
      messages: []
    };
    requestInterrupt();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_interrupt_slow_a\/interrupt/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await interrupt;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('interrupt completion ignores refreshed same-session state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];

  const interrupt = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_interrupt_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Running old state', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_interrupt_same_a', updated_at: '2026-05-30T07:00:00Z' },
      state: { status: 'running', updated_at: '2026-05-30T07:00:00Z' },
      messages: []
    };
    requestInterrupt();
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_interrupt_same_a\/interrupt/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded completed session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_interrupt_same_a', updated_at: '2026-05-30T07:01:00Z' },
      state: { status: 'completed', updated_at: '2026-05-30T07:01:00Z' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await interrupt;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title
  })`, appContext)), {
    selected: 'session_interrupt_same_a',
    generating: false,
    activityTitle: 'Loaded completed session'
  });
  assert.deepEqual(sameRealm(toasts), []);
});

test('stop completion does not update a newly selected session', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const stop = vm.runInContext(`
    state.sessionId = 'session_stop_slow_a';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Running A', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_stop_slow_a' },
      state: { status: 'running' },
      messages: []
    };
    requestStop();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_stop_slow_a\/stop/);

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded session B', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_fast_b' },
      state: { status: 'completed' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await stop;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title,
    stoppingA: isStoppingSession('session_stop_slow_a')
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B',
    stoppingA: true
  });
});

test('stop completion ignores refreshed same-session state', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];

  const stop = vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
    state.sessionId = 'session_stop_same_a';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Running old state', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_stop_same_a', updated_at: '2026-05-30T07:30:00Z' },
      state: { status: 'running', updated_at: '2026-05-30T07:30:00Z' },
      messages: []
    };
    requestStop();
  `, Object.assign(appContext, { toastsRef: toasts }));

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_stop_same_a\/stop/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded completed session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_stop_same_a', updated_at: '2026-05-30T07:31:00Z' },
      state: { status: 'completed', updated_at: '2026-05-30T07:31:00Z' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await stop;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title,
    stoppingA: isStoppingSession('session_stop_same_a')
  })`, appContext)), {
    selected: 'session_stop_same_a',
    generating: false,
    activityTitle: 'Loaded completed session',
    stoppingA: true
  });
  assert.deepEqual(sameRealm(toasts), []);
});

test('top-level stop stays available but interrupt hides for running sessions not owned by this web process', () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    const stopButton = {
      disabled: false,
      classList: {
        visible: false,
        toggle(name, value) {
          if (name === 'is-visible') this.visible = Boolean(value);
        }
      }
    };
    const interruptButton = {
      disabled: false,
      classList: {
        visible: false,
        toggle(name, value) {
          if (name === 'is-visible') this.visible = Boolean(value);
        }
      }
    };
    nodes.stopSessionBtn = stopButton;
    nodes.interruptSessionBtn = interruptButton;
    state.sessionId = 'session_external_owner_ui';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    state.sessionDetail = {
      metadata: { id: 'session_external_owner_ui' },
      state: { status: 'running' },
      active_handle: false,
      active_handle_owner: {
        state: 'running_not_owned',
        owned_by_current_process: false
      }
    };
    updateUI();
    ({
      stopVisible: stopButton.classList.visible,
      interruptVisible: interruptButton.classList.visible,
      stopDisabled: stopButton.disabled,
      interruptDisabled: interruptButton.disabled
    });
  `, appContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    stopVisible: nodes.stopSessionBtn.classList.visible,
    interruptVisible: nodes.interruptSessionBtn.classList.visible,
    stopDisabled: nodes.stopSessionBtn.disabled,
    interruptDisabled: nodes.interruptSessionBtn.disabled
  })`, appContext)), {
    stopVisible: true,
    interruptVisible: false,
    stopDisabled: false,
    interruptDisabled: true
  });
});

test('inactive stop and interrupt controls remain visibly disabled in the header', () => {
	const baseRule = stylesSource.match(/#stop-session-btn,\s*#interrupt-session-btn\s*\{([^}]*)\}/);
	assert.ok(baseRule, 'missing base stop/interrupt control rule');
	assert.match(baseRule[1], /opacity:\s*0\.52\s*;/);
	assert.doesNotMatch(baseRule[1], /opacity:\s*0\s*;/);
	assert.match(baseRule[1], /pointer-events:\s*none\s*;/);

	const activeRule = stylesSource.match(/#stop-session-btn\.is-visible,\s*#interrupt-session-btn\.is-visible\s*\{([^}]*)\}/);
	assert.ok(activeRule, 'missing actionable stop/interrupt control rule');
	assert.match(activeRule[1], /opacity:\s*1\s*;/);
	assert.match(activeRule[1], /pointer-events:\s*auto\s*;/);
});

test('child stop completion refreshes selected parent session', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  vm.runInContext(`
    refreshCalls = [];
    toastCalls = [];
    queueSessionRefresh = function(delay) {
      refreshCalls.push({ kind: 'session', delay, selected: state.sessionId });
    };
    queueOverviewRefresh = function(delay) {
      refreshCalls.push({ kind: 'overview', delay, selected: state.sessionId });
    };
    showToast = function(message, tone) {
      toastCalls.push({ message, tone });
    };
  `, appContext);

  const stop = vm.runInContext(`
    setCurrentViewName('chat');
    state.sessionId = 'parent_session_stop';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Parent loaded', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'parent_session_stop' },
      state: { status: 'awaiting_input' },
      children: { sessions: [{ id: 'child_session_stop', status: 'running' }] },
      messages: []
    };
    requestStopSession('child_session_stop');
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /child_session_stop\/stop/);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await stop;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    activityTitle: currentLiveActivity().title,
    stoppingChild: isStoppingSession('child_session_stop'),
    refreshCalls,
    toastCalls
  })`, appContext)), {
    selected: 'parent_session_stop',
    activityTitle: 'Parent loaded',
    stoppingChild: true,
    refreshCalls: [
      { kind: 'session', delay: 120, selected: 'parent_session_stop' },
      { kind: 'overview', delay: 180, selected: 'parent_session_stop' }
    ],
    toastCalls: [
      { message: 'Stop requested.', tone: 'success' }
    ]
  });
});

test('child stop completion ignores refreshed parent without child reference', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  vm.runInContext(`
    refreshCalls = [];
    toastCalls = [];
    queueSessionRefresh = function(delay) {
      refreshCalls.push({ kind: 'session', delay, selected: state.sessionId });
    };
    queueOverviewRefresh = function(delay) {
      refreshCalls.push({ kind: 'overview', delay, selected: state.sessionId });
    };
    showToast = function(message, tone) {
      toastCalls.push({ message, tone });
    };
  `, appContext);

  const stop = vm.runInContext(`
    setCurrentViewName('chat');
    state.sessionId = 'parent_session_stop_same_refresh';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Parent with child', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'parent_session_stop_same_refresh', updated_at: '2026-05-30T09:00:00Z' },
      state: { status: 'awaiting_input', updated_at: '2026-05-30T09:00:00Z' },
      children: { sessions: [{ id: 'child_session_stop_stale', status: 'running' }] },
      messages: []
    };
    requestStopSession('child_session_stop_stale');
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /child_session_stop_stale\/stop/);

  vm.runInContext(`
    setLiveActivity({ title: 'Parent refreshed without child', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'parent_session_stop_same_refresh', updated_at: '2026-05-30T09:01:00Z' },
      state: { status: 'awaiting_input', updated_at: '2026-05-30T09:01:00Z' },
      children: { sessions: [] },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].resolve({ status: 'accepted' });
  await stop;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    activityTitle: currentLiveActivity().title,
    stoppingChild: isStoppingSession('child_session_stop_stale'),
    refreshCalls,
    toastCalls
  })`, appContext)), {
    selected: 'parent_session_stop_same_refresh',
    activityTitle: 'Parent refreshed without child',
    stoppingChild: true,
    refreshCalls: [],
    toastCalls: []
  });
});

test('stop not-owned fallback does not steer after same-session terminal refresh', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  const toasts = [];
  vm.runInContext(`
    showToast = function(message, tone = 'info') {
      toastsRef.push({ message, tone });
    };
  `, Object.assign(appContext, { toastsRef: toasts }));

  const stop = vm.runInContext(`
    state.sessionId = 'session_stop_not_owned_terminal';
    state.sessionBacked = true;
    setGeneratingViewState(true);
    setLiveActivity({ title: 'Running old state', copy: '', tone: 'live' });
    state.sessionDetail = {
      metadata: { id: 'session_stop_not_owned_terminal', updated_at: '2026-05-30T10:00:00Z' },
      state: { status: 'running', updated_at: '2026-05-30T10:00:00Z' },
      messages: []
    };
    requestStop();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_stop_not_owned_terminal\/stop/);

  vm.runInContext(`
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Loaded completed session', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'session_stop_not_owned_terminal', updated_at: '2026-05-30T10:01:00Z' },
      state: { status: 'completed', updated_at: '2026-05-30T10:01:00Z' },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].reject({
    code: 'ACTIVE_HANDLE_NOT_OWNED',
    message: 'session is not actively owned by this web console; it may already be settled'
  });
  await stop;

  assert.deepEqual(sameRealm(appContext.pendingRequests.map((request) => request.url)), [
    '/api/sessions/session_stop_not_owned_terminal/stop'
  ]);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    generating: isGenerating(),
    activityTitle: currentLiveActivity().title,
    stopping: isStoppingSession('session_stop_not_owned_terminal')
  })`, appContext)), {
    selected: 'session_stop_not_owned_terminal',
    generating: false,
    activityTitle: 'Loaded completed session',
    stopping: false
  });
  assert.deepEqual(sameRealm(toasts), []);
});

test('child stop not-owned fallback does not steer after parent loses child reference', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  vm.runInContext(`
    refreshCalls = [];
    toastCalls = [];
    queueSessionRefresh = function(delay) {
      refreshCalls.push({ kind: 'session', delay, selected: state.sessionId });
    };
    queueOverviewRefresh = function(delay) {
      refreshCalls.push({ kind: 'overview', delay, selected: state.sessionId });
    };
    showToast = function(message, tone) {
      toastCalls.push({ message, tone });
    };
  `, appContext);

  const stop = vm.runInContext(`
    setCurrentViewName('chat');
    state.sessionId = 'parent_session_stop_not_owned';
    state.sessionBacked = true;
    setGeneratingViewState(false);
    setLiveActivity({ title: 'Parent with child', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'parent_session_stop_not_owned', updated_at: '2026-05-30T10:10:00Z' },
      state: { status: 'awaiting_input', updated_at: '2026-05-30T10:10:00Z' },
      children: { sessions: [{ id: 'child_session_stop_not_owned', status: 'running' }] },
      messages: []
    };
    requestStopSession('child_session_stop_not_owned');
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /child_session_stop_not_owned\/stop/);

  vm.runInContext(`
    setLiveActivity({ title: 'Parent refreshed without child', copy: '', tone: 'neutral' });
    state.sessionDetail = {
      metadata: { id: 'parent_session_stop_not_owned', updated_at: '2026-05-30T10:11:00Z' },
      state: { status: 'awaiting_input', updated_at: '2026-05-30T10:11:00Z' },
      children: { sessions: [] },
      messages: []
    };
  `, appContext);

  appContext.pendingRequests[0].reject({
    code: 'ACTIVE_HANDLE_NOT_OWNED',
    message: 'session is not actively owned by this web console; it may already be settled'
  });
  await stop;

  assert.deepEqual(sameRealm(appContext.pendingRequests.map((request) => request.url)), [
    '/api/sessions/child_session_stop_not_owned/stop'
  ]);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    activityTitle: currentLiveActivity().title,
    stoppingChild: isStoppingSession('child_session_stop_not_owned'),
    refreshCalls,
    toastCalls
  })`, appContext)), {
    selected: 'parent_session_stop_not_owned',
    activityTitle: 'Parent refreshed without child',
    stoppingChild: false,
    refreshCalls: [],
    toastCalls: []
  });
});

test('history stop not-owned fallback does not steer after history refresh', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);
  vm.runInContext(`
    refreshCalls = [];
    toastCalls = [];
    fetchHistory = async function(page = currentHistoryPage(), options = {}) {
      refreshCalls.push({ kind: 'history', page, silent: Boolean(options.silentError) });
    };
    showToast = function(message, tone) {
      toastCalls.push({ message, tone });
    };
  `, appContext);

  const stop = vm.runInContext(`
    setCurrentViewName('history');
    invalidateHistoryRenderSeq();
    requestStopSession('history_session_stop_stale', {
      historyRenderSeq: currentHistoryRenderSeq()
    });
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /history_session_stop_stale\/stop/);

  vm.runInContext(`invalidateHistoryRenderSeq();`, appContext);

  appContext.pendingRequests[0].reject({
    code: 'ACTIVE_HANDLE_NOT_OWNED',
    message: 'session is not actively owned by this web console; it may already be settled'
  });
  await stop;

  assert.deepEqual(sameRealm(appContext.pendingRequests.map((request) => request.url)), [
    '/api/sessions/history_session_stop_stale/stop'
  ]);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    stopping: isStoppingSession('history_session_stop_stale'),
    refreshCalls,
    toastCalls
  })`, appContext)), {
    stopping: false,
    refreshCalls: [],
    toastCalls: []
  });
});

test('loadWorkspaceDirectory ignores stale directory responses after navigation changes', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  const slowLoad = vm.runInContext(`loadWorkspaceDirectory('slow')`, workspaceContext);

  assert.equal(workspaceContext.pendingRequests.length, 1);
  assert.match(workspaceContext.pendingRequests[0].url, /slow/);

  const fastLoad = vm.runInContext(`loadWorkspaceDirectory('fast')`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[1].url, /fast/);

  workspaceContext.pendingRequests[1].resolve([
    { name: 'current.txt', path: 'fast/current.txt', type: 'file' }
  ]);
  await fastLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    path: currentWorkspacePath(),
    tree: currentWorkspaceTree().map((node) => node.name),
    stateHasWorkspacePath: Object.prototype.hasOwnProperty.call(state, 'workspacePath'),
    stateHasFileTree: Object.prototype.hasOwnProperty.call(state, 'fileTree'),
    stateHasRequestSeq: Object.prototype.hasOwnProperty.call(state, 'workspaceRequestSeq'),
    renderedNames: state.renderedTree.map((node) => node.name),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    path: 'fast',
    tree: ['current.txt'],
    stateHasWorkspacePath: false,
    stateHasFileTree: false,
    stateHasRequestSeq: false,
    renderedNames: ['current.txt'],
    filename: 'Workspace / fast',
    content: 'Choose a file or directory to inspect inside the current server workspace.'
  });

  workspaceContext.pendingRequests[0].resolve([
    { name: 'stale.txt', path: 'slow/stale.txt', type: 'file' }
  ]);
  await slowLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    path: currentWorkspacePath(),
    renderedNames: state.renderedTree.map((node) => node.name),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    path: 'fast',
    renderedNames: ['current.txt'],
    filename: 'Workspace / fast',
    content: 'Choose a file or directory to inspect inside the current server workspace.'
  });
});

test('loadFile ignores stale file responses after another file is selected', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  const slowLoad = vm.runInContext(`loadFile('slow.txt')`, workspaceContext);

  assert.equal(workspaceContext.pendingRequests.length, 1);
  assert.match(workspaceContext.pendingRequests[0].url, /slow\.txt/);

  const fastLoad = vm.runInContext(`loadFile('fast.txt')`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[1].url, /fast\.txt/);

  workspaceContext.pendingRequests[1].resolve({ content: 'current file' });
  await fastLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    filename: 'fast.txt',
    content: 'current file'
  });

  workspaceContext.pendingRequests[0].resolve({ content: 'stale file' });
  await slowLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    filename: 'fast.txt',
    content: 'current file'
  });
});

test('loadFile renders paged preview and ignores stale load-more responses', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  const firstLoad = vm.runInContext(`loadFile('large.txt')`, workspaceContext);

  assert.equal(workspaceContext.pendingRequests.length, 1);
  assert.match(workspaceContext.pendingRequests[0].url, /large\.txt/);
  assert.match(workspaceContext.pendingRequests[0].url, /offset=0/);
  assert.match(workspaceContext.pendingRequests[0].url, /limit=262144/);

  workspaceContext.pendingRequests[0].resolve({
    content: 'chunk-one\n',
    offset: 0,
    limit: 262144,
    size: 20,
    truncated: true,
    next_offset: 10
  });
  await firstLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText,
    preview: workspaceFilePreview(),
    stateHasWorkspaceFilePreview: Object.prototype.hasOwnProperty.call(state, 'workspaceFilePreview'),
    footerClass: nodes.editorContent.__children[1].className,
    buttonText: nodes.editorContent.__children[1].__children[1].innerText
  })`, workspaceContext)), {
    filename: 'large.txt',
    content: 'chunk-one\n',
    preview: {
      path: 'large.txt',
      content: 'chunk-one\n',
      offset: 0,
      nextOffset: 10,
      size: 20,
      truncated: true
    },
    stateHasWorkspaceFilePreview: false,
    footerClass: 'workspace-preview-footer',
    buttonText: 'Load more'
  });

  const loadMoreButton = vm.runInContext(`nodes.editorContent.__children[1].__children[1]`, workspaceContext);
  const loadMore = loadMoreButton.listeners.click();
  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[1].url, /large\.txt/);
  assert.match(workspaceContext.pendingRequests[1].url, /offset=10/);

  const otherLoad = vm.runInContext(`loadFile('other.txt')`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 3);
  workspaceContext.pendingRequests[2].resolve({ content: 'other body' });
  await otherLoad;

  workspaceContext.pendingRequests[1].resolve({
    content: 'stale chunk',
    offset: 10,
    limit: 262144,
    size: 20,
    truncated: false
  });
  await loadMore;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText,
    preview: workspaceFilePreview(),
    stateHasWorkspaceFilePreview: Object.prototype.hasOwnProperty.call(state, 'workspaceFilePreview')
  })`, workspaceContext)), {
    filename: 'other.txt',
    content: 'other body',
    preview: {
      path: 'other.txt',
      content: 'other body',
      offset: 0,
      nextOffset: 10,
      size: 10,
      truncated: false
    },
    stateHasWorkspaceFilePreview: false
  });
});

test('workspace file click does not activate stale file selection', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  vm.runInContext(`
    nodes.fileTree.contains = function(node) {
      return Boolean(node && node.isTreeButton);
    };
    nodes.fileTree.querySelector = function() {
      return null;
    };
    function treeButton(path) {
      return {
        isTreeButton: true,
        disabled: false,
        dataset: { path, type: 'file', navigation: '' },
        classList: {
          add() {},
          remove() {}
        }
      };
    }
    slowButton = treeButton('slow.txt');
    fastButton = treeButton('fast.txt');
  `, workspaceContext);

  const slowClick = vm.runInContext(`handleFileTreeClick({ target: { closest() { return slowButton; } } })`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 1);
  assert.match(workspaceContext.pendingRequests[0].url, /slow\.txt/);

  const fastClick = vm.runInContext(`handleFileTreeClick({ target: { closest() { return fastButton; } } })`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[1].url, /fast\.txt/);

  workspaceContext.pendingRequests[1].resolve({ content: 'current file' });
  await fastClick;
  assert.equal(vm.runInContext(`selectedWorkspaceTreePath()`, workspaceContext), 'fast.txt');

  workspaceContext.pendingRequests[0].resolve({ content: 'stale file' });
  await slowClick;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selectedTreePath: selectedWorkspaceTreePath(),
    stateHasSelectedTreePath: Object.prototype.hasOwnProperty.call(state, 'selectedTreePath'),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    selectedTreePath: 'fast.txt',
    stateHasSelectedTreePath: false,
    filename: 'fast.txt',
    content: 'current file'
  });
});

test('workspace file tree renders delegated tree semantics', () => {
  const workspaceContext = createWorkspaceHarnessContext();
  vm.runInContext(`
    window.lucide = null;
    nodes.fileTree.contains = function(node) {
      return Boolean(node);
    };
    renderFileTree([
      { name: 'src', path: 'src', type: 'directory', children: [
        { name: 'main.go', path: 'src/main.go', type: 'file' }
      ] },
      { name: 'README.md', path: 'README.md', type: 'file' }
    ]);
  `, workspaceContext);

  const result = vm.runInContext(`({
    role: nodes.fileTree.getAttribute('role'),
    label: nodes.fileTree.getAttribute('aria-label'),
    hasClick: typeof nodes.fileTree.listeners.click === 'function',
    hasKeydown: typeof nodes.fileTree.listeners.keydown === 'function',
    buttons: nodes.fileTree.querySelectorAll('.tree-node').map((button) => ({
      path: button.dataset.path,
      type: button.dataset.type,
      role: button.getAttribute('role'),
      level: button.getAttribute('aria-level'),
      expanded: button.getAttribute('aria-expanded')
    }))
  })`, workspaceContext);

  assert.deepEqual(sameRealm(result), {
    role: 'tree',
    label: 'Workspace files',
    hasClick: true,
    hasKeydown: true,
    buttons: [
      { path: 'src', type: 'directory', role: 'treeitem', level: '1', expanded: 'false' },
      { path: 'src/main.go', type: 'file', role: 'treeitem', level: '2', expanded: null },
      { path: 'README.md', type: 'file', role: 'treeitem', level: '1', expanded: null }
    ]
  });
});

test('workspace selected paths use the refresh-adjacent trash action', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  workspaceContext.nodes.workspaceDeleteDirBtn = fakeAppElement();
  workspaceContext.nodes.workspaceRefreshBtn = fakeAppElement();
  workspaceContext.nodes.workspaceNewFolderBtn = fakeAppElement();
  workspaceContext.nodes.workspaceSelectedChip = fakeAppElement();

  vm.runInContext(`
    confirmRequests = [];
    deletedPaths = [];
    confirmLocalAction = async function(options) {
      confirmRequests.push(options);
      return true;
    };
    deleteWorkspacePaths = async function(paths) {
      deletedPaths.push([...paths]);
      return { count: paths.length };
    };
    loadWorkspaceDirectory = async function(path) {
      state.reloadedPath = path;
    };
    setCurrentWorkspacePath('src');
    ensureWorkspaceActionBindings();
    toggleWorkspacePathSelection('src/main.go', true);
    toggleWorkspacePathSelection('src/util.go', true);
  `, workspaceContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selectedCount: selectedWorkspacePathCount(),
    hidden: nodes.workspaceDeleteDirBtn.classList.contains('is-hidden'),
    disabled: nodes.workspaceDeleteDirBtn.disabled,
    title: nodes.workspaceDeleteDirBtn.title,
    label: nodes.workspaceDeleteDirBtn.getAttribute('aria-label'),
    chipHidden: nodes.workspaceSelectedChip.classList.contains('is-hidden'),
    chipText: nodes.workspaceSelectedChip.textContent
  })`, workspaceContext)), {
    selectedCount: 2,
    hidden: false,
    disabled: false,
    title: 'Delete 2 selected items',
    label: 'Delete 2 selected items',
    chipHidden: false,
    chipText: '2 selected'
  });

  await vm.runInContext(`nodes.workspaceDeleteDirBtn.listeners.click()`, workspaceContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    confirmTitle: confirmRequests[0].title,
    confirmLabel: confirmRequests[0].confirmLabel,
    deletedPaths,
    selectedCount: selectedWorkspacePathCount(),
    reloadedPath: state.reloadedPath,
	pending: workspaceActionPending(),
	refreshFocused: nodes.workspaceRefreshBtn.focused === true
  })`, workspaceContext)), {
    confirmTitle: 'Delete selected items',
    confirmLabel: 'Delete selected',
    deletedPaths: [['src/main.go', 'src/util.go']],
    selectedCount: 0,
    reloadedPath: 'src',
	pending: '',
	refreshFocused: true
  });
});

test('workspace upload control sends the selected file to the current directory and refreshes', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  workspaceContext.nodes.workspaceUploadBtn = fakeAppElement();
  workspaceContext.nodes.workspaceUploadInput = fakeAppElement();
  workspaceContext.nodes.workspaceRefreshBtn = fakeAppElement();
  workspaceContext.nodes.workspaceNewFolderBtn = fakeAppElement();
  workspaceContext.nodes.workspaceDeleteDirBtn = fakeAppElement();
  workspaceContext.nodes.workspaceSelectedChip = fakeAppElement();

  vm.runInContext(`
    uploadCalls = [];
    toasts = [];
    uploadWorkspaceFile = async function(path, file) {
      uploadCalls.push({ path, name: file.name });
      return { path: 'src/' + file.name };
    };
    loadWorkspaceDirectory = async function(path) {
      state.reloadedPath = path;
    };
    showToast = function(message, tone) {
      toasts.push({ message, tone });
    };
    setCurrentWorkspacePath('src');
    nodes.workspaceUploadInput.files = [{ name: 'notes.txt' }];
    ensureWorkspaceActionBindings();
  `, workspaceContext);

  await vm.runInContext(`nodes.workspaceUploadInput.listeners.change()`, workspaceContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    uploadCalls,
    reloadedPath: state.reloadedPath,
    inputValue: nodes.workspaceUploadInput.value,
    pending: workspaceActionPending(),
    toast: toasts[0]
  })`, workspaceContext)), {
    uploadCalls: [{ path: 'src', name: 'notes.txt' }],
    reloadedPath: 'src',
    inputValue: '',
    pending: '',
    toast: { message: 'Uploaded src/notes.txt.', tone: 'success' }
  });
});

test('workspace rename control renames the previewed file and keeps its preview selected', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  workspaceContext.nodes.workspaceRenameBtn = fakeAppElement();
  workspaceContext.nodes.workspaceDownloadBtn = fakeAppElement();
  workspaceContext.nodes.workspaceDeleteFileBtn = fakeAppElement();
  workspaceContext.nodes.workspaceRefreshBtn = fakeAppElement();
  workspaceContext.nodes.workspaceNewFolderBtn = fakeAppElement();
  workspaceContext.nodes.workspaceDeleteDirBtn = fakeAppElement();
  workspaceContext.nodes.workspaceSelectedChip = fakeAppElement();

  vm.runInContext(`
    renameCalls = [];
    toasts = [];
		promptLocalAction = async function() { return 'final.txt'; };
    renameWorkspaceFile = async function(path, name) {
      renameCalls.push({ path, name });
      return { path: 'src/final.txt' };
    };
    loadWorkspaceDirectory = async function(path) {
      state.reloadedPath = path;
    };
    loadFile = async function(path) {
      state.loadedFile = path;
      return true;
    };
    showToast = function(message, tone) {
      toasts.push({ message, tone });
    };
    setCurrentWorkspacePath('src');
    setWorkspaceFilePreview({ path: 'src/draft.txt', content: 'draft' });
    ensureWorkspaceActionBindings();
  `, workspaceContext);

  await vm.runInContext(`nodes.workspaceRenameBtn.listeners.click()`, workspaceContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    renameCalls,
    reloadedPath: state.reloadedPath,
    loadedFile: state.loadedFile,
    selectedPath: selectedWorkspaceTreePath(),
    pending: workspaceActionPending(),
	renameFocused: nodes.workspaceRenameBtn.focused === true,
    toast: toasts[0]
  })`, workspaceContext)), {
    renameCalls: [{ path: 'src/draft.txt', name: 'final.txt' }],
    reloadedPath: 'src',
    loadedFile: 'src/final.txt',
    selectedPath: 'src/final.txt',
    pending: '',
	renameFocused: true,
    toast: { message: 'Renamed file to final.txt.', tone: 'success' }
  });
});

test('workspace file tree keyboard activates and moves focus', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  vm.runInContext(`
    window.lucide = null;
    nodes.fileTree.contains = function(node) {
      return Boolean(node);
    };
    renderFileTree([
      { name: 'src', path: 'src', type: 'directory', children: [
        { name: 'main.go', path: 'src/main.go', type: 'file' }
      ] },
      { name: 'README.md', path: 'README.md', type: 'file' }
    ]);
    treeButtons = nodes.fileTree.querySelectorAll('.tree-node');
    keyboardEvents = [];
    function keyEvent(key, button) {
      return {
        key,
        target: { closest() { return button; } },
        preventDefault() { keyboardEvents.push('prevent:' + key); },
        stopPropagation() { keyboardEvents.push('stop:' + key); }
      };
    }
  `, workspaceContext);

  const enterPress = vm.runInContext(`handleFileTreeKeydown(keyEvent('Enter', treeButtons[1]))`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 1);
  assert.match(workspaceContext.pendingRequests[0].url, /src%2Fmain\.go/);
  workspaceContext.pendingRequests[0].resolve({ content: 'package main' });
  await enterPress;

  await vm.runInContext(`handleFileTreeKeydown(keyEvent('ArrowDown', treeButtons[0]))`, workspaceContext);
  await vm.runInContext(`handleFileTreeKeydown(keyEvent('ArrowLeft', treeButtons[1]))`, workspaceContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selectedTreePath: selectedWorkspaceTreePath(),
    stateHasSelectedTreePath: Object.prototype.hasOwnProperty.call(state, 'selectedTreePath'),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText,
    secondFocused: Boolean(treeButtons[1].focused),
    parentFocused: Boolean(treeButtons[0].focused),
    events: keyboardEvents
  })`, workspaceContext)), {
    selectedTreePath: 'src/main.go',
    stateHasSelectedTreePath: false,
    filename: 'src/main.go',
    content: 'package main',
    secondFocused: true,
    parentFocused: true,
    events: [
      'prevent:Enter',
      'stop:Enter',
      'prevent:ArrowDown',
      'stop:ArrowDown',
      'prevent:ArrowLeft',
      'stop:ArrowLeft'
    ]
  });
});

test('workspace file responses do not overwrite later directory navigation', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  const fileLoad = vm.runInContext(`loadFile('old.txt')`, workspaceContext);
  const directoryLoad = vm.runInContext(`loadWorkspaceDirectory('next')`, workspaceContext);

  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[0].url, /old\.txt/);
  assert.match(workspaceContext.pendingRequests[1].url, /next/);

  workspaceContext.pendingRequests[1].resolve([
    { name: 'next.txt', path: 'next/next.txt', type: 'file' }
  ]);
  await directoryLoad;

  workspaceContext.pendingRequests[0].resolve({ content: 'old file body' });
  await fileLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    path: currentWorkspacePath(),
    renderedNames: state.renderedTree.map((node) => node.name),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    path: 'next',
    renderedNames: ['next.txt'],
    filename: 'Workspace / next',
    content: 'Choose a file or directory to inspect inside the current server workspace.'
  });
});

test('workspace directory responses do not overwrite later file selection', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  const directoryLoad = vm.runInContext(`loadWorkspaceDirectory('old')`, workspaceContext);
  const fileLoad = vm.runInContext(`loadFile('new.txt')`, workspaceContext);

  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[0].url, /old/);
  assert.match(workspaceContext.pendingRequests[1].url, /new\.txt/);

  workspaceContext.pendingRequests[1].resolve({ content: 'new file body' });
  await fileLoad;

  workspaceContext.pendingRequests[0].resolve([
    { name: 'old.txt', path: 'old/old.txt', type: 'file' }
  ]);
  await directoryLoad;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    path: currentWorkspacePath(),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    path: '',
    filename: 'new.txt',
    content: 'new file body'
  });
});

test('Workspace fetch preserves a manually browsed path across view changes', async () => {
  const appContext = createAppHarnessContext();
  await vm.runInContext(`(async () => {
    state.meta = { workspace_root: '/tmp/workspace', workspace_switch_supported: false };
    state.sessionId = 'session_workspace_keep';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: {
        id: 'session_workspace_keep',
        workdir: '/tmp/workspace/session-root'
      }
    };
    setCurrentViewName('workspace');
    syncWorkspaceToCurrentSession({ refresh: false });
    setCurrentWorkspacePath('session-root/deep');
    listWorkspaceFiles = async function(path) {
      state.requestedWorkspacePath = path;
      return [];
    };
    switchView('chat');
    await fetchWorkspace();
  })()`, appContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    path: currentWorkspacePath(),
    requestedPath: state.requestedWorkspacePath,
    sessionPath: currentSessionWorkspacePath()
  })`, appContext)), {
    path: 'session-root/deep',
    requestedPath: 'session-root/deep',
    sessionPath: 'session-root'
  });
});

test('Workspace path preferences are isolated per session and new-session composer', () => {
  const appContext = createAppHarnessContext();
  const result = vm.runInContext(`(() => {
    state.meta = { workspace_root: '/tmp/workspace', workspace_switch_supported: false };

    state.sessionId = 'session_workspace_a';
    state.sessionBacked = true;
    state.sessionDetail = { metadata: { id: 'session_workspace_a', workdir: '/tmp/workspace/a' } };
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    setCurrentWorkspacePath('a/deep');

    state.sessionId = 'session_workspace_b';
    state.sessionDetail = { metadata: { id: 'session_workspace_b', workdir: '/tmp/workspace/b' } };
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    const firstBPath = currentWorkspacePath();
    setCurrentWorkspacePath('b/review');

    state.sessionId = 'session_workspace_a';
    state.sessionDetail = { metadata: { id: 'session_workspace_a', workdir: '/tmp/workspace/a' } };
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    const restoredAPath = currentWorkspacePath();
    syncWorkspaceToCurrentSession({ refresh: false });
    const stableAPath = currentWorkspacePath();
    state.sessionDetail = { metadata: { id: 'session_workspace_a', workdir: '/tmp/workspace/a-next' } };
    syncWorkspaceToCurrentSession({ refresh: false });
    const changedWorkdirPath = currentWorkspacePath();

    state.sessionId = '0xABCDEF';
    state.sessionBacked = false;
    state.sessionDetail = null;
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    const initialComposerPath = currentWorkspacePath();
    setCurrentWorkspacePath('drafts/new-session');

    state.sessionId = 'session_workspace_b';
    state.sessionBacked = true;
    state.sessionDetail = { metadata: { id: 'session_workspace_b', workdir: '/tmp/workspace/b' } };
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    const restoredBPath = currentWorkspacePath();

    state.sessionId = '0x123456';
    state.sessionBacked = false;
    state.sessionDetail = null;
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    return {
      firstBPath,
      restoredAPath,
      stableAPath,
      changedWorkdirPath,
      initialComposerPath,
      restoredBPath,
      restoredComposerPath: currentWorkspacePath(),
      noSessionPath: currentSessionWorkspacePath()
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    firstBPath: 'b',
    restoredAPath: 'a/deep',
    stableAPath: 'a/deep',
    changedWorkdirPath: 'a-next',
    initialComposerPath: '',
    restoredBPath: 'b/review',
    restoredComposerPath: 'drafts/new-session',
    noSessionPath: null
  });
});

test('Workspace fetch preserves the new-session composer path without an active session', async () => {
  const appContext = createAppHarnessContext();
  const result = await vm.runInContext(`(async () => {
    state.meta = { workspace_root: '/tmp/workspace', workspace_switch_supported: false };
    state.sessionBacked = false;
    state.sessionDetail = null;
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    setCurrentWorkspacePath('drafts/composer');
    listWorkspaceFiles = async function(path) {
      state.requestedWorkspacePath = path;
      return [];
    };
    await fetchWorkspace();
    return {
      path: currentWorkspacePath(),
      requestedPath: state.requestedWorkspacePath,
      sessionPath: currentSessionWorkspacePath()
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    path: 'drafts/composer',
    requestedPath: 'drafts/composer',
    sessionPath: null
  });
});

test('Workspace path preference survives browser UI state restore', () => {
  const firstContext = createAppHarnessContext();
  vm.runInContext(`
    state.meta = { workspace_root: '/tmp/workspace', workspace_switch_supported: false };
    state.sessionId = 'session_workspace_restore';
    state.sessionBacked = true;
    state.sessionDetail = {
      metadata: { id: 'session_workspace_restore', workdir: '/tmp/workspace/session-home' }
    };
    resetWorkspaceSessionSync();
    syncWorkspaceToCurrentSession({ refresh: false });
    setCurrentWorkspacePath('session-home/restored');
    persistUIState();
  `, firstContext);
  const persisted = firstContext.storage.get('aegis-agent.webconsole.ui-state.v1');
  assert.ok(persisted);

  const secondContext = createAppHarnessContext({
    'aegis-agent.webconsole.ui-state.v1': persisted
  });
  const restoredPath = vm.runInContext(`(() => {
    restoreUIState();
    state.meta = { workspace_root: '/tmp/workspace', workspace_switch_supported: false };
    state.sessionDetail = {
      metadata: { id: 'session_workspace_restore', workdir: '/tmp/workspace/session-home' }
    };
    syncWorkspaceToCurrentSession({ refresh: false });
    return currentWorkspacePath();
  })()`, secondContext);
  assert.equal(restoredPath, 'session-home/restored');
});

test('Workspace invalid persisted path falls back to the nearest accessible parent once', async () => {
  const workspaceContext = createWorkspaceHarnessContext();
  vm.runInContext(`
    toastCalls = [];
    showToast = function(message, tone) {
      toastCalls.push({ message, tone });
    };
    setCurrentWorkspacePath('gone/deep');
  `, workspaceContext);
  const load = vm.runInContext(`loadWorkspaceDirectoryWithFallback(currentWorkspacePath())`, workspaceContext);
  assert.equal(workspaceContext.pendingRequests.length, 1);
  workspaceContext.pendingRequests[0].reject({ status: 404, message: 'missing deep path' });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(workspaceContext.pendingRequests.length, 2);
  assert.match(workspaceContext.pendingRequests[1].url, /gone/);
  workspaceContext.pendingRequests[1].resolve([]);
  await load;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    path: currentWorkspacePath(),
    toastCalls
  })`, workspaceContext)), {
    path: 'gone',
    toastCalls: [{
      message: 'Workspace path gone/deep is unavailable. Showing gone instead.',
      tone: 'info'
    }]
  });
});

test('task panel renders cancelled count separately from completed tasks', () => {
  const html = context.renderTasksPanel({
    task_board: {
      todo: [],
      tasks: [
        { id: 'task_0001', subject: 'Done', status: 'completed' },
        { id: 'task_0002', subject: 'Stopped', status: 'cancelled' }
      ],
      counters: {
        completed: 1,
        cancelled: 1,
        done: 2,
        in_progress: 0
      },
      groups: {
        ready: [],
        blocked: []
      }
    }
  });

  assert.match(html, /<span class="metric-label">Completed<\/span>/);
  assert.match(html, /<div class="metric-card-value">1<\/div>/);
  assert.match(html, /<div class="metric-card-copy">1 cancelled<\/div>/);
});

test('task panel counts Todo progress independently and renders derived task groups', () => {
	const html = context.renderTasksPanel({
		task_board: {
			todo: [
				{ content: 'One', status: 'in_progress' },
				{ content: 'Two', status: 'in_progress' },
				{ content: 'Three', status: 'pending' }
			],
			counters: { in_progress: 1, completed: 1, cancelled: 0 },
			groups: {
				in_progress: [{ id: 'task_0001', subject: 'Working', status: 'in_progress' }],
				ready: [{ id: 'task_0002', subject: 'Ready task', status: 'pending' }],
				blocked: [{ id: 'task_0003', subject: 'Blocked task', status: 'pending', blocked_by: ['task_0001'] }],
				completed: [{ id: 'task_0004', subject: 'Done', status: 'completed' }],
				cancelled: []
			}
		}
	});
	assert.match(html, /<div class="metric-card-copy">2 in progress<\/div>/);
	assert.match(html, /data-task-group="in_progress"/);
	assert.match(html, /data-task-group="ready"/);
	assert.match(html, /data-derived-status="ready"/);
	assert.match(html, />Ready<\/span>/);
	assert.match(html, />task_0002<\/span>/);
	assert.ok(html.indexOf('data-task-group="in_progress"') < html.indexOf('data-task-group="ready"'));
});

test('Task and Todo renderers isolate durable facts from localizable fallbacks', () => {
	const task = context.renderTaskItem({
		id: 'task_collision',
		subject: 'Settings',
		status: 'ready',
		priority: 'Settings',
		owner: 'Settings'
	}, 'ready');
	const todo = context.renderTodoItem({ status: 'pending' });

	assert.match(task, /class="task-card-title"><span translate="no" data-i18n-skip>Settings<\/span>/);
	assert.match(task, /class="task-card-copy">No description\.<\/div>/);
	assert.doesNotMatch(task, /class="task-card-copy"[^>]+translate="no"/);
	assert.equal((task.match(/class="task-chip" translate="no" data-i18n-skip>Settings<\/span>/g) || []).length, 2);
	assert.match(todo, /class="todo-card-title">Untitled todo<\/div>/);
});

test('Web Console v2 inspector excludes Background and exposes complete tab semantics', () => {
	context.document.documentElement = { dataset: { ui: 'aegis-v2' } };
	vm.runInContext(`
		state.sessionDetail = {
			metadata: { id: 'session_v2_tabs' },
			state: { status: 'paused' },
			task_board: { todo: [], counters: {}, groups: {} }
		};
		inspectorViewState.tab = 'tasks';
	`, context);
	const html = vm.runInContext('renderInspectorPanel()', context);
	delete context.document.documentElement;
	assert.doesNotMatch(html, /data-inspector-tab="agents"/);
	assert.doesNotMatch(html, />Background<\/button>/);
	assert.match(html, /role="tablist"/);
	assert.match(html, /role="tab"[^>]+aria-selected="true"[^>]+tabindex="0"/);
	assert.match(html, /role="tabpanel"[^>]+aria-labelledby="inspector-tab-tasks"/);
});

test('i18n defaults to zh-CN, switches to English, and persists the locale', () => {
	const values = new Map();
	const makeContext = () => {
		const document = {
			readyState: 'loading',
			documentElement: { lang: '', dataset: {}, setAttribute() {} },
			addEventListener() {},
			dispatchEvent() {},
			getElementById() { return null; }
		};
		const window = {
			document,
			localStorage: {
				getItem(key) { return values.get(key) || null; },
				setItem(key, value) { values.set(key, value); }
			}
		};
		const i18nContext = { window };
		vm.createContext(i18nContext);
		vm.runInContext(i18nSource, i18nContext, { filename: 'i18n.js' });
		return window.AegisI18n;
	};
	const first = makeContext();
	assert.equal(first.locale(), 'zh-CN');
	assert.equal(first.t('Settings'), '设置');
	assert.equal(first.t('2 in progress'), '2 个进行中');
	assert.equal(first.t('Root peak'), '根会话峰值');
	assert.equal(first.t('Provider usage'), '提供商用量');
	assert.equal(first.t('Lineage'), '会话链路');
	assert.equal(first.t('Turns'), '轮次');
	assert.equal(first.t('Tool calls'), '工具调用');
	assert.equal(first.t('Bounded view: 1 sessions and 7 requests omitted. Aggregate totals are complete.'), '有界视图：省略 1 个会话和 7 个请求；汇总总数完整。');
	assert.equal(first.t('by'), '作者');
	assert.equal(first.t('session'), '会话');
	assert.equal(first.t('No description.'), '无描述。');
	assert.equal(first.t('No final text recorded.'), '未记录最终文本。');
	assert.equal(first.setLocale('en'), 'en');
	assert.equal(first.t('Settings'), 'Settings');
	assert.equal(first.t('Root peak'), 'Root peak');
	assert.equal(first.t('by'), 'by');
	assert.equal(first.t('No final text recorded.'), 'No final text recorded.');
	const restored = makeContext();
	assert.equal(restored.locale(), 'en');
});

test('i18n covers dynamic composer, toast, and local action copy without rewriting durable values', () => {
	const document = {
		readyState: 'loading',
		documentElement: { lang: '', setAttribute() {} },
		addEventListener() {},
		dispatchEvent() {},
		getElementById() { return null; },
		querySelectorAll() { return []; }
	};
	const window = { document, localStorage: { getItem() { return null; }, setItem() {} } };
	const i18nContext = { window };
	vm.createContext(i18nContext);
	vm.runInContext(i18nSource, i18nContext, { filename: 'i18n.js' });
	const i18n = window.AegisI18n;

	const exactCases = [
		['Workspace path docs/api is unavailable. Showing workspace root instead.', '工作区路径 docs/api 不可用，已改为显示工作区根目录。'],
		['Workspace path docs/api is unavailable. Showing docs instead.', '工作区路径 docs/api 不可用，已改为显示 docs。'],
		['Created folder docs.', '已创建文件夹 docs。'],
		['Uploaded docs/report.txt.', '已上传 docs/report.txt。'],
		['Download started for docs/report.txt.', '已开始下载 docs/report.txt。'],
		['Renamed file to report-final.txt.', '文件已重命名为 report-final.txt。'],
		['Deleted 2 selected items.', '已删除 2 个所选项目。'],
		['Open child session "worker-a"?', '打开子会话“worker-a”？'],
		['Uninstall skill local-audit from the local catalog?', '从本地目录卸载技能 local-audit？'],
		['Delete session sess_123?', '删除会话 sess_123？'],
		['Create a new folder in docs.', '在 docs 中创建新文件夹。'],
		['Choose a new name for report.txt.', '为 report.txt 选择新名称。'],
		['Delete workspace folder docs and all of its contents? This cannot be undone from the web console.', '删除工作区文件夹 docs 及其全部内容？此操作无法在 Web 控制台中撤销。'],
		['Delete workspace file docs/report.txt? This cannot be undone from the web console.', '删除工作区文件 docs/report.txt？此操作无法在 Web 控制台中撤销。'],
		['Delete 2 selected workspace items? Folders will be removed with all of their contents. This cannot be undone from the web console.', '删除 2 个所选工作区项目？文件夹及其全部内容都会被删除。此操作无法在 Web 控制台中撤销。'],
		['Failed to load directory: docs/api', '加载目录失败：docs/api'],
		['Failed to load file: docs/report.txt', '加载文件失败：docs/report.txt'],
		['Continue Awaiting input session: next send resumes this durable session.', '继续等待输入的会话：下次发送将恢复此持久化会话。'],
		['Continue Failed session: next send resumes this durable session.', '继续失败的会话：下次发送将恢复此持久化会话。'],
		['Goal objective is required.', '请输入目标说明。'],
		['Delete selected item', '删除所选项目'],
		['Delete selected items', '删除所选项目'],
		['Delete 1 selected item', '删除 1 个所选项目'],
		['Delete 2 selected items', '删除 2 个所选项目'],
		['Delete folder', '删除文件夹'],
		['Delete folder docs', '删除文件夹 docs'],
		['File name', '文件名'],
		['Provider test passed: openai / gpt-5.5 / high. readable thinking returned. Strategy: summary.', '提供商测试通过：openai / gpt-5.5 / high。已返回可读思考内容。策略：summary。'],
		['Provider test passed: openai / gpt-5.5 / high. replay-only thinking returned. Strategy: replay.', '提供商测试通过：openai / gpt-5.5 / high。已返回仅用于回放的思考内容。策略：replay。'],
		['Provider test passed: openai / gpt-5.5 / high. provider accepted request but returned no readable thinking in this probe. Strategy: none.', '提供商测试通过：openai / gpt-5.5 / high。此探测中提供商已接受请求，但未返回可读思考内容。策略：none。'],
		['Provider test passed: openai / gpt-5.5 / high.', '提供商测试通过：openai / gpt-5.5 / high。']
	];
	for (const [source, expected] of exactCases) {
		assert.equal(i18n.t(source), expected, source);
	}

	const operatorCopy = [
		'Ignored malformed websocket payload from local server.',
		'The session failed.',
		'Session launch is already in progress.',
		'Interrupt steer sent.',
		'Steer queued for the running session.',
		'Failed to queue steer input.',
		'Plan revision sent.',
		'Plan Mode started.',
		'Failed to start session.',
		'This session is not ready to continue. Refresh the session state and try again.',
		'No running session is available for interrupt.',
		'Interrupt requested.',
		'Failed to request interrupt.',
		'No running session is available to stop.',
		'No session is available to stop.',
		'Stop already requested for this session.',
		'Failed to stop the session.',
		'No durable session is loaded.',
		'Plan approval was not overridden.',
		'Plan approved and execution started.',
		'Plan Mode cancelled.',
		'Type the requested plan change and send it.',
		'Plan Mode action failed.',
		'Plan input request is no longer pending.',
		'Answer every Plan Mode question before submitting.',
		'Plan input answered.',
		'Failed to answer Plan Mode input.',
		'Previously selected session is no longer available.',
		'Failed to delete session.',
		'Failed to clear sessions.',
		'Failed to uninstall skill.',
		'Provider test failed.',
		'Settings save cancelled.',
		'Failed to save configuration.',
		'Folder name is required.',
		'Folder name must not contain path separators.',
		'Failed to create folder.',
		'Failed to upload file.',
		'Workspace refreshed.',
		'Failed to refresh workspace.',
		'Failed to delete folder.',
		'File name is required.',
		'Choose a different file name.',
		'File name must not contain path separators.',
		'Failed to rename file.',
		'Failed to delete file.',
		'Deleted 1 selected item.',
		'Failed to delete selected items.',
		'Send an interrupt steer message to the running session...',
		'Describe the objective to plan before execution...',
		'Live event relay reconnecting; REST session actions remain available.',
		'Plan Mode enabled: next send starts a planning gate before execution.',
		'Interrupt armed: next send requests preemption, then merges your steer prompt.',
		'Steer running session: next send queues guidance into the current run.',
		'Open child session?',
		'Override validation coverage',
		'Validation coverage blocks approval. Continue only if you accept the uncovered validation risk for this local session.',
		'Override',
		'Clear the durable goal for this local session? This removes goal state and history links from the current session view.',
		'Uninstall skill',
		'Custom Plan Mode Answer',
		'Enter a custom answer for this Plan Mode question.',
		'Answer',
		'Use Answer',
		'Clear saved sessions',
		'Clear all saved sessions? This will remove sessions and queue history.',
		'Clear all',
		'Save settings',
		'Save settings and write the entered API key to the local env file?',
		'Save settings to the local configuration files?',
		'Save',
		'Sub-agent active runtime must be a duration such as 30m, 2h, 1h30m, or seconds.',
		'Sub-agent absolute elapsed deadline must be a duration such as 30m, 2h, 1h30m, or seconds.',
		'Context window tokens must be a non-negative integer.',
		'Sub-agent turns per attempt must be a non-negative integer.',
		'Enable at least one positive sub-agent budget limit.',
		'Soft checkpoint turns must be a positive integer.',
		'Hard max turns must be a positive integer, or disable the hard limit.',
		'Plan Mode objective is required.',
		'Job detail is unavailable.',
		'Failed to load parent directory.',
		'Failed to load session overview.',
		'Failed to load recent activity.',
		'Failed to load backend settings.'
	];
	for (const source of operatorCopy) {
		const translated = i18n.t(source);
		assert.notEqual(translated, source, source);
		assert.match(translated, /\p{Script=Han}/u, source);
	}

	i18n.setLocale('en', { persist: false });
	for (const [source] of exactCases) assert.equal(i18n.t(source), source);
	for (const source of operatorCopy) assert.equal(i18n.t(source), source);
});

test('i18n restores existing translated text and attributes when switching to English', () => {
	const document = {
		readyState: 'loading',
		documentElement: { lang: '', setAttribute() {} },
		addEventListener() {},
		dispatchEvent() {},
		getElementById() { return null; },
		querySelectorAll() { return []; }
	};
	const window = { document, localStorage: { getItem() { return null; }, setItem() {} } };
	const i18nContext = { window };
	vm.createContext(i18nContext);
	vm.runInContext(i18nSource, i18nContext, { filename: 'i18n.js' });
	const i18n = window.AegisI18n;
	const parent = {
		nodeType: 1,
		closest() { return null; },
		hasAttribute() { return false; }
	};
	const textNode = { nodeType: 3, nodeValue: 'Settings', parentElement: parent };
	const attributes = new Map([['aria-label', 'Settings']]);
	const element = {
		nodeType: 1,
		closest() { return null; },
		hasAttribute(name) { return attributes.has(name); },
		getAttribute(name) { return attributes.get(name) || ''; },
		setAttribute(name, value) { attributes.set(name, value); },
		querySelectorAll() { return []; }
	};

	i18n.apply(textNode);
	i18n.apply(element);
	assert.equal(textNode.nodeValue, '设置');
	assert.equal(attributes.get('aria-label'), '设置');
	i18n.setLocale('en', { persist: false });
	i18n.apply(textNode);
	i18n.apply(element);
	assert.equal(textNode.nodeValue, 'Settings');
	assert.equal(attributes.get('aria-label'), 'Settings');
});

test('summary panel renders provider attempt ledger facts', () => {
  const html = context.renderSummaryPanel({
    state: {
      status: 'failed',
      phase: 'provider_call',
      turn: 2,
      loaded_skills: []
    },
    metadata: {
      id: 'sess_provider_attempts',
      provider: 'openai-compatible',
      model: 'gpt-test',
      mode: 'exec',
      workdir: '/tmp/workspace'
    },
    messages: [],
    steer_requests: [],
    background_notifications: [],
    provider_attempts: [
      {
        turn: 2,
        attempt: 1,
        provider: 'openai-compatible',
        model: 'gpt-test',
        outcome: 'retry',
        error_class: 'upstream_timeout',
        timeout_kind: 'request_timeout',
        status_code: 504,
        error: 'gateway timed out'
      },
      {
        turn: 2,
        attempt: 2,
        provider: 'openai-compatible',
        model: 'gpt-test',
        outcome: 'success',
        provider_response_id: 'resp_provider_attempts_done',
        cache_read_input_tokens: 23,
        cache_creation_input_tokens: 11
      }
    ]
  });

  assert.match(html, />Provider attempts<\/h4>/);
  assert.match(html, /<span class="metric-label">Attempts<\/span>/);
  assert.match(html, /1 success · 0 failed/);
  assert.match(html, /1 retry · 0 auto-resume/);
  assert.match(html, /cache read 23/);
  assert.match(html, /cache create 11/);
  assert.match(html, /request_timeout/);
  assert.match(html, /response resp_p.+done/);
});

test('session panels render parent coordination and checkpoint recovery facts', () => {
  const detail = {
    state: {
      status: 'completed',
      phase: 'turn_decide',
      turn: 3,
      loaded_skills: []
    },
    metadata: {
      id: 'sess_parent_recovery_facts',
      provider: 'openai-compatible',
      model: 'gpt-test',
      mode: 'run',
      workdir: '/tmp/workspace'
    },
    messages: [],
    steer_requests: [],
    background_notifications: [],
    provider_attempts: [],
    children: {
      sessions: [],
      jobs: []
    },
    parent_coordination: {
      parent_session_id: 'sess_parent_recovery_facts',
      wait_mode: 'wait-all',
      parked: true,
      unresolved_child_sessions: ['child_coordination_only'],
      unresolved_queue_jobs: ['job_coordination_only'],
      completed_queue_jobs: ['job_completed'],
      updated_at: '2026-05-30T01:02:03Z'
    },
    longrun_checkpoint: {
      session_id: 'sess_parent_recovery_facts',
      parent_wait_state: 'parked',
      unresolved_child_sessions: ['child_coordination_only'],
      unresolved_queue_jobs: ['job_coordination_only'],
      resume_hints: ['resolve parent child or queue wait state'],
      created_at: '2026-05-30T01:03:04Z'
    }
  };

  const summaryHTML = context.renderSummaryPanel(detail);
  assert.match(summaryHTML, />Recovery facts<\/h4>/);
  assert.match(summaryHTML, /Parent coordination/);
  assert.match(summaryHTML, /wait-all · parked · unresolved 1\/1 · completed 0\/1/);
  assert.match(summaryHTML, /Long-run checkpoint/);
  assert.match(summaryHTML, /resolve parent child or queue wait state/);
  assert.match(summaryHTML, /child_coordination_only/);
  assert.match(summaryHTML, /job_coordination_only/);

  const agentsHTML = context.renderAgentsPanel(detail);
  assert.match(agentsHTML, /parent-coordination-panel/);
  assert.match(agentsHTML, /Parent coordination/);
  assert.match(agentsHTML, /Unresolved jobs: job_coordination_only/);
});

test('settings save keeps empty API key fields unmasked after success', async () => {
  const { elements, savedPayloads, toasts, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    await elements['settings-save-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(savedPayloads.length, 1);
  assert.equal(savedPayloads[0].apiKey, '');
  assert.equal(savedPayloads[0].legacyUIEnabled, false);
  assert.deepEqual(sameRealm(savedPayloads[0].childBudget), {
    disabled: true,
    maxActiveRuntimeSec: 0,
    maxElapsedSec: 0,
    maxTurnsPerAttempt: 0
  });
  assert.equal(elements['settings-apikey'].value, '');
  assert.equal(elements['settings-apikey'].dataset.originalHasKey, 'false');
  assert.equal(toasts.at(-1)?.tone, 'success');
});

test('settings can explicitly enable the legacy frontend rollback route', async () => {
  const { elements, savedPayloads, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    elements['settings-enable-legacy-ui'].checked = true;
    elements['settings-enable-legacy-ui'].listeners.change();
    await elements['settings-save-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(savedPayloads.length, 1);
  assert.equal(savedPayloads[0].legacyUIEnabled, true);
  assert.equal(elements['settings-legacy-ui-state'].textContent, 'Enabled');
});

test('settings copy states global scope, soft semantics, default Off, and new-work snapshot behavior', async () => {
  const { container, elements, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    assert.match(container.innerHTML, /Applies per run to master, foreground child, and background\/queue child sessions\./);
    assert.match(container.innerHTML, /Soft is a one-time checkpoint reminder and never stops execution\./);
    assert.match(container.innerHTML, /off by default/i);
    assert.match(container.innerHTML, /Changes affect newly created child\/job work only/);
    assert.equal(elements['settings-child-budget-state'].textContent, 'Off');
    assert.equal(elements['settings-disable-hard-turn-limit'].checked, true);
    assert.equal(elements['settings-legacy-ui-state'].textContent, 'Disabled');
  } finally {
    restore();
  }
});

test('settings saves an explicitly enabled sub-agent budget', async () => {
  const { elements, savedPayloads, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    elements['settings-enable-child-budget'].checked = true;
    elements['settings-enable-child-budget'].listeners.change();
    elements['settings-child-budget-active-runtime'].value = '1h30m';
    elements['settings-child-budget-elapsed'].value = '2h';
    elements['settings-child-budget-max-turns'].value = '320';
    await elements['settings-save-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(savedPayloads.length, 1);
  assert.deepEqual(sameRealm(savedPayloads[0].childBudget), {
    disabled: false,
    maxActiveRuntimeSec: 5400,
    maxElapsedSec: 7200,
    maxTurnsPerAttempt: 320
  });
  assert.equal(elements['settings-child-budget-active-runtime'].disabled, false);
  assert.equal(elements['settings-child-budget-elapsed'].disabled, false);
  assert.equal(elements['settings-child-budget-max-turns'].disabled, false);
  assert.equal(elements['settings-child-budget-state'].textContent, 'Enabled');
});

test('settings rejects an enabled sub-agent budget without a positive limit', async () => {
  const { elements, savedPayloads, confirmCalls, toasts, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    elements['settings-enable-child-budget'].checked = true;
    elements['settings-enable-child-budget'].listeners.change();
    await elements['settings-save-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(savedPayloads.length, 0);
  assert.equal(confirmCalls.length, 0);
  assert.deepEqual(toasts.at(-1), {
    message: 'Enable at least one positive sub-agent budget limit.',
    tone: 'error'
  });
});

test('provider settings test omits runtime child budget controls', async () => {
  const { elements, testedPayloads, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    elements['settings-enable-child-budget'].checked = true;
    elements['settings-enable-child-budget'].listeners.change();
    elements['settings-child-budget-max-turns'].value = '100';
    await elements['settings-test-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(testedPayloads.length, 1);
  assert.equal(Object.prototype.hasOwnProperty.call(testedPayloads[0], 'childBudget'), false);
  assert.equal(Object.prototype.hasOwnProperty.call(testedPayloads[0], 'maxTurnsHard'), false);
  assert.equal(Object.prototype.hasOwnProperty.call(testedPayloads[0], 'maxTurnsSoft'), false);
});

test('settings duration parser accepts canonical human-readable values', () => {
  assert.equal(context.parseSettingsDuration('30m', 'Duration'), 1800);
  assert.equal(context.parseSettingsDuration('2h', 'Duration'), 7200);
  assert.equal(context.parseSettingsDuration('1h30m', 'Duration'), 5400);
  assert.equal(context.parseSettingsDuration('90', 'Duration'), 90);
  assert.equal(context.formatSettingsDuration(1800), '30m');
  assert.equal(context.formatSettingsDuration(7200), '2h');
});

test('settings duration parser rejects malformed values', () => {
  assert.throws(
    () => context.parseSettingsDuration('1h30', 'Duration'),
    /must be a duration/
  );
  assert.throws(
    () => context.parseSettingsDuration('-1h', 'Duration'),
    /must be a duration/
  );
});

test('settings save keeps existing API key mask when cleared field means unchanged', async () => {
  const { elements, savedPayloads, restore } = await renderSettingsHarness({ hasKey: true });
  try {
    elements['settings-apikey'].value = '';
    await elements['settings-save-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(savedPayloads.length, 1);
  assert.equal(savedPayloads[0].apiKey, '');
  assert.equal(elements['settings-apikey'].value, '••••••••••••••••');
  assert.equal(elements['settings-apikey'].dataset.originalHasKey, 'true');
});

test('settings save cancellation uses local dialog and avoids config write', async () => {
  const harness = await renderSettingsHarness({ hasKey: false });
  const { elements, savedPayloads, confirmCalls, toasts, restore } = harness;
  const previousConfirmLocalAction = context.confirmLocalAction;
  context.confirmLocalAction = async (options) => {
    confirmCalls.push(options);
    return false;
  };
  try {
    elements['settings-apikey'].value = 'sk-local-test';
    await elements['settings-save-btn'].listeners.click();
  } finally {
    context.confirmLocalAction = previousConfirmLocalAction;
    restore();
  }

  assert.equal(savedPayloads.length, 0);
  assert.deepEqual(sameRealm(confirmCalls), [
    {
      title: 'Save settings',
      message: 'Save settings and write the entered API key to the local env file?',
      confirmLabel: 'Save',
      tone: 'danger'
    }
  ]);
  assert.deepEqual(toasts.at(-1), {
    message: 'Settings save cancelled.',
    tone: 'info'
  });
});

test('renderSettings ignores stale config responses', async () => {
  const previousNodes = context.nodes;
  const previousDocument = context.document;
  const previousRequestJSON = context.requestJSON;
  const previousShowToast = context.showToast;
  const previousState = context.state;
  const previousSettingsViewSeq = vm.runInContext(`settingsViewState.requestSeq`, context);
  const hadStateSettingsSeq = vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'settingsRequestSeq')`, context);
  const previousStateSettingsSeq = hadStateSettingsSeq ? vm.runInContext(`state.settingsRequestSeq`, context) : undefined;
  const pendingRequests = [];
  const container = fakeRendererElement();
  const elements = {
    'settings-provider': fakeRendererElement({ value: 'openai' }),
    'settings-api-provider': fakeRendererElement(),
    'settings-api-provider-help': fakeRendererElement(),
    'settings-guardrails': fakeRendererElement({ value: 'standard' }),
    'settings-max-turns-soft': fakeRendererElement(),
    'settings-max-turns-hard': fakeRendererElement(),
    'settings-disable-hard-turn-limit': fakeRendererElement(),
    'settings-global-turn-limit-state': fakeRendererElement(),
    'settings-enable-legacy-ui': fakeRendererElement(),
    'settings-legacy-ui-state': fakeRendererElement(),
    'settings-enable-child-budget': fakeRendererElement(),
    'settings-child-budget-active-runtime': fakeRendererElement(),
    'settings-child-budget-elapsed': fakeRendererElement(),
    'settings-child-budget-max-turns': fakeRendererElement(),
    'settings-child-budget-state': fakeRendererElement(),
    'settings-baseurl': fakeRendererElement(),
    'settings-model': fakeRendererElement(),
    'settings-context-window': fakeRendererElement(),
    'settings-reasoning-mode': fakeRendererElement(),
    'settings-reasoning-help': fakeRendererElement(),
    'settings-reasoning-summary': fakeRendererElement(),
    'settings-reasoning-summary-help': fakeRendererElement(),
    'settings-apikey': fakeRendererElement(),
    'settings-test-btn': fakeRendererElement({ innerText: 'Test Settings' }),
    'settings-save-btn': fakeRendererElement({ innerText: 'Save Changes' })
  };
  context.nodes = { views: { settings: container } };
  context.document = {
    getElementById(id) {
      return elements[id] || null;
    },
    createElement() {
      return fakeRendererElement();
    },
    body: {
      contains() {
        return true;
      }
    }
  };
  vm.runInContext(`delete state.settingsRequestSeq; settingsViewState.requestSeq = 0`, context);
  context.requestJSON = (url) => new Promise((resolve, reject) => {
    pendingRequests.push({ url, resolve, reject });
  });
  context.showToast = () => {};

  try {
    const firstRender = context.renderSettings();
    assert.equal(pendingRequests.length, 1);
    assert.equal(pendingRequests[0].url, '/api/config');
    assert.equal(vm.runInContext(`settingsViewState.requestSeq`, context), 1);
    assert.equal(vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'settingsRequestSeq')`, context), false);

    const secondRender = context.renderSettings();
    assert.equal(pendingRequests.length, 2);
    assert.equal(pendingRequests[1].url, '/api/config');
    assert.equal(vm.runInContext(`settingsViewState.requestSeq`, context), 2);

    pendingRequests[1].resolve(settingsConfig({ model: 'gpt-current', hasKey: true }));
    await secondRender;
    assert.equal(elements['settings-model'].value, 'gpt-current');
    assert.equal(elements['settings-apikey'].dataset.originalHasKey, 'true');

    pendingRequests[0].resolve(settingsConfig({ model: 'gpt-stale', hasKey: false }));
    await firstRender;
    assert.equal(elements['settings-model'].value, 'gpt-current');
    assert.equal(elements['settings-apikey'].dataset.originalHasKey, 'true');
    assert.equal(vm.runInContext(`Object.prototype.hasOwnProperty.call(state, 'settingsRequestSeq')`, context), false);
  } finally {
    context.nodes = previousNodes;
    context.document = previousDocument;
    context.requestJSON = previousRequestJSON;
    context.showToast = previousShowToast;
    context.state = previousState;
    if (hadStateSettingsSeq) {
      vm.runInContext(`state.settingsRequestSeq = ${JSON.stringify(previousStateSettingsSeq)}`, context);
    } else {
      vm.runInContext(`delete state.settingsRequestSeq`, context);
    }
    vm.runInContext(`settingsViewState.requestSeq = ${JSON.stringify(previousSettingsViewSeq)}`, context);
  }
});

test('settings test ignores stale completions after settings view refresh', async () => {
  const harness = await renderSettingsHarness({ hasKey: false });
  const { elements, toasts, restore } = harness;
  const pendingTests = [];
  context.testConfig = () => new Promise((resolve) => {
    pendingTests.push(resolve);
  });
  try {
    const testRun = elements['settings-test-btn'].listeners.click();
    assert.equal(pendingTests.length, 1);
    assert.equal(elements['settings-test-btn'].disabled, true);

    vm.runInContext(`settingsViewState.requestSeq += 1`, context);
    pendingTests[0]({
      success: true,
      provider: 'openai',
      model: 'gpt-stale',
      reasoning_mode: 'default'
    });
    await testRun;

    assert.deepEqual(toasts, []);
  } finally {
    restore();
  }
});

test('settings save ignores stale completions after settings view refresh', async () => {
  const harness = await renderSettingsHarness({ hasKey: false });
  const { elements, toasts, restore } = harness;
  const pendingSaves = [];
  context.saveConfig = () => new Promise((resolve) => {
    pendingSaves.push(resolve);
  });
  try {
    const saveRun = elements['settings-save-btn'].listeners.click();
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(pendingSaves.length, 1);
    assert.equal(elements['settings-save-btn'].disabled, true);

    vm.runInContext(`settingsViewState.requestSeq += 1`, context);
    pendingSaves[0]({ success: true });
    await saveRun;

    assert.deepEqual(toasts, []);
  } finally {
    restore();
  }
});

function fakeRendererElement(initial = {}) {
  return {
    value: initial.value || '',
    checked: Boolean(initial.checked),
    disabled: false,
    dataset: {},
    listeners: {},
    innerHTML: '',
    innerText: initial.innerText || '',
    textContent: initial.textContent || '',
    addEventListener(event, callback) {
      this.listeners[event] = callback;
    },
    querySelector() {
      return null;
    },
    querySelectorAll() {
      return [];
    },
    replaceChildren(...children) {
      this.__children = children;
      this.innerHTML = '';
    },
    appendChild(child) {
      if (!Array.isArray(this.__children)) {
        this.__children = [];
      }
      this.__children.push(child);
      return child;
    },
    remove() {
      this.removed = true;
    }
  };
}

function settingsConfig({ model, hasKey, reasoningMode = 'default' }) {
  return {
    default_provider: 'openai',
    guardrails_mode: 'standard',
    max_turns_soft: 24,
    max_turns_hard: -1,
    disable_hard_turn_limit: true,
    legacy_ui_enabled: false,
    child_budget: {
      disabled: true,
      max_active_runtime_sec: 0,
      max_elapsed_sec: 0,
      max_turns_per_attempt: 0
    },
    role_providers: {},
    providers: {
      openai: {
        has_key: hasKey,
        api_provider: '',
        effective_api_provider: 'openai-compatible',
        base_url: 'https://example.invalid/v1',
        model,
        reasoning_mode: reasoningMode,
        reasoning_summary: 'default'
      }
    }
  };
}

async function renderSettingsHarness({ hasKey, reasoningMode = 'default' }) {
  const previousNodes = context.nodes;
  const previousDocument = context.document;
  const previousRequestJSON = context.requestJSON;
  const previousSaveConfig = context.saveConfig;
  const previousTestConfig = context.testConfig;
  const previousShowToast = context.showToast;
  const previousConfirm = context.window.confirm;
  const previousConfirmLocalAction = context.confirmLocalAction;

  const container = fakeRendererElement();
  const elements = {
    'settings-provider': fakeRendererElement({ value: 'openai' }),
    'settings-api-provider': fakeRendererElement(),
    'settings-api-provider-help': fakeRendererElement(),
    'settings-guardrails': fakeRendererElement({ value: 'standard' }),
    'settings-max-turns-soft': fakeRendererElement(),
    'settings-max-turns-hard': fakeRendererElement(),
    'settings-disable-hard-turn-limit': fakeRendererElement(),
    'settings-global-turn-limit-state': fakeRendererElement(),
    'settings-enable-legacy-ui': fakeRendererElement(),
    'settings-legacy-ui-state': fakeRendererElement(),
    'settings-enable-child-budget': fakeRendererElement(),
    'settings-child-budget-active-runtime': fakeRendererElement(),
    'settings-child-budget-elapsed': fakeRendererElement(),
    'settings-child-budget-max-turns': fakeRendererElement(),
    'settings-child-budget-state': fakeRendererElement(),
    'settings-baseurl': fakeRendererElement(),
    'settings-model': fakeRendererElement(),
    'settings-context-window': fakeRendererElement(),
    'settings-reasoning-mode': fakeRendererElement(),
    'settings-reasoning-help': fakeRendererElement(),
    'settings-reasoning-summary': fakeRendererElement(),
    'settings-reasoning-summary-help': fakeRendererElement(),
    'settings-apikey': fakeRendererElement(),
    'settings-test-btn': fakeRendererElement({ innerText: 'Test Settings' }),
    'settings-save-btn': fakeRendererElement({ innerText: 'Save Changes' })
  };
  const savedPayloads = [];
  const testedPayloads = [];
  const toasts = [];

  context.nodes = { views: { settings: container } };
  context.document = {
    getElementById(id) {
      return elements[id] || null;
    },
    createElement() {
      return fakeRendererElement();
    },
    body: {
      contains() {
        return true;
      }
    }
  };
  context.requestJSON = async () => settingsConfig({ model: 'gpt-test', hasKey, reasoningMode });
  context.saveConfig = async (payload) => {
    savedPayloads.push(payload);
    return { success: true };
  };
  context.testConfig = async (payload) => {
    testedPayloads.push(payload);
    return { success: true };
  };
  context.showToast = (message, tone) => {
    toasts.push({ message, tone });
  };
  const confirmCalls = [];
  context.confirmLocalAction = async (options) => {
    confirmCalls.push(options);
    return true;
  };
  context.window.confirm = () => {
    throw new Error('native confirm should not be used');
  };

  await context.renderSettings();

  return {
    container,
    elements,
    savedPayloads,
    testedPayloads,
    toasts,
    confirmCalls,
    restore() {
      context.nodes = previousNodes;
      context.document = previousDocument;
      context.requestJSON = previousRequestJSON;
      context.saveConfig = previousSaveConfig;
      context.testConfig = previousTestConfig;
      context.showToast = previousShowToast;
      context.window.confirm = previousConfirm;
      context.confirmLocalAction = previousConfirmLocalAction;
    }
  };
}

test('settings preserves configured max reasoning for OpenAI-compatible providers', async () => {
  const harness = await renderSettingsHarness({ hasKey: true, reasoningMode: 'max' });
  try {
    assert.equal(harness.elements['settings-reasoning-mode'].value, 'max');
    assert.match(harness.elements['settings-reasoning-mode'].innerHTML, /value="max"/);
    assert.match(harness.container.innerHTML, /data-role-field="reasoning_effort"[\s\S]*value="max"/);
  } finally {
    harness.restore();
  }
});
