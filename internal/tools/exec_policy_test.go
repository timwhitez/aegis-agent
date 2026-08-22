package tools

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestExecPolicyDetectsSudo(t *testing.T) {
	violations := DetectExecPolicyViolations("sudo systemctl restart ssh")
	if !hasExecPolicyCategory(violations, "privilege_escalation") {
		t.Fatalf("expected privilege escalation violation, got %#v", violations)
	}
}

func TestExecPolicyDetectsRmRfRoot(t *testing.T) {
	violations := DetectExecPolicyViolations("rm -rf /")
	if !hasExecPolicyCategory(violations, "destructive") {
		t.Fatalf("expected destructive violation, got %#v", violations)
	}
}

func TestExecPolicyDetectsAbsoluteCommandPaths(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		category string
	}{
		{name: "sudo", command: "/usr/bin/sudo systemctl restart ssh", category: "privilege_escalation"},
		{name: "rm", command: "/bin/rm -rf /", category: "destructive"},
		{name: "curl", command: "/usr/bin/curl https://example.com", category: "network_egress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsWrappedPolicyCommands(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		category string
	}{
		{name: "env sudo", command: "env sudo systemctl restart ssh", category: "privilege_escalation"},
		{name: "assignment curl", command: "FOO=bar curl https://example.com", category: "network_egress"},
		{name: "env rm", command: "env rm -rf /", category: "destructive"},
		{name: "command cp", command: "command cp token.txt .env.local", category: "secret_path_write"},
		{name: "command path cp", command: "command -p /bin/cp token.txt .env.local", category: "secret_path_write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsBusyBoxApplets(t *testing.T) {
	for _, tt := range []struct {
		command  string
		category string
	}{
		{command: "busybox wget https://example.com", category: "network_egress"},
		{command: "/bin/busybox rm -rf /", category: "destructive"},
		{command: "busybox sh -c 'sudo id'", category: "privilege_escalation"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsSecretPathWrite(t *testing.T) {
	for _, command := range []string{
		"echo token > .env",
		"echo token > .envrc",
		"echo token > .env.local",
		"echo token > .env/token",
		"printf key > id_ecdsa",
		"printf key > deploy.pem",
		"printf key > service_private_key.json",
		"printf token > client_secret.json",
		"printf token > client-secret.json",
		"printf token > service_account.json",
		"printf token > service-account.json",
		"printf token > credentials.json",
		"printf token > service-account_credentials.json",
		"printf token > .azure/accessTokens.json",
		"printf token > .oci/config",
		"printf token > .config/gcloud/configurations/config_default",
		"printf token > .npmrc",
		"printf token > .yarnrc.yml",
		"printf token > .pnpmrc",
		"printf token > .m2/settings.xml",
		"printf token > .m2/settings-security.xml",
		"printf token > .gradle/gradle.properties",
		"printf token > .nuget/NuGet.Config",
		"printf token > .config/pip/pip.conf",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsSecretPathWriteCommands(t *testing.T) {
	for _, command := range []string{
		"cp token.txt .env.local",
		"cp token.txt .envrc",
		"cp token.txt client_secret.json",
		"cp token.txt service_account.json",
		"mv token.txt .ssh/id_rsa",
		"touch .aws/credentials",
		"mkdir -p .kube",
		"install -m 600 token.txt .config/gcloud/application_default_credentials.json",
		"cp --target-directory=.ssh token.txt",
		"install -d .config/gcloud",
		"env cp token.txt .env.local",
		"env FOO=bar /bin/cp token.txt .env.local",
		"touch .m2/settings.xml",
		"cp token.txt .pnpmrc",
		"mv token.txt .nuget/NuGet.Config",
		"install -m 600 token.txt .gradle/gradle.properties",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyAllowsEnvTemplateWriteCommands(t *testing.T) {
	for _, command := range []string{
		"cp token.txt .env.example",
		"mv token.txt .env.sample",
		"touch .env.template",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected env template write command to be allowed for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsSecretPathWriteFromLaterTeeTargets(t *testing.T) {
	for _, command := range []string{
		"printf token | tee reports/out.txt .env.local",
		"printf token | tee -a reports/out.txt .env/token",
		"printf token | /usr/bin/tee reports/out.txt configs/.env.production/token",
		"printf token | tee reports/out.txt .m2/settings.xml",
		"printf token | tee -a reports/out.txt .config/pip/pip.conf",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyAllowsEnvTemplateWrites(t *testing.T) {
	for _, command := range []string{
		"echo token > .env.example",
		"echo token > .env.sample",
		"echo token > .env.template",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected env template write to be allowed for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyAllowsEnvTemplateLaterTeeTargets(t *testing.T) {
	for _, command := range []string{
		"printf token | tee reports/out.txt .env.example",
		"printf token | tee reports/out.txt .env.sample",
		"printf token | tee reports/out.txt .env.template",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected env template write to be allowed for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsXargsOptionalArgumentOptions(t *testing.T) {
	// `-i` / `--replace` / `-e` / `--eof` take an optional argument, so the
	// separated forms do not consume the wrapped command name.
	for _, tt := range []struct {
		command  string
		category string
	}{
		{command: "xargs -i rm -rf /", category: "destructive"},
		{command: "xargs --replace rm -rf /", category: "destructive"},
		{command: "xargs -i sudo rm -rf /tmp/x", category: "privilege_escalation"},
		{command: "xargs --replace sudo curl http://example.com", category: "privilege_escalation"},
		{command: "xargs --eof sudo curl http://example.com", category: "privilege_escalation"},
		{command: "xargs -e sudo curl http://example.com", category: "privilege_escalation"},
		// options that really do consume the next argument must keep working.
		{command: "xargs -I {} rm -rf /", category: "destructive"},
		{command: "xargs -n 1 rm -rf /", category: "destructive"},
		{command: "xargs -E END sudo id", category: "privilege_escalation"},
		{command: "xargs -a list.txt sudo id", category: "privilege_escalation"},
		{command: "xargs --replace=R rm -rf /", category: "destructive"},
		{command: "xargs --eof=END curl http://example.com", category: "network_egress"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsShellCommandOperandAfterOptionBlock(t *testing.T) {
	// A `--` option terminator and option values carried by the same short option
	// block must not end up at the start of the nested command string.
	for _, tt := range []struct {
		command  string
		category string
	}{
		{command: "bash -c -- 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "sh -c -- 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -co pipefail 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -oc pipefail 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -co pipefail -- 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -eco pipefail 'curl http://example.com'", category: "network_egress"},
		// already-working forms must keep working.
		{command: "bash -c 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -o pipefail -c 'sudo rm -rf /'", category: "privilege_escalation"},
		{command: "bash -eo pipefail -c 'sudo rm -rf /'", category: "privilege_escalation"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyReportsUnverifiableWhenNestingBudgetIsExhausted(t *testing.T) {
	// Padding or over-nesting must not turn a hidden payload into a clean result.
	// Asserting only "something was reported" would not cover that: the same
	// nesting shape reports `unverifiable` for a harmless payload too, so such an
	// assertion keeps passing even if payload detection breaks entirely. Every
	// case therefore pins the exact category set and is paired with a harmless
	// control payload, so dangerous and harmless inputs must stay distinguishable
	// wherever the implementation can distinguish them.
	for _, tt := range []struct {
		name    string
		payload string
		// withinBudget is the exact category set required while nesting stays
		// inside execPolicyMaxNestedDepth, where expansion still reaches the
		// payload.
		withinBudget []string
		// pastBudgetConcrete reports whether the concrete category survives once
		// the depth budget is exhausted; when it does not, the result may only
		// degrade to `unverifiable`, never to clean.
		pastBudgetConcrete bool
	}{
		{
			// `rm` is not at a command-name position after `sudo`, so this reports
			// privilege escalation only — pinning the exact set also documents that.
			name:         "sudo",
			payload:      "sudo rm -rf /",
			withinBudget: []string{"privilege_escalation"},
		},
		{
			name:         "rm_rf_root",
			payload:      "rm -rf /",
			withinBudget: []string{"destructive"},
		},
		{
			name:         "curl",
			payload:      "curl http://example.com",
			withinBudget: []string{"network_egress"},
		},
		{
			// A secret-path write is matched on the raw command string rather than
			// on a derived view, so nesting depth cannot hide it at all.
			name:               "secret_path_write",
			payload:            "tee /root/.ssh/authorized_keys",
			withinBudget:       []string{"secret_path_write"},
			pastBudgetConcrete: true,
		},
		{
			// Control side. Without these rows the dangerous rows above would also
			// be satisfied by a detector that flags every deeply nested command,
			// and the guard would say nothing about the payload being seen.
			name:         "benign_echo",
			payload:      "echo hello",
			withinBudget: nil,
		},
		{
			name:         "benign_go_build",
			payload:      "go build ./...",
			withinBudget: nil,
		},
	} {
		for _, wrapper := range []string{"eval ", "sh -c ", "nohup "} {
			for _, depth := range []int{1, execPolicyMaxNestedDepth - 1, execPolicyMaxNestedDepth} {
				t.Run(fmt.Sprintf("%s/within_budget_depth_%d/%s", tt.name, depth, strings.TrimSpace(wrapper)), func(t *testing.T) {
					command := strings.Repeat(wrapper, depth) + tt.payload
					violations := DetectExecPolicyViolations(command)
					got := execPolicyCategorySet(violations)
					if !execPolicySameCategories(got, tt.withinBudget) {
						t.Fatalf("expected categories %v for %d-deep %q nesting of %q, got %v (%#v)",
							tt.withinBudget, depth, strings.TrimSpace(wrapper), tt.payload, got, violations)
					}
				})
			}
			for _, depth := range []int{execPolicyMaxNestedDepth + 1, execPolicyMaxNestedDepth + 8} {
				t.Run(fmt.Sprintf("%s/past_budget_depth_%d/%s", tt.name, depth, strings.TrimSpace(wrapper)), func(t *testing.T) {
					command := strings.Repeat(wrapper, depth) + tt.payload
					violations := DetectExecPolicyViolations(command)
					got := execPolicyCategorySet(violations)
					if len(got) == 0 {
						// Fail-closed: an exhausted budget must never look clean,
						// otherwise nesting past the budget is a bypass.
						t.Fatalf("expected %d-deep %q nesting of %q to stay flagged, got no violation",
							depth, strings.TrimSpace(wrapper), tt.payload)
					}
					if tt.pastBudgetConcrete && !execPolicySameCategories(got, tt.withinBudget) {
						t.Fatalf("expected categories %v to survive %d-deep %q nesting of %q, got %v (%#v)",
							tt.withinBudget, depth, strings.TrimSpace(wrapper), tt.payload, got, violations)
					}
					// Beyond the budget the concrete category may be lost, but the
					// result may only fall back to `unverifiable` — never to a
					// different category, and in particular a harmless payload must
					// never be attributed a dangerous one.
					allowed := append(append([]string{}, tt.withinBudget...), "unverifiable")
					if !execPolicyCategoriesSubsetOf(got, allowed) {
						t.Fatalf("expected categories within %v for %d-deep %q nesting of %q, got %v (%#v)",
							allowed, depth, strings.TrimSpace(wrapper), tt.payload, got, violations)
					}
				})
			}
		}
	}
}

func TestExecPolicyDetectsPolicyCommandsPastLengthLimits(t *testing.T) {
	// Input length alone must not disable nested expansion.
	for _, tt := range []struct {
		name     string
		command  string
		category string
	}{
		{
			name:     "padded shell operand",
			command:  "sh -c 'sudo rm -rf /' #" + strings.Repeat("A", 70000),
			category: "privilege_escalation",
		},
		{
			name:     "padded nested curl",
			command:  "bash -c 'curl http://example.com' #" + strings.Repeat("A", 70000),
			category: "network_egress",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation despite padding, got %#v", tt.category, violations)
			}
		})
	}
}

func TestExecPolicyAllowsLongBenignCommands(t *testing.T) {
	// Fail-closed nesting budgets must not flag ordinary long command lines.
	var files []string
	for i := 0; i < 9000; i++ {
		files = append(files, fmt.Sprintf("pkg/mod/file_%05d.go", i))
	}
	var lines []string
	for i := 0; i < 3000; i++ {
		lines = append(lines, fmt.Sprintf("go build -o bin/app%d ./cmd/app%d", i, i))
	}
	for _, tt := range []struct {
		name    string
		command string
	}{
		{name: "long go test", command: "go test -count=1 " + strings.Repeat("./internal/somepackage/... ", 2000)},
		{name: "grep many files", command: "grep -n pattern " + strings.Join(files, " ")},
		{name: "ls many files", command: "ls -la " + strings.Join(files, " ")},
		{name: "long inline go build", command: "bash -c 'go build " + strings.Repeat("./x/... ", 9000) + "'"},
		{name: "multi-line inline script", command: "bash -c '" + strings.Join(lines, "\n") + "'"},
		{name: "nested ci one-liner", command: "nice -n 10 timeout 300 bash -c 'timeout 5 xargs -I{} sh -c \"gofmt -l {}\" < list.txt'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if violations := DetectExecPolicyViolations(tt.command); len(violations) != 0 {
				t.Fatalf("expected benign command %q to stay clean, got %#v", tt.name, violations)
			}
		})
	}
}

func hasExecPolicyCategory(violations []ExecPolicyViolation, category string) bool {
	for _, violation := range violations {
		if violation.Category == category {
			return true
		}
	}
	return false
}

// execPolicyCategorySet returns the reported categories in a stable order, so a
// test can pin the exact set rather than only probing for one membership.
func execPolicyCategorySet(violations []ExecPolicyViolation) []string {
	categories := make([]string, 0, len(violations))
	for _, violation := range violations {
		categories = append(categories, violation.Category)
	}
	sort.Strings(categories)
	return categories
}

func execPolicySameCategories(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	sorted := append([]string{}, want...)
	sort.Strings(sorted)
	for i := range got {
		if got[i] != sorted[i] {
			return false
		}
	}
	return true
}

func execPolicyCategoriesSubsetOf(got, allowed []string) bool {
	for _, category := range got {
		found := false
		for _, candidate := range allowed {
			if category == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
