// Session message stream (spec 17-web-console-frontend §7.1).
// Contract: export function MessageList(); no props. Reads session.messages,
// session.optimistic (optional, pending local user messages), session.detail,
// session.generating, session.liveEvents, session.activity (optional override)
// and composer.interrupt from the store. Renders user / steer bubbles,
// assistant prose (sanitized Markdown, cached per message), thinking, harness
// and system notices, background-result cards, step groups with goal/agent
// tool cards, and the end of the stream (live activity, latest-turn file
// changes, state note). Scrolling and paging live in SessionView.
// Test ids: message (data-role, data-variant), step-group, tool-step,
// step-detail, thinking-block, background-result, goal-tool-card,
// agent-tool-card, open-child-session, live-activity, stream-state,
// turn-file-changes, message-actions, message-list.
// Performance: buildConversation() re-runs only when the message window,
// optimistic list or live flag changes; unchanged items reuse their previous
// vnode (Preact skips them), and Markdown renders are cached by message id.
import { html, useMemo, useRef } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { AssistantMessage, BackgroundResultsMessage, NoticeMessage, UserMessage } from './stream/MessageItem.js';
import { StepGroup } from './stream/StepGroup.js';
import { ActivityIndicator, FileChangesCard, RefreshNote, StateNote } from './stream/StreamEnd.js';
import {
  createConversationMemo,
  itemSignature,
  latestTurnFileChanges,
  selectLiveActivity,
  streamEndNote,
  turnStartMs,
} from './stream/model.js';

const EMPTY = Object.freeze([]);

function renderItem(item, key) {
  if (item.kind === 'steps') return html`<${StepGroup} key=${key} group=${item} />`;
  switch (item.variant) {
    case 'user':
    case 'steer':
      return html`<${UserMessage} key=${key} item=${item} />`;
    case 'background_results':
      return html`<${BackgroundResultsMessage} key=${key} item=${item} />`;
    case 'harness_reminder':
    case 'system':
    case 'tool':
      return html`<${NoticeMessage} key=${key} item=${item} />`;
    default:
      return html`<${AssistantMessage} key=${key} item=${item} />`;
  }
}

export function MessageList() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    messages: state.session.messages,
    optimistic: Array.isArray(state.session.optimistic) ? state.session.optimistic : EMPTY,
    detail: state.session.detail,
    generating: state.session.generating,
    liveEvents: state.session.liveEvents,
    activityOverride: state.session.activity || null,
    interruptArmed: Boolean(state.composer.interrupt),
    error: state.session.error,
  }));
  const memoRef = useRef(null);
  if (!memoRef.current) memoRef.current = createConversationMemo();
  const cacheRef = useRef(new Map());

  const pendingSend = view.optimistic.some((message) => message?.pending);
  const live = Boolean(view.generating);
  const items = memoRef.current({
    messages: view.messages,
    optimistic: view.optimistic,
    live,
    agentName: view.detail?.metadata?.agent_name || '',
    agentRole: view.detail?.metadata?.agent_role || '',
  });

  // Reuse the previous vnode of every unchanged item.
  const nextCache = new Map();
  const seen = new Set();
  const rendered = items.map((item, index) => {
    let key = item.id || `item:${index}`;
    if (seen.has(key)) key = `${key}#${index}`;
    seen.add(key);
    const signature = `${itemSignature(item)}|${view.locale}`;
    const cached = cacheRef.current.get(key);
    if (cached && cached.signature === signature) {
      nextCache.set(key, cached);
      return cached.vnode;
    }
    const entry = { signature, vnode: renderItem(item, key) };
    nextCache.set(key, entry);
    return entry.vnode;
  });
  cacheRef.current = nextCache;

  const fileChanges = useMemo(() => (live ? null : latestTurnFileChanges(view.messages)), [view.messages, live]);
  const note = streamEndNote(view.detail, { generating: live || pendingSend, messages: view.messages });
  const startMs = turnStartMs(view.messages, view.optimistic);
  const showActivity = live || pendingSend;
  const activity = showActivity
    ? selectLiveActivity({ detail: view.detail, liveEvents: view.liveEvents, sinceMs: startMs, override: view.activityOverride })
    : null;

  if (!items.length && !showActivity && !note) return null;
  return html`<section class="stream" aria-label=${t('session.stream.label')} data-testid="message-list">
    ${rendered}
    <div class="stream-end" aria-live="polite">
      ${activity ? html`<${ActivityIndicator} activity=${activity} startMs=${startMs} interruptArmed=${view.interruptArmed} />` : null}
      ${fileChanges?.files?.length ? html`<${FileChangesCard} changes=${fileChanges} />` : null}
      ${note ? html`<${StateNote} note=${note} />` : null}
    </div>
    ${view.error && view.detail ? html`<${RefreshNote} error=${view.error} />` : null}
  </section>`;
}
