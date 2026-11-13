import { html, useEffect, useId, useLayoutEffect, useRef } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { Portal } from './Portal.js';
import { IconButton } from './IconButton.js';
import { acquireLayoutInert, focusableElements, trapTabKey } from './focus.js';

/**
 * Modal layer rendered in a Portal with focus trap, Esc to close, focus
 * restore and an inert background.
 *
 * <Dialog title description? onClose placement="center|top|left|right"
 *   size="sm|md|lg" initialFocusRef? showClose? labelledBy? testId? class?>…</Dialog>
 * Mount it only while open (the parent controls visibility).
 */
export function Dialog(props) {
  return html`<${Portal}><${DialogFrame} ...${props} /><//>`;
}

function DialogFrame({
  title = '',
  description = '',
  onClose,
  placement = 'center',
  size = 'md',
  initialFocusRef,
  showClose = true,
  hideTitle = false,
  role = 'dialog',
  testId,
  class: className = '',
  children,
  footer = null,
}) {
  const frameRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useLayoutEffect(() => {
    const previous = document.activeElement;
    const release = acquireLayoutInert();
    const frame = frameRef.current;
    const target = initialFocusRef?.current || focusableElements(frame).find((element) => !element.classList.contains('dialog-close')) || frame;
    target?.focus({ preventScroll: true });
    return () => {
      release();
      if (previous && typeof previous.focus === 'function' && document.contains(previous)) {
        previous.focus({ preventScroll: true });
      }
    };
  }, []);

  useEffect(() => {
    const onKeyDown = (event) => {
      if (event.key === 'Escape' && !event.isComposing) {
        event.stopPropagation();
        onCloseRef.current?.();
      }
    };
    const frame = frameRef.current;
    frame?.addEventListener('keydown', onKeyDown);
    return () => frame?.removeEventListener('keydown', onKeyDown);
  }, []);

  return html`<div class=${`dialog-layer placement-${placement}`} onMouseDown=${(event) => { if (event.target === event.currentTarget) onCloseRef.current?.(); }}>
    <div
      ref=${frameRef}
      class=${`dialog dialog-${size} dialog-${placement} ${className}`.trim()}
      role=${role}
      aria-modal="true"
      aria-labelledby=${title ? titleId : undefined}
      aria-describedby=${description ? descriptionId : undefined}
      tabindex="-1"
      data-testid=${testId}
      onKeyDown=${(event) => trapTabKey(event, frameRef.current)}
    >
      ${title || showClose ? html`<div class=${`dialog-header ${hideTitle ? 'is-title-hidden' : ''}`}>
        ${title ? html`<h2 id=${titleId} class=${hideTitle ? 'sr-only' : 'dialog-title'}>${title}</h2>` : null}
        ${showClose ? html`<${IconButton} icon="x" label=${t('common.close')} size="sm" class="dialog-close" onClick=${() => onCloseRef.current?.()} />` : null}
      </div>` : null}
      ${description ? html`<p id=${descriptionId} class="dialog-description">${description}</p>` : null}
      <div class="dialog-body">${children}</div>
      ${footer ? html`<div class="dialog-footer">${footer}</div>` : null}
    </div>
  </div>`;
}
