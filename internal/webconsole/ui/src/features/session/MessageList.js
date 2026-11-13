// PLACEHOLDER — owned by the session feature worker.
// Contract:
//   export function MessageList(): reads useStore(s => s.session.messages,
//   s.session.liveEvents, s.session.detail) and renders the message stream per
//   spec §7.1 (user bubbles, assistant prose via lib/markdown.js, step groups
//   from lib/session-model.js, thinking collapsed, activity indicator).
//   Test ids: data-testid="message" (one per rendered message, with
//   data-role), "tool-step" for step rows. Raw text inside translate="no".
//   Scrolling, "load earlier" and "back to latest" are handled by SessionView.
import { html } from '../../lib/html.js';
import { useStore } from '../../state/hooks.js';

export function MessageList() {
  const messages = useStore((state) => state.session.messages, Object.is);
  return html`<ol class="message-list" role="list">
    ${messages.filter((message) => message.text && (message.role === 'user' || message.role === 'assistant')).map((message) => html`<li
      key=${message.id}
      class=${`message message-${message.role}`}
      data-testid="message"
      data-role=${message.role}
    ><div class="message-text" translate="no">${message.text}</div></li>`)}
  </ol>`;
}
