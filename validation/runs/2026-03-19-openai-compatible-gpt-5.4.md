# Real Task Validation Run

## Run Metadata

- date: 2026-03-19
- config: `validation/config.openai-compatible.yaml`
- provider: `openai-compatible`
- base_url: `http://69.63.215.40:24634/v1`
- model: `gpt-5.4`
- wire_api: `responses`
- session_dir: `/root/.go-cli-agent/validation-sessions`
- tester: Codex

## Preflight

- `./build.sh`: pass
- `./test.sh`: pass
- `doctor --skip-probe --json`: pass
  - initial validation config used repo-local `validation/sessions` on `/mnt/...`; `doctor` warned that WSL mount permissions did not honor owner-only mode
  - validation config was updated to use `/root/.go-cli-agent/validation-sessions`, after which `session.dir.mode` and `session.root.strategy` both passed
- `probe-provider --json`: pass
  - returned one `finish` tool call with `finish_message="provider probe ok"`

## Mid-run Fixes

- Fixed validation-only skill tool schemas so `openai-compatible` Responses no longer rejects empty-object parameter schemas.
- Fixed `run/exec` config resolution so relative `skills.dirs` and other config paths stay anchored to the invocation directory instead of drifting with `--workdir`.
- Fixed compaction recent-tail selection so any retained `tool_results` keep the matching assistant `tool_calls` needed for OpenAI Responses replay. Added a regression test and reran the original RT01 steer path successfully.
- Strengthened prompt/tool guidance:
  - system prompt now separates `Tool Use`, `Skills`, and `Skill Command Tools`
  - `load_skill` description now states that it is for skill names, not tool names
  - skill command tool descriptions now explicitly say they are direct-call tools, not files or shell commands

## Scenario Results

### RT01 Self Repo Architecture Audit

- command: `exec --workdir . "Audit the current repository..."`
- session_id: `20260319-102039-0bffbe`
- status: failed
- artifact: terminal only
- observations:
  - the model over-read large spec and implementation files, repeatedly triggered compaction, and did not converge cleanly
  - a corrective `steer --interrupt` was accepted, but the resumed OpenAI replay failed with `No tool call found for function call output with call_id ...`
  - this exposed a real replay/control-path issue under long, tool-heavy sessions plus steer

### RT01R Self Repo Architecture Audit Re-run After Compaction Fix

- command: `exec --workdir . "Audit the current repository..."`
- steer_command: `steer 20260319-110744-5e16f9 --interrupt --message "Stop reading additional files..."`
- session_id: `20260319-110744-5e16f9`
- status: completed
- artifact: terminal only
- observations:
  - reran the same long self-repo audit prompt against `openai-compatible` Responses after patching compaction replay preservation
  - `steer --interrupt` was accepted while the session was active
  - the resumed session completed with a `finish` tool call instead of failing replay
  - this validates the concrete fix for the earlier `No tool call found for function call output with call_id ...` failure class

### RT02 Self Repo Provider Drift Audit

- command: `exec --workdir . "Inspect only spec/03-provider-contracts.md and provider adapters..."`
- session_id: `20260319-103947-15d24d`
- status: completed
- artifact: terminal only
- observations:
  - successful constrained self-repo audit
  - model reported several likely drifts worth manual follow-up:
    - OpenAI adapter omits explicit `store=false` when `req.Store` is nil
    - Anthropic and Google thinking enablement does not require `include_thoughts=true`
    - Google defaults unknown finish reasons to `done_candidate`
    - Anthropic/Google do not fully populate `TurnResult` fields promised by spec

### RT03 Docset Synthesis

- command: `exec --workdir validation/workspaces/docset "...write reports/product-brief.md..."`
- session_id: `20260319-102216-a51f9c`
- status: completed
- artifact: `validation/workspaces/docset/reports/product-brief.md`
- observations:
  - clean multi-file read -> write -> finish path
  - respected local `AGENTS.md` and did not modify source docs

### RT04 Incident Triage

- command: `exec --workdir validation/workspaces/incident "...write reports/incident-summary.md..."`
- session_id: `20260319-102253-81e629`
- status: completed
- artifact: `validation/workspaces/incident/reports/incident-summary.md`
- observations:
  - successful log/config triage
  - output cleanly separated confirmed facts from inference and produced a usable timeline/root-cause memo

### RT05 Patch Smallest Bug

