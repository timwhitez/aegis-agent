// Web console browser acceptance suite (npm run test:e2e).
//
// Credential-free: builds aegis-agent and the scripted provider fixture
// (validation/cmd/budgetsmokeprovider), renders a temporary config, starts
// both on random ports and drives Chromium through every area of
// spec/17-web-console-frontend.md §10 / spec/17-web-console.md §12.
// Screenshots, provider decisions and manifest.json are written to
// validation/runs/webconsole-e2e-<stamp>/ (or --output=<dir>).
//
//   node validation/scripts/webconsole_e2e.mjs [--only=shell,session] [--output=dir] [--keep-runtime]
//
// Areas: server, shell, session, plan, approvals (approval CAS), receipts
// (approval receipts), recovery (failed sends), showcase (README
// screenshots), pages (Sessions, Skills, Workspace, Settings; runs last
// because it clears the history). Approval CAS runs in zh-CN/en ×
// desktop/mobile, receipts in zh-CN desktop + en mobile; set
// E2E_APPROVAL_PROFILES=all (or a list like zh-desktop,en-mobile) to override.
import path from 'node:path';
import process from 'node:process';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { createHarness } from './e2e/harness.mjs';
import { runServer } from './e2e/server.mjs';
import { runShell } from './e2e/shell.mjs';
import { runSession } from './e2e/session.mjs';
import { runPlan } from './e2e/plan.mjs';
import { runShowcase } from './e2e/showcase.mjs';
import { runPages } from './e2e/pages.mjs';
import { runApprovalCASE2E } from './webconsole_approval_cas_e2e.mjs';
import { runApprovalReceiptsE2E } from './webconsole_approval_receipts_e2e.mjs';
import { runSendRecoveryE2E } from './webconsole_send_recovery_e2e.mjs';

const AREAS = [
  ['server', runServer],
  ['shell', runShell],
  ['session', runSession],
  ['plan', runPlan],
  ['approvals', runApprovalCASE2E],
  ['receipts', runApprovalReceiptsE2E],
  ['recovery', runSendRecoveryE2E],
  ['showcase', runShowcase],
  ['pages', runPages],
];

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '..', '..');
const argValue = (name) => process.argv.find((arg) => arg.startsWith('--' + name + '='))?.slice(name.length + 3);
const stamp = new Date().toISOString().replace(/[:.]/g, '-');
const outputDir = path.resolve(repoRoot, argValue('output') || 'validation/runs/webconsole-e2e-' + stamp);
const only = (argValue('only') || '').split(',').map((value) => value.trim()).filter(Boolean);
const unknown = only.filter((name) => !AREAS.some(([area]) => area === name));
if (unknown.length) {
  process.stderr.write('unknown --only area(s): ' + unknown.join(', ') + '; known: ' + AREAS.map(([area]) => area).join(', ') + '\n');
  process.exit(2);
}

// A wait created before a failing step rejects later; the step already failed and was recorded.
process.on('unhandledRejection', (error) => {
  process.stdout.write('     (late rejection after a failed step: ' + String(error?.message || error).split('\n')[0].slice(0, 200) + ')\n');
});

const h = await createHarness({ repoRoot, outputDir, only, keepRuntime: process.argv.includes('--keep-runtime') });
const durations = {};
let fatal = null;
try {
  await h.start();
  process.stdout.write('web console ' + h.baseURL + ' · provider ' + h.providerURL + ' · output ' + path.relative(repoRoot, outputDir) + '\n');
  for (const [area, run] of AREAS) {
    if (only.length && !only.includes(area)) continue;
    durations[area] = await h.runArea(area, run);
  }
} catch (error) {
  fatal = error;
  process.stdout.write('HARNESS FAILURE ' + String(error?.stack || error) + '\n');
} finally {
  const commit = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: repoRoot, encoding: 'utf8' }).stdout.trim() || 'unknown';
  const dirty = spawnSync('git', ['status', '--porcelain', '--untracked-files=no'], { cwd: repoRoot, encoding: 'utf8' }).stdout.trim() !== '';
  if (fatal) h.checks.push({ area: h.area, name: 'harness', ok: false, error: String(fatal?.stack || fatal).slice(0, 3000) });
  const manifest = await h.writeManifest({ commit: { sha: commit, dirty }, durations }).catch((error) => {
    process.stdout.write('manifest write failed: ' + error + '\n');
    return null;
  });
  await h.stop();
  const failed = h.checks.filter((item) => !item.ok);
  process.stdout.write('\n' + (h.checks.length - failed.length) + '/' + h.checks.length + ' checks passed, '
    + h.screenshots.length + ' screenshots, ' + Math.round((manifest?.duration_ms || 0) / 1000) + 's · ' + path.relative(repoRoot, outputDir) + '\n');
  for (const item of failed) process.stdout.write('  FAILED [' + item.area + '] ' + item.name + '\n');
  for (const bug of h.knownBugs()) process.stdout.write('  KNOWN UI BUG ' + bug.id + ': ' + bug.title + ' (e.g. ' + bug.screenshots[0] + ')\n');
  process.exitCode = failed.length || fatal || !manifest ? 1 : 0;
}
