// Compact cards rendered under goal and child-agent tool steps. Raw model
// values (summary, kind, ids, agent names) sit in their own translate="no"
// nodes; fixed labels and fallbacks are separate operator text.
import { html } from '../../../lib/html.js';
import { formatDateTime, t, tx } from '../../../lib/i18n.js';
import { agentToolResultView, goalToolCallBody, goalToolCard, shortId } from '../../../lib/session-model.js';
import { openSession } from '../../../state/actions.js';
import { Badge } from '../../../ui/Badge.js';
import { Button } from '../../../ui/Button.js';
import { Icon } from '../../../ui/Icon.js';
import { Slot } from './Slot.js';
import { badgeTone, slotText } from './model.js';

/** Goal progress summary: operator label, then raw kind and summary (or their fallbacks). */
export function GoalActionSummary({ summary }) {
  if (!summary) return null;
  const facts = summary.rawFacts || {};
  const fallbacks = summary.fallbackKeys || {};
  const kind = facts.kind || '';
  const text = facts.summary || '';
  return html`<span class="slot goal-action">
    <span class="slot-label">${slotText(summary)}</span>
    ${kind ? html`<span class="goal-kind" translate="no">${kind}</span>` : fallbacks.kind ? html`<span class="goal-kind is-fallback">${t(fallbacks.kind)}</span>` : null}
    ${text ? html`<span class="slot-raw" translate="no">${text}</span>` : fallbacks.summary ? html`<span class="slot-secondary">${t(fallbacks.summary)}</span>` : null}
  </span>`;
}

function ChipSlot({ slot }) {
  if (!slot) return null;
  return html`<span class="chip"><${Slot} slot=${slot} /></span>`;
}

export function GoalStepCard({ step }) {
  const body = step.kind === 'tool' ? goalToolCallBody({ name: step.name, arguments: step.rawArguments }) : null;
  const card = step.result && !step.result.is_error ? goalToolCard(step.result) : null;
  if (!body && !card) return null;
  const objective = body?.objective || (card && !card.goalOff ? card.objective : null);
  const chips = [...(body?.chips || []), ...(card && !card.goalOff ? [...card.metrics, ...card.counts] : [])];
  const progress = card && !card.goalOff ? card.latestProgress : null;
  return html`<div class="goal-card" data-testid="goal-tool-card" data-goal-tool=${step.name}>
    <div class="goal-card-head">
      <${Icon} name="target" size=${14} class="goal-card-icon" />
      <span class="goal-card-action">${body ? slotText(body.action) : t('goal.tool.state')}</span>
      ${card && !card.goalOff ? html`<${Badge} tone=${badgeTone(card.tone)}>${tx(card.statusLabel)}<//>` : null}
      ${card?.goalId?.raw ? html`<span class="chip"><span class="meta-chip-label">${t('session.goal.id')}</span><span translate="no">${card.goalId.raw}</span></span>` : null}
    </div>
    ${card?.goalOff ? html`<p class="goal-card-note">${slotText(card.title)}</p>` : null}
    ${body?.summary ? html`<p class="goal-card-summary" translate="no">${body.summary.raw}</p>` : null}
    ${objective ? html`<p class="goal-card-note">${slotText(objective)}</p>` : null}
    ${chips.length ? html`<div class="chip-row">${chips.map((chip, index) => html`<${ChipSlot} key=${index} slot=${chip} />`)}</div>` : null}
    ${progress ? html`<div class="goal-card-progress">
      <span class="goal-card-progress-title">${slotText(card.latestProgressTitle)}</span>
      ${progress.kind ? html`<span class="goal-kind" translate="no">${progress.kind}</span>` : html`<span class="goal-kind is-fallback">${t('goal.progress.kindFallback')}</span>`}
      ${progress.summary ? html`<span translate="no">${progress.summary}</span>` : html`<span class="slot-secondary">${t('goal.progress.summaryFallback')}</span>`}
    </div>` : null}
    ${card?.updatedAt ? html`<span class="goal-card-time">${t('session.goal.updated', { at: formatDateTime(card.updatedAt) })}</span>` : null}
  </div>`;
}

function OpenChildButton({ sessionId }) {
  if (!sessionId) return null;
  return html`<${Button} size="sm" variant="ghost" iconRight="arrow-right" onClick=${() => openSession(sessionId)} data-testid="open-child-session" data-session-id=${sessionId}>${t('session.step.openChild')}<//>`;
}

function AgentRow({ label, status, statusLabel, tone, sessionId, meta, error }) {
  const badge = badgeTone(tone);
  return html`<div class="agent-row" data-status=${status}>
    <div class="agent-row-top">
      <span class=${`status-dot tone-${badge === 'success' ? 'success' : badge === 'warning' ? 'warning' : badge === 'danger' ? 'danger' : 'neutral'}`} aria-hidden="true"></span>
      <${Slot} slot=${label} class="agent-row-label" />
      <${Badge} tone=${badge}>${tx(statusLabel)}<//>
      ${meta ? html`<span class="agent-row-meta" translate="no">${meta}</span>` : null}
      <span class="agent-row-spacer"></span>
      <${OpenChildButton} sessionId=${sessionId} />
    </div>
    ${error ? html`<p class="agent-row-error"><${Slot} slot=${error} /></p>` : null}
  </div>`;
}

export function AgentStepCard({ step }) {
  const view = step.result && !step.result.is_error ? agentToolResultView(step.result) : null;
  if (!view) return null;
  if (view.kind === 'agent') {
    return html`<div class="agent-card" data-testid="agent-tool-card">
      <${AgentRow} label=${view.label} status=${view.status} statusLabel=${view.statusLabel} tone=${view.tone} sessionId=${view.sessionId} meta=${shortId(view.sessionId)} error=${view.error} />
    </div>`;
  }
  return html`<div class="agent-card" data-testid="agent-tool-card">
    <p class="agent-card-title">${t('session.step.agents')}</p>
    ${view.rows.map((row) => html`<${AgentRow} key=${row.id} label=${row.label} status=${row.status} statusLabel=${row.statusLabel} tone=${row.tone} sessionId=${row.sessionId} meta=${row.model?.raw || shortId(row.id)} error=${row.error} />`)}
  </div>`;
}
