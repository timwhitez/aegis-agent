import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const disclosureSource = readFileSync(new URL('../../internal/webconsole/assets/file-change-disclosure.js', import.meta.url), 'utf8');
const indexSource = readFileSync(new URL('../../internal/webconsole/assets/index.html', import.meta.url), 'utf8');

function disclosureContext(markup) {
  const context = {
    renderFileChangesFloat() {
      return markup;
    }
  };
  vm.createContext(context);
  vm.runInContext(disclosureSource, context, { filename: 'file-change-disclosure.js' });
  return context;
}

test('file-change panel identifies heuristic shell coverage as partial', () => {
  const context = disclosureContext('<div><span class="tf-title">Files</span><div class="tf-body"><div>row</div></div></div>');
  const html = context.renderFileChangesFloat();
  assert.match(html, />partial<\/span>/);
  assert.match(html, /Best-effort view/);
  assert.match(html, /shell changes are inferred from recognized redirects and may be incomplete/);
});

test('empty file-change panel remains empty', () => {
  const context = disclosureContext('');
  assert.equal(context.renderFileChangesFloat(), '');
});

test('disclosure loads after the renderer and before app startup', () => {
  const sessionIndex = indexSource.indexOf('<script src="session-view.js"></script>');
  const disclosureIndex = indexSource.indexOf('<script src="file-change-disclosure.js"></script>');
  const appIndex = indexSource.indexOf('<script src="app.js"></script>');
  assert.ok(sessionIndex >= 0, 'session-view.js is missing');
  assert.ok(disclosureIndex > sessionIndex, 'disclosure must load after session-view.js');
  assert.ok(appIndex > disclosureIndex, 'disclosure must load before app.js');
});
