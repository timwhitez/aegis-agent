// Plan tab: Plan Mode objective, status, versions, summary, assumptions,
// risks, verification, the full Markdown plan, pending questions (read-only)
// and history. Approve / revise / cancel / answer live on the inline cards
// in the conversation; "前往审批" / "前往回答" reveal them (closing an
// overlay inspector first).
import { html, useMemo } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { renderMarkdownCached } from '../../lib/markdown.js';
import { describeTimelineItem, maybeArray, shortId } from '../../lib/session-model.js';
import { useStore } from '../../state/hooks.js';
import { Badge } from '../../ui/Badge.js';
import { Button } from '../../ui/Button.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { planHistory } from './inspector-model.js';
import { Notice, Raw, Section, StatusPill, TimeText, Txt, num, revealInlineCard, text } from './parts.js';

function RawList({ items, testId }) {
  const values = maybeArray(items).map((item) => String(item ?? '').trim()).filter(Boolean);
  if (!values.length) return null;
  return html`<ul class="insp-bullets" role="list" data-testid=${testId}>
    ${values.map((value) => html`<li><${Raw}>${value}<//></li>`)}
  </ul>`;
}

function PendingQuestions({ request }) {
  const questions = maybeArray(request?.questions);
  if (!questions.length) return null;
  return html`<ol class="insp-questions" data-testid="inspector-plan-questions">
    ${questions.map((question) => html`<li class="insp-question">
      <p class="insp-question-head">
        ${question.header ? html`<${Raw} class="insp-question-tag">${question.header}<//>` : null}
        ${question.question ? html`<${Raw}>${question.question}<//>` : null}
      </p>
      ${maybeArray(question.options).length ? html`<ul class="insp-options" role="list">
        ${question.options.map((option) => html`<li>
          <${Raw} class="insp-option-label">${option.label || ''}<//>
          ${option.description ? html`<${Raw} class="insp-option-desc">${option.description}<//>` : null}
        </li>`)}
      </ul>` : null}
    </li>`)}
  </ol>`;
}

const TONE_DOT = { danger: 'danger', live: 'success', queued: 'warning' };

function HistoryList({ entries }) {
  if (!entries.length) return html`<p class="insp-muted">${t('inspector.plan.noHistory')}</p>`;
  return html`<ol class="insp-history" data-testid="inspector-plan-history">
    ${entries.map((entry) => {
      if (entry.kind === 'approval') {
        return html`<li class="insp-history-item">
          <span class="insp-dot tone-success" aria-hidden="true"></span>
          <div class="insp-history-body">
            <p class="insp-history-title">${t('inspector.plan.approvedVersion', { version: entry.version || '–' })}</p>
            <p class="insp-card-meta">
              ${entry.by || entry.source ? html`<${Raw}>${entry.by || entry.source}<//>` : null}
              ${(entry.by || entry.source) && entry.time ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}
              <${TimeText} at=${entry.time} />
            </p>
          </div>
        </li>`;
      }
      const descriptor = describeTimelineItem(entry.item);
      return html`<li class="insp-history-item">
        <span class=${`insp-dot tone-${TONE_DOT[descriptor.tone] || 'neutral'}`} aria-hidden="true"></span>
        <div class="insp-history-body">
          <p class="insp-history-title"><${Txt} d=${descriptor.title} /></p>
          ${descriptor.copy ? html`<p class="insp-card-copy"><${Txt} d=${descriptor.copy} /></p>` : null}
          <p class="insp-card-meta">
            ${descriptor.meta ? html`<${Txt} d=${descriptor.meta} />` : null}
            ${descriptor.meta && entry.time ? html`<span class="insp-sep" aria-hidden="true">·</span>` : null}
            <${TimeText} at=${entry.time} />
          </p>
        </div>
      </li>`;
    })}
  </ol>`;
}

export function PlanTab({ mode = 'panel' }) {
  const view = useStore((state) => ({ locale: state.ui.locale, detail: state.session.detail }));
  const detail = view.detail;
  const planMode = detail?.plan_mode || null;
  const markdown = String(planMode?.plan_markdown || '').trim();
  const markdownHTML = useMemo(
    () => (markdown ? renderMarkdownCached('inspector-plan:' + view.locale + ':' + (planMode?.plan_mode_id || '') + ':' + (planMode?.plan_version || 0), markdown, { t }) : ''),
    [markdown, planMode?.plan_mode_id, planMode?.plan_version, view.locale],
  );
  const history = useMemo(() => planHistory(detail), [detail]);
  if (!planMode) {
    return html`<${EmptyState} icon="clipboard-list" title=${t('inspector.plan.emptyTitle')} body=${t('inspector.plan.emptyBody')} data-testid="inspector-plan-empty" />`;
  }
  const status = String(planMode.status || 'planning');
  const awaitingApproval = status === 'awaiting_approval';
  const awaitingInput = status === 'awaiting_user_input';
  const pendingRequest = awaitingInput ? planMode.pending_request : null;
  return html`<div class="insp-tab insp-plan" data-testid="inspector-plan" data-plan-status=${status}>
    <header class="insp-tab-head">
      <div class="insp-tab-head-row">
        <${StatusPill} status=${status} label=${text({ key: 'planStatus.' + status, params: { fallback: status } })} testId="inspector-plan-status" />
        ${planMode.plan_version ? html`<${Badge} tone="neutral">${t('inspector.plan.version', { version: num(planMode.plan_version) })}<//>` : null}
        ${planMode.approved_version ? html`<${Badge} tone="success">${t('inspector.plan.approved', { version: num(planMode.approved_version) })}<//>` : null}
        ${planMode.plan_mode_id ? html`<span class="insp-raw insp-mono insp-id" translate="no" title=${planMode.plan_mode_id}>${shortId(planMode.plan_mode_id)}</span>` : null}
      </div>
      ${planMode.objective ? html`<p class="insp-objective"><${Raw}>${planMode.objective}<//></p>` : null}
    </header>

    ${awaitingApproval ? html`<${Notice} tone="warning" testId="inspector-plan-awaiting"
      title=${t('inspector.plan.awaitingTitle')}
      actions=${html`<${Button} size="sm" variant="secondary" iconRight="arrow-down" onClick=${() => revealInlineCard('approval-card', mode)} data-testid="inspector-plan-goto-approval">${t('inspector.plan.gotoApproval')}<//>`}
    >${t('inspector.plan.awaitingBody')}<//>` : null}
    ${awaitingInput ? html`<${Notice} tone="warning" testId="inspector-plan-awaiting-input"
      title=${t('inspector.plan.inputTitle')}
      actions=${html`<${Button} size="sm" variant="secondary" iconRight="arrow-down" onClick=${() => revealInlineCard('plan-input-card', mode)} data-testid="inspector-plan-goto-input">${t('inspector.plan.gotoInput')}<//>`}
    >${t('inspector.plan.inputBody')}<//>` : null}

    ${pendingRequest ? html`<${Section} title=${t('inspector.plan.questions')}>
      <${PendingQuestions} request=${pendingRequest} />
    <//>` : null}

    ${planMode.summary ? html`<${Section} title=${t('inspector.plan.summary')} testId="inspector-plan-summary">
      <p class="insp-prose-text"><${Raw}>${planMode.summary}<//></p>
    <//>` : null}
    ${maybeArray(planMode.assumptions).length ? html`<${Section} title=${t('inspector.plan.assumptions')}><${RawList} items=${planMode.assumptions} /><//>` : null}
    ${maybeArray(planMode.risks).length ? html`<${Section} title=${t('inspector.plan.risks')}><${RawList} items=${planMode.risks} /><//>` : null}
    ${maybeArray(planMode.verification).length ? html`<${Section} title=${t('inspector.plan.verification')}><${RawList} items=${planMode.verification} testId="inspector-plan-verification" /><//>` : null}

    ${markdownHTML ? html`<${Section} title=${t('inspector.plan.markdown')} testId="inspector-plan-markdown">
      <div class="insp-markdown" translate="no" dangerouslySetInnerHTML=${{ __html: markdownHTML }}></div>
    <//>` : !planMode.summary && !pendingRequest ? html`<p class="insp-muted">${t('inspector.plan.drafting')}</p>` : null}

    <${Section} title=${t('inspector.plan.history')}>
      <${HistoryList} entries=${history} />
    <//>
  </div>`;
}
