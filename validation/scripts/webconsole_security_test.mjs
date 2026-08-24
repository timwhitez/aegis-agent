import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const utilsSource = readFileSync(new URL('../../internal/webconsole/assets/utils.js', import.meta.url), 'utf8');
const securitySource = readFileSync(new URL('../../internal/webconsole/assets/markdown-security.js', import.meta.url), 'utf8');
const indexSource = readFileSync(new URL('../../internal/webconsole/assets/index.html', import.meta.url), 'utf8');

function markdownContext() {
  const context = {
    console: {
      warn() {}
    }
  };
  vm.createContext(context);
  vm.runInContext(utilsSource, context, { filename: 'utils.js' });
  vm.runInContext(securitySource, context, { filename: 'markdown-security.js' });
  return context;
}

test('remote Markdown images render as explicit links rather than live image elements', () => {
  const context = markdownContext();
  const html = context.safeMarkdown([
    '![external](https://example.invalid/tracker.png?view=1&session=2)',
    '![localhost](http://127.0.0.1:8080/admin/action)',
    '![private](http://192.168.1.1/probe)'
  ].join(' '));

  assert.doesNotMatch(html, /<img[^>]+https?:/i);
  assert.match(html, /class="md-img-link" href="https:\/\/example\.invalid\/tracker\.png\?view=1&amp;session=2"/);
  assert.match(html, /class="md-img-link" href="http:\/\/127\.0\.0\.1:8080\/admin\/action"/);
  assert.match(html, /class="md-img-link" href="http:\/\/192\.168\.1\.1\/probe"/);
  assert.match(html, /target="_blank" rel="noopener noreferrer"/);
});

test('backslash-normalized network paths never remain live images', () => {
  const context = markdownContext();
  const html = context.safeMarkdown('![escape](/\\attacker.example/pixel)');

  assert.doesNotMatch(html, /<img\b/i);
  assert.doesNotMatch(html, /href=/i);
  assert.match(html, /!\[escape\]\(\/\\attacker\.example\/pixel\)/);
});

test('only explicit same-origin Markdown images remain inline', () => {
  const context = markdownContext();
  const html = context.safeMarkdown([
    '![relative](./image.png)',
    '![parent](../image.png)',
    '![root](/assets/image.png)',
    '![fragment](#image)'
  ].join(' '));

  assert.match(html, /<img class="md-img" src="\.\/image\.png" alt="relative" loading="lazy" \/>/);
  assert.match(html, /<img class="md-img" src="\.\.\/image\.png" alt="parent" loading="lazy" \/>/);
  assert.match(html, /<img class="md-img" src="\/assets\/image\.png" alt="root" loading="lazy" \/>/);
  assert.doesNotMatch(html, /<img[^>]+src="#image"/);
  assert.match(html, /Blocked image: fragment/);
});

test('unsafe image schemes remain inert text', () => {
  const context = markdownContext();
  const html = context.safeMarkdown('![bad](javascript:alert(1)) ![data](data:text/html,bad)');

  assert.doesNotMatch(html, /<(?:img|a)\b/);
  assert.match(html, /!\[bad\]\(javascript:alert\(1\)\)/);
  assert.match(html, /!\[data\]\(data:text\/html,bad\)/);
});

test('cached Markdown rendering applies the remote-image policy before caching', () => {
  const context = markdownContext();
  const first = context.renderMarkdownCached('remote-message', '![remote](https://example.invalid/x.png)');
  const second = context.renderMarkdownCached('remote-message', '![remote](https://example.invalid/x.png)');

  assert.equal(first, second);
  assert.doesNotMatch(first, /<img\b/);
  assert.match(first, /class="md-img-link"/);
});

test('the image policy loads immediately after utils and before message renderers', () => {
  const utilsIndex = indexSource.indexOf('<script src="utils.js"></script>');
  const policyIndex = indexSource.indexOf('<script src="markdown-security.js"></script>');
  const sessionViewIndex = indexSource.indexOf('<script src="session-view.js"></script>');

  assert.ok(utilsIndex >= 0, 'utils.js script missing');
  assert.ok(policyIndex > utilsIndex, 'image policy must load after utils.js');
  assert.ok(sessionViewIndex > policyIndex, 'image policy must load before session renderer');
});
