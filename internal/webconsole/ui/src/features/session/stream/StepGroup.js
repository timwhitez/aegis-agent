// Step groups: consecutive tool calls (and thinking-only turns) between two
// assistant texts, folded into one summary row. Auto-expanded while running
// or when a step failed; each step expands to its arguments and results.
import { html, useId, useState } from '../../../lib/html.js';
import { t } from '../../../lib/i18n.js';
import { toolResultText } from '../../../lib/session-model.js';
import { Badge } from '../../../ui/Badge.js';
import { Icon } from '../../../ui/Icon.js';
import { Spinner } from '../../../ui/Spinner.js';
import { CodeBlock } from './CodeBlock.js';
import { Markdown } from './Markdown.js';
import { Slot } from './Slot.js';
import { AgentStepCard, GoalActionSummary, GoalStepCard } from './ToolCards.js';
import { useTicker } from './hooks.js';
import {
  formatElapsed,
  resultMetadataEntries,
  stepArgumentsText,
  stepDurationMs,
  stepGroupAutoOpen,
  stepGroupDurationMs,
  stepGroupLabel,
  stepGroupSummary,
  toolIcon,
} from './model.js';

const STATUS_KEYS = {
  ok: 'session.step.status.ok',
  error: 'session.step.status.error',
  running: 'session.step.status.running',
  pending: 'session.step.status.pending',
};

function StepStatus({ status }) {
  const label = t(STATUS_KEYS[status] || STATUS_KEYS.pending);
  if (status === 'running') return html`<span class="step-status is-running"><${Spinner} size=${13} /><span class="sr-only">${label}</span></span>`;
  const icon = status === 'ok' ? 'check' : status === 'error' ? 'alert-circle' : 'circle-dot';
  return html`<span class=${`step-status is-${status || 'pending'}`} title=${label}><${Icon} name=${icon} size=${13} /><span class="sr-only">${label}</span></span>`;
}

function StepSummary({ step }) {
  if (step.kind === 'thinking') return html`<span class="slot">${t('step.thinking', { chars: step.thinking.length })}</span>`;
  if (step.isGoal && step.kind === 'tool') return html`<${GoalActionSummary} summary=${step.summary} />`;
  return html`<${Slot} slot=${step.summary} />`;
}

