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

const (
	// execPolicyMaxNestedDepth bounds how deep wrapper/interpreter nesting is
	// expanded and the expansion allowance below bounds the total size of the
	// command strings that nesting derives. Detection runs synchronously without
	// a context, so these limits keep a crafted command from burning unbounded
	// CPU and memory.
	//
	// Neither budget meters the input itself: the views derived from the
	// top-level segments of a command are always inspected in full, because that
	// work is linear in the input and leaves nothing uninspected. Only nested
	// expansion is metered, since re-scanning the remainder at every level is
	// what multiplies work by the nesting depth. That split is what keeps a
	// legitimately huge command (an inline script of tens of thousands of lines,
	// a command line naming thousands of files) from being charged for a budget
	// it cannot avoid and reported as unverifiable.
	//
	// Real nesting is shallow (observed depth <= 4) and derives kilobytes, so
	// exhausting either budget means the command is genuinely uninspectable and
	// is reported as unverifiable rather than clean.
	execPolicyMaxNestedDepth = 12
	// execPolicyExpansionByteAllowance is the fixed nested-expansion allowance
	// granted on top of the input length, so total expansion work stays O(input)
	// instead of O(depth x input): a wrapper chain re-scans nearly the whole
	// remainder at every level, which is what turned a multi-megabyte command
	// into tens of seconds of synchronous CPU. Sizing the allowance off the input
	// keeps a single legitimately huge wrapped payload (`bash -c '<big script>'`)
	// fully inspectable while still refusing to pay for it depth-many times.
	execPolicyExpansionByteAllowance = 1 << 20
)

// execPolicyExpansionBudget meters nested expansion work for one detection call
// and records why inspection stopped, so an inconclusive result can say whether
// the command was too deeply nested or expanded to too much work.
type execPolicyExpansionBudget struct {
	remainingBytes int
	depthExhausted bool
	workExhausted  bool
}

func newExecPolicyExpansionBudget(command string) *execPolicyExpansionBudget {
	return &execPolicyExpansionBudget{remainingBytes: len(command) + execPolicyExpansionByteAllowance}
}

// charge reserves budget for expanding a nested command string of n bytes and
// reports whether it fit. Charging happens before the string is expanded
// further, so the work is bounded rather than merely observed after the fact.
func (b *execPolicyExpansionBudget) charge(n int) bool {
	if b.workExhausted || n > b.remainingBytes {
		b.workExhausted = true
		return false
	}
	b.remainingBytes -= n
	return true
}

func (b *execPolicyExpansionBudget) complete() bool {
	return !b.depthExhausted && !b.workExhausted
}

// execPolicyInconclusiveReason describes which budget ran out, so `unverifiable`
// distinguishes "too deeply nested to judge" from "too much work to judge".
func (b *execPolicyExpansionBudget) inconclusiveReason() (string, string) {
	switch {
	case b.depthExhausted:
		return "nested expansion depth budget exhausted",
			"command wrapper nesting is deeper than the inspection budget, so policy checks are inconclusive"
	case b.workExhausted:
		return "nested expansion work budget exhausted",
			"command nesting expands to more work than the inspection budget allows, so policy checks are inconclusive"
	default:
		return "", ""
	}
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
	budget := newExecPolicyExpansionBudget(trimmed)
	commandViews := execPolicyCommandViews(trimmed, budget)
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
	if detectSecretPathWrite(trimmed, budget) {
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
	if !budget.complete() && len(violations) == 0 {
		// A budget ran out before every wrapped command could be inspected, so
		// "no match" does not mean "no violation". Report the command as
		// unverifiable instead of implicitly clean, otherwise nesting a payload
		// past a budget becomes a bypass. Only nested expansion is metered, so a
		// merely long command never lands here.
		pattern, message := budget.inconclusiveReason()
		violations = append(violations, ExecPolicyViolation{
			Category: "unverifiable",
			Pattern:  pattern,
			Message:  message,
		})
	}
	return violations
}

// execPolicyCommandViews returns the derived command views. Whether expansion
// completed within budget is recorded on the shared budget, so a caller that
// also runs other budgeted checks sees a single verdict.
func execPolicyCommandViews(command string, budget *execPolicyExpansionBudget) []string {
	views := []string{strings.TrimSpace(command)}
	seen := map[string]struct{}{views[0]: {}}
	execPolicyExpandCommandViews(command, 0, seen, &views, budget)
	return views
}

// execPolicyExpandCommandViews derives command views, recursing into wrapped
// command strings within the budget. Nested expansion re-scans the remaining
// string at every level, so an adversarial input such as
// `eval eval eval ... sudo rm -rf /` would otherwise cost tens of seconds of CPU
// on this synchronous, ctx-less path.
//
// Same-layer normalization (segment splitting, environment assignment prefixes,
// the `env` / `command` builtins, quote and backslash stripping) is deliberately
// decoupled from recursing further down: reaching the depth budget stops the
// descent but never skips normalizing the layer that was reached. Skipping it
// would leave the exact layer where the budget lands unnormalized, so a payload
// such as `env A=1 sudo ...` parked at that depth would match nothing.
func execPolicyExpandCommandViews(command string, depth int, seen map[string]struct{}, views *[]string, budget *execPolicyExpansionBudget) {
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
		execPolicyAppendView(view, seen, views)
		for _, nested := range execPolicyNestedCommandStrings(commandName, args) {
			nested = strings.TrimSpace(nested)
			if nested == "" {
				continue
			}
			execPolicyAppendView(nested, seen, views)
			if depth+1 >= execPolicyMaxNestedDepth {
				// The nested string is recorded and normalized in place below, but
				// anything it wraps in turn stays uninspected.
				execPolicyNormalizeCommandViewsInPlace(nested, seen, views)
				if execPolicyHasNestedCommand(nested) {
					budget.depthExhausted = true
				}
				continue
			}
			if !budget.charge(len(nested)) {
				// Out of expansion work: normalize this layer so the payload parked
				// here is still matched, then stop descending. The exhausted budget
				// makes the overall result inconclusive rather than clean.
				execPolicyNormalizeCommandViewsInPlace(nested, seen, views)
				continue
			}
			execPolicyExpandCommandViews(nested, depth+1, seen, views, budget)
		}
	}
}

