// Keeps the documented browser acceptance areas in step with the areas
// webconsole_e2e.mjs actually accepts for --only (issue #153). Reads source
// text only, so it needs neither Playwright nor a browser.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const AREA_NAME = '[a-z][a-z0-9-]*';
const read = (relative) => readFileSync(new URL('../../' + relative, import.meta.url), 'utf8').replace(/\r\n/g, '\n');

function runnerAreas() {
  const block = /^const AREAS = \[\n([\s\S]*?)\n\];$/m.exec(read('validation/scripts/webconsole_e2e.mjs'));
  assert.ok(block, 'webconsole_e2e.mjs declares its areas in one const AREAS = [...] block');
  // Comments and blank lines may sit between entries; every remaining line is one [name, runner] pair.
  const entries = block[1].split('\n').map((line) => line.replace(/\/\/.*$/, '').trim()).filter(Boolean);
  const pair = new RegExp('^\\[\\s*([\'"])(' + AREA_NAME + ')\\1\\s*,\\s*[A-Za-z_$][\\w$]*\\s*\\],?$');
  return entries.map((entry) => {
    const match = pair.exec(entry);
    assert.ok(match, 'AREAS entry is a [name, runner] pair: ' + entry);
    return match[2];
  });
}

function readmeAreas() {
  const line = read('validation/README.md').split('\n').find((text) => text.startsWith('`--only` 可用区域为：'));
  assert.ok(line, 'validation/README.md lists the --only areas');
  const sentence = line.slice(0, line.indexOf('。'));
  return [...sentence.matchAll(new RegExp('`(' + AREA_NAME + ')`', 'g'))].map((match) => match[1]);
}

function specAreas() {
  const spec = read('spec/07-testing-strategy.md');
  const start = spec.indexOf('\n## Web E2E 区域\n');
  assert.ok(start >= 0, 'spec/07 has a Web E2E 区域 section');
  const end = spec.indexOf('\n## ', start + 1);
  const section = spec.slice(start, end < 0 ? undefined : end);
  return [...section.matchAll(new RegExp('^- `(' + AREA_NAME + ')`：', 'gm'))].map((match) => match[1]);
}

test('the runner declares the browser acceptance areas', () => {
  const areas = runnerAreas();
  assert.ok(areas.length > 0, 'AREAS is not empty');
  assert.equal(new Set(areas).size, areas.length, 'area names are unique: ' + areas.join(', '));
});

test('validation/README.md lists exactly the areas --only accepts, in run order', () => {
  assert.deepEqual(readmeAreas(), runnerAreas());
});

test('spec/07 lists exactly the areas --only accepts, in run order', () => {
  assert.deepEqual(specAreas(), runnerAreas());
});
