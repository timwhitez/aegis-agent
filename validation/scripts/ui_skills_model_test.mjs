import test from 'node:test';
import assert from 'node:assert/strict';
import {
  KNOWN_REASON_KEYS,
  SKILL_UPLOAD_MAX_BYTES,
  canInstallViaUpload,
  canUninstall,
  dragHasFiles,
  groupSkills,
  reasonKey,
  skillCounts,
  skillKind,
  skillLocation,
  validateSkillFile,
} from '../../internal/webconsole/ui/src/features/skills/skills-model.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';

const managed = { id: 'b-skill', name: 'Beta', installed: true, discovery_path: '/w/skills' };
const readOnly = { id: 'a-skill', name: 'alpha', installed: true, read_only: true, disabled_reason: 'Only the first configured skill directory is managed by this WebConsole.' };
const extension = { id: 'ext:demo', name: 'demo', installed: false, read_only: true, trust: 'untrusted', extension_path: '/w/.agents/demo' };

test('skill kinds and actions follow installed/read_only', () => {
  assert.equal(skillKind(managed), 'managed');
  assert.equal(skillKind(readOnly), 'readonly');
  assert.equal(skillKind(extension), 'discovered');
  assert.ok(canUninstall(managed));
  assert.ok(!canUninstall(readOnly));
  assert.ok(!canUninstall(extension));
  assert.ok(canInstallViaUpload({ id: 'x', installed: false }));
  assert.ok(!canInstallViaUpload(extension));
  assert.equal(skillLocation(extension), '/w/.agents/demo');
  assert.equal(skillLocation(managed), '/w/skills');
  assert.equal(skillLocation({}), '');
});

test('groups are sorted by name and empty groups are dropped', () => {
  const groups = groupSkills([managed, extension, readOnly, null]);
  assert.deepEqual(groups.map((group) => group.key), ['installed', 'discovered']);
  assert.deepEqual(groups[0].items.map((skill) => skill.id), ['a-skill', 'b-skill']);
  assert.deepEqual(groupSkills([extension]).map((group) => group.key), ['discovered']);
  assert.deepEqual(groupSkills(null), []);
  assert.deepEqual(skillCounts([managed, readOnly, extension]), { total: 3, installed: 2, managed: 1, discovered: 1 });
});

test('known server reasons map to existing i18n keys', () => {
  for (const key of Object.values(KNOWN_REASON_KEYS)) {
    for (const locale of ['zh-CN', 'en']) assert.ok(dictionaries[locale][key], `${locale} ${key}`);
  }
  assert.equal(reasonKey(readOnly.disabled_reason), 'skills.reason.unmanagedDir');
  assert.equal(reasonKey('something else'), '');
});

test('upload pre-check accepts .zip packages within the limit', () => {
  assert.equal(validateSkillFile({ name: 'skill.ZIP', size: 10 }), '');
  assert.equal(validateSkillFile({ name: 'skill.tar.gz', size: 10 }), 'skills.upload.notZip');
  assert.equal(validateSkillFile({ name: 'skill.zip', size: 0 }), 'skills.upload.empty');
  assert.equal(validateSkillFile({ name: 'skill.zip', size: SKILL_UPLOAD_MAX_BYTES + 1 }), 'skills.upload.tooLarge');
  assert.equal(validateSkillFile(null), 'skills.upload.notZip');
  for (const key of ['skills.upload.notZip', 'skills.upload.empty', 'skills.upload.tooLarge']) {
    for (const locale of ['zh-CN', 'en']) assert.ok(dictionaries[locale][key], `${locale} ${key}`);
  }
  assert.ok(dragHasFiles({ types: ['Files', 'text/plain'] }));
  assert.ok(!dragHasFiles({ types: ['text/plain'] }));
  assert.ok(!dragHasFiles(null));
});
