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
	commandViews := execPolicyCommandViews(trimmed)
	if execPolicyAnyViewMatches(commandViews, privilegeEscalationPattern) {
		violations = append(violations, ExecPolicyViolation{
			Category: "privilege_escalation",
			Pattern:  "sudo|doas|pkexec",
			Message:  "command invokes a privilege escalation tool",
		})
	}
	if execPolicyAnyViewMatches(commandViews, rmRfRootPattern) {
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
	if execPolicyAnyViewMatches(commandViews, networkEgressPattern) {
		violations = append(violations, ExecPolicyViolation{
			Category: "network_egress",
			Pattern:  "curl|wget|nc|ncat|telnet|ssh|scp|sftp",
			Message:  "command invokes a common network egress client",
		})
	}
	return violations
}

func execPolicyCommandViews(command string) []string {
	views := []string{strings.TrimSpace(command)}
	for _, segment := range splitExecPolicyCommandSegments(command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		commandIndex := 0
		for commandIndex < len(fields) && execPolicyLooksLikeEnvAssignment(fields[commandIndex]) {
			commandIndex++
		}
		if commandIndex >= len(fields) {
			continue
		}
		commandName, args := execPolicyCommandAfterWrappers(fields[commandIndex:])
		if commandName == "" {
			continue
		}
		viewFields := append([]string{commandName}, args...)
		view := strings.TrimSpace(strings.Join(viewFields, " "))
		if view != "" && !execPolicyViewExists(views, view) {
			views = append(views, view)
		}
	}
	return views
}

func execPolicyViewExists(views []string, view string) bool {
	for _, existing := range views {
		if existing == view {
			return true
		}
	}
	return false
}

func execPolicyAnyViewMatches(views []string, pattern *regexp.Regexp) bool {
	for _, view := range views {
		if pattern.MatchString(view) {
			return true
		}
	}
	return false
}

func detectSecretPathWrite(command string) bool {
	if secretPathWritePattern.MatchString(command) {
		return true
	}
	if execPolicyTeeTargetsSecretPath(command) {
		return true
	}
	if execPolicyCommonWriteTargetsSecretPath(command) {
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

func execPolicyCommonWriteTargetsSecretPath(command string) bool {
	for _, segment := range splitExecPolicyCommandSegments(command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		commandIndex := 0
		for commandIndex < len(fields) && execPolicyLooksLikeEnvAssignment(fields[commandIndex]) {
			commandIndex++
		}
		if commandIndex >= len(fields) {
			continue
		}
		commandName, args := execPolicyCommandAfterWrappers(fields[commandIndex:])
		if commandName == "" {
			continue
		}
		targets := execPolicyCommonWriteTargets(commandName, args)
		for _, target := range targets {
			if execPolicyTargetSecretPath(target) {
				return true
			}
		}
	}
	return false
}

func execPolicyCommandAfterWrappers(fields []string) (string, []string) {
	if len(fields) == 0 {
		return "", nil
	}
	commandIndex := 0
	for commandIndex < len(fields) {
		commandName := filepath.Base(strings.Trim(strings.TrimSpace(fields[commandIndex]), `"'`))
		switch commandName {
		case "env":
			commandIndex++
			for commandIndex < len(fields) {
				field := strings.Trim(strings.TrimSpace(fields[commandIndex]), `"'`)
				if field == "" {
					commandIndex++
					continue
				}
				if field == "--" {
					commandIndex++
					break
				}
				if execPolicyLooksLikeEnvAssignment(field) {
					commandIndex++
					continue
				}
				if strings.HasPrefix(field, "-") && field != "-" {
					if execPolicyEnvOptionTakesValue(field) && !strings.Contains(field, "=") && commandIndex+1 < len(fields) {
						commandIndex += 2
						continue
					}
					commandIndex++
					continue
				}
				break
			}
		case "command":
			nextIndex, ok := execPolicyCommandBuiltinTargetIndex(fields, commandIndex+1)
			if !ok {
				return "", nil
			}
			commandIndex = nextIndex
		default:
			return commandName, fields[commandIndex+1:]
		}
	}
	return "", nil
}

func execPolicyCommandBuiltinTargetIndex(fields []string, commandIndex int) (int, bool) {
	for commandIndex < len(fields) {
		field := strings.Trim(strings.TrimSpace(fields[commandIndex]), `"'`)
		if field == "" {
			commandIndex++
			continue
		}
		if field == "--" {
			commandIndex++
			break
		}
		switch field {
		case "-p":
			commandIndex++
			continue
		case "-v", "-V":
			return 0, false
		}
		if strings.HasPrefix(field, "-") && field != "-" {
			return 0, false
		}
		break
	}
	return commandIndex, commandIndex < len(fields)
}

func execPolicyEnvOptionTakesValue(option string) bool {
	switch option {
	case "-u", "--unset", "-C", "--chdir", "-S", "--split-string":
		return true
	default:
		return false
	}
}

func splitExecPolicyCommandSegments(command string) []string {
	return strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ';', '&', '|', '(', ')', '\n':
			return true
		default:
			return false
		}
	})
}

