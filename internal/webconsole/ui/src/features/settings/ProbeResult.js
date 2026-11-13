// Result panel of POST /api/config/test: separates "request accepted" from
// "readable thinking observed", lists provider option facts and, on failure,
// the typed error class with backend detail and suggested action.
import { html } from '../../lib/html.js';
import { t, tx } from '../../lib/i18n.js';
import { Icon } from '../../ui/Icon.js';
import { IconButton } from '../../ui/IconButton.js';
import { toggleStateLabel } from '../../lib/settings-model.js';
import { probeFactRows, probeObservations } from './settings-helpers.js';
import { Raw, richText } from './rich.js';

function Title({ result }) {
  if (result.success && result.title?.key === 'settings.probe.passed') {
    const params = result.title.params || {};
    return richText('settings.probe.passed', {
      provider: html`<${Raw} value=${params.provider} />`,
      model: html`<${Raw} value=${params.model} />`,
      mode: tx(params.modeLabel || { key: 'settings.reasoning.default' }),
    });
  }
  return tx(result.title);
}

/** Props: result (interpretProbeResult/interpretProbeError output), raw, onDismiss. */
export function ProbeResult({ result, raw, onDismiss }) {
  if (!result) return null;
  const facts = probeFactRows(result, raw);
  const observations = result.thinking ? probeObservations(result) : [];
  return html`<div class=${`st-probe tone-${result.success ? 'success' : 'danger'}`} data-testid="settings-probe-result" data-success=${String(result.success)}>
    <div class="st-probe-head">
      <span class="st-probe-icon"><${Icon} name=${result.success ? 'check' : 'alert-circle'} size=${16} /></span>
      <p class="st-probe-title">
        <${Title} result=${result} />
      </p>
      <${IconButton} icon="x" size="sm" label=${t('settings.probe.dismiss')} onClick=${onDismiss} />
    </div>
    ${result.thinking ? html`<p class="st-probe-thinking">${tx(result.thinking.key)}</p>` : null}
    ${result.message ? html`<p class="st-probe-line"><${Raw} value=${result.message.raw} /></p>` : null}
    ${result.code || result.detail || result.action || result.strategy || result.finishMessage || facts.length ? html`<dl class="st-probe-facts">
      ${result.code ? html`<div><dt>${t('settings.probe.fact.code')}</dt><dd><code translate=${false}>${result.code}</code></dd></div>` : null}
      ${result.detail ? html`<div><dt>${t('settings.probe.fact.detail')}</dt><dd><${Raw} value=${result.detail.raw} /></dd></div>` : null}
      ${result.action ? html`<div><dt>${t('settings.probe.fact.action')}</dt><dd><${Raw} value=${result.action.raw} /></dd></div>` : null}
      ${facts.map((fact) => html`<div><dt>${t(fact.key)}</dt><dd>${'bool' in fact ? tx(toggleStateLabel(fact.bool)) : html`<${Raw} value=${fact.value} />`}</dd></div>`)}
      ${result.strategy ? html`<div><dt>${t('settings.probe.fact.strategy')}</dt><dd><${Raw} value=${result.strategy.raw} /></dd></div>` : null}
      ${result.finishMessage ? html`<div><dt>${t('settings.probe.fact.finishMessage')}</dt><dd><${Raw} value=${result.finishMessage.raw} class="st-probe-quote" /></dd></div>` : null}
    </dl>` : null}
    ${observations.length ? html`<ul class="st-probe-observed" role="list" aria-label=${t('settings.probe.observed.label')}>
      ${observations.map((item) => html`<li class=${item.observed ? 'is-yes' : 'is-no'}>
        <${Icon} name=${item.observed ? 'check' : 'x'} size=${13} />
        <span>${t(item.key)}</span>
        <span class="sr-only">${item.observed ? t('settings.probe.observed.yes') : t('settings.probe.observed.no')}</span>
      </li>`)}
    </ul>` : null}
  </div>`;
}
