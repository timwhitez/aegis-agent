// session area copy. Owned by the session feature worker.
// Every key must start with "session." or one of the extra prefixes below
// (key families emitted by lib/ modules) and exist in both locales
// (validation/scripts/ui_i18n_test.mjs enforces parity and prefixing).
export default {
  prefixes: ['actor.', 'activity.', 'stage.', 'step.', 'tool.', 'goal.', 'background.', 'markdown.'],
  'zh-CN': {},
  en: {},
};
