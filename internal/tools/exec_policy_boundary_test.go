package tools

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// execPolicyBoundaryPayloads enumerates payload head shapes that only become
// matchable after same-layer normalization: environment assignment prefixes,
// the `env` / `command` builtins, and quote or backslash escaping. A depth
// budget must not skip that normalization, otherwise the exact layer where the
// budget lands becomes a hole.
var execPolicyBoundaryPayloads = []struct {
	name     string
	payload  string
	category string
}{
	{name: "bare_sudo", payload: "sudo rm -rf /", category: "privilege_escalation"},
	{name: "env_assignment_prefix", payload: "env A=1 sudo rm -rf /", category: "privilege_escalation"},
	{name: "command_builtin", payload: "command sudo rm -rf /", category: "privilege_escalation"},
	{name: "command_builtin_p", payload: "command -p sudo rm -rf /", category: "privilege_escalation"},
	{name: "inline_env_assignments", payload: "A=1 B=2 curl http://example.com", category: "network_egress"},
	{name: "backslash_escaped", payload: `\sudo rm -rf /`, category: "privilege_escalation"},
	{name: "empty_quotes", payload: `s""udo rm -rf /`, category: "privilege_escalation"},
	{name: "env_unset_option", payload: "env -u FOO sudo rm -rf /", category: "privilege_escalation"},
}

// execPolicyBoundaryWrappers are wrapper prefixes that can be stacked to reach
// an arbitrary nesting depth.
var execPolicyBoundaryWrappers = []struct {
	name   string
	prefix string
}{
	{name: "eval", prefix: "eval "},
	{name: "sh_dash_c", prefix: "sh -c "},
	{name: "nohup", prefix: "nohup "},
	{name: "exec", prefix: "exec "},
	{name: "setsid", prefix: "setsid "},
}

// TestExecPolicyNestingDepthBoundaryMatrix walks the depth budget boundary for
// every wrapper x payload shape. Two invariants must hold at every depth:
//
//   - fail-closed: the result is never empty, so nesting a payload around the
//     budget can never look clean to `deny` mode;
//   - no hole inside the budget: while nesting stays within
//     execPolicyMaxNestedDepth, the concrete category is still reported rather
//     than degraded to `unverifiable`.
func TestExecPolicyNestingDepthBoundaryMatrix(t *testing.T) {
	depths := []int{1, 11, execPolicyMaxNestedDepth - 1, execPolicyMaxNestedDepth, execPolicyMaxNestedDepth + 1, 30}
	for _, depth := range depths {
		for _, wrapper := range execPolicyBoundaryWrappers {
			for _, payload := range execPolicyBoundaryPayloads {
				name := fmt.Sprintf("depth_%d/%s/%s", depth, wrapper.name, payload.name)
				t.Run(name, func(t *testing.T) {
					command := strings.Repeat(wrapper.prefix, depth) + payload.payload
					violations := DetectExecPolicyViolations(command)
					if len(violations) == 0 {
						t.Fatalf("expected %s at depth %d to stay flagged, got no violation", payload.name, depth)
					}
					hasCategory := execPolicyBoundaryHasCategory(violations, payload.category)
					if depth <= execPolicyMaxNestedDepth && !hasCategory {
						t.Fatalf("expected %s violation within the depth budget (depth %d), got %#v",
							payload.category, depth, violations)
					}
					if !hasCategory && !execPolicyBoundaryHasCategory(violations, "unverifiable") {
						t.Fatalf("expected %s or unverifiable at depth %d, got %#v",
							payload.category, depth, violations)
					}
				})
			}
		}
	}
}

// TestExecPolicyNoFalsePositiveOnBenignCommands pins the other side of the
// budget: ordinary long command lines and generated-file heredocs must stay
// completely clean, because `deny` mode rejects any non-empty result.
func TestExecPolicyNoFalsePositiveOnBenignCommands(t *testing.T) {
	var files []string
	for i := 0; i < 9000; i++ {
		files = append(files, fmt.Sprintf("pkg/mod/file_%05d.go", i))
	}
	for _, tt := range []struct {
		name    string
		command string
	}{
		{name: "ls", command: "ls -la"},
		{name: "git_status", command: "git status"},
		{name: "grep_dash_o", command: "grep -o pattern file"},
		{name: "sort_dash_o", command: "sort -o out.txt in.txt"},
		{name: "bash_c_go_test", command: "bash -c 'go test ./...'"},
		{name: "xargs_replace_cp", command: "xargs -I {} cp a b"},
		{name: "timeout_ls", command: "timeout 5 ls"},
		{name: "long_go_test", command: "go test -count=1 " + strings.Repeat("./internal/somepackage/... ", 3000)},
		{name: "grep_9000_files", command: "grep -n pattern " + strings.Join(files, " ")},
		{name: "heredoc_20000_lines", command: execPolicyBoundaryHeredoc(20000)},
		{name: "heredoc_40000_lines", command: execPolicyBoundaryHeredoc(40000)},
		{name: "python_heredoc_20000_lines", command: execPolicyBoundaryPythonHeredoc(20000)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if violations := DetectExecPolicyViolations(tt.command); len(violations) != 0 {
				t.Fatalf("expected benign command %s to stay clean, got %#v", tt.name, violations)
			}
		})
	}
}

