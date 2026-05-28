import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const utilsSource = readFileSync(new URL('../../internal/webconsole/assets/utils.js', import.meta.url), 'utf8');
const sessionViewSource = readFileSync(new URL('../../internal/webconsole/assets/session-view.js', import.meta.url), 'utf8');
const settingsViewSource = readFileSync(new URL('../../internal/webconsole/assets/settings-view.js', import.meta.url), 'utf8');
const workspaceViewSource = readFileSync(new URL('../../internal/webconsole/assets/workspace-view.js', import.meta.url), 'utf8');
const appSource = readFileSync(new URL('../../internal/webconsole/assets/app.js', import.meta.url), 'utf8');
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
  const state = { selectedQueueJobId: '' };
  const stopActionViewState = { sessionIds: new Set() };
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

function createAppHarnessContext() {
  const pendingRequests = [];
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
      }
    },
    document: {
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
      addEventListener() {},
      body: {
        contains() {
          return true;
        },
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
    renderCurrentSession() {},
    requestJSON(url, payload) {
      return new Promise((resolve, reject) => {
        pendingRequests.push({ url, payload, resolve, reject });
      });
    }
  };
  vm.createContext(appContext);
  vm.runInContext(utilsSource, appContext, { filename: 'utils.js' });
  vm.runInContext(appSource, appContext, { filename: 'app.js' });
  appContext.pendingRequests = pendingRequests;
  return appContext;
}

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
    'pending',
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
      'layoutObserver'
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
    state.currentView = 'chat';
    state.sessionId = 'session_stop_pending';
    state.sessionBacked = true;
    state.isGenerating = true;
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
    isStopping: isStoppingSession('session_stop_pending')
  })`, appContext);
  assert.equal(finalState.stateHasStoppingSessionIds, false);
  assert.equal(finalState.isStopping, false);
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
      },
      workspacePath: '',
      selectedTreePath: '',
      fileTree: []
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
    showToast() {}
  };
  vm.createContext(workspaceContext);
  vm.runInContext(utilsSource, workspaceContext, { filename: 'utils.js' });
  vm.runInContext(workspaceViewSource, workspaceContext, { filename: 'workspace-view.js' });
  vm.runInContext(`delete state.workspaceRequestSeq; workspaceViewState.requestSeq = 0;`, workspaceContext);
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
  const mainButton = fakeControl('Upload .zip Skill');
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
    assert.equal(control.textContent, 'Uploading...');
    assert.equal(control.getAttribute('aria-busy'), 'true');
  }
  assert.equal(uploadInput.disabled, true);

  context.setSkillUploadPending(root, false);

  assert.equal(mainButton.disabled, false);
  assert.equal(mainButton.textContent, 'Upload .zip Skill');
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

test('background notification cards expose queue job and child session actions', () => {
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

  assert.match(full, /data-open-job="job_notification_open"/);
  assert.match(full, />Open job<\/button>/);
  assert.match(full, /data-open-session="child_notification_open"/);
  assert.match(preview, /data-open-job="job_notification_open"/);
  assert.match(preview, />Open job<\/button>/);
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
    state.selectedQueueJobId = 'job_failed_handoff';
    state.selectedQueueJobDetail = ${JSON.stringify(job)};
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
    state.selectedQueueJobId = 'job_slow_a';
    state.selectedQueueJobDetail = null;
    refreshSelectedQueueJobDetail();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /job_slow_a/);

  await vm.runInContext(`
    state.selectedQueueJobId = 'job_fast_b';
    refreshSelectedQueueJobDetail([{ id: 'job_fast_b', prompt: 'fast selected' }]);
  `, appContext);

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.selectedQueueJobId,
    detailID: state.selectedQueueJobDetail?.id,
    prompt: state.selectedQueueJobDetail?.prompt
  })`, appContext)), {
    selected: 'job_fast_b',
    detailID: 'job_fast_b',
    prompt: 'fast selected'
  });

  appContext.pendingRequests[0].resolve({ id: 'job_slow_a', prompt: 'stale slow' });
  await slowRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.selectedQueueJobId,
    detailID: state.selectedQueueJobDetail?.id,
    prompt: state.selectedQueueJobDetail?.prompt
  })`, appContext)), {
    selected: 'job_fast_b',
    detailID: 'job_fast_b',
    prompt: 'fast selected'
  });
});

test('selected current-session queue job detail keeps chat polling active while job runs', () => {
  const appContext = createAppHarnessContext();

  const result = vm.runInContext(`(() => {
    state.currentView = 'chat';
    state.visibilityHidden = false;
    state.isConnected = true;
    state.isGenerating = false;
    state.sessionId = 'parent_polling';
    state.sessionBacked = true;
    state.overview = { sessions: [] };
    state.sessionDetail = {
      metadata: { id: 'parent_polling' },
      state: { status: 'awaiting_input' },
      children: { sessions: [], jobs: [] },
      messages: []
    };
    state.selectedQueueJobId = 'job_outside_window';
    state.selectedQueueJobDetail = {
      id: 'job_outside_window',
      status: 'running',
      parent_session_id: 'parent_polling'
    };
    return {
      runLoop: shouldRunPollingLoop(),
      overview: shouldPollChatOverview(),
      current: shouldPollCurrentSession(),
      interval: pollingIntervalForState()
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    runLoop: true,
    overview: true,
    current: true,
    interval: 1600
  });
});

