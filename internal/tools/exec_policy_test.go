package tools

import "testing"

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

func TestExecPolicyDetectsSecretPathWrite(t *testing.T) {
	for _, command := range []string{
		"echo token > .env",
		"printf token > .azure/accessTokens.json",
		"printf token > .oci/config",
		"printf token > .config/gcloud/configurations/config_default",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
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
