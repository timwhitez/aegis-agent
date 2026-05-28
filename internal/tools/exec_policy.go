package tools

import (
	"path/filepath"
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
	secretPathWritePattern     = regexp.MustCompile(`(?i)(?:^|[\s;&|])(?:\d*>>?|\d*>\|?|tee(?:\s+-a)?)\s*[^\n;&|]*(\.env|\.ssh/[^\s;&|]*|\.aws/credentials|\.azure/[^\s;&|]*|\.oci/[^\s;&|]*|\.config/gcloud/[^\s;&|]*|\.gnupg/[^\s;&|]*|\.kube/config|\.docker/config\.json|identity|id_[^\s;&|]*|[^\s;&|]*private[_-]key[^\s;&|]*|[^\s;&|]*\.(?:pem|key|p12|pfx)|credentials(?:\.[^\s;&|]*)?|[^\s;&|]*(?:_credentials|-credentials)\.json|[^\s;&|]*\.credentials)(?:$|[\s;&|])`)
	shellWriteTargetPattern    = regexp.MustCompile(`(?i)(?:^|[\s;&|])(?:\d*>>?|\d*>\|?)\s*("[^"]+"|'[^']+'|[^\s;&|]+)|(?:^|[\s;&|])tee(?:\s+-a)?\s+("[^"]+"|'[^']+'|[^\s;&|]+)`)
	teeCommandPattern          = regexp.MustCompile(commandNamePrefix + `tee(?:"|')?((?:\s+[^;&|()\n]+)*)`)
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
	if detectSecretPathWrite(trimmed) {
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

func detectSecretPathWrite(command string) bool {
	if secretPathWritePattern.MatchString(command) {
		return true
	}
	if execPolicyTeeTargetsSecretPath(command) {
		return true
	}
	for _, match := range shellWriteTargetPattern.FindAllStringSubmatch(command, -1) {
		for _, target := range match[1:] {
			if execPolicyTargetSecretPath(target) {
				return true
			}
		}
	}
	return false
}

func execPolicyTeeTargetsSecretPath(command string) bool {
	for _, match := range teeCommandPattern.FindAllStringSubmatch(command, -1) {
		if len(match) == 0 {
			continue
		}
		args := strings.Fields(match[len(match)-1])
		stopOptions := false
		for _, arg := range args {
			target := strings.TrimSpace(arg)
			trimmedTarget := strings.Trim(target, `"'`)
			if trimmedTarget == "" {
				continue
			}
			if !stopOptions {
				if trimmedTarget == "--" {
					stopOptions = true
					continue
				}
				if strings.HasPrefix(trimmedTarget, "-") {
					continue
				}
			}
			if execPolicyTargetSecretPath(target) {
				return true
			}
		}
	}
	return false
}

func execPolicyTargetSecretPath(target string) bool {
	target = strings.TrimSpace(target)
	target = strings.Trim(target, `"'`)
	if target == "" {
		return false
	}
	displayPath := filepath.ToSlash(filepath.Clean(target))
	parts := strings.Split(displayPath, "/")
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		for _, denied := range deniedWorkspaceWriteDirs {
			if strings.EqualFold(part, denied) {
				return true
			}
		}
		if deniedWorkspaceWritePathComponentPattern(part) != "" {
			return true
		}
	}
	for _, denied := range deniedWorkspaceWriteDirPaths {
		if displayPathContainsDirPath(parts, denied) {
			return true
		}
	}
	return false
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