- command: `exec --workdir validation/workspaces/patch "...fix tests, write reports/change-summary.md..."`
- session_id: `20260319-102350-19cc95`
- status: completed
- artifact: `validation/workspaces/patch/reports/change-summary.md`
- observations:
  - successfully diagnosed failing Python tests, edited `inventory.py`, re-ran tests, and wrote summary
  - before the fix, the model wasted early turns trying to read `../spec/...` paths outside the workspace because outer-scope `AGENTS.md` instructions were taken too literally

### RT06 Explicit Skill Loading

- command: `exec --workdir . "Before any other analysis, call load_skill for repo_audit..."`
- session_id: `20260319-102450-f6a5fc`
- status: completed
- artifact: terminal only
- observations:
  - explicitly called `load_skill` for `repo_audit`
  - then limited itself to `README.md` and `AGENTS.md` and finished correctly

### RT07 Command Tool Usage

- command: `exec --workdir validation/workspaces/docset "...must call markdown_inventory and pretty_json_args..."`
- session_id: `20260319-103849-04053c`
- status: completed
- artifact: `validation/workspaces/docset/reports/tool-assisted-brief-v3.md`
- observations:
  - first attempt before runtime fix failed badly:
    - session `20260319-102519-35e810`
    - command tools were not actually loaded because config-relative `skills.dirs` had been resolved against `--workdir`
    - model kept searching for matching files/commands and finally had to be stopped with a failure note
  - after fixing config resolution and improving prompt/description handling:
    - direct tool calls succeeded in a clean `/tmp` diagnostic run (`20260319-103806-903c6f`)
    - the original docset scenario also succeeded (`20260319-103849-04053c`), calling both `markdown_inventory` and `pretty_json_args` before writing `reports/tool-assisted-brief-v3.md`

### RT08 Awaiting Input And Continue

- run_command: `run --workdir validation/workspaces/docset "...summarize in three bullets then wait..."`
- continue_command: `continue 20260319-104110-098b24 --message "...write reports/continue-brief.md..."`
- session_id: `20260319-104110-098b24`
- status: completed
- artifact: `validation/workspaces/docset/reports/continue-brief.md`
- observations:
  - `run` correctly ended in `awaiting_input`
  - `continue` resumed the same session and completed cleanly with a report write + `finish`

### RT09 Live Steer Reprioritization

- run_command: `exec --workdir /tmp "First call shell sleep 10, then finish with original message..."`
- steer_command: `steer 20260319-104408-037495 --interrupt --message "Ignore the original finish message..."`
- session_id: `20260319-104408-037495`
- status: completed
- artifact: terminal only
- observations:
  - clean control-plane validation without docset/repo noise
  - `session.steer.interrupt_requested` was emitted
  - running `shell sleep 10` was interrupted
  - steer message was accepted and the final `finish` message changed from the original value to `changed by steer`
  - an earlier attempt on a faster task missed the running window and returned `session is not running; use continue instead`, which is acceptable but shows a narrow race on fast tasks

### RT10 Durable Task Graph

- command: `exec --workdir validation/workspaces/incident "...create tasks, update states, write reports/taskboard-summary.md..."`
- session_id: `20260319-104447-7f4f90`
- status: completed
- artifact: `validation/workspaces/incident/reports/taskboard-summary.md`
- observations:
  - successfully created three durable tasks, including one dependency
  - moved one task to `completed`, unblocked the dependent task, marked another `in_progress`, and summarized the board

## Issues Found

### Fixed During Run

- `run/exec` incorrectly resolved relative config paths against `--workdir`, which made local skill command tools disappear in nested or external workdirs. This was the root cause behind the command-tool discovery failure and is now fixed.
- Command-tool/system guidance was too weak. Prompt sections and tool descriptions were tightened so skill command tools are presented as direct-call capabilities instead of ambiguous names.
- Compaction could retain a `tool_results` message while dropping the assistant `tool_calls` message that introduced the same `call_id`, which broke OpenAI Responses replay after `steer --interrupt`. Recent-message selection now preserves these dependencies, and the original RT01 scenario was rerun successfully.

### Remaining Or Follow-up Issues

- Long, tool-heavy self-repo sessions can still degrade into excessive file reads and repeated compaction before converging. This is visible in `RT01`.
- Nested task workspaces under `go-cli-agent/` still inherit outer `AGENTS.md` instructions that mention repo-level spec files. When those paths fall outside the current workspace boundary, the model may burn turns retrying inaccessible `../spec/...` reads.
- Built-in `grep` can pull in binary noise if a prompt points it at a broad tree that includes compiled artifacts such as `bin/go-cli-agent`, which inflates context and hurts convergence.
