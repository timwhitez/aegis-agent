// Builds the command palette command list from the current state (spec §7.4).
import { t } from '../lib/i18n.js';
import { shortId } from '../lib/format.js';
import { summaryTitle } from '../lib/session-list.js';
import { canInterruptSession, canStopSession } from '../state/selectors.js';
import {
  focusComposer, navigate, newSession, openSession, openShortcuts, toggleGoalMode, toggleInspector,
  toggleLocale, togglePlanMode, toggleSidebar,
} from '../state/actions.js';
import { interruptRun, stopRun } from '../state/session-actions.js';

export function buildShellCommands(state) {
  const sessionRoute = state.route.name === 'new' || state.route.name === 'session';
  const id = state.session.id;
  const commands = [
    { id: 'new-session', group: 'actions', icon: 'plus', title: t('shell.cmd.newSession'), keywords: ['new', 'session', 'xinjian'], shortcut: ['mod', 'shift', 'O'], run: newSession },
    { id: 'go-sessions', group: 'navigation', icon: 'history', title: t('shell.cmd.goSessions'), keywords: ['history', 'sessions'], run: () => navigate('/sessions') },
    { id: 'go-skills', group: 'navigation', icon: 'box', title: t('shell.cmd.goSkills'), keywords: ['skills'], run: () => navigate('/skills') },
    { id: 'go-workspace', group: 'navigation', icon: 'folder', title: t('shell.cmd.goWorkspace'), keywords: ['workspace', 'files'], run: () => navigate('/workspace') },
    { id: 'go-settings', group: 'navigation', icon: 'settings', title: t('shell.cmd.goSettings'), keywords: ['settings', 'config', 'provider'], run: () => navigate('/settings') },
    { id: 'toggle-sidebar', group: 'view', icon: 'panel-left', title: t('shell.cmd.toggleSidebar'), keywords: ['sidebar'], shortcut: ['mod', 'B'], run: toggleSidebar },
    { id: 'toggle-inspector', group: 'view', icon: 'panel-right', title: t('shell.cmd.toggleInspector'), keywords: ['inspector'], shortcut: ['mod', '.'], enabled: sessionRoute, run: () => toggleInspector() },
    { id: 'toggle-language', group: 'view', icon: 'globe', title: t('shell.cmd.toggleLanguage'), keywords: ['language', 'locale', 'english', '中文'], run: toggleLocale },
    { id: 'shortcuts', group: 'view', icon: 'keyboard', title: t('shell.cmd.shortcuts'), keywords: ['keyboard', 'help'], shortcut: ['?'], run: openShortcuts },
    { id: 'stop', group: 'session', icon: 'square', title: t('shell.cmd.stop'), keywords: ['stop'], enabled: canStopSession(state), run: () => stopRun(id) },
    { id: 'interrupt', group: 'session', icon: 'hand', title: t('shell.cmd.interrupt'), keywords: ['interrupt'], enabled: canInterruptSession(state), run: () => interruptRun(id) },
    { id: 'focus-composer', group: 'composer', icon: 'message-square', title: t('shell.cmd.focusComposer'), keywords: ['composer', 'input'], shortcut: ['/'], enabled: sessionRoute, run: focusComposer },
    { id: 'toggle-goal', group: 'composer', icon: 'target', title: t('shell.cmd.toggleGoal'), keywords: ['goal'], enabled: sessionRoute, run: () => toggleGoalMode() },
    { id: 'toggle-plan', group: 'composer', icon: 'clipboard-list', title: t('shell.cmd.togglePlan'), keywords: ['plan'], enabled: sessionRoute, run: () => togglePlanMode() },
  ];
  const recent = state.overview.sessions.slice(0, 50).map((item) => {
    const title = summaryTitle(item, state.overview.titles);
    return {
      id: `session:${item.id}`,
      group: 'recent',
      icon: 'message-square',
      title: title || t('shell.session.untitled', { id: shortId(item.id) }),
      rawTitle: Boolean(title),
      subtitle: item.id,
      keywords: [item.id, item.workdir || '', item.agent_name || ''].filter(Boolean),
      run: () => openSession(item.id),
    };
  });
  return { commands, recent };
}
