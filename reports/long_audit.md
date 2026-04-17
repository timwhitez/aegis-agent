# Long Consistency Audit

## Scope
- Audited spec, README, implementation, and tests for the requested focus areas.
- Focus areas: built-in tool surface vs registry, `experimental web` positioning, multi-agent default semantics, and web frontend `history` / `refresh` / `clear` actions against backend contracts.
- Result: no blocking drift found. One previously repaired frontend interaction remains aligned: after clearing history from the history view, the UI resets chat state and returns to chat.

## Finding 1: Tool Surface Matches The Registry
Severity: pass  
Confidence: high

Evidence:
- `internal/tools/registry.go:153` registers the core built-in tool surface: shell, file read/write/edit, glob/grep, finish, skill loading, todo tools, and task graph tools.
- `internal/tools/registry.go:171` appends multi-agent tools only when `cfg.Runtime.MultiAgent.Enabled` is true.
- `internal/tools/registry_test.go:245` verifies `agent_spawn`, `agent_status`, and `agent_list` are registered by default and hidden when `runtime.multi_agent.enabled=false`.
- `internal/tools/registry_test.go:120` covers shell/file tool metadata behavior, including compaction-relevant metadata.
- `README.md:245` documents default exposure of `agent_spawn` / `agent_status` / `agent_list` and the explicit disable switch.

Assessment:
- The implementation, tests, and README agree that built-in tools are registry-owned and multi-agent tools are default-visible but configurable.
- No spec correction is required.

## Finding 2: `experimental web` Remains Explicitly Experimental
Severity: pass  
Confidence: high

Evidence:
- `README.md:233` exposes experimental commands only under `./bin/go-cli-agent experimental <delegate|children|queue|tui|web> ...`.
- `README.md:241` states default `go-cli-agent` help shows only core v1 commands and experimental entries appear only under the explicit `experimental` subtree.
- `README.md:258` keeps `internal/webconsole` described as a local Web console service/API/frontend, not a stable public SDK surface.
- `README.md:259` states `pkg/agent` remains core-only and experimental/store-only surfaces stay internal until stabilized.
- `spec/17-web-console.md` positions the web console as an explicit experimental extension surface.

Assessment:
- Docs preserve the CLI-first/core-first story and keep the web console out of the default command surface.
- No drift found between README positioning and the web console implementation location.

## Finding 3: Multi-Agent Default Semantics Are Consistent
Severity: pass  
Confidence: high

Evidence:
- `internal/config/config.go:225` sets `Runtime.MultiAgent.Enabled` to true by default with `MaxDepth: 4`.
- `internal/config/config_test.go:76` asserts multi-agent is enabled by default.
- `internal/tools/registry.go:171` makes default enablement mean tool exposure, not automatic child creation.
- `internal/tools/registry_test.go:245` verifies tools are visible by default and hidden when disabled.
- `README.md:245` states default exposure lets the master agent decide whether to create child agents, and operators can disable exposure with `runtime.multi_agent.enabled=false`.

Assessment:
- The intended semantics are correctly represented: default-enabled multi-agent means available tools, while actual delegation remains a master/agent decision.
- No code or doc change is required.

## Finding 4: Web `history` / `refresh` / `clear` Actions Match Backend Contracts
Severity: pass  
Confidence: high

Evidence:
- `internal/webconsole/service.go:210` exposes `GET /api/history`.
- `internal/webconsole/service.go:214` exposes `POST /api/sessions/clear`.
- `internal/webconsole/service.go:500` deletes a single session tree and maps missing sessions to 404.
- `internal/webconsole/service.go:510` rejects clear-history while this web console has active session handles.
- `internal/webconsole/service.go:518` rejects clear-history while queue jobs are running.
- `internal/webconsole/assets/app.js:2215` fetches paginated history from `/api/history`.
- `internal/webconsole/assets/app.js:2361` deletes one history session via `DELETE /api/sessions/{id}` and refreshes history/overview afterward.
- `internal/webconsole/assets/app.js:2385` clears all history via `POST /api/sessions/clear`, resets chat/session UI state, refetches history, refreshes overview, and returns from history to chat when appropriate.

Assessment:
- Frontend calls use the backend routes and respect the destructive-action contract through user confirmation and backend conflict handling.
- The clear-history UX no longer leaves the user stranded on an empty history view after the backing store is removed.

## Minor Notes
- No additional small drift was identified during this audit, so no new code change was necessary in this pass.
- Existing targeted coverage already exercises the key risky seams: default multi-agent exposure, tool registry registration, web service routes, and session/history deletion behavior.
