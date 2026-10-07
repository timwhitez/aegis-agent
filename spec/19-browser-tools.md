# Optional browser tools (#136)

Experimental Phase 16+ profile, default OFF. This adapter adds exactly
`browser_exec({"code": string})` and `browser_screenshot({})` to the existing
Registry/ExecContext/role/Plan Mode/hooks/result chain used by CLI and Web.
Arbitrary Python has the host account's filesystem/process/network authority;
clean environment/profile is not a sandbox. Shell sandbox configuration does
not sandbox this adapter. Chromium's own sandbox remains enabled. No model
agent, gateway, approval layer, policy engine, Python interception, vision
subsystem, browser workflow, queue lock or generation protocol is added.

## Configuration and installation

`tools.browser`: `enabled: false`, absolute private `install_root` (default
XDG_DATA_HOME/aegis-agent/browser-use, otherwise ~/.local/share/aegis-agent/browser-use),
optional absolute `browser_executable`, and `timeout_sec: 0` (inherit host
command timeout). Positive timeout never extends the host command deadline.
Invalid relative paths/negative timeout fail config validation. Disabled means
no registration/disclosure, filesystem initialization, installation or process.
Settings GET exposes read-only local mode/install target/sandbox facts without
credentials; changes use the existing explicit config-file target guidance.
No runtime install, browser download, upgrade, global skill write or OS change.

First supported install: Linux x86_64, CPython 3.12.3 / cpython-312-x86_64-linux-gnu.
Use scripts/browser-use/install.sh with absolute Python and target; uninstall
only a marked owned install with scripts/browser-use/uninstall.sh. The resolved
104-package wheel closure is requirements.lock, installed with --require-hashes
and --only-binary=:all: (no sdist/build closure or --no-deps). PyPI refresh on
2026-10-08 selected browser-use 0.13.11 and browser-harness 0.1.13. Wheel URLs,
SHA256 and installed source hashes/metadata/licenses are recorded alongside
this lock. Future upgrades require another source-contract review and lock.

## Fixed browser guidance

The skill catalog currently scans filesystem SKILL.md files and has no builtin
asset registration pattern. Ship the fixed helper guidance as a go:embed asset
at internal/tools/browser_guidance.md, attached to browser_exec's existing tool
description only when enabled. No browser skill is placed in the default
./skills scan, no global skill is written, and no new catalog mechanism is
introduced. Guidance works in arbitrary workspaces; disabled, explorer and
pending Plan Mode requests do not disclose browser tools or their guidance.

## Installed source contract and deltas

0.13.11 replaces the issue's 0.13.10. Installed official CLI still reads Python
from stdin and delegates to harness run; BU --version reports harness version,
and BU doctor --json is rejected. Only harness doctor --json
--require-existing-daemon is used: chrome_running=null and healthy=(daemon &&
browser_ready); it never starts/repairs/discovers another daemon or updates.
BU_AUTOSPAWN="0" remains truthy and is removed. BH_REQUIRE_EXISTING_DAEMON is
recognized only as "1". Private BH_RUNTIME_DIR uses bu.sock/bu.pid stems even
with BU_NAME; distinct names alone are not isolation. Actual helpers REPO_ROOT
is lib/python3.12 (helpers file parent.parent.parent), not cwd or venv root.
Daemon has the same handwritten dotenv loader as helpers (an additional source
contract recorded here). Both loaders bypass python-dotenv's disable flag.
Browser-use init imports logging_config with unconditional load_dotenv before
checking BROWSER_USE_SETUP_LOGGING; python-dotenv 1.2.2 disables discovery with
PYTHON_DOTENV_DISABLED=1. Config's env_file remains cwd-relative .env.

Fixed private Python argv uses isolated mode and a small pre-import launcher
then calls the installed official browser_use.cli.main or harness run.main.
This retains official stdin semantics and lets the pre-import check reject
unexpected .env/agent_helpers.py in the installed REPO_ROOT and owned helper
workspace before either import. It checks only these owned roots, never user
ancestor/global files. Dependencies/versions are checked via importlib.metadata.
The adapter starts browser_harness.daemon directly with its own process handle
instead of allowing ensure_daemon to detach an untracked process. This is the
closest owned-process equivalent of CLI lazy initialization; every subsequent
CLI call requires that daemon. No raw Python source analysis is performed.

## Lifecycle and environment

