// Product bugs found by this suite and routed to the UI owners. A matching
// audit violation is reported as a known failure (manifest + summary) instead
// of failing the run; anything else still fails. Remove an entry once fixed:
// the summary lists entries that were not observed in a run.
//
// Entry shape: { id, title, audit: 'i18n' | 'hitTargets' | 'behaviour' | …,
//   test: (violation) => boolean }. Behavioural checks report through
// h.reportKnownBug(id, …), which fails for ids that are not listed here.
export const KNOWN_UI_BUGS = [];
