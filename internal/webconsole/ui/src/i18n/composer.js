// composer area copy. Owned by the composer feature worker.
// Every key must start with "composer." or one of the extra prefixes below
// (key families emitted by lib/ modules) and exist in both locales
// (validation/scripts/ui_i18n_test.mjs enforces parity and prefixing).
export default {
  prefixes: ['send.', 'kind.', 'approval.'],
  'zh-CN': {},
  en: {},
};
