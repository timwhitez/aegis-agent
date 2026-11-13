// End of the stream: live activity while a run is active, the latest turn's
// file-change summary, the inline session state note and a refresh warning.
import { html, useState } from '../../../lib/html.js';
import { formatTime, t, tx } from '../../../lib/i18n.js';
import { pendingStage } from '../../../lib/session-model.js';
import { focusComposer, refreshSession, setInspectorTab } from '../../../state/actions.js';
import { Badge } from '../../../ui/Badge.js';
import { Button } from '../../../ui/Button.js';
import { CopyButton } from '../../../ui/CopyButton.js';
import { Icon } from '../../../ui/Icon.js';
import { Slot } from './Slot.js';
import { useTicker } from './hooks.js';
import { badgeTone, formatElapsed, slotText } from './model.js';

/** Live activity: phase/event title, raw subject, copy and an elapsed timer. */
export function ActivityIndicator({ activity, startMs, interruptArmed }) {
  const now = useTicker(true, 1000);
  const stage = pendingStage({ generating: true, liveActivity: activity, interruptArmed });
  const elapsed = Number.isFinite(startMs) ? formatElapsed(Math.max(0, now - startMs)) : '';
  const subject = activity?.subject;
  return html`<div class=${`stream-activity tone-${badgeTone(stage.tone)}`} data-testid="live-activity" data-event=${activity?.eventType || undefined}>
    <span class="activity-dots" aria-hidden="true"><span></span><span></span><span></span></span>
    <div class="activity-text" role="status">
      <span class="sr-only">${tx(stage.stage)}</span>
      <span class="activity-title"><${Slot} slot=${stage.title} /></span>
      ${subject && (subject.raw || subject.key) ? html`<span class="activity-subject"><${Slot} slot=${subject} /></span>` : null}
      ${stage.copy ? html`<span class="activity-copy"><${Slot} slot=${stage.copy} /></span>` : null}
    </div>
    ${stage.interruptLabel ? html`<${Badge} tone="warning" class="activity-interrupt">${tx(stage.interruptLabel)}<//>` : null}
    ${elapsed ? html`<span class="activity-elapsed" aria-hidden="true" title=${t('session.activity.elapsed', { duration: elapsed })}>${elapsed}</span>` : null}
  </div>`;
}

function FailureDetail({ note }) {
  const failure = note.failure;
  return html`<div class="failure-detail">
    ${failure ? html`<p class="failure-kind">${slotText(failure.label)}</p>
      <p class="failure-hint">${slotText(failure.hint)}${failure.retries ? html` ${slotText(failure.retries)}` : null}</p>` : null}
    ${note.error ? html`<div class="failure-raw">
      <div class="failure-raw-head">
        <span>${t('session.failure.rawLabel')}</span>
        <${CopyButton} text=${note.error.raw} label=${t('session.failure.copy')} size="sm" />
      </div>
      <pre translate="no">${note.error.raw}</pre>
    </div>` : null}
  </div>`;
}

/** Paused / failed / awaiting input / completed note after the last message. */
export function StateNote({ note }) {
  if (!note) return null;
  const at = note.at ? formatTime(note.at) : '';
  if (note.kind === 'completed') {
    return html`<div class="stream-note kind-completed" data-testid="stream-state" data-state="completed">
      <${Icon} name="check" size=${13} />
      <span><${Slot} slot=${note.title} /></span>
      ${at ? html`<time datetime=${note.at}>${at}</time>` : null}
    </div>`;
  }
  const canReply = note.kind !== 'cancelled';
  return html`<div class=${`stream-note tone-${note.tone} kind-${note.kind}`} data-testid="stream-state" data-state=${note.kind}>
    <span class="stream-note-icon"><${Icon} name=${note.icon} size=${16} /></span>
    <div class="stream-note-body">
      <p class="stream-note-title"><${Slot} slot=${note.title} />${at ? html`<time datetime=${note.at}>${at}</time>` : null}</p>
      ${note.reason ? html`<p class="stream-note-reason" translate="no">${note.reason.raw}</p>` : null}
      ${note.kind === 'failed' ? html`<${FailureDetail} note=${note} />` : null}
      ${note.copy ? html`<p class="stream-note-copy"><${Slot} slot=${note.copy} /></p>` : null}
    </div>
    ${canReply ? html`<${Button} size="sm" variant="ghost" icon="message-square" class="stream-note-action" onClick=${() => focusComposer()} data-testid="stream-state-reply">${t('session.continueHint')}<//>` : null}
  </div>`;
}

const FILE_PREVIEW_LIMIT = 6;

/** Files written or edited during the latest turn (inferred; partial disclosure). */
export function FileChangesCard({ changes }) {
  const [expanded, setExpanded] = useState(false);
  const files = changes?.files || [];
  if (!files.length) return null;
  const shown = expanded ? files : files.slice(0, FILE_PREVIEW_LIMIT);
  const rest = files.length - shown.length;
  const title = files.length === 1 ? t('session.files.one') : t('session.files.many', { count: files.length });
  return html`<section class="files-card" data-testid="turn-file-changes" aria-label=${title}>
    <header class="files-card-head">
      <${Icon} name="file-text" size=${15} class="files-card-icon" />
      <span class="files-card-title">${title}</span>
      ${changes.totals.added || changes.totals.removed ? html`<span class="diffstat"><span class="diff-add">+${changes.totals.added}</span> <span class="diff-del">\u2212${changes.totals.removed}</span></span>` : null}
      ${changes.disclosure?.badge ? html`<${Badge} tone="neutral">${slotText(changes.disclosure.badge)}<//>` : null}
      <span class="files-card-spacer"></span>
      <${Button} size="sm" variant="ghost" iconRight="arrow-right" onClick=${() => setInspectorTab('files')} data-testid="turn-file-changes-open">${t('session.files.viewAll')}<//>
    </header>
    <ul class="files-list" role="list">
      ${shown.map((file) => html`<li key=${file.path} class="files-row" data-op=${file.op || 'unknown'}>
        <${Icon} name=${file.op === 'new' ? 'plus' : 'pencil'} size=${13} class="files-row-icon" />
        <span class="files-row-op">${t(file.op === 'new' ? 'session.files.created' : 'session.files.modified')}</span>
        <span class="files-row-path" translate="no" title=${file.path}>${file.path}</span>
        ${file.linesAdded || file.linesRemoved ? html`<span class="diffstat"><span class="diff-add">+${file.linesAdded}</span> <span class="diff-del">\u2212${file.linesRemoved}</span></span>` : null}
      </li>`)}
    </ul>
    ${rest > 0 ? html`<button type="button" class="files-more" onClick=${() => setExpanded(true)}>${t('session.files.showMore', { count: rest })}</button>` : null}
    ${changes.disclosure?.note ? html`<p class="files-card-note">${slotText(changes.disclosure.note)}</p>` : null}
  </section>`;
}

/** Shown when a background refresh failed while a detail is already on screen. */
export function RefreshNote({ error }) {
  if (!error) return null;
  return html`<div class="refresh-note" role="status" data-testid="session-refresh-error">
    <${Icon} name="alert-triangle" size=${14} />
    <span class="refresh-note-text">${t('session.refreshFailed')}${error.message ? html` <span translate="no">${error.message}</span>` : null}</span>
    <${Button} size="sm" variant="ghost" icon="refresh" onClick=${() => refreshSession()}>${t('common.retry')}<//>
  </div>`;
}