// execPolicyNormalizeCommandViewsInPlace derives the same-layer normalized views
// of a command string without recursing into anything it wraps. This is the part
// of expansion that strips environment assignment prefixes, the `env` /
// `command` builtins and quote or backslash escaping, and it must still run at
// the layer where a budget lands: the patterns only match a normalized view, so
// omitting it would make that layer a detection hole rather than merely the
// point where the descent stops.
func execPolicyNormalizeCommandViewsInPlace(command string, seen map[string]struct{}, views *[]string) {
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
		execPolicyAppendView(strings.TrimSpace(strings.Join(viewFields, " ")), seen, views)
	}
}

// execPolicyAppendView records a derived view, ignoring empties and duplicates.
// There is no separate cap on the number of views: expansion work is metered in
// bytes by execPolicyExpansionBudget, and counting views instead would charge a
// legitimate inline script one unit per line.
func execPolicyAppendView(view string, seen map[string]struct{}, views *[]string) {
	if view == "" {
		return
	}
	if _, ok := seen[view]; ok {
		return
	}
	seen[view] = struct{}{}
	*views = append(*views, view)
}

// execPolicyHasNestedCommand reports whether a command string would itself
// expand into further nested command strings, so hitting the depth budget on a
// leaf command is not mistaken for an incomplete inspection.
func execPolicyHasNestedCommand(command string) bool {
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
		if len(execPolicyNestedCommandStrings(commandName, args)) > 0 {
			return true
		}
	}
	return false
}

// execPolicyNestedCommandStrings returns command strings that the given
// interpreter/wrapper invocation would itself execute, so the policy patterns
// can also be applied to the wrapped command name. This covers the common
// `sh -c '<command>'` / `eval '<command>'` / `xargs <command>` shapes; it is
// heuristic depth (defense in depth), not a shell parser, so obfuscation such
// as base64 pipelines or indirect variable expansion stays out of reach.
func execPolicyNestedCommandStrings(commandName string, args []string) []string {
	var nested []string
	wrapper := execPolicyNormalizeCommandName(commandName)
	switch wrapper {
	case "sh", "bash", "dash", "zsh", "ksh", "ash":
		if command, ok := execPolicyShellCommandOperand(args); ok {
			nested = append(nested, command)
		}
	case "busybox":
		if command, ok := execPolicyShellCommandOperand(args); ok {
			nested = append(nested, command)
		}
		if len(args) > 0 && !execPolicyIsShellCommandName(args[0]) {
			// BusyBox applets are commands in their own right (`busybox wget`,
			// `busybox rm`). Inspect the applet invocation without the BusyBox
			// launcher or the leading command name masks every policy pattern.
			nested = execPolicyAppendNestedCommand(nested, execPolicyUnquote(strings.Join(args, " ")))
		}
	case "eval", "exec", "nohup", "time", "ionice", "setsid", "watch":
		if len(args) > 0 {
			nested = append(nested, execPolicyUnquote(strings.Join(args, " ")))
		}
		// `time -p curl ...` / `ionice -c2 curl ...`: the wrapper's own options
		// shift the wrapped command name away from the start of the joined
		// string, so also inspect the view with that option block removed.
		if remainder := execPolicyWrapperRemainder(wrapper, args); remainder != "" {
			nested = execPolicyAppendNestedCommand(nested, remainder)
		}
	case "nice", "timeout", "stdbuf", "xargs":
		if remainder := execPolicyWrapperRemainder(wrapper, args); remainder != "" {
			nested = append(nested, remainder)
		}
	}
	return nested
}

