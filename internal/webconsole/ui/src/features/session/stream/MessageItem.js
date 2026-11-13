// Message items of the session stream: user / steer bubbles, assistant prose,
// harness and system notices, background-result cards and thinking blocks.
// Each item root carries data-testid="message" with data-role/data-variant.
import { html, useState } from '../../../lib/html.js';
import { formatDateTime, formatTime, t, tx } from '../../../lib/i18n.js';
import { shortId, workdirBase } from '../../../lib/session-model.js';
import { statusTone } from '../../../lib/status.js';
import { openSession } from '../../../state/actions.js';
import { Badge } from '../../../ui/Badge.js';
import { Button } from '../../../ui/Button.js';
import { CopyButton } from '../../../ui/CopyButton.js';
import { Icon } from '../../../ui/Icon.js';
import { IconButton } from '../../../ui/IconButton.js';
import { Spinner } from '../../../ui/Spinner.js';
import { Markdown } from './Markdown.js';
import { Slot } from './Slot.js';
import { StepList } from './StepGroup.js';
import { clipText, displayRole, previewLine, slotText } from './model.js';

function exactTime(value) {
  return value ? formatDateTime(value, { dateStyle: 'full', timeStyle: 'medium' }) : '';
}

/** Hover actions: exact-time tooltip and copy. */
export function MessageActions({ createdAt, copyText = '', align = 'start', extra = null }) {
  const time = createdAt ? formatTime(createdAt) : '';
  if (!time && !copyText && !extra) return null;
  return html`<div class=${`msg-actions align-${align}`} data-testid="message-actions">
    ${time ? html`<time class="msg-time" datetime=${createdAt} title=${exactTime(createdAt)}>${time}</time>` : null}
    ${copyText ? html`<${CopyButton} text=${copyText} label=${t('session.message.copy')} size="sm" class="msg-copy" />` : null}
    ${extra}
  </div>`;
}

function rootAttrs(item) {
  return {
    'data-testid': 'message',
    'data-role': displayRole(item.variant),
    'data-variant': item.variant,
    'data-message-id': item.message?.id || undefined,
  };
}

export function UserMessage({ item }) {
  const steer = item.variant === 'steer';
  const interrupt = steer && Boolean(item.meta?.interrupt);
  return html`<article class=${`msg msg-user ${steer ? 'is-steer' : ''} ${item.pending ? 'is-pending' : ''}`} ...${rootAttrs(item)} data-pending=${item.pending ? 'true' : undefined}>
    <span class="sr-only">${tx(item.actor)}</span>
    ${steer ? html`<div class="msg-user-label"><${Icon} name=${interrupt ? 'hand' : 'corner-down-right'} size=${13} />${t(interrupt ? 'session.message.interruptSteer' : 'session.message.steer')}</div>` : null}
    <div class="msg-bubble" translate="no">${item.text}</div>
    ${item.pending
      ? html`<div class="msg-actions align-end is-visible"><span class="msg-pending" role="status"><${Spinner} size=${12} />${t('session.message.pending')}</span></div>`
      : html`<${MessageActions} createdAt=${item.createdAt} copyText=${item.text} align="end" />`}
  </article>`;
}

export function ThinkingBlock({ text, cacheKey }) {
  const [open, setOpen] = useState(false);
  return html`<div class=${`thinking ${open ? 'is-open' : ''}`} data-testid="thinking-block">
    <button type="button" class="thinking-toggle" aria-expanded=${String(open)} onClick=${() => setOpen(!open)}>
      <${Icon} name="brain" size=${14} />
      <span class="thinking-label">${t('session.thinking.label')}</span>
      ${open ? null : html`<span class="thinking-preview" translate="no">${previewLine(text, 160)}</span>`}
      <${Icon} name="chevron-right" size=${14} class="thinking-chevron" />
    </button>
    ${open ? html`<div class="thinking-body"><${Markdown} cacheKey=${cacheKey} text=${text} /></div>` : null}
  </div>`;
}

export function AssistantMessage({ item }) {
  const [showRaw, setShowRaw] = useState(false);
  const rawSteps = Array.isArray(item.finishSteps) && item.finishSteps.length ? item.finishSteps : null;
  const rawToggle = rawSteps
    ? html`<${IconButton} icon="terminal" size="sm" label=${t('session.message.rawSteps')} pressed=${showRaw} onClick=${() => setShowRaw(!showRaw)} data-testid="message-raw-steps" />`
    : null;
  return html`<article class=${`msg msg-assistant ${item.final ? 'is-final' : ''}`} ...${rootAttrs(item)} data-final=${item.final ? 'true' : undefined}>
    <span class="sr-only"><${Slot} slot=${item.actor} /></span>
    ${item.thinking ? html`<${ThinkingBlock} text=${item.thinking} cacheKey=${'thinking:' + item.id} />` : null}
    ${item.text ? html`<${Markdown} cacheKey=${item.markdownKey} text=${item.text} />` : null}
    <${MessageActions} createdAt=${item.createdAt} copyText=${item.text} extra=${rawToggle} />
    ${rawSteps && showRaw ? html`<${StepList} steps=${rawSteps} testId="message-raw-step-list" />` : null}
  </article>`;
}

