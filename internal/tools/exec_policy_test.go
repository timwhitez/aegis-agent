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

func TestExecPolicyDetectsSecretPathWrite(t *testing.T) {
	violations := DetectExecPolicyViolations("echo token > .env")
	if !hasExecPolicyCategory(violations, "secret_path_write") {
		t.Fatalf("expected secret path write violation, got %#v", violations)
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