test('selected queue job detail from another parent does not keep chat polling active', () => {
  const appContext = createAppHarnessContext();

  const result = vm.runInContext(`(() => {
    state.currentView = 'chat';
    state.visibilityHidden = false;
    state.isConnected = true;
    state.isGenerating = false;
    state.sessionId = 'parent_current';
    state.sessionBacked = true;
    state.overview = { sessions: [] };
    state.sessionDetail = {
      metadata: { id: 'parent_current' },
      state: { status: 'awaiting_input' },
      children: { sessions: [], jobs: [] },
      messages: []
    };
    state.selectedQueueJobId = 'job_other_parent';
    state.selectedQueueJobDetail = {
      id: 'job_other_parent',
      status: 'running',
      parent_session_id: 'parent_other'
    };
    return {
      runLoop: shouldRunPollingLoop(),
      overview: shouldPollChatOverview(),
      current: shouldPollCurrentSession(),
      interval: pollingIntervalForState()
    };
  })()`, appContext);

  assert.deepEqual(sameRealm(result), {
    runLoop: false,
    overview: false,
    current: false,
    interval: 5000
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
    state.inspectorTab = 'agents';
    state.selectedQueueJobId = 'job_slow_queue_a';
    state.selectedQueueJobDetail = null;
    state.liveEvents = [];
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
    state.selectedQueueJobId = '';
    state.selectedQueueJobDetail = null;
    state.sessionDetail = {
      metadata: { id: 'session_fast_queue_b' },
      state: { status: 'completed' },
      messages: []
    };
    state.isGenerating = false;
    state.liveActivity = { title: 'Fast selected', copy: '', tone: 'neutral' };
  `, appContext);

  appContext.pendingRequests[1].resolve({ id: 'job_slow_queue_a', prompt: 'stale queue detail' });
  await slowRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    detailID: state.sessionDetail?.metadata?.id,
    status: state.sessionDetail?.state?.status,
    generating: state.isGenerating,
    activityTitle: state.liveActivity?.title,
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
    state.inspectorTab = 'agents';
    state.selectedQueueJobId = 'job_same_enrich';
    state.selectedQueueJobDetail = null;
    state.liveEvents = [];
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
  assert.equal(vm.runInContext(`state.needsSessionRefresh`, appContext), true);

  appContext.pendingRequests[1].resolve({ id: 'job_same_enrich', prompt: 'stale enriched detail' });
  await firstRefresh;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    selectedJob: state.selectedQueueJobId,
    selectedJobDetail: state.selectedQueueJobDetail,
    renderCount: state.renderCount || 0
  })`, appContext)), {
    selected: 'session_same_enrich',
    selectedJob: 'job_same_enrich',
    selectedJobDetail: null,
    renderCount: 0
  });
});

test('refreshCurrentSession skips stale same-session detail when a newer refresh is queued', async () => {
  const appContext = createAppHarnessContext();
  vm.runInContext(`
    window.setTimeout = function(callback) {
      callback();
      return 0;
    };
  `, appContext);

  const firstRefresh = vm.runInContext(`
    state.sessionId = 'session_same_refresh';
    state.sessionBacked = true;
    state.isGenerating = true;
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
    status: state.sessionDetail?.state?.status,
    generating: state.isGenerating,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id)
  })`, appContext)), {
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
    status: state.sessionDetail?.state?.status,
    generating: state.isGenerating,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id)
  })`, appContext)), {
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
    state.hasMoreMessages = true;
    state.oldestMessageId = 'm8';
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
    state.hasMoreMessages = false;
    state.oldestMessageId = 'b1';
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
    selected: state.sessionId,
    detailID: state.sessionDetail?.metadata?.id,
    messageIDs: maybeArray(state.sessionDetail?.messages).map((message) => message.id),
    hasMore: state.hasMoreMessages,
    oldest: state.oldestMessageId,
    loadingEarlier: state.loadingEarlier
  })`, appContext)), {
    stateHasMessagePageRequestSeq: false,
    selected: 'session_fast_b',
    detailID: 'session_fast_b',
    messageIDs: ['b1'],
    hasMore: false,
    oldest: 'b1',
    loadingEarlier: false
  });
});

