import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const v2Styles = readFileSync(new URL('../../internal/webconsole/assets-v2/aegis.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');

test('Web Console v2 reserves two placeholder lines in the narrow composer', () => {
  assert.match(v2Styles, /@media \(max-width: 400px\) \{ #chat-input \{ min-height: 68px; line-height: 1\.5; \} \}/);
});
