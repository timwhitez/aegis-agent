// Session view: a centred reading column (~760px) inside a scroll container
// with the composer dock below. Owns scrolling: auto-follow while the reader
// is near the bottom, stop following as soon as they scroll up, "back to
// latest" with a new-content dot, and "load earlier messages" that keeps the
// reading position after the prepend. Mounts (order kept): GoalBanner,
// MessageList, PlanInputCard, ApprovalCard in the column; FailedDrafts,
// TodoStrip, Composer in the dock; EmptySession on "/".
import { html, useEffect, useLayoutEffect, useRef, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { loadEarlierMessages, refreshSession } from '../../state/actions.js';
import { Button } from '../../ui/Button.js';
import { Icon } from '../../ui/Icon.js';
import { MessageList } from './MessageList.js';
import { Composer } from './Composer.js';
import { ApprovalCard } from './ApprovalCard.js';
import { PlanInputCard } from './PlanInputCard.js';
import { GoalBanner } from './GoalBanner.js';
import { FailedDrafts } from './FailedDrafts.js';
import { TodoStrip } from './TodoStrip.js';
import { EmptySession } from './EmptySession.js';

const FOLLOW_THRESHOLD_PX = 120;

function prefersReducedMotion() {
  return globalThis.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches ?? false;
}

function SessionSkeleton() {
  return html`<div class="stream-skeleton" data-testid="session-skeleton" aria-busy="true">
    <span class="sr-only" role="status">${t('session.loading')}</span>
    <div class="sk-row sk-user"><span class="sk-block"></span></div>
    <div class="sk-row"><span class="sk-line"></span><span class="sk-line"></span><span class="sk-line"></span></div>
    <div class="sk-row sk-steps"><span class="sk-pill"></span></div>
    <div class="sk-row"><span class="sk-line"></span><span class="sk-line"></span></div>
  </div>`;
}

function SessionError({ error }) {
  return html`<div class="session-error" role="alert" data-testid="session-error">
    <span class="session-error-icon"><${Icon} name="alert-circle" size=${20} /></span>
    <p class="session-error-title">${t('session.loadFailed')}</p>
    ${error.message ? html`<p class="session-error-message" translate="no">${error.message}</p>` : null}
    ${error.detail ? html`<p class="session-error-detail">${t('common.error.detail', { detail: '' })}<span translate="no">${error.detail}</span></p>` : null}
    ${error.action ? html`<p class="session-error-detail">${t('common.error.action', { action: '' })}<span translate="no">${error.action}</span></p>` : null}
    <${Button} size="sm" icon="refresh" onClick=${() => refreshSession()}>${t('common.retry')}<//>
  </div>`;
}

export function SessionView() {
  const view = useStore((state) => {
    const messages = state.session.messages;
    const optimistic = Array.isArray(state.session.optimistic) ? state.session.optimistic : [];
    const last = messages[messages.length - 1];
    return {
      locale: state.ui.locale,
      isNew: state.route.name === 'new',
      sessionId: state.session.id,
      loading: state.session.loading,
      hasDetail: Boolean(state.session.detail),
      error: state.session.error,
      hasMoreEarlier: state.session.hasMoreEarlier,
      loadingEarlier: state.session.loadingEarlier,
      firstId: messages[0]?.id || '',
      tailKey: `${messages.length}:${last?.id || ''}:${optimistic.length}:${state.session.generating ? 1 : 0}`,
      hasOptimistic: optimistic.length > 0,
      hasMessages: messages.length > 0,
    };
  });
  const scrollRef = useRef(null);
  const contentRef = useRef(null);
  const followRef = useRef(true);
  const autoScrollRef = useRef(false);
  const prependRef = useRef(null);
  const interactionRef = useRef(0);
  const [following, setFollowing] = useState(true);
  const [hasNew, setHasNew] = useState(false);

  const setFollow = (value) => {
    if (followRef.current !== value) {
      followRef.current = value;
      setFollowing(value);
    }
    if (value) setHasNew(false);
  };

  const isNearBottom = () => {
    const element = scrollRef.current;
    return !element || element.scrollHeight - element.scrollTop - element.clientHeight < FOLLOW_THRESHOLD_PX;
  };

  const scrollToBottom = (smooth = false) => {
    const element = scrollRef.current;
    if (!element) return;
    const behavior = smooth && !prefersReducedMotion() ? 'smooth' : 'auto';
    autoScrollRef.current = behavior === 'smooth';
    element.scrollTo({ top: element.scrollHeight, behavior });
  };

  // New session or route change: start at the bottom and follow.
  useLayoutEffect(() => {
    prependRef.current = null;
    setFollow(true);
    scrollToBottom();
  }, [view.sessionId, view.isNew]);

  // Follow content (and viewport) size changes while the reader is near the bottom.
  useEffect(() => {
    const content = contentRef.current;
    const scroller = scrollRef.current;
    if (!content || !scroller || typeof ResizeObserver !== 'function') return undefined;
    // Growth caused by the reader (expanding a step, "show all") must not yank
    // the view to the bottom: re-evaluate following from the position instead.
    const onContentResize = () => {
      if (prependRef.current !== null || !followRef.current) return;
      if (Date.now() - interactionRef.current < 700) {
        if (!isNearBottom()) setFollow(false);
        return;
      }
      scrollToBottom();
    };
    const contentObserver = new ResizeObserver(onContentResize);
    const viewportObserver = new ResizeObserver(() => {
      if (prependRef.current === null && followRef.current) scrollToBottom();
    });
    contentObserver.observe(content);
    viewportObserver.observe(scroller);
    return () => {
      contentObserver.disconnect();
      viewportObserver.disconnect();
    };
  }, []);

  // Earlier page merged: keep the previously visible content in place.
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (!element || prependRef.current === null) return;
    element.scrollTop = element.scrollHeight - prependRef.current;
    prependRef.current = null;
  }, [view.firstId]);

  // New tail content while the reader is scrolled up: light the dot.
  useEffect(() => {
    if (!followRef.current) setHasNew(true);
  }, [view.tailKey]);

  const onScroll = () => {
    const element = scrollRef.current;
    if (!element) return;
    const near = isNearBottom();
    if (autoScrollRef.current) {
      if (!near) return;
      autoScrollRef.current = false;
    }
    if (near !== followRef.current) setFollow(near);
  };

  // Scrolling up stops following immediately, even mid-stream.
  const onWheel = (event) => {
    if (event.deltaY < 0 && scrollRef.current && scrollRef.current.scrollHeight > scrollRef.current.clientHeight) {
      autoScrollRef.current = false;
      if (followRef.current) setFollow(false);
    }
  };

  const markInteraction = () => {
    interactionRef.current = Date.now();
  };

  const onLoadEarlier = async () => {
    const element = scrollRef.current;
    if (element) prependRef.current = element.scrollHeight - element.scrollTop;
    try {
      await loadEarlierMessages();
    } finally {
      prependRef.current = null;
    }
  };

  const backToLatest = () => {
    setFollow(true);
    scrollToBottom(true);
  };

  const showEmpty = view.isNew && !view.hasOptimistic;
  const showSkeleton = !view.isNew && view.loading && !view.hasDetail && !view.hasMessages;
  const showError = !view.isNew && view.error && !view.hasDetail;

  return html`<div class="session-view" data-testid="session-view" data-session-id=${view.sessionId || undefined}>
    <div class="session-scroll" ref=${scrollRef} onScroll=${onScroll} onWheel=${onWheel} onPointerDown=${markInteraction} onKeyDown=${markInteraction}>
      <div class="reading-column session-content" ref=${contentRef}>
        ${showEmpty ? html`<${EmptySession} />` : html`
          ${view.hasMoreEarlier && !view.isNew ? html`<div class="load-earlier">
            <${Button} size="sm" variant="ghost" icon="arrow-up" pending=${view.loadingEarlier} onClick=${onLoadEarlier} data-testid="load-earlier">
              ${view.loadingEarlier ? t('session.loadingEarlier') : t('session.loadEarlier')}
            <//>
          </div>` : null}
          ${showSkeleton ? html`<${SessionSkeleton} />` : null}
          ${showError ? html`<${SessionError} error=${view.error} />` : null}
          <${GoalBanner} />
          <${MessageList} />
          <${PlanInputCard} />
          <${ApprovalCard} />
        `}
      </div>
    </div>
    <div class="session-dock">
      ${!following && !showEmpty ? html`<div class="back-to-latest">
        <${Button} size="sm" variant="secondary" icon="arrow-down" onClick=${backToLatest} data-testid="back-to-latest" data-new-content=${hasNew ? 'true' : undefined}>
          ${t('session.backToLatest')}
          ${hasNew ? html`<span class="new-content-dot" data-testid="new-content-dot"><span class="sr-only">${t('session.newContent')}</span></span>` : null}
        <//>
      </div>` : null}
      <div class="reading-column">
        <${FailedDrafts} />
        <${TodoStrip} />
        <${Composer} />
      </div>
    </div>
  </div>`;
}
