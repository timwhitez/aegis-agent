// Approval CAS browser acceptance (spec/17-web-console.md §7.13): the browser
// approves exactly the target it displayed. Stale versions, linked mission
// edits (reviewed in the Goal tab, approved on the single inline approval
// path) and coverage confirmation against a changed scope are rejected before
// execution and shown as "计划已更新，请重新审阅" / "The plan has changed.
// Please review it again." in zh-CN/en × desktop/mobile.
//
// Only page A's detail GETs are frozen (to keep displaying the reviewed
// target); approval, revision, mission mutations and provider execution use
// the real service. Runs as the "approvals" area of webconsole_e2e.mjs.
import assert from 'node:assert/strict';
import {
  allowStale, approvalPosts, approvalProfiles, assertApprovalPost, assertStaleResponse, assertStaleUI, assertUnaccepted, clickApprove,
  coherentSnapshot, createFixture, displayedTarget, durableFacts, freezeDetail, missionPlan, nextPost, openApproval,
  patchMission, refreshOpenSession, settleToasts, staleToastCount, suffixOf, target, waitForPlan,
} from './e2e/approvals.mjs';
import { gotoApp, openInspectorTab, storeState, tid, waitSessionLoaded } from './e2e/ui.mjs';

export async function runApprovalCASE2E(h) {
  const evidence = [];
  for (const profile of approvalProfiles()) {
    const suffix = suffixOf(profile);
    const { locale } = profile;
    const context = await h.newContext({ locale, mobile: profile.mobile });
    const a = await h.newPage(context, 'cas-A-' + suffix);
    const b = await h.newPage(context, 'cas-B-' + suffix);
    await Promise.all([gotoApp(h, a, '/'), gotoApp(h, b, '/')]);
    const settled = async (page) => h.until(async () => !(await storeState(page, 'session.pendingAction')), 'approval action settled', 30000, 40);

    await h.check('approval CAS: stale V1 rejects the real V2 before execution (' + suffix + ')', async () => {
      assert.equal(await a.locator('html').getAttribute('lang'), locale);
      await settleToasts(a);
      const id = await createFixture(h, 'version ' + suffix);
      const v1 = await coherentSnapshot(h, id);
      assert.equal(v1.plan_mode.plan_version, 1);
      let staleRequestID;
      const thaw = await freezeDetail(a, h, id, v1);
      try {
        await openApproval(a, id);
        await tid(a, 'approval-card').filter({ hasText: 'Original plan' }).waitFor();
        const reviewed = await displayedTarget(a);
        assert.deepEqual(reviewed, target(v1));
        await openApproval(b, id);
        await tid(b, 'revise-plan').click();
        await tid(b, 'composer-revising').waitFor();
        await tid(b, 'composer-input').fill('Use the narrower verified scope for the browser CAS test.');
        const revised = nextPost(b, h, id, 'planmode/revise');
        await tid(b, 'composer-input').press('Enter');
        assert.equal((await revised).status(), 202);
        const v2 = await waitForPlan(h, id, 2);
        await b.locator('[data-testid="approval-card"][data-plan-version="2"]').filter({ hasText: 'Revised plan' }).waitFor({ timeout: 60000 });
        assert.notEqual(v2.plan_mode.approval_revision, reviewed.expected_revision);
        assert.deepEqual(await displayedTarget(a), reviewed, 'A still displays the reviewed V1');
        const before = await durableFacts(h, id);
        allowStale(h, id);
        const posts = approvalPosts(a, h, id);
        try {
          const pending = nextPost(a, h, id, 'planmode/approve');
          const toasts = await staleToastCount(a, locale);
          await clickApprove(h, a);
          const response = await pending;
          await assertStaleResponse(response, reviewed);
          await assertStaleUI(h, a, locale, toasts);
          await settled(a);
          assert.equal(posts.payloads.length, 1, 'a stale approval never retries with the latest target');
          staleRequestID = response.request().postDataJSON().approval_request_id;
          await assertUnaccepted(h, id, before, 2);
          evidence.push({ scenario: 'version', locale, viewport: profile.size, session_id: id, reviewed, status: response.status(), code: (await response.json()).code });
        } finally {
          posts.dispose();
        }
        await h.capture(a, 'approval-cas-version-' + suffix, { retainToasts: true });
      } finally {
        await thaw();
      }
      // Positive control: a fresh review of V2 is accepted with a new request id.
      await a.reload();
      await waitSessionLoaded(a, id);
      await a.locator('[data-testid="approval-card"][data-plan-version="2"]').waitFor();
      const reviewedV2 = await displayedTarget(a);
      const approved = nextPost(a, h, id, 'planmode/approve');
      await clickApprove(h, a);
      const response = await approved;
      assert.equal(response.status(), 202, await response.text());
      assertApprovalPost(response.request().postDataJSON(), reviewedV2);
      assert.notEqual(response.request().postDataJSON().approval_request_id, staleRequestID, 'a fresh review starts a new request');
      const done = await h.waitDetail(id, (d) => d.state?.status === 'completed' && !d.active_handle, 'completed');
      assert.equal(done.plan_mode.approved_revision, reviewedV2.expected_revision);
      assert.ok(done.messages.some((message) => message.meta?.source === 'planmode_approval' && message.meta.approved_revision === reviewedV2.expected_revision));
    }, { page: a });

    await h.check('approval CAS: a linked mission change rejects the same plan version (' + suffix + ')', async () => {
      await settleToasts(a);
      const id = await createFixture(h, 'linked ' + suffix, { linked: true });
      await patchMission(h, id, missionPlan(true));
      const v1 = await coherentSnapshot(h, id);
      assert.equal(v1.plan_mode.linked_goal_id, v1.goal.goal_id);
      const thaw = await freezeDetail(a, h, id, v1);
      try {
        await openApproval(a, id, 'goal');
        await tid(a, 'inspector-goal').filter({ hasText: 'Reviewed linked scope' }).waitFor();
        // One approval path: the Goal tab links to the inline card instead of approving itself.
        await tid(a, 'inspector-goal-goto-approval').waitFor();
        assert.equal(await tid(a, 'inspector-goal-approve-plan').count(), 0, 'no second approval control for a linked plan');
        const reviewed = await displayedTarget(a);
        await patchMission(h, id, {
          requirements: [{ id: 'requirement_scope', text: 'The linked mission now includes an additional deliverable.' }],
          features: [{ id: 'feature_scope', title: 'Changed linked scope', status: 'pending', claimed_assertions: ['validation_scope'] }],
        });
        const changed = await coherentSnapshot(h, id);
        assert.equal(changed.plan_mode.plan_mode_id, reviewed.plan_mode_id);
        assert.equal(changed.plan_mode.plan_version, reviewed.plan_version);
        assert.notEqual(changed.plan_mode.approval_revision, reviewed.expected_revision);
        await b.goto(h.baseURL + '/s/' + encodeURIComponent(id));
        await waitSessionLoaded(b, id);
        await openInspectorTab(b, 'goal');
        await tid(b, 'inspector-goal').filter({ hasText: 'Changed linked scope' }).waitFor();
        assert.deepEqual(await displayedTarget(a), reviewed);
        const before = await durableFacts(h, id);
        allowStale(h, id);
        const posts = approvalPosts(a, h, id);
        try {
          await tid(a, 'inspector-goal-goto-approval').click();
          await a.waitForFunction(() => document.activeElement?.closest?.('[data-testid="approval-card"]'));
          const pending = nextPost(a, h, id, 'planmode/approve');
          const toasts = await staleToastCount(a, locale);
          await clickApprove(h, a);
          const response = await pending;
          await assertStaleResponse(response, reviewed);
          await assertStaleUI(h, a, locale, toasts);
          await settled(a);
          assert.equal(posts.payloads.length, 1);
          await assertUnaccepted(h, id, before, reviewed.plan_version);
          evidence.push({ scenario: 'linked_mission', locale, viewport: profile.size, session_id: id, reviewed, status: response.status(), code: (await response.json()).code });
        } finally {
          posts.dispose();
        }
        await h.capture(a, 'approval-cas-linked-mission-' + suffix, { retainToasts: true });
      } finally {
        await thaw();
      }
    }, { page: a });

    await h.check('approval CAS: coverage confirmation keeps the reviewed target and detects a changed scope (' + suffix + ')', async () => {
      await settleToasts(a);
      const id = await createFixture(h, 'coverage ' + suffix, { linked: true });
      await patchMission(h, id, missionPlan(false));
      const v1 = await coherentSnapshot(h, id);
      assert.equal(v1.goal_facts.coverage.approval_blocked, true);
      allowStale(h, id, { responses: 3, label: 'coverage rejection or stale target' });
      const thaw = await freezeDetail(a, h, id, v1);
      try {
        await openApproval(a, id);
        const reviewed = await displayedTarget(a);
        const posts = approvalPosts(a, h, id);
        try {
          const first = nextPost(a, h, id, 'planmode/approve');
          await clickApprove(h, a);
          const blocked = await first;
          assert.equal(blocked.status(), 409);
          const blockedJSON = await blocked.json();
          assert.equal(blockedJSON.code, 'APPROVAL_REJECTED');
          assert.equal(blockedJSON.approval.lookup.receipt.stage, 'rejected');
          assert.match(JSON.stringify(blockedJSON), /coverage/i);
          const blockedRequest = blocked.request().postDataJSON();
          assertApprovalPost(blockedRequest, reviewed);
          const dialog = tid(a, 'confirm-dialog');
          await dialog.waitFor();
          assert.match(await dialog.innerText(), locale === 'zh-CN' ? /覆盖率|验证/ : /validation coverage/i);
          await h.capture(a, 'approval-cas-coverage-confirm-' + suffix);
          await patchMission(h, id, {
            features: [{ id: 'feature_scope', title: 'Changed coverage scope', status: 'pending', claimed_assertions: ['validation_scope'] }],
            milestones: [{ id: 'milestone_scope', title: 'Coverage milestone', status: 'pending', feature_ids: ['feature_scope'], validation_ids: ['validation_scope'] }],
          });
          const changed = await coherentSnapshot(h, id);
          assert.equal(changed.plan_mode.plan_mode_id, reviewed.plan_mode_id);
          assert.equal(changed.plan_mode.plan_version, reviewed.plan_version);
          assert.notEqual(changed.plan_mode.approval_revision, reviewed.expected_revision);
          assert.equal(changed.goal_facts.coverage.approval_blocked, false);
          const before = await durableFacts(h, id);
          const retry = nextPost(a, h, id, 'planmode/approve');
          const toasts = await staleToastCount(a, locale);
          await tid(a, 'confirm-dialog-confirm').click();
          const rejected = await retry;
          await assertStaleResponse(rejected, { ...reviewed, override_coverage: true });
          await assertStaleUI(h, a, locale, toasts);
          await settled(a);
          await assertUnaccepted(h, id, before, reviewed.plan_version);
          assert.equal(posts.payloads.length, 2, 'coverage never approves another target automatically');
          assertApprovalPost(posts.payloads[0], reviewed);
          assertApprovalPost(posts.payloads[1], reviewed, true);
          assert.notEqual(posts.payloads[1].approval_request_id, blockedRequest.approval_request_id, 'the override uses a new request id');
          evidence.push({ scenario: 'coverage_confirmation', locale, viewport: profile.size, session_id: id, reviewed, status: rejected.status(), code: (await rejected.json()).code });
        } finally {
          posts.dispose();
        }
        await h.capture(a, 'approval-cas-coverage-' + suffix, { retainToasts: true });
      } finally {
        await thaw();
      }
      // Unfrozen: the open dialog notices a newly displayed target locally.
      await settleToasts(a);
      await patchMission(h, id, missionPlan(false));
      await a.reload();
      await waitSessionLoaded(a, id);
      const reviewed = await displayedTarget(a);
      const posts = approvalPosts(a, h, id);
      try {
        const first = nextPost(a, h, id, 'planmode/approve');
        await clickApprove(h, a);
        const blocked = await first;
        assert.equal(blocked.status(), 409);
        assert.equal((await blocked.json()).approval.lookup.receipt.stage, 'rejected');
        const dialog = tid(a, 'confirm-dialog');
        await dialog.waitFor();
        await patchMission(h, id, { requirements: [{ id: 'requirement_scope', text: 'This scope changed while the coverage dialog was open.' }] });
        await refreshOpenSession(a);
        assert.notEqual((await displayedTarget(a)).expected_revision, reviewed.expected_revision);
        const before = await durableFacts(h, id);
        const toasts = await staleToastCount(a, locale);
        await tid(a, 'confirm-dialog-confirm').click();
        await assertStaleUI(h, a, locale, toasts);
        await settled(a);
        await assertUnaccepted(h, id, before, reviewed.plan_version);
        assert.equal(posts.payloads.length, 1, 'a changed display never approves or retries automatically');
        assertApprovalPost(posts.payloads[0], reviewed);
        evidence.push({ scenario: 'coverage_display_changed', locale, viewport: profile.size, session_id: id, reviewed, approval_requests: 1 });
      } finally {
        posts.dispose();
      }
      await h.capture(a, 'approval-cas-coverage-refreshed-' + suffix, { retainToasts: true });
    }, { page: a });
    await context.close();
  }
  h.evidence.approval_cas = { scenarios: evidence.length, evidence };
}