test('Plan Mode approval does not mark a newly selected session as generating', async () => {
  const appContext = createAppHarnessContext();
  installPlanModeAPITestWrappers(appContext);
  appContext.planApproveButton = fakeActionButton({ 'data-plan-action': 'approve' });

  const approval = vm.runInContext(`
    state.sessionId = 'session_plan_slow_a';
    state.sessionBacked = true;
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded plan A', copy: '', tone: 'neutral' };
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    activityTitle: state.liveActivity.title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
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
    state.planInputSelections = {
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

test('running-session steer completion does not mark a newly selected session as queued', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_steer_slow_a';
    state.sessionBacked = true;
    state.isGenerating = true;
    state.nextSendInterrupt = true;
    state.liveActivity = { title: 'Running A', copy: '', tone: 'live' };
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
    state.isGenerating = false;
    state.nextSendInterrupt = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    interruptArmed: state.nextSendInterrupt,
    activityTitle: state.liveActivity.title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    interruptArmed: false,
    activityTitle: 'Loaded session B'
  });
});

test('continue completion does not mark a newly selected session as generating', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_continue_slow_a';
    state.sessionBacked = true;
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded A', copy: '', tone: 'neutral' };
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    activityTitle: state.liveActivity.title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('start completion does not replace a session selected while launch is pending', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    state.isGenerating = false;
    state.launchInFlight = false;
    state.liveActivity = { title: 'Ready', copy: '', tone: 'neutral' };
    state.sessionDetail = null;
    nodes.chatInput.value = 'start a slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.isGenerating = false;
    state.launchInFlight = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    launchInFlight: state.launchInFlight,
    activityTitle: state.liveActivity.title
  })`, appContext)), {
    selected: 'session_fast_b',
    backed: true,
    generating: false,
    launchInFlight: false,
    activityTitle: 'Loaded session B'
  });
});

test('new session start includes role-aware composer fields', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    state.isGenerating = false;
    state.launchInFlight = false;
    state.liveActivity = { title: 'Ready', copy: '', tone: 'neutral' };
    state.sessionDetail = null;
    nodes.chatInput.value = 'start an evaluator session';
    nodes.agentNameInput.value = 'reviewer';
    nodes.agentRoleSelect.value = 'evaluator';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');
  assert.equal(appContext.pendingRequests[0].payload.payload.agentName, 'reviewer');
  assert.equal(appContext.pendingRequests[0].payload.payload.agentRole, 'evaluator');
  appContext.pendingRequests[0].resolve({ session_id: 'session_role_start', status: 'accepted' });
  await send;
});

test('start completion does not clear a newer pending launch', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const firstSend = vm.runInContext(`
    selectedWorkspaceWorkdir = function() { return ''; };
    state.sessionId = '0xA11CE0';
    state.sessionBacked = false;
    state.isGenerating = false;
    state.launchInFlight = false;
    state.liveActivity = { title: 'Ready', copy: '', tone: 'neutral' };
    state.sessionDetail = null;
    nodes.chatInput.value = 'start first slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  vm.runInContext(`
    state.sessionId = '0xB22CE0';
    state.sessionBacked = false;
    state.isGenerating = true;
    state.launchInFlight = true;
    state.liveActivity = { title: 'Launching second session', copy: '', tone: 'live' };
    state.sessionDetail = null;
  `, appContext);

  appContext.pendingRequests[0].resolve({ session_id: 'session_created_slow_a', status: 'accepted' });
  await firstSend;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    generating: state.isGenerating,
    launchInFlight: state.launchInFlight,
    activityTitle: state.liveActivity.title
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
    state.isGenerating = true;
    state.launchInFlight = true;
    state.liveActivity = { title: 'Launching session', copy: '', tone: 'live' };
    openSession('session_fast_b', { switchToChat: false });
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.match(appContext.pendingRequests[0].url, /session_fast_b/);
  assert.deepEqual(sameRealm(vm.runInContext(`({
    selected: state.sessionId,
    backed: state.sessionBacked,
    generating: state.isGenerating,
    launchInFlight: state.launchInFlight,
    activityTitle: state.liveActivity.title
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
    state.isGenerating = false;
    state.launchInFlight = false;
    state.liveActivity = { title: 'Ready', copy: '', tone: 'neutral' };
    state.sessionDetail = null;
    nodes.chatInput.value = 'start a slow session';
    sendMessage();
  `, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/sessions/start');

  vm.runInContext(`
    state.sessionId = 'session_fast_b';
    state.sessionBacked = true;
    state.isGenerating = true;
    state.launchInFlight = false;
    state.liveActivity = { title: 'Session B running', copy: '', tone: 'live' };
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
    generating: state.isGenerating,
    launchInFlight: state.launchInFlight,
    activityTitle: state.liveActivity.title
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded A', copy: '', tone: 'neutral' };
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    stateHasHistoryRequestSeq: Object.prototype.hasOwnProperty.call(state, 'historyRequestSeq'),
    page: state.historyPage,
    historyIDs: maybeArray(state.historyData?.items).map((item) => item.id)
  })`, appContext)), {
    stateHasHistoryRequestSeq: false,
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
    page: state.historyPage,
    dataPage: state.historyData?.page,
    historyIDs: maybeArray(state.historyData?.items).map((item) => item.id)
  })`, appContext)), {
    page: 3,
    dataPage: 3,
    historyIDs: ['session_page_3_current']
  });
});

