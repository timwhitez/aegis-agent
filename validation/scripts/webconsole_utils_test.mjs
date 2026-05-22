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