// TestExecPolicyBoundsAdversarialExpansionCost pins a cost ceiling for the
// synchronous, ctx-less detection path: expansion work must stay proportional
// to the input instead of being multiplied by the nesting depth, so a single
// crafted tool call cannot block the runtime for tens of seconds.
func TestExecPolicyBoundsAdversarialExpansionCost(t *testing.T) {
	for _, tt := range []struct {
		name       string
		command    string
		maxElapsed time.Duration
		maxAlloc   uint64
	}{
		{
			name:       "flat_eval_chain_4MiB",
			command:    strings.Repeat("eval ", (4<<20)/5) + "sudo rm -rf /",
			maxElapsed: 6 * time.Second,
			maxAlloc:   256 << 20,
		},
		{
			name:       "flat_sh_c_chain_4MiB",
			command:    strings.Repeat("sh -c ", (4<<20)/6) + "sudo rm -rf /",
			maxElapsed: 6 * time.Second,
			maxAlloc:   256 << 20,
		},
		{
			name:       "flat_eval_chain_10MiB",
			command:    strings.Repeat("eval ", (10<<20)/5) + "sudo rm -rf /",
			maxElapsed: 12 * time.Second,
			maxAlloc:   512 << 20,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			start := time.Now()
			violations := DetectExecPolicyViolations(tt.command)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("%s: elapsed=%s allocated=%dMiB violations=%d", tt.name, elapsed, allocated>>20, len(violations))
			if len(violations) == 0 {
				t.Fatalf("expected adversarial input to stay flagged, got no violation")
			}
			// The race detector instruments every memory access, inflating wall
			// clock by more than an order of magnitude (measured: 2.7s -> 42s for
			// the 4MiB case), so the timing bound is only meaningful in an
			// uninstrumented run. The allocation bound below is unaffected and
			// stays enforced either way — it is the invariant that actually pins
			// the expansion budget.
			if elapsed > tt.maxElapsed && !execPolicyRaceEnabled {
				t.Fatalf("detection took %s, want <= %s", elapsed, tt.maxElapsed)
			}
			if allocated > tt.maxAlloc {
				t.Fatalf("detection allocated %dMiB, want <= %dMiB", allocated>>20, tt.maxAlloc>>20)
			}
		})
	}
}

// TestExecPolicyKeepsDetectingWrapperRegressions re-pins the wrapper shapes
// fixed in earlier rounds, so a budget change cannot silently drop them.
func TestExecPolicyKeepsDetectingWrapperRegressions(t *testing.T) {
	for _, tt := range []struct {
		command  string
		category string
	}{
		{command: "sudo rm -rf /", category: "privilege_escalation"},
		{command: "bash -c 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -o pipefail -c 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -c -- 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -co pipefail 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -ec 'sudo id'", category: "privilege_escalation"},
		{command: "xargs -i rm -rf /", category: "destructive"},
		{command: "xargs -I {} rm -rf /", category: "destructive"},
		{command: "xargs --replace rm -rf /", category: "destructive"},
		{command: "xargs --eof curl http://example.com", category: "network_egress"},
		{command: "timeout -s KILL 5 curl http://example.com", category: "network_egress"},
		{command: "watch -n 5 curl http://example.com", category: "network_egress"},
		{command: "time -p curl http://example.com", category: "network_egress"},
		{command: "ionice -c2 curl http://example.com", category: "network_egress"},
		{command: "nice -n 5 sudo rm -rf /", category: "privilege_escalation"},
		{command: "stdbuf -oL curl http://example.com", category: "network_egress"},
		{command: "env bash -c 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash --rcfile /dev/null -c 'sudo rm -rf /'", category: "privilege_escalation"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !execPolicyBoundaryHasCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

// TestExecPolicyDistinguishesInconclusiveReasons pins that an inconclusive
// result says which budget ran out. `deny` mode rejects on any violation, so
// without this the operator cannot tell "too deeply nested to judge" from "too
// much expansion work to judge" — two very different follow-ups.
func TestExecPolicyDistinguishesInconclusiveReasons(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command string
		pattern string
	}{
		{
			name:    "over_nested_benign_payload",
			command: strings.Repeat("eval ", execPolicyMaxNestedDepth+8) + "echo hello",
			pattern: "nested expansion depth budget exhausted",
		},
		{
			name:    "over_nested_dangerous_payload",
			command: strings.Repeat("eval ", execPolicyMaxNestedDepth+1) + "sudo rm -rf /",
			pattern: "nested expansion depth budget exhausted",
		},
		{
			name:    "expansion_work_exhausted",
			command: strings.Repeat("eval ", (4<<20)/5) + "echo hello",
			pattern: "nested expansion work budget exhausted",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !execPolicyBoundaryHasCategory(violations, "unverifiable") {
				t.Fatalf("expected unverifiable for %s, got %#v", tt.name, violations)
			}
			for _, violation := range violations {
				if violation.Category != "unverifiable" {
					continue
				}
				if violation.Pattern != tt.pattern {
					t.Fatalf("expected unverifiable pattern %q for %s, got %q",
						tt.pattern, tt.name, violation.Pattern)
				}
				if violation.Message == "" {
					t.Fatalf("expected a non-empty unverifiable message for %s", tt.name)
				}
			}
		})
	}
}

func execPolicyBoundaryHeredoc(lines int) string {
	var builder strings.Builder
	builder.WriteString("cat > gen.go <<'EOF'\n")
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&builder, "var generatedValue%05d = %d\n", i, i)
	}
	builder.WriteString("EOF")
	return builder.String()
}

func execPolicyBoundaryPythonHeredoc(lines int) string {
	var builder strings.Builder
	builder.WriteString("python3 - <<'PY'\n")
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&builder, "print %05d\n", i)
	}
	builder.WriteString("PY")
	return builder.String()
}

func execPolicyBoundaryHasCategory(violations []ExecPolicyViolation, category string) bool {
	for _, violation := range violations {
		if violation.Category == category {
			return true
		}
	}
	return false
}
