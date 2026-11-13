// PLACEHOLDER — owned by the composer feature worker.
// Contract (spec §7.2): export function FailedDrafts(); lists
// composer.failedDrafts (memory only, see send-recovery.js) above the
// composer, labelled with the original session and distinguishing "rejected"
// from "delivery unconfirmed"; restore only when composer.text is empty
// (session-actions restoreFailedDraft / discardFailedDraft). Test id: "failed-drafts".
import { html } from '../../lib/html.js';
import { useStore } from '../../state/hooks.js';

export function FailedDrafts() {
  const drafts = useStore((state) => state.composer.failedDrafts, Object.is);
  if (!drafts.length) return null;
  return html`<div class="failed-drafts" data-testid="failed-drafts"></div>`;
}