Lazy initialization allocates unique 0700 attempt directories below the
canonical session directory, with private profile/cookies/cache/downloads,
HOME/BH_HOME/config/tmp/workspace, plus a short random private IPC directory
under os.TempDir keyed by a hash of the canonical session directory. SessionID
already binds child and queue identity; no alternate ownership protocol.
Chrome and all adapter subprocesses receive TMPDIR=<ipc>/t (0700), since Chrome
creates com.google.Chrome.XXXXXX/SingletonSocket below TMPDIR. Before starting
any subprocess, validate the byte lengths of that worst-case socket path,
the longer Chromium prefix and <ipc>/bu.sock against Linux's 107-byte sun_path
capacity. An overly long
os.TempDir fails clearly rather than starting Chrome. Session-root tmp and
BH_TMP_DIR remain available for private screenshot PNGs and non-socket files;
profile/cache/download paths remain in the session root. Successful owned
cleanup removes the entire IPC directory, including t.
Calls in one registry session serialize with a mutex; sessions/children use
independent directories and process handles. Launch one owned Chrome with
--user-data-dir and --remote-debugging-port=0; read its DevToolsActivePort,
pass exact loopback BU_CDP_WS to owned daemon. Never pass --no-sandbox or
reuse a user's browser/profile. Startup failures include sandbox/permission
diagnostics. Wait for named daemon readiness, then force existing-only calls.
A dead daemon fails closed; no reconnect, retry or replay of effects. Continue
starts a fresh browser attempt and does not restore old code/PIDs/browser state.
After an interrupted attempt is closed and cleanup confirmed, a NEW explicit
call in the same active run can lazily allocate a fresh attempt (including after
accepted interrupt steering). The failed call is never retried or replayed;
a call observing a dead daemon still fails closed. Cleanup unknown blocks a
fresh attempt in that registry.

Start from filteredEnv, retain only safe locale/platform basics for this
adapter, replace task paths, remove ambient keys/proxies/auth/BU_* and BH_*
(including custom allowlisted values), inject BH_TELEMETRY=0,
ANONYMIZED_TELEMETRY=false, BH_UPDATE_CHECK=0, BH_RECORD=0,
BH_OPEN_LIVE_URL=0, PYTHON_DOTENV_DISABLED=1,
BROWSER_USE_SETUP_LOGGING=false. General shell allowlist stays unchanged.
Python's working directory is owned and clean. This blocks automatic ambient
credentials/telemetry, not an authorized Python program deliberately reading
host files or making network calls. Web pages are external data, not authority.

Existing tool cancellation kills CLI process groups. Timeout/interrupt also
closes owned browser and daemon; run exit (end/pause/abort/child stop/owner loss)
closes remaining handles and records cleanup success/error/unknown durably.
Owned process reaping and cancellation are coordinated: while the child leader
is still unreaped, its group identity is pinned. Settle inherited CLI descendants
before reaping; never send a numeric group signal once reaping starts, even when
output drain keeps the done channel open. Retain each CLI handle through group
verification and include any unresolved CLI cleanup in session close and call
metadata. /proc survivor checks can report unknown; they do not authorize
signals to recovered PIDs. Browser cleanup must settle before durable completed
state, session.completed, or linked queue success publication. Cleanup failure
uses the existing failed/LastError/session.failed/queue reconciliation paths;
the Run defer still covers exceptional exits.
Never recover a PID from disk to kill it. Cleanup failure is an error, not
rollback or proof that remote effects were undone. Process logs are bounded
and use the existing collector, not unbounded CombinedOutput.

## Result and screenshot contracts

Raw exec reports process completion, exit code/signal, actual combined
stdout/stderr, uncaught traceback and source completeness through the existing
collector/byte finalizer/failure_class. Exit 0 and unchecked False do not prove
business success. Model code should assert/print helper results and observe
state. No AST/tracing or inference from arbitrary printed dictionaries.

Typed screenshot checks wait_for_load (bounded by half the host tool timeout,
at most 15 seconds), then generates a unique private regular PNG and its own JSON
response; only that controlled JSON is parsed from a separate, bounded raw
stdout capture (16 KiB), independent of display/LLM artifact notices or truncation.
Stderr remains in the existing output collector and cannot contaminate JSON. A reported False wait field is
condition_not_met (effects may already have been sent); raw False is opaque.
Verify PNG MIME, dimensions/full decode (at most 32 megapixels), a 16 MiB
source-file read cap and SHA256, then save with the existing
Store tool-output quota lock and single-file/session-byte/file-count caps.
Partial files remain explicitly partial/unrecoverable; write failure produces
no fabricated complete ref. PNGs share quota with command text artifacts.
Current session/provider tool-result types are text-only; no native image-byte
request path exists. Screenshots therefore explicitly report ref-only and
model_image_visible=false. Text read_file retains its UTF-8 contract; binary
PNG readback is via the session artifact file, not invented image perception.

## Verification evidence

Offline tests and gated real-runtime results are recorded in the acceptance
notes below after execution. Actual external internet browsing, Cloud, model,
personal profile/login, remote CDP, untested OS/CPU and performance gains are
NOT_RUN. Do not substitute Web Playwright devDependency for this runtime.