test('refreshOverview queues the latest refresh and ignores stale in-flight overview', async () => {
  const appContext = createAppHarnessContext();

  const firstRefresh = vm.runInContext(`refreshOverview()`, appContext);

  assert.equal(appContext.pendingRequests.length, 1);
  assert.equal(appContext.pendingRequests[0].url, '/api/overview');

  await vm.runInContext(`refreshOverview()`, appContext);

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
    refreshing: state.refreshingOverview
  })`, appContext)), {
    overviewIDs: ['session_current_overview'],
    queued: 0,
    refreshing: false
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
    skillIDs: maybeArray(state.skills).map((skill) => skill.id),
    grid: nodes.skillsGrid.innerHTML
  })`, appContext));
  assert.equal(currentState.stateHasSkillsRequestSeq, false);
  assert.deepEqual(currentState.skillIDs, ['skill_current']);
  assert.match(currentState.grid, /Current Skill/);

  appContext.pendingRequests[0].resolve([
    { id: 'skill_stale', name: 'Stale Skill', author: 'agent', description: 'stale catalog', installed: true }
  ]);
  await firstLoad;

  const finalState = sameRealm(vm.runInContext(`({
    skillIDs: maybeArray(state.skills).map((skill) => skill.id),
    grid: nodes.skillsGrid.innerHTML
  })`, appContext));
  assert.deepEqual(finalState.skillIDs, ['skill_current']);
  assert.match(finalState.grid, /Current Skill/);
  assert.doesNotMatch(finalState.grid, /Stale Skill/);
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

test('plan revision completion does not mark a newly selected session as generating', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const send = vm.runInContext(`
    state.sessionId = 'session_revision_slow_a';
    state.sessionBacked = true;
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded plan A', copy: '', tone: 'neutral' };
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    activityTitle: state.liveActivity.title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('interrupt completion does not update a newly selected session', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const interrupt = vm.runInContext(`
    state.sessionId = 'session_interrupt_slow_a';
    state.sessionBacked = true;
    state.isGenerating = true;
    state.liveActivity = { title: 'Running A', copy: '', tone: 'live' };
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    activityTitle: state.liveActivity.title
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B'
  });
});

test('stop completion does not update a newly selected session', async () => {
  const appContext = createAppHarnessContext();
  installChatActionAPITestWrappers(appContext);

  const stop = vm.runInContext(`
    state.sessionId = 'session_stop_slow_a';
    state.sessionBacked = true;
    state.isGenerating = true;
    state.liveActivity = { title: 'Running A', copy: '', tone: 'live' };
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
    state.isGenerating = false;
    state.liveActivity = { title: 'Loaded session B', copy: '', tone: 'neutral' };
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
    generating: state.isGenerating,
    activityTitle: state.liveActivity.title,
    stoppingA: isStoppingSession('session_stop_slow_a')
  })`, appContext)), {
    selected: 'session_fast_b',
    generating: false,
    activityTitle: 'Loaded session B',
    stoppingA: false
  });
});

