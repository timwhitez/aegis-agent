import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const baseStyles = readFileSync(new URL('../../internal/webconsole/assets/styles.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');
const v2Styles = readFileSync(new URL('../../internal/webconsole/assets-v2/aegis.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');

test('harness reminder bubbles have a distinct left-aligned visual treatment', () => {
  assert.match(baseStyles, /\.message\.harness-reminder \.message-header-icon, \.message\.harness-reminder \.message-header-name \{ color: var\(--warning\); \}/);
  assert.match(baseStyles, /\.message\.harness-reminder \.message-bubble \{ background: var\(--warning-soft\); border: 1px solid rgba\(148, 98, 0, 0\.18\); color: var\(--text-main\); \}/);
  assert.match(v2Styles, /\.message\.assistant\.harness-reminder \.message-bubble \{ background: #fff8e8; border-color: rgba\(148,98,0,\.2\); color: #3b321d; \}/);
});