## 2026-10-08 upstream refresh and first platform run

GitHub latest stable releases agree with PyPI: browser-use 0.13.11 tag commit
`914c59bdd4acd50e9628a97a96a3919313aebc85` (published 2026-10-07),
browser-harness v0.1.13 commit `c24e5072ee66f8499bacd663f4f4bcb089bc4492`.
The actually installed top wheels are browser_use-0.13.11-py3-none-any.whl
SHA256 `50b0d16efc44215f99ed1c9197f05e2519bb79fa1501eed7371aff450cc56045`
and browser_harness-0.1.13-py3-none-any.whl SHA256
`2491459e4bfc0ee8aea22dc6c4680fc0f791b7ba553446323c50d2883449d769`.
Full source URLs/hashes are in the wheel inventory; installed leaf hashes are
in the platform inventory. Reviewed installed sources cover CLI, strict doctor,
IPC/name/path derivation, environment switches, both daemon/helpers handwritten
loaders, browser-use init/logging/Config, and dotenv disable-before-discovery.

Executed `scripts/browser-use/install.sh /tmp/aegis-browser136/installed
/usr/bin/python3.12`: VERIFIED reproducible hashed, wheel-only install of all
104 application packages (no pruning). Pre-installed `/usr/bin/google-chrome`
reports **Google Chrome 154.0.8037.92**, backed by `/opt/google/chrome/chrome`.
`ldd` resolved every linked library, including NSS/NSPR, ATK/AT-SPI, X11/XCB,
GBM/DRM, ALSA, Cairo/Pango, libc/glib/glibc. No native dependency or browser was
downloaded; font/rendering requirements remain unverified until sandboxed CDP
startup can run under an ordinary account.

Executed `AEGIS_BROWSER_E2E=1 AEGIS_BROWSER_INSTALL_ROOT=/tmp/aegis-browser136/installed
go test ./internal/tools -run '^TestBrowserRealRuntime$' -v -count=1`:
- VERIFIED pinned installed metadata/Python; all .env/agent_helpers negative
  controls in actual lib/python3.12 REPO_ROOT and owned BH_AGENT_WORKSPACE.
- VERIFIED source-independent official browser-use import with synthetic
  ancestor dotenv (Cloud/provider/proxy/autospawn controls), clean cwd Config,
  both launcher flags, telemetry outbound mock, and clean positive control.
- VERIFIED real pinned strict JSON dead named daemon, healthy=false,
  chrome_running=null; no daemon/browser auto-start.
- VERIFIED fail-closed owned Chrome startup and confirmed owned cleanup.
  This host UID is 0: Chromium rejects root without --no-sandbox. The adapter
  retains sandboxing and reports the actual startup diagnostic. The live
  capability portion is SKIP / NOT_VERIFIED, not a browser success.
- NOT_VERIFIED real live CDP readiness, navigate/observe/input/JS/wait/screenshot,
  strict-live daemon health, live tab/cookie/download isolation, browser rendering
  and runtime telemetry/cloud mocks during live page interaction. The gated
  runnable fixture retains these checks for a sandbox-capable ordinary account.

Screenshot model visibility is **ref-only**, verified through the next mock
provider request. Current ToolResult/provider builders have no native image-byte
attachment contract; text read_file still rejects binary PNG. No vision path
has been invented. Ordinary cancellation cleanup is tested; host SIGKILL or a
Python-created process that escapes its owned group cannot be claimed cleaned.
Never signal recovered/unowned PIDs to hide that uncertainty.

## Unprivileged live acceptance (2026-10-08)

Live acceptance runs as an unprivileged uid with the Chromium sandbox enabled
(host setuid chrome-sandbox + AppArmor profile); root hosts fail closed by design
and --no-sandbox is never passed. The root-run "NOT_VERIFIED live" items above
were then closed outside the implementing sandbox as UID 65534 (nobody), against
a marked private install created by scripts/browser-use/install.sh:

```sh
B=/tmp/bu-nobody   # private, marked, nobody-owned test root; TMPDIR=$B
cd <worktree> && go test -c -o "$B/tools.test" ./internal/tools && chown 65534:65534 "$B/tools.test"
cd "$B" && setpriv --reuid=65534 --regid=65534 --clear-groups env -i PATH=/usr/bin:/bin HOME="$B/home" TMPDIR="$B" AEGIS_BROWSER_E2E=1 \
  AEGIS_BROWSER_INSTALL_ROOT="$B/installed" timeout 300 "$B/tools.test" -test.run '^TestBrowserRealRuntime$' -test.v -test.count=1
```

- First run FAILED: Chrome's SingletonSocket exceeded sun_path because TMPDIR
  pointed into the deep session directory. Fixed by TMPDIR=<ipc>/t plus up-front
  socket-length validation (offline regression RED then GREEN).