/** Harness reminders (amber), system and tool notes (neutral). Never "You". */
export function NoticeMessage({ item }) {
  const [expanded, setExpanded] = useState(false);
  const harness = item.variant === 'harness_reminder';
  const clip = clipText(item.text, { maxLines: 4, maxChars: 480 });
  const icon = harness ? 'bell' : item.variant === 'tool' ? 'wrench' : 'terminal';
  return html`<article class=${`msg msg-notice ${harness ? 'tone-warning' : 'tone-neutral'}`} ...${rootAttrs(item)}>
    <div class="notice-head">
      <${Icon} name=${icon} size=${14} class="notice-icon" />
      <span class="notice-label"><${Slot} slot=${item.actor} /></span>
      <${MessageActions} createdAt=${item.createdAt} copyText=${item.text} align="end" />
    </div>
    <div class="notice-body" translate="no">${expanded || !clip.truncated ? item.text : clip.text + '\u2026'}</div>
    ${clip.truncated ? html`<button type="button" class="stream-link-btn" aria-expanded=${String(expanded)} onClick=${() => setExpanded(!expanded)}>
      ${expanded ? t('session.showLess') : t('session.showMore')}
    </button>` : null}
  </article>`;
}

function MetaChip({ label, raw, title }) {
  if (!raw) return null;
  return html`<span class="chip meta-chip" title=${title || undefined}>${label ? html`<span class="meta-chip-label">${label}</span>` : null}<span translate="no">${raw}</span></span>`;
}

function BackgroundResultRow({ view }) {
  const tone = view.isError ? 'danger' : statusTone(view.status);
  return html`<li class=${`bg-item tone-${tone}`} data-testid="background-result" data-status=${view.status}>
    <div class="bg-item-top">
      <span class=${`status-dot tone-${tone}`} aria-hidden="true"></span>
      <${Slot} slot=${view.label} class="bg-item-label" />
      <${Badge} tone=${tone}>${tx(view.statusLabel)}<//>
      <span class="bg-item-spacer"></span>
      ${view.sessionId ? html`<${Button} size="sm" variant="ghost" iconRight="arrow-right" onClick=${() => openSession(view.sessionId)} data-testid="open-child-session" data-session-id=${view.sessionId}>${t('session.step.openChild')}<//>` : null}
    </div>
    <div class=${`bg-item-body ${view.isError ? 'is-error' : ''}`}><${Slot} slot=${view.body} /></div>
    <div class="bg-item-meta">
      <${MetaChip} label=${t('session.background.job')} raw=${shortId(view.queueJobId)} title=${view.queueJobId} />
      <${MetaChip} label=${t('session.background.child')} raw=${shortId(view.sessionId)} title=${view.sessionId} />
      <${MetaChip} label=${t('session.background.workdir')} raw=${workdirBase(view.workdir)} title=${view.workdir} />
      ${view.visiblePaths.map((path) => html`<${MetaChip} raw=${String(path)} title=${String(path)} />`)}
    </div>
  </li>`;
}

export function BackgroundResultsMessage({ item }) {
  const summary = item.background;
  if (!summary || !summary.count) return html`<${NoticeMessage} item=${item} />`;
  return html`<article class="msg msg-background" ...${rootAttrs(item)} data-count=${summary.count}>
    <header class="bg-head">
      <span class="bg-icon"><${Icon} name="git-branch" size=${15} /></span>
      <div class="bg-titles">
        <span class="bg-eyebrow"><${Slot} slot=${item.actor} /></span>
        <span class="bg-title">${slotText(summary.headline)}</span>
      </div>
      <div class="bg-badges">
        ${summary.completed ? html`<${Badge} tone="success">${t('session.background.completed', { count: summary.completed })}<//>` : null}
        ${summary.failed ? html`<${Badge} tone="danger">${t('session.background.failed', { count: summary.failed })}<//>` : null}
      </div>
    </header>
    <ul class="bg-list" role="list">${summary.items.map((view, index) => html`<${BackgroundResultRow} key=${view.sessionId || view.queueJobId || index} view=${view} />`)}</ul>
    <${MessageActions} createdAt=${item.createdAt} />
  </article>`;
}
