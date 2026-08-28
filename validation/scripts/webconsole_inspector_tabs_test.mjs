import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const v2Styles = readFileSync(new URL('../../internal/webconsole/assets-v2/aegis.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');

test('Web Console v2 keeps all inspector tabs in one horizontally scrollable row', () => {
  assert.match(v2Styles, /html\[data-ui="aegis-v2"\] \.inspector-tabs \{ display: flex; flex-wrap: nowrap; overflow-x: auto; overscroll-behavior-inline: contain; scrollbar-width: thin; \}/);
  assert.match(v2Styles, /html\[data-ui="aegis-v2"\] \.inspector-tab \{ flex: 1 0 auto; min-width: 44px; min-height: 44px; white-space: nowrap; \}/);
  assert.match(v2Styles, /html\[data-ui="aegis-v2"\] \.inspector-tab\.active \{ background: var\(--accent-soft\); color: #28310f; box-shadow: inset 0 -2px var\(--accent\); \}/);
});