test('top-level stop and interrupt controls hide for running sessions not owned by this web process', () => {
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
    state.isGenerating = true;
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
    stopVisible: false,
    interruptVisible: false,
    stopDisabled: true,
    interruptDisabled: true
  });
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
    state.currentView = 'chat';
    state.sessionId = 'parent_session_stop';
    state.sessionBacked = true;
    state.isGenerating = false;
    state.liveActivity = { title: 'Parent loaded', copy: '', tone: 'neutral' };
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
    activityTitle: state.liveActivity.title,
    stoppingChild: isStoppingSession('child_session_stop'),
    refreshCalls,
    toastCalls
  })`, appContext)), {
    selected: 'parent_session_stop',
    activityTitle: 'Parent loaded',
    stoppingChild: false,
    refreshCalls: [
      { kind: 'session', delay: 120, selected: 'parent_session_stop' },
      { kind: 'overview', delay: 180, selected: 'parent_session_stop' }
    ],
    toastCalls: [
      { message: 'Stop requested.', tone: 'success' }
    ]
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
    path: state.workspacePath,
    stateHasRequestSeq: Object.prototype.hasOwnProperty.call(state, 'workspaceRequestSeq'),
    renderedNames: state.renderedTree.map((node) => node.name),
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    path: 'fast',
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
    path: state.workspacePath,
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
    preview: state.workspaceFilePreview,
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
    preview: state.workspaceFilePreview
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
    }
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
  assert.equal(vm.runInContext(`state.selectedTreePath`, workspaceContext), 'fast.txt');

  workspaceContext.pendingRequests[0].resolve({ content: 'stale file' });
  await slowClick;

  assert.deepEqual(sameRealm(vm.runInContext(`({
    selectedTreePath: state.selectedTreePath,
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    selectedTreePath: 'fast.txt',
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
    selectedTreePath: state.selectedTreePath,
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText,
    secondFocused: Boolean(treeButtons[1].focused),
    parentFocused: Boolean(treeButtons[0].focused),
    events: keyboardEvents
  })`, workspaceContext)), {
    selectedTreePath: 'src/main.go',
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
    path: state.workspacePath,
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
    path: state.workspacePath,
    filename: nodes.editorFilename.innerText,
    content: nodes.editorContent.innerText
  })`, workspaceContext)), {
    path: '',
    filename: 'new.txt',
    content: 'new file body'
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

test('settings save keeps empty API key fields unmasked after success', async () => {
  const { elements, savedPayloads, toasts, restore } = await renderSettingsHarness({ hasKey: false });
  try {
    await elements['settings-save-btn'].listeners.click();
  } finally {
    restore();
  }

  assert.equal(savedPayloads.length, 1);
  assert.equal(savedPayloads[0].apiKey, '');
  assert.equal(elements['settings-apikey'].value, '');
  assert.equal(elements['settings-apikey'].dataset.originalHasKey, 'false');
  assert.equal(toasts.at(-1)?.tone, 'success');
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
    'settings-max-turns-hard': fakeRendererElement(),
    'settings-disable-hard-turn-limit': fakeRendererElement(),
    'settings-baseurl': fakeRendererElement(),
    'settings-model': fakeRendererElement(),
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
    }
  };
}

function settingsConfig({ model, hasKey }) {
  return {
    default_provider: 'openai',
    guardrails_mode: 'standard',
    max_turns_hard: 40,
    disable_hard_turn_limit: false,
    role_providers: {},
    providers: {
      openai: {
        has_key: hasKey,
        api_provider: '',
        effective_api_provider: 'openai-compatible',
        base_url: 'https://example.invalid/v1',
        model,
        reasoning_mode: 'default',
        reasoning_summary: 'default'
      }
    }
  };
}

async function renderSettingsHarness({ hasKey }) {
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
    'settings-max-turns-hard': fakeRendererElement(),
    'settings-disable-hard-turn-limit': fakeRendererElement(),
    'settings-baseurl': fakeRendererElement(),
    'settings-model': fakeRendererElement(),
    'settings-reasoning-mode': fakeRendererElement(),
    'settings-reasoning-help': fakeRendererElement(),
    'settings-reasoning-summary': fakeRendererElement(),
    'settings-reasoning-summary-help': fakeRendererElement(),
    'settings-apikey': fakeRendererElement(),
    'settings-test-btn': fakeRendererElement({ innerText: 'Test Settings' }),
    'settings-save-btn': fakeRendererElement({ innerText: 'Save Changes' })
  };
  const savedPayloads = [];
  const toasts = [];

  context.nodes = { views: { settings: container } };
  context.document = {
    getElementById(id) {
      return elements[id] || null;
    }
  };
  context.requestJSON = async () => settingsConfig({ model: 'gpt-test', hasKey });
  context.saveConfig = async (payload) => {
    savedPayloads.push(payload);
    return { success: true };
  };
  context.testConfig = async () => ({ success: true });
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
    elements,
    savedPayloads,
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
