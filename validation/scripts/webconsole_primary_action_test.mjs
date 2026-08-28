import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const v2Styles = readFileSync(new URL('../../internal/webconsole/assets-v2/aegis.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');

test('Web Console v2 uses one primary-action palette at overriding specificity', () => {
  assert.match(v2Styles, /html\[data-ui="aegis-v2"\] \.skill-btn\.install, html\[data-ui="aegis-v2"\] \.plan-action-btn\.primary, html\[data-ui="aegis-v2"\] \.settings-save-btn \{ background: var\(--accent-fill\); color: #10130b; \}/);
  assert.match(v2Styles, /html\[data-ui="aegis-v2"\] \.skill-btn\.install:hover, html\[data-ui="aegis-v2"\] \.plan-action-btn\.primary:hover, html\[data-ui="aegis-v2"\] \.settings-save-btn:hover \{ background: var\(--accent-strong\); color: #10130b; \}/);
});
