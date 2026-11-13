// inspector area copy. Owned by the inspector feature worker.
// Every key must start with "inspector." or one of the extra prefixes below
// (key families emitted by lib/ modules) and exist in both locales
// (validation/scripts/ui_i18n_test.mjs enforces parity and prefixing).
export default {
  prefixes: ['event.', 'eventType.', 'timeline.', 'tasks.', 'files.', 'budget.', 'budgetStatus.', 'subagent.', 'notification.', 'goalStatus.', 'planStatus.', 'phase.'],
  'zh-CN': {},
  en: {},
};
