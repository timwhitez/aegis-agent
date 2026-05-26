import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const utilsSource = readFileSync(new URL('../../internal/webconsole/assets/utils.js', import.meta.url), 'utf8');
const sessionViewSource = readFileSync(new URL('../../internal/webconsole/assets/session-view.js', import.meta.url), 'utf8');
const settingsViewSource = readFileSync(new URL('../../internal/webconsole/assets/settings-view.js', import.meta.url), 'utf8');
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
  const state = { selectedQueueJobId: '', stoppingSessionIds: new Set() };
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

test('background notification cards expose queue job detail actions', () => {
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
  function fakeElement(initial = {}) {
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

  const previousNodes = context.nodes;
  const previousDocument = context.document;
  const previousRequestJSON = context.requestJSON;
  const previousSaveConfig = context.saveConfig;
  const previousTestConfig = context.testConfig;
  const previousShowToast = context.showToast;
  const previousConfirm = context.window.confirm;

  const container = fakeElement();
  const elements = {
    'settings-provider': fakeElement({ value: 'openai' }),
    'settings-api-provider': fakeElement(),
    'settings-api-provider-help': fakeElement(),
    'settings-guardrails': fakeElement({ value: 'standard' }),
    'settings-max-turns-hard': fakeElement(),
    'settings-disable-hard-turn-limit': fakeElement(),
    'settings-baseurl': fakeElement(),
    'settings-model': fakeElement(),
    'settings-reasoning-mode': fakeElement(),
    'settings-reasoning-help': fakeElement(),
    'settings-reasoning-summary': fakeElement(),
    'settings-reasoning-summary-help': fakeElement(),
    'settings-apikey': fakeElement(),
    'settings-test-btn': fakeElement({ innerText: 'Test Settings' }),
    'settings-save-btn': fakeElement({ innerText: 'Save Changes' })
  };
  const savedPayloads = [];
  const toasts = [];

  context.nodes = { views: { settings: container } };
  context.document = {
    getElementById(id) {
      return elements[id] || null;
    }
  };
  context.requestJSON = async () => ({
    default_provider: 'openai',
    guardrails_mode: 'standard',
    max_turns_hard: 40,
    disable_hard_turn_limit: false,
    role_providers: {},
    providers: {
      openai: {
        has_key: false,
        api_provider: '',
        effective_api_provider: 'openai-compatible',
        base_url: 'https://example.invalid/v1',
        model: 'gpt-test',
        reasoning_mode: 'default',
        reasoning_summary: 'default'
      }
    }
  });
  context.saveConfig = async (payload) => {
    savedPayloads.push(payload);
    return { success: true };
  };
  context.testConfig = async () => ({ success: true });
  context.showToast = (message, tone) => {
    toasts.push({ message, tone });
  };
  context.window.confirm = () => true;

  try {
    await context.renderSettings();
    await elements['settings-save-btn'].listeners.click();
  } finally {
    context.nodes = previousNodes;
    context.document = previousDocument;
    context.requestJSON = previousRequestJSON;
    context.saveConfig = previousSaveConfig;
    context.testConfig = previousTestConfig;
    context.showToast = previousShowToast;
    context.window.confirm = previousConfirm;
  }

  assert.equal(savedPayloads.length, 1);
  assert.equal(savedPayloads[0].apiKey, '');
  assert.equal(elements['settings-apikey'].value, '');
  assert.equal(elements['settings-apikey'].dataset.originalHasKey, 'false');
  assert.equal(toasts.at(-1)?.tone, 'success');
});
