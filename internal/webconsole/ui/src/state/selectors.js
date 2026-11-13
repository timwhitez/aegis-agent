// Pure selectors over the store state shared by the shell and features.
import { t } from '../lib/i18n.js';
import { shortId } from '../lib/format.js';
import { sessionDetailDisplayStatus, sessionSummaryDisplayStatus, sessionHasDirectControl, isStoppableStatus } from '../lib/status.js';
import { summaryTitle } from '../lib/session-list.js';

export const isSessionRoute = (state) => state.route.name === 'session' || state.route.name === 'new';

export function currentSummary(state) {
  const id = state.session.id;
  return id ? (state.overview.sessions.find((item) => item.id === id) || null) : null;
}

/** Display status of the open session ('' on /). */
export function currentDisplayStatus(state) {
  if (!state.session.id) return '';
  if (state.session.detail) return sessionDetailDisplayStatus(state.session.detail);
  const summary = currentSummary(state);
  return summary ? sessionSummaryDisplayStatus(summary) : '';
}

/**
 * { text, raw } title for the open session: raw=true means durable text that
 * must render with translate="no"; raw=false is localized fallback copy.
 */
export function currentSessionTitle(state) {
  const id = state.session.id;
  if (!id) return { text: t('shell.topbar.newSession'), raw: false };
  const learned = state.overview.titles[id];
  const summary = currentSummary(state);
  const metadata = state.session.detail?.metadata || {};
  const text = summaryTitle({ id, ...(summary || {}), agent_name: summary?.agent_name || metadata.agent_name }, learned ? { [id]: learned } : {});
  return text ? { text, raw: true } : { text: t('shell.session.untitled', { id: shortId(id) }), raw: false };
}

/** Stop is offered while the open session is running (old canUseStopSessionControl). */
export function canStopSession(state) {
  return Boolean(state.session.id) && isStoppableStatus(state.session.detail?.state?.status) && state.session.generating;
}

/** Interrupt requires a run handle owned by this server process. */
export function canInterruptSession(state) {
  return Boolean(state.session.id) && state.session.generating && sessionHasDirectControl(state.session.detail);
}