func execPolicyLooksLikeEnvAssignment(field string) bool {
	field = strings.Trim(field, `"'`)
	if strings.HasPrefix(field, "-") || strings.ContainsAny(field, `/\`) {
		return false
	}
	eq := strings.IndexByte(field, '=')
	return eq > 0
}

func execPolicyCommonWriteTargets(commandName string, args []string) []string {
	commandName = strings.ToLower(strings.TrimSpace(commandName))
	switch commandName {
	case "cp", "mv", "install":
	case "touch", "mkdir":
	default:
		return nil
	}
	operands, explicitTargets := execPolicyCommandOperands(commandName, args)
	targets := append([]string{}, explicitTargets...)
	switch commandName {
	case "touch", "mkdir":
		targets = append(targets, operands...)
	case "install":
		if execPolicyInstallCreatesDirectories(args) {
			targets = append(targets, operands...)
		} else if len(operands) > 0 {
			targets = append(targets, operands[len(operands)-1])
		}
	case "cp", "mv":
		if len(operands) > 0 {
			targets = append(targets, operands[len(operands)-1])
		}
	}
	return targets
}

func execPolicyCommandOperands(commandName string, args []string) ([]string, []string) {
	var operands []string
	var explicitTargets []string
	stopOptions := false
	for i := 0; i < len(args); i++ {
		arg := strings.Trim(strings.TrimSpace(args[i]), `"'`)
		if arg == "" {
			continue
		}
		if !stopOptions {
			if arg == "--" {
				stopOptions = true
				continue
			}
			if target, ok := execPolicyInlineTargetDirectory(commandName, arg); ok {
				if target != "" {
					explicitTargets = append(explicitTargets, target)
				}
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				if execPolicyOptionTakesValue(commandName, arg) && execPolicyShortOptionNeedsNextArg(arg) && i+1 < len(args) {
					value := strings.Trim(strings.TrimSpace(args[i+1]), `"'`)
					if execPolicyIsTargetDirectoryOption(commandName, arg) && value != "" {
						explicitTargets = append(explicitTargets, value)
					}
					i++
				}
				continue
			}
		}
		operands = append(operands, arg)
	}
	return operands, explicitTargets
}

func execPolicyInlineTargetDirectory(commandName, option string) (string, bool) {
	if strings.HasPrefix(option, "--target-directory=") {
		return strings.TrimPrefix(option, "--target-directory="), true
	}
	if commandName == "cp" || commandName == "mv" || commandName == "install" {
		if strings.HasPrefix(option, "-t=") {
			return strings.TrimPrefix(option, "-t="), true
		}
		if strings.HasPrefix(option, "-t") && option != "-t" {
			return strings.TrimPrefix(option, "-t"), true
		}
	}
	return "", false
}

func execPolicyOptionTakesValue(commandName, option string) bool {
	option = strings.TrimSpace(option)
	if execPolicyIsTargetDirectoryOption(commandName, option) {
		return true
	}
	switch option {
	case "-m", "--mode":
		return commandName == "install" || commandName == "mkdir"
	case "-o", "--owner", "-g", "--group", "-S", "--suffix":
		return commandName == "install" || commandName == "cp" || commandName == "mv"
	case "-d", "--date", "-r", "--reference", "-t", "--time":
		return commandName == "touch"
	default:
		return false
	}
}

func execPolicyShortOptionNeedsNextArg(option string) bool {
	if strings.HasPrefix(option, "--") {
		return !strings.Contains(option, "=")
	}
	return len([]rune(option)) == 2
}

func execPolicyIsTargetDirectoryOption(commandName, option string) bool {
	if commandName != "cp" && commandName != "mv" && commandName != "install" {
		return false
	}
	return option == "-t" || option == "--target-directory"
}

func execPolicyInstallCreatesDirectories(args []string) bool {
	for _, arg := range args {
		arg = strings.Trim(strings.TrimSpace(arg), `"'`)
		if arg == "--" {
			return false
		}
		if arg == "-d" || arg == "--directory" {
			return true
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
	if deniedWorkspaceWriteFilePathPattern(parts) != "" {
		return true
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
