// Raw payload block (tool arguments / results): label, copy, and clipping of
// long output behind a "show all" toggle. Content is translate="no".
import { html, useState } from '../../../lib/html.js';
import { formatNumber, t } from '../../../lib/i18n.js';
import { CopyButton } from '../../../ui/CopyButton.js';
import { clipText } from './model.js';

export function CodeBlock({ label, text, tone = '', copyLabel = '', emptyLabel = '', maxLines = 18, maxChars = 4000, badge = null, testId }) {
  const [expanded, setExpanded] = useState(false);
  const value = String(text ?? '');
  const clip = clipText(value, { maxLines, maxChars });
  const shown = expanded || !clip.truncated ? value : clip.text;
  const moreLabel = clip.totalLines > maxLines
    ? t('session.code.showAllLines', { count: formatNumber(clip.totalLines) })
    : t('session.code.showAllChars', { count: formatNumber(clip.totalChars) });
  return html`<figure class=${`code-block ${tone ? `tone-${tone}` : ''}`.trim()} data-testid=${testId}>
    <figcaption class="code-block-head">
      <span class="code-block-label">${label}</span>
      ${badge}
      <span class="code-block-spacer"></span>
      ${value ? html`<${CopyButton} text=${value} label=${copyLabel || t('common.copy')} size="sm" />` : null}
    </figcaption>
    ${value
      ? html`<pre class=${`code-block-body ${clip.truncated && !expanded ? 'is-clipped' : ''}`.trim()} translate="no"><code>${shown}</code></pre>`
      : html`<p class="code-block-empty">${emptyLabel}</p>`}
    ${clip.truncated ? html`<button type="button" class="code-block-more" aria-expanded=${String(expanded)} onClick=${() => setExpanded(!expanded)}>
      ${expanded ? t('session.showLess') : moreLabel}
    </button>` : null}
  </figure>`;
}