function ResultBlock({ result, index }) {
  const [metaOpen, setMetaOpen] = useState(false);
  const metaId = useId();
  const text = toolResultText(result);
  const meta = resultMetadataEntries(result, 24);
  return html`<div class="step-result">
    <${CodeBlock}
      label=${result.is_error ? t('session.step.errorOutput') : t('session.step.result')}
      tone=${result.is_error ? 'danger' : ''}
      text=${text}
      copyLabel=${t('session.step.copyResult')}
      emptyLabel=${t('tool.noOutput')}
      badge=${result.final ? html`<${Badge} tone="success">${t('session.step.final')}<//>` : null}
      testId=${'step-result-' + index}
    />
    ${meta.length ? html`<button type="button" class="stream-link-btn step-meta-toggle" aria-expanded=${String(metaOpen)} aria-controls=${metaId} onClick=${() => setMetaOpen(!metaOpen)}>
      <${Icon} name="chevron-right" size=${12} class="step-meta-chevron" />${t('session.step.metadataCount', { count: meta.length })}
    </button>` : null}
    ${meta.length && metaOpen ? html`<dl class="step-meta" id=${metaId} aria-label=${t('session.step.metadata')}>
      ${meta.map(([key, value]) => html`<div class="step-meta-item"><dt translate="no">${key}</dt><dd translate="no">${value}</dd></div>`)}
    </dl>` : null}
  </div>`;
}

function StepDetail({ step }) {
  if (step.kind === 'thinking') {
    return html`<div class="step-detail" data-testid="step-detail"><div class="step-thinking"><${Markdown} cacheKey=${step.id} text=${step.thinking} /></div></div>`;
  }
  const args = stepArgumentsText(step);
  const isShell = step.name === 'shell' && typeof step.args?.command === 'string';
  return html`<div class="step-detail" data-testid="step-detail">
    ${step.kind === 'tool' ? html`<${CodeBlock}
      label=${isShell ? t('session.step.command') : t('session.step.args')}
      text=${args}
      copyLabel=${t('session.step.copyArgs')}
      emptyLabel=${t('tool.noArguments')}
      maxLines=${12}
      testId="step-args"
    />` : null}
    ${step.results.length
      ? step.results.map((result, index) => html`<${ResultBlock} key=${index} result=${result} index=${index} />`)
      : html`<p class="step-detail-empty">${step.status === 'running' ? t('session.step.waiting') : t('session.step.noResult')}</p>`}
  </div>`;
}

function StepRow({ step }) {
  const [open, setOpen] = useState(step.status === 'error');
  const detailId = useId();
  const now = useTicker(step.status === 'running', 1000);
  const duration = stepDurationMs(step, now);
  const icon = step.kind === 'thinking' ? 'brain' : toolIcon(step.name);
  return html`<li class=${`step status-${step.status || 'pending'} ${open ? 'is-open' : ''}`} data-testid="tool-step" data-tool=${step.name} data-status=${step.status} data-step-kind=${step.kind}>
    <button type="button" class="step-row" aria-expanded=${String(open)} aria-controls=${detailId} onClick=${() => setOpen(!open)}>
      <span class="step-icon"><${Icon} name=${icon} size=${14} /></span>
      ${step.kind === 'thinking'
        ? html`<span class="step-name is-label">${t('session.thinking.label')}</span>`
        : html`<span class="step-name" translate="no">${step.name}</span>`}
      <span class="step-summary"><${StepSummary} step=${step} /></span>
      ${duration !== null && duration >= 1000 ? html`<span class="step-time">${formatElapsed(duration)}</span>` : null}
      <${StepStatus} status=${step.status} />
    </button>
    ${step.isGoal ? html`<${GoalStepCard} step=${step} />` : null}
    ${step.isDelegate ? html`<${AgentStepCard} step=${step} />` : null}
    <div id=${detailId} hidden=${!open}>${open ? html`<${StepDetail} step=${step} />` : null}</div>
  </li>`;
}

/** Plain step list (used for raw finish calls folded into a final answer). */
export function StepList({ steps, testId }) {
  return html`<ol class="step-list is-standalone" role="list" data-testid=${testId}>
    ${steps.map((step) => html`<${StepRow} key=${step.id} step=${step} />`)}
  </ol>`;
}

export function StepGroup({ group }) {
  const [override, setOverride] = useState(null);
  const listId = useId();
  const summary = stepGroupSummary(group);
  const open = override === null ? stepGroupAutoOpen(summary) : override;
  const now = useTicker(summary.running, 1000);
  const duration = stepGroupDurationMs(group, now);
  const statusIcon = summary.running
    ? html`<${Spinner} size=${14} />`
    : html`<${Icon} name=${summary.errors ? 'alert-circle' : summary.toolCount === 0 ? 'brain' : 'list-checks'} size=${14} />`;
  return html`<section class=${`step-group status-${group.status} ${open ? 'is-open' : ''}`} data-testid="step-group" data-status=${group.status} data-step-count=${summary.count}>
    <button type="button" class="step-group-toggle" aria-expanded=${String(open)} aria-controls=${listId} onClick=${() => setOverride(!open)}>
      <span class="step-group-icon">${statusIcon}</span>
      <span class="step-group-label">${stepGroupLabel(summary)}</span>
      ${summary.running && summary.current
        ? html`<span class="step-group-tools" translate="no">${summary.current.name}</span>`
        : summary.names.length ? html`<span class="step-group-tools"><span translate="no">${summary.names.join(' · ')}</span>${summary.moreNames ? html` <span>${t('session.steps.more', { count: summary.moreNames })}</span>` : null}</span>` : null}
      ${summary.errors ? html`<span class="step-group-errors">${t('session.steps.errors', { count: summary.errors })}</span>` : null}
      ${duration !== null && (summary.running || duration >= 1000) ? html`<span class="step-group-time">${formatElapsed(duration)}</span>` : null}
      <${Icon} name="chevron-right" size=${14} class="step-group-chevron" />
    </button>
    <ol class="step-list" id=${listId} role="list" hidden=${!open}>
      ${open ? group.steps.map((step) => html`<${StepRow} key=${step.id} step=${step} />`) : null}
    </ol>
  </section>`;
}