// execPolicyShellCommandOperand consumes the leading option block of a shell
// invocation and returns the string the shell would run for `-c`. Options are
// skipped with their values (`-o pipefail`) so an option value is never mistaken
// for the command, and bundled short options (`-ec`) are recognized as carrying
// `-c` themselves.
func execPolicyShellCommandOperand(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := execPolicyUnquote(args[i])
		if arg == "" {
			continue
		}
		if arg == "--" {
			return "", false
		}
		if !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "+") {
			// `busybox sh -c '<command>'`: the applet name is followed by the
			// shell's own option block, so keep scanning past it.
			if execPolicyIsShellCommandName(arg) {
				continue
			}
			// Any other first operand is a script path or positional argument,
			// not an inline command string.
			return "", false
		}
		if arg == "-" || arg == "+" {
			return "", false
		}
		if strings.HasPrefix(arg, "--") {
			// Long options such as `--posix` / `--norc`; `--rcfile <path>` style
			// values are handled below.
			if execPolicyShellLongOptionTakesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.ContainsRune(arg[1:], 'c') {
			// `-c`, `-ec`, `-co pipefail`, ... : any option values carried by this
			// block come first, an optional `--` may terminate the option block,
			// and only what follows is the command string.
			next := i + 1 + execPolicyShellShortOptionBlockValueCount(arg)
			if next < len(args) && execPolicyUnquote(args[next]) == "--" {
				// Only the first `--` is the option terminator; a second one is
				// already part of the command string.
				next++
			}
			if next < len(args) {
				return execPolicyUnquote(strings.Join(args[next:], " ")), true
			}
			return "", false
		}
		if count := execPolicyShellShortOptionBlockValueCount(arg); count > 0 {
			// `-o pipefail`: the value must not be treated as the command.
			i += count
		}
	}
	return "", false
}

// execPolicyIsShellCommandName reports whether the normalized command name is
// one of the interpreters whose `-c` operand is treated as a nested command.
func execPolicyIsShellCommandName(commandName string) bool {
	switch execPolicyNormalizeCommandName(commandName) {
	case "sh", "bash", "dash", "zsh", "ksh", "ash", "busybox":
		return true
	default:
		return false
	}
}

func execPolicyShellLongOptionTakesValue(option string) bool {
	switch option {
	case "--rcfile", "--init-file", "--wordexp":
		return true
	default:
		return false
	}
}

// execPolicyShellShortOptionBlockValueCount reports how many following arguments
// a bundled short option block consumes as option values. Each `-o` / `-O` shell
// option name in the block takes one value regardless of its position, so
// `bash -eo pipefail` and `bash -oc pipefail` both consume `pipefail`.
func execPolicyShellShortOptionBlockValueCount(block string) int {
	if len(block) < 2 {
		return 0
	}
	count := 0
	for _, r := range block[1:] {
		switch r {
		case 'o', 'O':
			count++
		}
	}
	return count
}

// execPolicyAppendNestedCommand appends a nested command string unless it is
// already present, so recursive expansion does not re-walk the same string.
func execPolicyAppendNestedCommand(nested []string, command string) []string {
	for _, existing := range nested {
		if existing == command {
			return nested
		}
	}
	return append(nested, command)
}

// execPolicyWrapperRemainder skips the leading option block of a wrapper such
// as `xargs -I{} <command>` or `timeout 5 <command>` and returns the wrapped
// command string.
func execPolicyWrapperRemainder(wrapper string, args []string) string {
	for i := 0; i < len(args); i++ {
		arg := execPolicyUnquote(args[i])
		if arg == "" {
			continue
		}
		if arg == "--" {
			return execPolicyUnquote(strings.Join(args[i+1:], " "))
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			// `xargs -I {}` / `timeout -s KILL`: the option value is not the
			// wrapped command name, so consume it together with the option.
			if execPolicyWrapperOptionTakesValue(wrapper, arg) && i+1 < len(args) {
				i++
			}
			continue
		}
		// `timeout 5 curl ...` / `stdbuf -oL curl ...`: a bare duration operand
		// is not the wrapped command, so keep scanning past it.
		if execPolicyLooksLikeDurationOperand(arg) {
			continue
		}
		return execPolicyUnquote(strings.Join(args[i:], " "))
	}
	return ""
}

