package tools

import (
	"regexp"
	"strings"

	"go-cli-agent/internal/config"
)

type ExecPolicyViolation struct {
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	Message  string `json:"message"`
}

var (
	commandNamePrefix          = `(^|[;&|()])\s*(?:"|')?(?:[^\s;&|()'"]*/)?`
	privilegeEscalationPattern = regexp.MustCompile(commandNamePrefix + `(sudo|doas|pkexec)(?:"|')?(\s|$)`)
	rmRfRootPattern            = regexp.MustCompile(commandNamePrefix + `rm(?:"|')?\s+(?:-[^\s;&|]*[rR][^\s;&|]*[fF][^\s;&|]*|-[^\s;&|]*[fF][^\s;&|]*[rR][^\s;&|]*)\s+(?:/|/\*)($|[\s;&|])`)
	secretPathWritePattern     = regexp.MustCompile(`(?i)(?:^|[\s;&|])(?:\d*>>?|\d*>\|?|tee(?:\s+-a)?)\s*[^\n;&|]*(\.env|\.ssh/[^\s;&|]*|\.aws/credentials|\.azure/[^\s;&|]*|\.oci/[^\s;&|]*|\.config/gcloud/[^\s;&|]*|\.gnupg/[^\s;&|]*|\.kube/config|\.docker/config\.json|id_rsa|id_ed25519|credentials)(?:$|[\s;&|])`)
	networkEgressPattern       = regexp.MustCompile(commandNamePrefix + `(curl|wget|nc|ncat|telnet|ssh|scp|sftp)(?:"|')?(\s|$)`)
)

func DetectExecPolicyViolations(command string) []ExecPolicyViolation {
	var violations []ExecPolicyViolation
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return violations
	}
	if privilegeEscalationPattern.MatchString(trimmed) {
		violations = append(violations, ExecPolicyViolation{
			Category: "privilege_escalation",
			Pattern:  "sudo|doas|pkexec",
			Message:  "command invokes a privilege escalation tool",
		})
	}
	if rmRfRootPattern.MatchString(trimmed) {
		violations = append(violations, ExecPolicyViolation{
			Category: "destructive",
			Pattern:  "rm -rf /",
			Message:  "command appears to recursively delete a root path",
		})
	}
	if secretPathWritePattern.MatchString(trimmed) {
		violations = append(violations, ExecPolicyViolation{
			Category: "secret_path_write",
			Pattern:  "redirect-or-tee secret path",
			Message:  "command appears to write to a secret or credential path",
		})
	}
	if networkEgressPattern.MatchString(trimmed) {
		violations = append(violations, ExecPolicyViolation{
			Category: "network_egress",
			Pattern:  "curl|wget|nc|ncat|telnet|ssh|scp|sftp",
			Message:  "command invokes a common network egress client",
		})
	}
	return violations
}

func effectiveExecPolicyMode(cfg *config.Config) string {
	if cfg == nil {
		return "warn"
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Runtime.ExecPolicy.Mode)) {
	case "deny":
		return "deny"
	case "off":
		return "off"
	default:
		return "warn"
	}
}

func execPolicyMetadata(mode string, violations []ExecPolicyViolation) map[string]any {
	if mode == "off" || len(violations) == 0 {
		return nil
	}
	return map[string]any{
		"mode":       mode,
		"violations": violations,
	}
}

func attachExecPolicyMetadata(metadata map[string]any, policy map[string]any) map[string]any {
	if policy != nil {
		metadata["exec_policy"] = policy
	}
	return metadata
}
