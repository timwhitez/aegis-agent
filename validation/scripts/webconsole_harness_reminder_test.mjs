import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const baseStyles = readFileSync(new URL('../../internal/webconsole/assets/styles.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');
const v2Styles = readFileSync(new URL('../../internal/webconsole/assets-v2/aegis.css', import.meta.url), 'utf8')
  .replace(/\s+/g, ' ');
const iconsSource = readFileSync(new URL('../../internal/webconsole/assets/icons.js', import.meta.url), 'utf8');

test('harness reminder bubbles have a distinct left-aligned visual treatment', () => {
  assert.match(baseStyles, /\.message\.harness-reminder \.message-header-icon, \.message\.harness-reminder \.message-header-name \{ color: #9a4f00; \}/);
  assert.match(baseStyles, /\.message\.harness-reminder \.message-bubble \{ background: var\(--warning-soft\); border: 1px solid rgba\(148, 98, 0, 0\.18\); color: var\(--text-main\); \}/);
  assert.match(v2Styles, /\.message\.assistant\.harness-reminder \.message-bubble \{ background: #fff8e8; border-color: rgba\(148,98,0,\.2\); color: #3b321d; \}/);
  assert.match(v2Styles, /\.message\.harness-reminder \.message-header-icon, \.message\.harness-reminder \.message-header-name \{ color: var\(--warning\); \}/);
  assert.ok(contrastRatio('#9a4f00', '#f7f8fb') >= 4.5);
});

test('the local bell-ring icon is registered instead of using the box fallback', () => {
  const nodes = ['bell-ring', 'box'].map((name) => ({
    name,
    innerHTML: '',
    getAttribute(attribute) {
      return attribute === 'data-lucide' ? name : null;
    },
    classList: { add() {} }
  }));
  const context = {
    window: {},
    document: {
      querySelectorAll() {
        return nodes;
      }
    }
  };
  vm.createContext(context);
  vm.runInContext(iconsSource, context, { filename: 'icons.js' });
  context.window.lucide.createIcons();

  assert.match(nodes[0].innerHTML, /M18 8a6 6/);
  assert.notEqual(nodes[0].innerHTML, nodes[1].innerHTML);
});

function contrastRatio(foreground, background) {
  const foregroundLuminance = relativeLuminance(foreground);
  const backgroundLuminance = relativeLuminance(background);
  return (Math.max(foregroundLuminance, backgroundLuminance) + 0.05) /
    (Math.min(foregroundLuminance, backgroundLuminance) + 0.05);
}

function relativeLuminance(hex) {
  const channels = hex.match(/[0-9a-f]{2}/gi).map((channel) => Number.parseInt(channel, 16) / 255);
  const [red, green, blue] = channels.map((channel) => channel <= 0.04045
    ? channel / 12.92
    : ((channel + 0.055) / 1.055) ** 2.4);
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}
