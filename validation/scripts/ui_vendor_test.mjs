import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync } from 'node:fs';

const vendorDir = new URL('../../internal/webconsole/ui/vendor/', import.meta.url);
const EXPECTED = {
  'preact.module.js': '25a5df7e9f628a587743c4641a368737a3b218f28ef738ba0376a2d5bdfc948c',
  'hooks.module.js': '9e7eff58e0ae604583461eba1344da69a6894eab575eb591c635cc0d3f9ec57d',
  'htm.module.js': 'ab33dd3f38059b9be4d5f5350128eefb2356639c4e0bbe9d9e8b3ba75847e9e4',
};
const read = (name) => readFileSync(new URL(name, vendorDir));

test('vendored files match the SHA-256 recorded in THIRD_PARTY_NOTICES.md', () => {
  const notices = read('THIRD_PARTY_NOTICES.md').toString('utf8');
  for (const [name, hash] of Object.entries(EXPECTED)) {
    assert.equal(createHash('sha256').update(read(name)).digest('hex'), hash, name);
    assert.ok(notices.includes(hash), `notices list the hash of ${name}`);
  }
  const shipped = readdirSync(vendorDir).filter((name) => name.endsWith('.js')).sort();
  assert.deepEqual(shipped, Object.keys(EXPECTED).sort(), 'no unrecorded vendored scripts');
});

test('vendored modules only use relative imports and carry no sourcemap references', () => {
  for (const name of Object.keys(EXPECTED)) {
    const text = read(name).toString('utf8');
    const specifiers = [...text.matchAll(/(?:\bfrom\s*|\bimport\s*\(?\s*)["']([^"']+)["']/g)].map((match) => match[1]);
    for (const specifier of specifiers) {
      assert.ok(specifier.startsWith('./'), `${name} imports bare or absolute specifier ${specifier}`);
    }
    assert.ok(!text.includes('sourceMappingURL'), `${name} keeps a sourcemap comment`);
  }
  assert.match(read('hooks.module.js').toString('utf8'), /from"\.\/preact\.module\.js"/);
});

test('notices carry the full MIT and Apache-2.0 license texts and versions', () => {
  const notices = read('THIRD_PARTY_NOTICES.md').toString('utf8');
  for (const marker of ['10.29.8', '3.1.1', 'The MIT License (MIT)', 'Copyright (c) 2015-present Jason Miller', 'Apache License', 'Version 2.0, January 2004', 'END OF TERMS AND CONDITIONS']) {
    assert.ok(notices.includes(marker), `missing ${marker}`);
  }
});

test('vendored modules load as ES modules and htm renders through Preact', async () => {
  const { h } = await import('../../internal/webconsole/ui/vendor/preact.module.js');
  const hooks = await import('../../internal/webconsole/ui/vendor/hooks.module.js');
  const { default: htm } = await import('../../internal/webconsole/ui/vendor/htm.module.js');
  for (const name of ['useState', 'useEffect', 'useLayoutEffect', 'useMemo', 'useCallback', 'useRef', 'useReducer', 'useContext', 'useId']) {
    assert.equal(typeof hooks[name], 'function', name);
  }
  const html = htm.bind(h);
  const vnode = html`<p class="x">${'hi'}</p>`;
  assert.equal(vnode.type, 'p');
  assert.equal(vnode.props.class, 'x');
});
