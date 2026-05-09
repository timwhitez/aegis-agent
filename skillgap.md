# Skill Gap Notes

This file records skill-side issues observed in session `20260509-053303-043dc1`.
The current round intentionally does not modify skill files.

## pentest-toolset

- Required reference docs should be bundled under the registered skill root and documented as read-only skill resources, not as workspace files. Current harness behavior already supports `read_file` paths such as `skills/pentest-toolset/references/...`; missing reference files or workspace-local reference assumptions should be handled in the skill package/docs.
- Report finalization order is underspecified. The skill should state that supporting records such as `reports/progress.md` and `reports/validation.md` should be written before the final report, and that any later supporting-doc update requires a final-report edit before `finish`.
- Shell artifact examples should include parent-directory preparation for report outputs. The harness should not auto-create directories for shell commands, but the skill can show safe command templates that start with `mkdir -p reports`.
- Payload-heavy examples should discourage fragile inline shell/Python quoting for SQLi, JSON, and raw-request bodies. Prefer JSON request files and the CLI's `payload`, `probe`, or `replay` primitives where possible.
- Closure guidance should explicitly tell the agent to stop broad exploration once evidence is sufficient for the report and to use existing artifacts for finalization.