// execPolicyWrapperOptionTakesValue reports whether the wrapper option consumes
// the following argument as its value. Only the separated forms matter here:
// bundled (`-c2`) and `--opt=value` forms already carry their value.
func execPolicyWrapperOptionTakesValue(wrapper, option string) bool {
	if strings.Contains(option, "=") {
		return false
	}
	switch wrapper {
	case "xargs":
		// `-i` / `--replace` / `-e` / `--eof` take an optional argument, so the
		// separated forms do not consume the next argument and must stay out of
		// this list: `xargs -i rm -rf /` still runs `rm`.
		switch option {
		case "-I", "-n", "-P", "-L", "-s", "-d", "-E", "-a",
			"--max-args", "--max-procs", "--max-lines",
			"--max-chars", "--delimiter", "--arg-file",
			"--process-slot-var":
			return true
		}
	case "timeout":
		switch option {
		case "-s", "-k", "--signal", "--kill-after":
			return true
		}
	case "stdbuf":
		switch option {
		case "-i", "-o", "-e", "--input", "--output", "--error":
			return true
		}
	case "nice":
		switch option {
		case "-n", "--adjustment":
			return true
		}
	case "time":
		switch option {
		case "-o", "-f", "--output", "--format":
			return true
		}
	case "ionice":
		switch option {
		case "-c", "-n", "-p", "--class", "--classdata", "--pid":
			return true
		}
	case "watch":
		switch option {
		case "-n", "--interval":
			return true
		}
	}
	return false
}

func execPolicyLooksLikeDurationOperand(arg string) bool {
	trimmed := strings.TrimRight(arg, "smhd")
	if trimmed == "" || trimmed == arg && strings.ContainsAny(arg, "smhd") {
		return false
	}
	for _, r := range trimmed {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// execPolicyCommandNameUnescaper strips the quoting and backslash escaping a
// shell would remove. It is built once rather than per call: normalization runs
// for every field of every derived view, and rebuilding the replacer each time
// dominated the allocation profile of a large command (observed 95% of all bytes
// allocated while inspecting a 20000-line inline script).
var execPolicyCommandNameUnescaper = strings.NewReplacer(`"`, "", `'`, "", `\`, "")

// execPolicyNormalizeCommandName strips the quoting and backslash escaping a
// shell would remove before resolving the command name, so `s""udo` / `\sudo`
// normalize back to `sudo`.
func execPolicyNormalizeCommandName(commandName string) string {
	normalized := execPolicyUnquote(commandName)
	normalized = execPolicyCommandNameUnescaper.Replace(normalized)
	return filepath.Base(strings.TrimSpace(normalized))
}

func execPolicyUnquote(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func execPolicyAnyViewMatches(views []string, pattern *regexp.Regexp) bool {
	for _, view := range views {
		if pattern.MatchString(view) {
			return true
		}
	}
	return false
}

func detectSecretPathWrite(command string, budget *execPolicyExpansionBudget) bool {
	return detectSecretPathWriteAtDepth(command, 0, budget)
}

func detectSecretPathWriteAtDepth(command string, depth int, budget *execPolicyExpansionBudget) bool {
	if secretPathWritePattern.MatchString(command) {
		return true
	}
	if execPolicyTeeTargetsSecretPath(command) {
		return true
	}
	if execPolicyCommonWriteTargetsSecretPath(command, depth, budget) {
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

func execPolicyCommonWriteTargetsSecretPath(command string, depth int, budget *execPolicyExpansionBudget) bool {
	expandNested := depth < execPolicyMaxNestedDepth
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
		if !expandNested {
			continue
		}
		for _, nested := range execPolicyNestedCommandStrings(commandName, args) {
			if nested == "" {
				continue
			}
			if !budget.charge(len(nested)) {
				// The scan budget is exhausted. Callers treat a `false` result as
				// "no secret-path write found", which is why the shared budget is
				// also reported to DetectExecPolicyViolations as an incomplete
				// inspection rather than being silently swallowed here.
				return false
			}
			if detectSecretPathWriteAtDepth(nested, depth+1, budget) {
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
		commandName := execPolicyNormalizeCommandName(fields[commandIndex])
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
