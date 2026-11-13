// Session view layout (shell-owned, final): a centred reading column inside a
// scroll container with a sticky composer dock. Owns scroll behaviour:
// auto-follow near the bottom, "back to latest", and "load earlier messages"
// with scroll-position preservation. Feature content lives in the children.
import { html, useEffect, useLayoutEffect, useRef, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { useStore } from '../../state/hooks.js';
import { loadEarlierMessages, refreshSession } from '../../state/actions.js';
import { Button } from '../../ui/Button.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { MessageList } from './MessageList.js';
import { Composer } from './Composer.js';
import { ApprovalCard } from './ApprovalCard.js';
import { PlanInputCard } from './PlanInputCard.js';
import { GoalBanner } from './GoalBanner.js';
import { FailedDrafts } from './FailedDrafts.js';
import { TodoStrip } from './TodoStrip.js';
import { EmptySession } from './EmptySession.js';

const FOLLOW_THRESHOLD_PX = 96;

export function SessionView() {
  const view = useStore((state) => ({
    locale: state.ui.locale,
    isNew: state.route.name === 'new',
    sessionId: state.session.id,
    loading: state.session.loading,
    hasDetail: Boolean(state.session.detail),
    error: state.session.error,
    hasMoreEarlier: state.session.hasMoreEarlier,
    loadingEarlier: state.session.loadingEarlier,
    messageCount: state.session.messages.length,
  }));
  const scrollRef = useRef(null);
  const contentRef = useRef(null);
  const followRef = useRef(true);
  const prependRef = useRef(null);
  const [following, setFollowing] = useState(true);

  const scrollToBottom = (behavior = 'auto') => {
    const element = scrollRef.current;
    if (element) element.scrollTo({ top: element.scrollHeight, behavior });
  };

  // New session or route change: start at the bottom and follow.
  useLayoutEffect(() => {
    followRef.current = true;
    setFollowing(true);
    scrollToBottom();
  }, [view.sessionId, view.isNew]);

  // Follow content growth while the reader is near the bottom.
  useEffect(() => {
    const content = contentRef.current;
    if (!content || typeof ResizeObserver !== 'function') return undefined;
    const observer = new ResizeObserver(() => {
      const element = scrollRef.current;
      if (!element) return;
      if (prependRef.current !== null) {
        element.scrollTop = element.scrollHeight - prependRef.current;
        prependRef.current = null;
        return;
      }
      if (followRef.current) scrollToBottom();
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, []);

  const onScroll = () => {
    const element = scrollRef.current;
    if (!element) return;
    const near = element.scrollHeight - element.scrollTop - element.clientHeight < FOLLOW_THRESHOLD_PX;
    if (near !== followRef.current) {
      followRef.current = near;
      setFollowing(near);
    }
  };

  const onLoadEarlier = async () => {
    const element = scrollRef.current;
    if (element) prependRef.current = element.scrollHeight - element.scrollTop;
    await loadEarlierMessages();
  };

  return html`<div class="session-view" data-testid="session-view" data-session-id=${view.sessionId || undefined}>
    <div class="session-scroll" ref=${scrollRef} onScroll=${onScroll}>
      <div class="reading-column session-content" ref=${contentRef}>
        ${view.isNew ? html`<${EmptySession} />` : html`
          ${view.hasMoreEarlier ? html`<div class="load-earlier">
            <${Button} size="sm" variant="ghost" icon="arrow-up" pending=${view.loadingEarlier} onClick=${onLoadEarlier} data-testid="load-earlier">
              ${view.loadingEarlier ? t('shell.messages.loadingEarlier') : t('shell.messages.loadEarlier')}
            <//>
          </div>` : null}
          ${view.loading && !view.hasDetail ? html`<div class="session-loading" aria-live="polite">
            <span class="sr-only">${t('shell.messages.loading')}</span>
            <${Skeleton} lines=${3} /><${Skeleton} lines=${4} />
          </div>` : null}
          ${view.error && !view.hasDetail ? html`<${EmptyState} icon="alert-circle" title=${t('shell.session.loadFailed')} body=${view.error.message} role="alert">
            <${Button} size="sm" icon="refresh" onClick=${() => refreshSession()}>${t('common.retry')}<//>
          <//>` : null}
          <${GoalBanner} />
          <${MessageList} />
          <${PlanInputCard} />
          <${ApprovalCard} />
        `}
      </div>
    </div>
    <div class="session-dock">
      ${!following && !view.isNew ? html`<div class="back-to-latest">
        <${Button} size="sm" variant="secondary" icon="arrow-down" onClick=${() => { followRef.current = true; setFollowing(true); scrollToBottom('smooth'); }} data-testid="back-to-latest">
          ${t('shell.messages.backToLatest')}
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
