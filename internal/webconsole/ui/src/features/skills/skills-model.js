// Skills page derivations (pure, DOM-free). The catalog comes from
// GET /api/skills: managed skills (first configured skill dir, installed and
// removable), read-only configured dirs, and discovery-only workspace
// extensions (installed=false, read_only=true). Installing means uploading a
// .zip package (POST /api/skills/upload); the marketplace install endpoint is
// intentionally unsupported by the server.

/** Mirrors maxSkillUploadBytes in internal/webconsole/service.go. */
export const SKILL_UPLOAD_MAX_BYTES = 50 * 1024 * 1024;

/** 'managed' | 'readonly' | 'discovered' */
export function skillKind(skill) {
  if (!skill?.installed) return 'discovered';
  return skill.read_only ? 'readonly' : 'managed';
}

export function canUninstall(skill) {
  return skillKind(skill) === 'managed' && Boolean(skill?.id);
}

/** Not installed but manageable: install through an upload. */
export function canInstallViaUpload(skill) {
  return !skill?.installed && !skill?.read_only;
}

function byName(a, b) {
  return String(a?.name || a?.id || '').localeCompare(String(b?.name || b?.id || ''), undefined, { sensitivity: 'base' });
}

/** [{ key: 'installed' | 'discovered', items }] without empty groups, sorted by name. */
export function groupSkills(skills) {
  const list = Array.isArray(skills) ? skills.filter((skill) => skill && (skill.id || skill.name)) : [];
  const installed = list.filter((skill) => skill.installed).sort(byName);
  const discovered = list.filter((skill) => !skill.installed).sort(byName);
  return [{ key: 'installed', items: installed }, { key: 'discovered', items: discovered }].filter((group) => group.items.length > 0);
}

export function skillCounts(skills) {
  const list = Array.isArray(skills) ? skills : [];
  return {
    total: list.length,
    installed: list.filter((skill) => skill?.installed).length,
    managed: list.filter((skill) => skillKind(skill) === 'managed').length,
    discovered: list.filter((skill) => !skill?.installed).length,
  };
}

/** Where the skill lives on disk: extension path, else its discovery dir. */
export function skillLocation(skill) {
  return String(skill?.extension_path || skill?.discovery_path || '');
}

/** Server reasons the console can localize; unknown reasons render raw. */
export const KNOWN_REASON_KEYS = Object.freeze({
  'Only the first configured skill directory is managed by this WebConsole.': 'skills.reason.unmanagedDir',
  'Skill id is not manageable by this WebConsole.': 'skills.reason.unmanageableId',
});

export function reasonKey(reason) {
  return KNOWN_REASON_KEYS[String(reason || '').trim()] || '';
}

/**
 * Client-side pre-check of a picked/dropped file. Returns '' when acceptable
 * or the skills.* i18n key describing the problem. The server stays the
 * authority on package contents.
 */
export function validateSkillFile(file) {
  if (!file) return 'skills.upload.notZip';
  const name = String(file.name || '').toLowerCase();
  if (!name.endsWith('.zip')) return 'skills.upload.notZip';
  const size = Number(file.size);
  if (Number.isFinite(size) && size <= 0) return 'skills.upload.empty';
  if (Number.isFinite(size) && size > SKILL_UPLOAD_MAX_BYTES) return 'skills.upload.tooLarge';
  return '';
}

/** True when a drag carries files (ignores text/link drags). */
export function dragHasFiles(dataTransfer) {
  const types = dataTransfer?.types;
  if (!types) return false;
  return Array.from(types).includes('Files');
}
