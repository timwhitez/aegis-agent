import test from 'node:test';
import assert from 'node:assert/strict';
import { html, hx } from '../../internal/webconsole/ui/src/lib/html.js';

test('translate="no" renders as the boolean false property on DOM elements', () => {
  assert.equal(hx('span', { translate: 'no' }).props.translate, false);
  assert.equal(hx('span', { translate: 'NO ' }).props.translate, false);
  assert.equal(hx('span', { translate: 'yes' }).props.translate, true);
  assert.equal(hx('span', { translate: false }).props.translate, false);
  assert.equal(html`<code translate="no">x</code>`.props.translate, false);
});

test('components and missing props are left untouched', () => {
  const Component = () => null;
  assert.equal(hx(Component, { translate: 'no' }).props.translate, 'no');
  assert.equal(hx('div', null).props.translate, undefined);
});
