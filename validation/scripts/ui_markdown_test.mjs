import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import {
  clearMarkdownCache,
  escapeAttr,
  escapeHTML,
  inlineMarkdown,
  isExplicitSameOriginImageSource,
  markdownCacheSize,
  renderMarkdown,
  renderMarkdownCached,
  safeMarkdown,
  sanitizeHref
} from '../../internal/webconsole/ui/src/lib/markdown.js';

const BT = String.fromCharCode(96);
const FENCE3 = BT.repeat(3);
const FENCE4 = BT.repeat(4);
const moduleURL = new URL('../../internal/webconsole/ui/src/lib/markdown.js', import.meta.url);

// Ported from webconsole_utils_test.mjs (safeMarkdown fences, links, cache).

test('language-tagged fences stay inside an open code block', () => {
  const html = renderMarkdown([FENCE3 + 'markdown', FENCE3 + 'go', 'fmt.Println("x")', FENCE3].join('\n'));
  assert.ok(html.includes('<code class="language-markdown">' + FENCE3 + 'go\nfmt.Println(&quot;x&quot;)</code>'), html);
  assert.doesNotMatch(html, /<p>fmt\.Println/);
});

test('nested fences only close when the closing fence is long enough', () => {
  const html = renderMarkdown([FENCE4 + 'markdown', FENCE3 + 'go', 'x', FENCE3, FENCE4].join('\n'));
  assert.ok(html.includes('<code class="language-markdown">' + FENCE3 + 'go\nx\n' + FENCE3 + '</code>'), html);
});

test('links use noopener and same-origin images stay inert by default', () => {
  const html = renderMarkdown('[docs](https://example.test) ![alt](./image.png)');
  assert.match(html, /<a href="https:\/\/example\.test" target="_blank" rel="noopener noreferrer">docs<\/a>/);
  assert.doesNotMatch(html, /<img\b/);
  assert.match(html, /<a class="md-img-link" href="\.\/image\.png" target="_blank" rel="noopener noreferrer">/);
});

test('opt-in inline same-origin images keep the v1 lazy image markup', () => {
  const html = renderMarkdown('[docs](https://example.test) ![alt](./image.png)', { inlineSameOriginImages: true });
  assert.match(html, /rel="noopener noreferrer"/);
  assert.match(html, /class="md-img"/);
  assert.match(html, /loading="lazy"/);
});

