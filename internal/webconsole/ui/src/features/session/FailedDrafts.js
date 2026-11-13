// Failed sends above the composer (spec/17-web-console.md §8.1.1): memory
// only, labelled with the original session, distinguishing an explicit
// rejection from "未确认是否送达". Restore copies the raw text back only into
// an empty composer and never resends; dismiss drops the card. The card is
// the object-level error (backend message, detail and action inline); a
// polite live region stays mounted so new failures are announced.
// Test ids: failed-drafts, failed-draft (data-status, data-session-id),
// failed-draft-restore, failed-draft-open, failed-draft-dismiss.
import { html } from '../../lib/html.js';
import { t, tx } from '../../lib/i18n.js';
import { failedDraftView } from '../../lib/send-recovery.js';
import { useStore } from '../../state/hooks.js';
import { openSession } from '../../state/actions.js';
import { discardFailedDraft, restoreFailedDraft } from '../../state/session-actions.js';
import { Button } from '../../ui/Button.js';
import { IconButton } from '../../ui/IconButton.js';
import { Icon } from '../../ui/Icon.js';

export function FailedDrafts() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    drafts: state.composer.failedDrafts,
    composerEmpty: state.composer.text === '',
    sessionId: state.session.id,
  }));
  const latest = view.drafts[view.drafts.length - 1];
  const announcement = latest ? `${tx(failedDraftView(latest).notice)} ${latest.error?.message || ''}`.trim() : '';
  const live = html`<p class="sr-only" aria-live="polite" data-testid="failed-drafts-live">${announcement}</p>`;
  if (!view.drafts.length) return live;
  return html`${live}<section class="failed-drafts" data-testid="failed-drafts" aria-label=${t('send.draft.title')}>
    ${view.drafts.map((draft) => {
      const card = failedDraftView(draft);
      const unconfirmed = draft.status === 'unconfirmed';
      const otherSession = !draft.ephemeral && draft.sessionId && draft.sessionId !== view.sessionId;
      return html`<article
        key=${draft.id}
        class=${`failed-draft ${unconfirmed ? 'is-unconfirmed' : 'is-rejected'}`}
        data-testid="failed-draft"
        data-status=${draft.status}
        data-kind=${draft.kind}
        data-session-id=${draft.sessionId}
      >
        <span class="failed-draft-icon" aria-hidden="true"><${Icon} name=${unconfirmed ? 'alert-triangle' : 'alert-circle'} size=${16} /></span>
        <div class="failed-draft-body">
          <p class="failed-draft-head">
            <strong>${tx(card.notice)}</strong>
          </p>
          <p class="failed-draft-meta">
            <span>${tx(card.kind)}</span>
            <span aria-hidden="true">·</span>
            <span>${tx({ key: card.session.key })}</span>
            <code translate="no" title=${card.session.raw}>${card.session.raw}</code>
          </p>
          <p class="failed-draft-text" translate="no">${card.text.raw}</p>
          ${draft.error?.message ? html`<p class="failed-draft-error" translate="no">${draft.error.message}</p>` : null}
          ${draft.errorDetail ? html`<p class="failed-draft-error">${t('common.error.detail', { detail: '' })}<span translate="no">${draft.errorDetail}</span></p>` : null}
          ${draft.errorAction ? html`<p class="failed-draft-error">${t('common.error.action', { action: '' })}<span translate="no">${draft.errorAction}</span></p>` : null}
          ${!view.composerEmpty ? html`<p class="failed-draft-hint">${t('send.restore.composerNotEmpty')}</p>` : null}
        </div>
        <div class="failed-draft-actions">
          <${Button} size="sm" variant="secondary" icon="arrow-up" disabled=${!view.composerEmpty} data-testid="failed-draft-restore" onClick=${() => restoreFailedDraft(draft.id)}>
            ${tx(card.restore)}
          <//>
          ${otherSession ? html`<${Button} size="sm" variant="ghost" data-testid="failed-draft-open" onClick=${() => openSession(draft.sessionId)}>${t('composer.drafts.open')}<//>` : null}
          <${IconButton} icon="x" size="sm" label=${t('composer.drafts.dismiss')} data-testid="failed-draft-dismiss" onClick=${() => discardFailedDraft(draft.id)} />
        </div>
      </article>`;
    })}
  </section>`;
}
