// Context tab: GET /api/sessions/{id}/context, requested only when this tab
// is opened or Refresh is clicked (spec 17 §7.4.1). The report is loaded once
// per visit of a session: switching to another tab and back reuses it
// (contextLoadPlan), Refresh loads again, and after a session switch the last
// report is shown while a fresh one loads. Memory only.
// Operator labels are translated; ids and agent roles stay raw.
import { html, useEffect, useRef, useState } from '../../lib/html.js';
import { t } from '../../lib/i18n.js';
import { getSessionContext } from '../../lib/api.js';
import { useLocale } from '../../state/hooks.js';
import { store } from '../../state/store.js';
import { Button } from '../../ui/Button.js';
import { EmptyState } from '../../ui/EmptyState.js';
import { Skeleton } from '../../ui/Skeleton.js';
import { contextLoadPlan, contextReportView } from './inspector-model.js';
import { Metrics, Notice, Raw, Section, duration, num } from './parts.js';

// A visit lasts while one session stays open; any session switch starts a new
// one. Read-only store subscription (never writes the store).
let visit = 0;
let visitSession = store.getState().session.id;
store.subscribe((state) => {
  if (state.session.id === visitSession) return;
  visitSession = state.session.id;
  visit += 1;
});

const reports = new Map(); // sessionId -> { report, visit } of the last loaded report
const inflight = new Map(); // sessionId -> { visit, promise } of the latest request

/** Latest /context request for a session; re-opening the tab joins one already running. */
function loadReport(id, { refresh = false } = {}) {
  const running = inflight.get(id);
  if (!refresh && running && running.visit === visit) return running.promise;
  const entry = { visit, promise: null };
  entry.promise = getSessionContext(id).then((report) => {
    if (inflight.get(id) === entry) {
      inflight.delete(id);
      reports.set(id, { report, visit: entry.visit });
    }
    return report;
  }, (error) => {
    if (inflight.get(id) === entry) inflight.delete(id);
    throw error;
  });
  inflight.set(id, entry);
  return entry.promise;
}

function errorInfo(error) {
  return { message: String(error?.message || ''), detail: String(error?.detail || ''), action: String(error?.action || '') };
}

export function ContextTab({ sessionId }) {
  useLocale();
  const [state, setState] = useState(() => {
    const plan = contextLoadPlan(reports.get(sessionId), { visit });
    return { report: plan.report, loading: plan.load, error: null };
  });
  const seq = useRef(0);
  const mounted = useRef(true);

  const load = async ({ refresh = false } = {}) => {
    const id = sessionId;
    const current = ++seq.current;
    setState((previous) => ({ ...previous, loading: true, error: null }));
    try {
      const report = await loadReport(id, { refresh });
      if (!mounted.current || current !== seq.current) return;
      setState({ report, loading: false, error: null });
    } catch (error) {
      if (!mounted.current || current !== seq.current) return;
      setState((previous) => ({ ...previous, loading: false, error: errorInfo(error) }));
    }
  };
  const reload = () => load({ refresh: true });

  useEffect(() => {
    mounted.current = true;
    if (contextLoadPlan(reports.get(sessionId), { visit }).load) load();
    return () => { mounted.current = false; };
  }, [sessionId]);

  const view = contextReportView(state.report);
  const refresh = html`<${Button} size="sm" variant="ghost" icon="refresh" pending=${state.loading} onClick=${reload} data-testid="inspector-context-refresh">
    ${state.loading ? t('inspector.context.refreshing') : t('inspector.context.refresh')}
  <//>`;

  if (!view) {
    if (state.error) {
      return html`<${EmptyState} icon="alert-circle" title=${t('inspector.context.errorTitle')} body=${state.error.message} role="alert" data-testid="inspector-context-error">
        <${Button} size="sm" icon="refresh" onClick=${reload}>${t('common.retry')}<//>
      <//>`;
    }
    return html`<div class="insp-loading" aria-live="polite" data-testid="inspector-context-loading">
      <p class="insp-muted">${t('inspector.context.loading')}</p>
      <${Skeleton} lines=${3} /><${Skeleton} lines=${3} />
    </div>`;
  }

  return html`<div class="insp-tab insp-context context-panel" data-testid="inspector-context" aria-busy=${state.loading ? 'true' : 'false'}>
    <header class="insp-tab-head">
      <div class="insp-tab-head-row is-spread">
        <div>
          <p class="insp-eyebrow">${t('inspector.context.report', { version: view.version })}</p>
          <h4 class="insp-context-root" translate="no" title=${view.rootId}>${view.rootId}</h4>
        </div>
        ${refresh}
      </div>
      <p class="insp-muted">${t('inspector.context.lazyNote')}</p>
    </header>
    ${state.error ? html`<${Notice} tone="warning" role="alert" title=${t('inspector.context.refreshFailed')}><${Raw}>${state.error.message}<//><//>` : null}
    <${Section} title=${t('inspector.context.budget')} testId="inspector-context-peaks">
      <${Metrics} class="is-3" items=${view.peaks.map((metric) => ({ label: t(metric.key), value: num(metric.value) }))} />
    <//>
    <${Section} title=${t('inspector.context.providerUsage')} testId="inspector-context-usage">
      <${Metrics} class="is-2" items=${view.usage.map((metric) => ({ label: t(metric.key), value: num(metric.value) }))} />
    <//>
    <${Section} title=${t('inspector.context.lineage')} testId="inspector-context-lineage">
      <${Metrics} class="is-3" items=${[
        ...view.lineage.map((metric) => ({ label: t(metric.key), value: num(metric.value) })),
        { label: t('inspector.context.wallTime'), value: duration(view.wallTimeMs) },
      ]} />
    <//>
    <${Section} title=${t('inspector.context.sessionDetail')} testId="inspector-context-sessions">
      ${view.sessions.length ? html`<table class="insp-table is-full">
        <thead><tr>
          <th scope="col">${t('inspector.context.session')}</th>
          <th scope="col">${t('inspector.context.peak')}</th>
          <th scope="col">${t('inspector.context.aggregate')}</th>
          <th scope="col">${t('inspector.context.requestsShort')}</th>
        </tr></thead>
        <tbody>${view.sessions.map((item) => html`<tr data-session-id=${item.id}>
          <th scope="row">
            <span class="insp-raw insp-mono" translate="no" title=${item.id}>${item.shortId}</span>
            <span class="insp-role">${item.isRoot ? t('inspector.context.root') : item.role ? html`<span translate="no">${item.role}</span>` : t('inspector.context.child')}</span>
          </th>
          <td>${num(item.peak)}</td><td>${num(item.aggregate)}</td><td>${num(item.requests)}</td>
        </tr>`)}</tbody>
      </table>` : html`<p class="insp-muted">${t('inspector.context.noRequests')}</p>`}
    <//>
    ${view.truncation ? html`<p class="insp-muted" data-testid="inspector-context-truncation">${t('inspector.context.bounded', { sessions: num(view.truncation.sessions), requests: num(view.truncation.requests) })}</p>` : null}
  </div>`;
}