test('protocol-relative links and images are rejected', () => {
  const html = renderMarkdown('[docs](//example.test/path) ![alt](//example.test/image.png)');
  assert.doesNotMatch(html, /href="\/\/example\.test/);
  assert.doesNotMatch(html, /src="\/\/example\.test/);
  assert.doesNotMatch(html, /<a\b/);
  assert.doesNotMatch(html, /<img\b/);
  assert.doesNotMatch(html, /class="md-img"/);
  assert.match(html, /<p>docs !\[alt\]\(\/\/example\.test\/image\.png\)<\/p>/);
});

test('cached rendering invalidates by content hash', () => {
  clearMarkdownCache();
  const first = renderMarkdownCached('same-message', '**first**');
  const second = renderMarkdownCached('same-message', '**second**');
  assert.notEqual(first, second);
  assert.match(second, /<strong>second<\/strong>/);
});

// Ported from webconsole_security_test.mjs (inert Markdown images, issue #4/#18).

test('remote Markdown images render as explicit links rather than live image elements', () => {
  const html = renderMarkdown([
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
  const html = renderMarkdown('![escape](/\\attacker.example/pixel)');
  assert.doesNotMatch(html, /<img\b/i);
  assert.doesNotMatch(html, /href=/i);
  assert.match(html, /!\[escape\]\(\/\\attacker\.example\/pixel\)/);
});

test('only explicit same-origin images can be inlined, and only when opted in', () => {
  const source = [
    '![relative](./image.png)',
    '![parent](../image.png)',
    '![root](/assets/image.png)',
    '![fragment](#image)'
  ].join(' ');
  const inline = renderMarkdown(source, { inlineSameOriginImages: true });
  assert.match(inline, /<img class="md-img" src="\.\/image\.png" alt="relative" loading="lazy" \/>/);
  assert.match(inline, /<img class="md-img" src="\.\.\/image\.png" alt="parent" loading="lazy" \/>/);
  assert.match(inline, /<img class="md-img" src="\/assets\/image\.png" alt="root" loading="lazy" \/>/);
  assert.doesNotMatch(inline, /<img[^>]+src="#image"/);
  assert.match(inline, /<span class="md-img-blocked"><span class="md-img-alt" translate="no">fragment<\/span><\/span>/);

  const inert = renderMarkdown(source);
  assert.doesNotMatch(inert, /<img\b/);
  assert.equal((inert.match(/class="md-img-link"/g) || []).length, 3);
  assert.match(inert, /class="md-img-blocked"/);
});

test('unsafe image schemes remain inert text', () => {
  const html = renderMarkdown('![bad](javascript:alert(1)) ![data](data:text/html,bad)');
  assert.doesNotMatch(html, /<(?:img|a)\b/);
  assert.match(html, /!\[bad\]\(javascript:alert\(1\)\)/);
  assert.match(html, /!\[data\]\(data:text\/html,bad\)/);
});

test('cached rendering applies the remote-image policy before caching', () => {
  const first = renderMarkdownCached('remote-message', '![remote](https://example.invalid/x.png)');
  const second = renderMarkdownCached('remote-message', '![remote](https://example.invalid/x.png)');
  assert.equal(first, second);
  assert.doesNotMatch(first, /<img\b/);
  assert.match(first, /class="md-img-link"/);
});

// New sanitizer and GFM coverage.

test('raw HTML from input is always escaped', () => {
  const html = renderMarkdown('<script>alert(1)</script>\n<img src=x onerror=alert(1)>\n**<b>bold</b>**');
  assert.doesNotMatch(html, /<script|<img|<b>/i);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.match(html, /<strong>&lt;b&gt;bold&lt;\/b&gt;<\/strong>/);
});

test('unsafe hrefs drop to plain labels and attribute breakouts are escaped', () => {
  const html = renderMarkdown('[a](javascript:alert(1)) [b](data:text/html,x) [c](vbscript:x) [d](https://x.test/" onmouseover="alert(1))');
  assert.doesNotMatch(html, /href="(?:javascript|data|vbscript):/i);
  assert.doesNotMatch(html, /" onmouseover="/);
  assert.match(html, /href="https:\/\/x\.test\/&quot; onmouseover=&quot;alert\(1"/);
  // The first ')' closes the link target (v1 parsing), leaving ')' as text.
  assert.match(html, /<p>a\) b c <a /);
});

test('sanitizeHref allows only http(s), mailto and explicit relative paths', () => {
  assert.equal(sanitizeHref('https://a.test/x?y=1'), 'https://a.test/x?y=1');
  assert.equal(sanitizeHref('  mailto:a@b.test '), 'mailto:a@b.test');
  assert.equal(sanitizeHref('./a/b.md'), './a/b.md');
  assert.equal(sanitizeHref('#section'), '#section');
  assert.equal(sanitizeHref('/api/x'), '/api/x');
  for (const bad of ['//evil.test', 'javascript:alert(1)', 'JaVaScRiPt:x', 'data:text/html,x', 'file:///etc/passwd', '/\\evil', 'relative/path', '', null]) {
    assert.equal(sanitizeHref(bad), '', String(bad));
  }
  assert.equal(isExplicitSameOriginImageSource('/a.png'), true);
  assert.equal(isExplicitSameOriginImageSource('https://a/b.png'), false);
  assert.equal(isExplicitSameOriginImageSource('//a/b.png'), false);
  assert.equal(isExplicitSameOriginImageSource('#x'), false);
});

test('escape helpers cover all HTML-significant characters', () => {
  assert.equal(escapeHTML('<a href="x">\'&'), '&lt;a href=&quot;x&quot;&gt;&#39;&amp;');
  assert.equal(escapeAttr('"'), '&quot;');
  assert.equal(escapeHTML(0), '0');
  assert.equal(escapeHTML(null), '');
});

test('code spans and fences never interpret Markdown or HTML', () => {
  const html = renderMarkdown('Use ' + BT + '**not bold** <b>' + BT + ' and **bold**');
  assert.match(html, /<code>\*\*not bold\*\* &lt;b&gt;<\/code>/);
  assert.match(html, /<strong>bold<\/strong>/);
  const block = renderMarkdown([FENCE3 + 'html', '<script>x</script> [l](javascript:x)', FENCE3].join('\n'));
  assert.match(block, /<pre><code class="language-html">&lt;script&gt;x&lt;\/script&gt; \[l\]\(javascript:x\)<\/code><\/pre>/);
});

test('fence info strings cannot inject attributes', () => {
  const html = renderMarkdown([FENCE3 + 'js"onload=alert(1)', 'x', FENCE3].join('\n'));
  assert.match(html, /<pre><code>x<\/code><\/pre>/);
  assert.doesNotMatch(html, /onload/);
});

test('unclosed fences keep their content', () => {
  const html = renderMarkdown([FENCE3 + 'sh', 'echo one', 'echo two'].join('\n'));
  assert.match(html, /<pre><code class="language-sh">echo one\necho two<\/code><\/pre>/);
});

test('GFM tables render escaped cells with alignment classes and no style attributes', () => {
  const html = renderMarkdown(['| Name | Value |', '|:-----|------:|', '| <b>x</b> | ' + BT + 'a|b' + BT + ' |', '| esc \\| pipe | 2 |'].join('\n'));
  assert.match(html, /<table><thead><tr><th class="md-align-left">Name<\/th><th class="md-align-right">Value<\/th><\/tr><\/thead>/);
  assert.match(html, /<td class="md-align-left">&lt;b&gt;x&lt;\/b&gt;<\/td>/);
  assert.match(html, /<td class="md-align-left">esc \| pipe<\/td>/);
  assert.doesNotMatch(html, /style=/);
});

test('lists, task lists, headings, quotes and rules', () => {
  const html = renderMarkdown([
    '## Plan',
    '- [ ] open item',
    '- [x] done item',
    '',
    '3. third',
    '4. fourth',
    '',
    '> quoted **text**',
    '---',
    'line one',
    'line two'
  ].join('\n'));
  assert.match(html, /<h2>Plan<\/h2>/);
  assert.match(html, /<li class="task-list-item"><input type="checkbox" disabled \/> open item<\/li>/);
  assert.match(html, /<li class="task-list-item"><input type="checkbox" disabled checked \/> done item<\/li>/);
  assert.match(html, /<ol start="3"><li>third<\/li><li>fourth<\/li><\/ol>/);
  assert.match(html, /<blockquote><p>quoted <strong>text<\/strong><\/p><\/blockquote>/);
  assert.match(html, /<hr>/);
  assert.match(html, /<p>line one<br>line two<\/p>/);
});

test('bare URLs autolink through sanitizeHref', () => {
  const html = renderMarkdown('See https://example.test/a_(b), then stop.');
  assert.match(html, /<a href="https:\/\/example\.test\/a_\(b\)" target="_blank" rel="noopener noreferrer">https:\/\/example\.test\/a_\(b\)<\/a>, then stop\./);
});

test('placeholder markers in input cannot duplicate generated tokens', () => {
  const html = inlineMarkdown('\u0001T0\u0001 [x](https://a.test)');
  assert.equal((html.match(/<a /g) || []).length, 1);
  assert.doesNotMatch(html, /\u0001/);
});

test('translator only supplies image labels and is part of the cache key', () => {
  clearMarkdownCache();
  const source = '![chart](https://example.test/c.png) ![x](mailto:a@b.test)';
  const en = renderMarkdown(source, { t: (key) => ({ 'markdown.openImage': 'Open image', 'markdown.blockedImage': 'Blocked image' }[key] || key) });
  const zh = renderMarkdown(source, { t: (key) => ({ 'markdown.openImage': '打开图片', 'markdown.blockedImage': '已阻止图片' }[key] || key) });
  assert.match(en, /<span class="md-img-label">Open image<\/span> <span class="md-img-alt" translate="no">chart<\/span>/);
  assert.match(en, /<span class="md-img-label">Blocked image<\/span>/);
  assert.match(zh, /<span class="md-img-label">打开图片<\/span>/);
  assert.ok(markdownCacheSize() >= 2);
  const untranslated = renderMarkdown(source, { t: (key) => key });
  assert.doesNotMatch(untranslated, /md-img-label/);
});

test('empty and odd input never throw', () => {
  assert.equal(renderMarkdown(''), '<p></p>');
  assert.equal(renderMarkdown(null), '<p></p>');
  for (const value of ['[', '![', '[]()', '**', BT, '|', '> ', '1.', '- ', '#'.repeat(9)]) {
    assert.equal(typeof safeMarkdown(value), 'string');
  }
});

test('the renderer is a pure module without DOM or network globals', () => {
  const source = readFileSync(moduleURL, 'utf8');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage|innerHTML)\b/);
});
