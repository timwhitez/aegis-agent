// Product bugs found by this suite and routed to the UI owners. A matching
// audit violation is reported as a known failure (manifest + summary) instead
// of failing the run; anything else still fails. Remove an entry once fixed:
// the summary lists entries that were not observed in a run.
export const KNOWN_UI_BUGS = [
  {
    id: 'context-refetch-on-tab-switch',
    title: 'Inspector Context re-requests /api/sessions/{id}/context every time the tab is re-opened (Tasks → Context) although the session detail did not change; earlier builds of this branch loaded it once (lazy-load once + explicit Refresh)',
    audit: 'behaviour',
    test: () => false,
  },
  {
    id: 'approval-recovery-intent-blocks-new-target',
    title: 'After a refused linked-legacy approval (APPROVAL_RECOVERY_REQUIRED) the saved recovery_required intent makes Approve on a newly reviewed target only re-check the old request id; no new approval can be sent from that browser (spec 17 §7.13: a new target can be reviewed and approved). The Goal tab no longer offers the mission fact-repair that used to release the intent.',
    audit: 'behaviour',
    test: () => false,
  },
  {
    id: 'timeline-event-type-i18n',
    title: 'zh-CN Timeline shows humanized English fallbacks for event types without a dictionary key (e.g. "Task created", "Task updated")',
    audit: 'i18n',
    test: (violation) => /: (Task created|Task updated)$/.test(violation),
  },
  {
    id: 'phase-plan-approval-i18n',
    title: 'zh-CN Inspector Overview shows the session phase "plan_approval" as the humanized English fallback "Plan approval" (missing phase.plan_approval key)',
    audit: 'i18n',
    test: (violation) => /: Plan approval$/.test(violation),
  },
  {
    id: 'timeline-filter-hit-target',
    title: 'Timeline kind filter buttons (.insp-segment) are 28px tall on a fine pointer (< 32px)',
    audit: 'hitTargets',
    test: (violation) => /^button\.insp-segment /.test(violation),
  },
  {
    id: 'toast-close-hit-target',
    title: 'Toast close buttons (icon-btn-sm "关闭通知" / "Dismiss notification") are 31x31 on a fine pointer (< 32px)',
    audit: 'hitTargets',
    test: (violation) => /^button\.icon-btn\.btn-ghost\.icon-btn-sm 3[01]x3[01] <32 "(关闭通知|Dismiss[^"]*)"/.test(violation),
  },
  {
    id: 'history-filter-hit-target',
    title: 'Sessions page status filter buttons (.segmented-btn) are 28px tall on a fine pointer (< 32px)',
    audit: 'hitTargets',
    test: (violation) => /^button\[history-filter-[a-z]+\]\.segmented-btn[^ ]* \d+x2\d <32 /.test(violation),
  },
  {
    id: 'history-row-link-hit-target',
    title: 'Sessions page row title links (history-row-link) are 18px tall (< 32px desktop, < 44px at 390px)',
    audit: 'hitTargets',
    test: (violation) => /^a\[history-row-link\]\.history-row-link \d+x1\d <(32|44) /.test(violation),
  },
  {
    id: 'composer-context-action-width-mobile',
    title: 'Composer context actions ("查看" / "取消" on the plan hint and revising bar) are 42px wide at 390px (< 44x44)',
    audit: 'hitTargets',
    test: (violation) => /^button(\[composer-revise-cancel\])?\.composer-context-action 4[0-3]x4\d <44 /.test(violation),
  },
  {
    id: 'inspector-tab-width-mobile',
    title: 'Mobile inspector tabs with two-character zh-CN labels are 42px wide (< 44x44 at 390px)',
    audit: 'hitTargets',
    test: (violation) => /^button\[inspector-tab-[a-z]+\]\.tab 4[0-3]x4\d <44 /.test(violation),
  },
];
