import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const utilsSource = readFileSync(new URL('../../internal/webconsole/assets/utils.js', import.meta.url), 'utf8');
const context = {
  console: {
    warn() {}
  }
};

vm.createContext(context);
vm.runInContext(utilsSource, context, { filename: 'utils.js' });

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
