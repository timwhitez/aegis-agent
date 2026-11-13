// Timeline tab: durable messages + events (buildTimelineItems) with text
// search and an all / message / event filter, newest first. Event
// descriptors are translated with tx(); raw payloads stay in translate="no".
import { html, useId, useMemo, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { buildTimelineItems, maybeArray, mergeMessageTimelineEntries } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { Disclosure } from '../../ui/Disclosure.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { IconButton } from '../../ui/IconButton.js';
import { TimeText, Txt, text } from './parts.js';

const FILTERS = [
  { id: 'all', label: () => t('inspector.timeline.all') },
  { id: 'message', label: () => t('inspector.timeline.messages') },
  { id: 'event', label: () => t('inspector.timeline.events') },
];

const TONE_DOT = { danger: 'danger', live: 'success', queued: 'warning' };

const translate = (key, params) => text({ key, params });

export function TimelineTab() {
  const view = useStore((state) => ({ locale: state.ui.locale, detail: state.session.detail, messages: state.session.messages }));
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState('all');
  const searchId = useId();
  const timelineDetail = useMemo(
    () => (view.detail ? { ...view.detail, timeline: mergeMessageTimelineEntries(view.detail.timeline, view.messages) } : null),
    [view.detail, view.messages],
  );
  const total = maybeArray(timelineDetail?.timeline).length;
  const items = useMemo(
    () => buildTimelineItems(timelineDetail, { search, filter, t: translate }).reverse(),
    [timelineDetail, search, filter, view.locale],
  );
  if (!total) {
    return html`<${EmptyState} icon="clock" title=${t('inspector.timeline.emptyTitle')} body=${t('inspector.timeline.emptyBody')} data-testid="inspector-timeline-empty" />`;
  }
  return html`<div class="insp-tab insp-timeline-tab" data-testid="inspector-timeline">
    <div class="insp-toolbar">
      <div class="insp-search-wrap">
        <label class="sr-only" for=${searchId}>${t('inspector.timeline.search')}</label>
        <input
          id=${searchId}
          type="search"
          class="input insp-search"
          placeholder=${t('inspector.timeline.searchPlaceholder')}
          value=${search}
          onInput=${(event) => setSearch(event.currentTarget.value)}
          data-testid="inspector-timeline-search"
        />
        ${search ? html`<${IconButton} icon="x" size="sm" class="insp-search-clear" label=${t('inspector.timeline.clearSearch')} onClick=${() => setSearch('')} />` : null}
      </div>
      <div class="insp-segmented" role="group" aria-label=${t('inspector.timeline.filter')} data-testid="inspector-timeline-filter">
        ${FILTERS.map((item) => html`<button
          type="button"
          class="insp-segment"
          aria-pressed=${String(filter === item.id)}
          data-filter=${item.id}
          onClick=${() => setFilter(item.id)}
        >${item.label()}</button>`)}
      </div>
    </div>
    <p class="insp-muted insp-count-line" aria-live="polite" data-testid="inspector-timeline-count">${t('inspector.timeline.count', { shown: items.length, total })}</p>
    ${items.length ? html`<ol class="insp-timeline">
      ${items.map((entry) => {
        const d = entry.descriptor;
        return html`<li class="insp-tl-item" data-kind=${entry.kind} data-event-type=${entry.eventType || undefined} data-testid="inspector-timeline-item">
          <span class=${`insp-dot tone-${TONE_DOT[d.tone] || 'neutral'}`} aria-hidden="true"></span>
          <div class="insp-tl-body">
            <div class="insp-tl-head">
              <span class="insp-tl-title"><${Txt} d=${d.title} /></span>
              ${d.subject ? html`<span class="chip insp-tl-subject"><${Txt} d=${d.subject} /></span>` : null}
              <${TimeText} at=${entry.time} class="insp-tl-time" />
            </div>
            ${d.copy ? html`<p class="insp-card-copy insp-tl-copy"><${Txt} d=${d.copy} /></p>` : null}
            ${d.meta ? html`<p class="insp-card-meta"><${Txt} d=${d.meta} /></p>` : null}
            ${d.data ? html`<${Disclosure} summary=${t('inspector.timeline.details')} class="insp-disclosure is-compact">
              <pre class="insp-pre" translate="no">${d.data}</pre>
            <//>` : null}
          </div>
        </li>`;
      })}
    </ol>` : html`<p class="insp-muted" data-testid="inspector-timeline-nomatch">${t('inspector.timeline.noMatch')}</p>`}
  </div>`;
}
