// Keeps the documented browser acceptance areas in step with the areas
// webconsole_e2e.mjs actually accepts for --only (issue #153). Reads source
// text only, so it needs neither Playwright nor a browser.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (relative) => readFileSync(new URL('../../' + relative, import.meta.url), 'utf8');

function runnerAreas() {
  const block = /^const AREAS = \[\n([\s\S]*?)\n\];$/m.exec(read('validation/scripts/webconsole_e2e.mjs'));
  assert.ok(block, 'webconsole_e2e.mjs declares its areas in one const AREAS = [...] block');
  const names = [...block[1].matchAll(/^\s*\['([a-z]+)',\s*run\w+\],?$/gm)].map((match) => match[1]);
  assert.equal(names.length, block[1].trim().split('\n').length, 'every AREAS line is a [name, runner] pair');
  return names;
}

function readmeAreas() {
  const line = read('validation/README.md').split('\n').find((text) => text.startsWith('`--only` 可用区域为：'));
  assert.ok(line, 'validation/README.md lists the --only areas');
  const sentence = line.slice(0, line.indexOf('。'));
  return [...sentence.matchAll(/`([a-z]+)`/g)].map((match) => match[1]);
}

function specAreas() {
  const spec = read('spec/07-testing-strategy.md');
  const start = spec.indexOf('\n## Web E2E 区域\n');
  assert.ok(start >= 0, 'spec/07 has a Web E2E 区域 section');
  const end = spec.indexOf('\n## ', start + 1);
  const section = spec.slice(start, end < 0 ? undefined : end);
  return [...section.matchAll(/^- `([a-z]+)`：/gm)].map((match) => match[1]);
}

test('the runner declares the browser acceptance areas', () => {
  const areas = runnerAreas();
  assert.ok(areas.length >= 9, 'found ' + areas.join(', '));
  assert.equal(new Set(areas).size, areas.length, 'area names are unique');
});

test('validation/README.md lists exactly the areas --only accepts, in run order', () => {
  assert.deepEqual(readmeAreas(), runnerAreas());
});

test('spec/07 lists exactly the areas --only accepts, in run order', () => {
  assert.deepEqual(specAreas(), runnerAreas());
});