- Post-fix run PASSED (Google Chrome 154.0.8037.92): pinned metadata; .env /
  agent_helpers negative controls; official import with synthetic ancestor .env
  and outbound mock; strict dead-daemon doctor; localhost navigate/observe/input/
  JS/CDP/wait positive and negative controls; private PNG screenshot; strict live
  named-daemon doctor healthy=true, chrome_running=null; owned cleanup confirmed,
  no leftover owned processes. Only owned handles are cleaned; unrelated
  processes of the same uid are never signaled.
- Still NOT_RUN: external internet, Cloud/proxy, personal profile/login, remote
  CDP, paid models, other OS/CPU/Python, fonts/rendering fidelity, performance.

## PR #144 independent-review fixes

All five regressions were RED against d768359, then GREEN: post-reap group
cancellation during blocked output drain, redirected CLI descendants surviving
exit 0, durable session/queue success before cleanup failure, browser calls
blocked after accepted interrupt steering, and valid long-path screenshot JSON
under a 512-byte output budget. A separate RED/GREEN regression preserves
cleanup-error metadata when a CLI collector is finalized.

VERIFIED this round:
- `AEGIS_BROWSER_E2E=0 go test ./internal/tools ./internal/runtime -run TestBrowser -count=1`
- `AEGIS_BROWSER_E2E=0 go test -race ./internal/tools ./internal/runtime -run TestBrowser -count=1`
- `go vet ./...`, gofmt and `git diff --check`.

Install checks and doctor commands also use the coordinated owned-process path.
Shell's existing numeric cancellation and discarded completed CLI handles share
the reviewed weaknesses; shell was intentionally unchanged. No new state machine,
policy engine or disk-PID kill authority was introduced.

NOT_RUN / NOT_VERIFIED for these revisions: the nobody live gate and real
browser/daemon cleanup (reserved for the architect), plus the repository-wide
suite. Earlier live evidence above describes the prior revision. Escaped groups
and host SIGKILL remain outside confirmed owned-group cleanup. Changes remain
uncommitted for the architect to commit.

## Offline verification and delivery limitations

- Initial RED: `go test ./internal/tools -run '^TestBrowserRegistryAssembly$'
  failed with "real registry assembly lacks browser_exec dispatch" before
  browser registration was implemented. A read-only registration-disabled Go
  overlay also reproduced real CLI/Web wire-schema and result-dispatch RED;
  the working tree remained unchanged during that reproduction.
- GREEN: `go test -p 1 ./internal/tools ./internal/runtime ./internal/app
  ./internal/webconsole -run TestBrowser -count=1` passed. This includes actual
  registry/role/Plan Mode/hooks -> owned stdin child -> bounded collector/text
  artifact and typed private PNG -> next mock provider request; CLI and Web
  tests also inspect the actual OpenAI-compatible wire request.
- `go test -race -p 1 ./internal/tools ./internal/runtime -run TestBrowser
  -count=1` passed. Same-session serialization, two-session/child path isolation,
  timeout/pause/abort/child stop, dead daemon/no replay, incomplete initial
  DevToolsActivePort publication, raw unchecked False, typed condition_not_met,
  nonzero/signal/traceback, bad PNG/JSON, environment overrides, disabled/role
  gating, file/session-byte/file-count caps and persistence failure are covered.
- #132/#134 focused regressions passed in internal/app, internal/runtime and
  internal/session: real config selection/admission and metadata provenance,
  canonical lease/settlement/ownership-loss, foreign claim preservation,
  heartbeat continuation, provisional resume, stale reader rejection and
  final publication/rollback guards. No queue protocol changes were made.
- `go vet ./...`, gofmt and `git diff --check` passed. `npm test` passed all
  254 tests; node syntax checks passed for the changed shared assets.
- Focused rendered Settings QA passed at http://127.0.0.1:39737/: Settings ->
  expand optional browser facts -> switch zh-CN/en. Viewports 1440x1000 and
  390x844; correct Agent Console title, meaningful page, no overlay/console/page
  errors, private install path escaped/rendered, no install/mutation controls,
  and summary target >=44px. Browser plugin not available; existing repo
  Playwright/Chrome used for UI QA only, never as browser-use acceptance.
- Full `go test ./... -p 4 -timeout 60m` outside the sandbox: all packages pass
  except load-sensitive existing tests (app approval-recovery matrix, runtime
  child-budget timing, Web approval receipt 2s wait) that fail identically on
  unmodified main on this overloaded host (load ~230 on 20 cores); main CI is
  green and PR CI is the repository-wide arbiter.

No venv, browser profile, runtime screenshot, synthetic .env or credential is
included in the source tree.
