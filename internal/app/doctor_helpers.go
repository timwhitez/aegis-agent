package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/extensions"
	"go-cli-agent/internal/session"
)

var hookCommandLookPath = exec.LookPath
var sessionRootCandidateProbe = probeSessionRootCandidate

type hookPoint struct {
	name  string
	items []config.HookDefinition
}

type sessionRootCandidate struct {
	Label string
	Path  string
}

type sessionRootProbeResult struct {
	Path               string
	Writable           bool
	Mode               fs.FileMode
	ExpectedMode       fs.FileMode
	SupportsOwnerOnly  bool
	Error              string
	Reason             string
	Advice             []string
	Recommended        bool
	ProbeDir           string
	ProbeChmodError    string
	CreatedDuringProbe bool
}

func configuredHookPoints(cfg *config.Config) []hookPoint {
	return []hookPoint{
		{name: "session.start", items: cfg.Hooks.SessionStart},
		{name: "session.awaiting_input", items: cfg.Hooks.SessionAwaiting},
		{name: "session.pause", items: cfg.Hooks.SessionPause},
		{name: "session.complete", items: cfg.Hooks.SessionComplete},
		{name: "session.fail", items: cfg.Hooks.SessionFail},
		{name: "user.message", items: cfg.Hooks.UserMessage},
		{name: "assistant.message", items: cfg.Hooks.AssistantMessage},
		{name: "tool.before", items: cfg.Hooks.ToolBefore},
		{name: "tool.after", items: cfg.Hooks.ToolAfter},
	}
}

func checkHookCommands(cfg *config.Config, workdir string) doctorCheck {
	check := doctorCheck{
		Name:   "hooks.commands",
		Status: "ok",
		Details: map[string]any{
			"workdir": workdir,
		},
	}
	var commands []map[string]any
	for _, point := range configuredHookPoints(cfg) {
		for idx, hook := range point.items {
			if len(hook.Command) == 0 {
				continue
			}
			status, detail := probeHookCommand(workdir, point.name, idx, hook, cfg.Hooks.DefaultTimeoutSec)
			commands = append(commands, detail)
			switch status {
			case "fail":
				check.Status = "fail"
			case "warn":
				if check.Status == "ok" {
					check.Status = "warn"
				}
			}
		}
	}
	check.Details["count"] = len(commands)
	if len(commands) > 0 {
		check.Details["commands"] = commands
	}
	return check
}

func probeHookCommand(workdir, point string, idx int, hook config.HookDefinition, defaultTimeout int) (string, map[string]any) {
	detail := map[string]any{
		"point":       point,
		"index":       idx,
		"name":        hook.Name,
		"command":     hook.Command,
		"fail_closed": hook.FailClosed,
	}
	status := "ok"
	timeout := hook.TimeoutSec
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	detail["timeout_sec"] = timeout
	if timeout <= 0 {
		status = "warn"
		detail["warning"] = "command hook has no timeout"
	}

	argv0 := strings.TrimSpace(hook.Command[0])
	if argv0 == "" {
		finalStatus := hookCommandStatus(status, hook.FailClosed)
		return finalStatus, mergeHookDetail(detail, map[string]any{
			"status": finalStatus,
			"error":  "empty command argv[0]",
		})
	}
	if strings.Contains(argv0, "$") {
		finalStatus := hookCommandStatus(maxHookStatus(status, "warn"), hook.FailClosed)
		return finalStatus, mergeHookDetail(detail, map[string]any{
			"status": finalStatus,
			"reason": "dynamic_command_path",
		})
	}

	resolved, err := resolveHookCommandPath(workdir, argv0)
	if err != nil {
		finalStatus := hookCommandStatus(maxHookStatus(status, "warn"), hook.FailClosed)
		return finalStatus, mergeHookDetail(detail, map[string]any{
			"status": finalStatus,
			"error":  err.Error(),
		})
	}
	detail["resolved"] = resolved
	if info, statErr := os.Stat(resolved); statErr == nil {
		detail["mode"] = info.Mode().Perm().String()
	} else {
		finalStatus := hookCommandStatus(maxHookStatus(status, "warn"), hook.FailClosed)
		return finalStatus, mergeHookDetail(detail, map[string]any{
			"status": finalStatus,
			"error":  statErr.Error(),
		})
	}

	if isDirectExecutablePath(argv0) && runtime.GOOS != "windows" && !isExecutableFile(resolved) {
		finalStatus := hookCommandStatus(maxHookStatus(status, "warn"), hook.FailClosed)
		return finalStatus, mergeHookDetail(detail, map[string]any{
			"status": finalStatus,
			"reason": "command_not_executable",
		})
	}

	if scriptPath, ok := shellScriptOperand(workdir, hook.Command); ok {
		detail["script_path"] = scriptPath
		info, statErr := os.Stat(scriptPath)
		if statErr != nil {
			finalStatus := hookCommandStatus(maxHookStatus(status, "warn"), hook.FailClosed)
			return finalStatus, mergeHookDetail(detail, map[string]any{
				"status": finalStatus,
				"reason": "script_missing",
				"error":  statErr.Error(),
			})
		}
		detail["script_mode"] = info.Mode().Perm().String()
	}

	detail["status"] = status
	return status, detail
}

func hookCommandStatus(status string, failClosed bool) string {
	if status != "warn" || !failClosed {
		return status
	}
	return "fail"
}

func maxHookStatus(current, next string) string {
	order := map[string]int{"ok": 0, "warn": 1, "fail": 2}
	if order[next] > order[current] {
		return next
	}
	return current
}

func mergeHookDetail(base, extra map[string]any) map[string]any {
	for key, value := range extra {
		base[key] = value
	}
	return base
}

func resolveHookCommandPath(workdir, command string) (string, error) {
	if filepath.IsAbs(command) {
		return command, nil
	}
	if isDirectExecutablePath(command) {
		return filepath.Clean(filepath.Join(workdir, command)), nil
	}
	return hookCommandLookPath(command)
}

func isDirectExecutablePath(command string) bool {
	return strings.HasPrefix(command, ".") || strings.ContainsRune(command, os.PathSeparator)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func shellScriptOperand(workdir string, command []string) (string, bool) {
	if len(command) < 2 {
		return "", false
	}
	base := filepath.Base(command[0])
	if base != "sh" && base != "bash" {
		return "", false
	}
	operand := command[1]
	if strings.HasPrefix(operand, "-") {
		return "", false
	}
	if filepath.IsAbs(operand) {
		return operand, true
	}
	return filepath.Clean(filepath.Join(workdir, operand)), true
}

func checkSessionRootStrategy(cfg *config.Config) doctorCheck {
	check := doctorCheck{
		Name:   "session.root.strategy",
		Status: "ok",
		Details: map[string]any{
			"configured_dir": cfg.Session.Dir,
		},
	}
	expectedMode, err := config.ParseFileMode(cfg.Session.DirMode, 0o700)
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}

	candidates := sessionRootCandidates(cfg.Session.Dir)
	results := make([]map[string]any, 0, len(candidates))
	var configured *sessionRootProbeResult
	var recommended *sessionRootProbeResult
	for _, candidate := range candidates {
		result := sessionRootCandidateProbe(candidate.Path, expectedMode)
		if candidate.Label == "configured" {
			configured = &result
		}
		if recommended == nil && result.Writable && result.SupportsOwnerOnly {
			result.Recommended = true
			recommended = &result
		}
		results = append(results, map[string]any{
			"label":                      candidate.Label,
			"path":                       result.Path,
			"writable":                   result.Writable,
			"mode":                       result.Mode.String(),
			"expected_mode":              result.ExpectedMode.String(),
			"posix_owner_only_supported": result.SupportsOwnerOnly,
			"reason":                     result.Reason,
			"advice":                     result.Advice,
			"error":                      result.Error,
		})
	}
	if recommended == nil {
		for _, candidate := range candidates {
			result := sessionRootCandidateProbe(candidate.Path, expectedMode)
			if result.Writable {
				result.Recommended = true
				recommended = &result
				break
			}
		}
	}

	check.Details["candidates"] = results
	if recommended != nil {
		check.Details["recommended_dir"] = recommended.Path
	}

	switch {
	case configured == nil || !configured.Writable:
		if recommended == nil {
			check.Status = "fail"
			check.Details["reason"] = "configured_dir_unusable_and_no_fallback"
		} else {
			check.Status = "warn"
			check.Details["reason"] = "configured_dir_unusable"
		}
	case !configured.SupportsOwnerOnly:
		check.Status = "warn"
		check.Details["reason"] = "configured_dir_not_posix_owner_only"
	default:
		check.Status = "ok"
		check.Details["reason"] = "configured_dir_ready"
	}

	if recommended != nil && configured != nil && recommended.Path != configured.Path {
		check.Details["advice"] = sessionRootStrategyAdvice(configured.Path, recommended.Path)
	}
	if configured != nil && !configured.SupportsOwnerOnly {
		check.Details["advice"] = sessionRootStrategyAdvice(configured.Path, defaultRecommendedDir(recommended))
	}
	return check
}

func checkWorkspaceExtensionTrust(workdir string) doctorCheck {
	check := doctorCheck{
		Name:   "extensions.trust",
		Status: "ok",
		Details: map[string]any{
			"workdir": workdir,
			"trusted": false,
		},
	}
	result, err := extensions.Discover(workdir, false)
	if err != nil {
		check.Status = "warn"
		check.Details["error"] = err.Error()
		return check
	}
	check.Details["discovery_path"] = result.DiscoveryPath
	check.Details["trusted"] = result.Trusted
	check.Details["candidate_count"] = len(result.Candidates)
	if len(result.Candidates) > 0 {
		check.Details["candidates"] = result.Candidates
	}
	return check
}

func checkSessionPartialState(sessionRoot string) doctorCheck {
	check := doctorCheck{
		Name:   "session.partial_state",
		Status: "ok",
		Details: map[string]any{
			"dir": sessionRoot,
		},
	}
	missingFiles, err := doctorMissingSessionFiles(sessionRoot)
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
	queueJobs, unreadableJobs, err := doctorQueueJobRecords(sessionRoot)
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
	duplicates := doctorDuplicateQueueJobs(queueJobs)
	runningWithoutLease := doctorRunningJobsWithoutLease(queueJobs)
	staleRunning := doctorStaleRunningJobs(queueJobs, time.Now().UTC())
	missingSessionRefs := doctorQueueJobsMissingSessions(sessionRoot, queueJobs)

	check.Details["missing_session_files"] = missingFiles
	check.Details["duplicate_queue_jobs"] = duplicates
	check.Details["running_jobs_without_lease"] = runningWithoutLease
	check.Details["stale_running_jobs"] = staleRunning
	check.Details["queue_jobs_missing_session"] = missingSessionRefs
	check.Details["unreadable_queue_jobs"] = unreadableJobs
	check.Details["counts"] = map[string]int{
		"missing_session_files":      len(missingFiles),
		"duplicate_queue_jobs":       len(duplicates),
		"running_jobs_without_lease": len(runningWithoutLease),
		"stale_running_jobs":         len(staleRunning),
		"queue_jobs_missing_session": len(missingSessionRefs),
		"unreadable_queue_jobs":      len(unreadableJobs),
	}
	if len(missingFiles)+len(duplicates)+len(runningWithoutLease)+len(staleRunning)+len(missingSessionRefs)+len(unreadableJobs) > 0 {
		check.Status = "warn"
	}
	return check
}

func doctorMissingSessionFiles(sessionRoot string) ([]map[string]any, error) {
	entries, err := os.ReadDir(sessionRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	var out []map[string]any
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "_queue" {
			continue
		}
		sessionID := entry.Name()
		sessionDir := filepath.Join(sessionRoot, sessionID)
		var missing []string
		for _, name := range []string{"session.json", "state.json", "messages.jsonl"} {
			if _, err := os.Stat(filepath.Join(sessionDir, name)); err != nil {
				if os.IsNotExist(err) {
					missing = append(missing, name)
					continue
				}
				return nil, err
			}
		}
		if len(missing) > 0 {
			out = append(out, map[string]any{
				"session_id": sessionID,
				"missing":    missing,
			})
		}
	}
	return out, nil
}

type doctorQueueJobRecord struct {
	Status  string
	Job     session.QueueJob
	Path    string
	ModTime time.Time
}

func doctorQueueJobRecords(sessionRoot string) ([]doctorQueueJobRecord, []map[string]any, error) {
	var records []doctorQueueJobRecord
	var unreadable []map[string]any
	for _, status := range doctorQueueStatuses() {
		dir := filepath.Join(sessionRoot, "_queue", status)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				unreadable = append(unreadable, map[string]any{"path": path, "error": err.Error()})
				continue
			}
			var job session.QueueJob
			if err := json.Unmarshal(data, &job); err != nil {
				unreadable = append(unreadable, map[string]any{"path": path, "error": err.Error()})
				continue
			}
			if job.ID == "" {
				job.ID = strings.TrimSuffix(entry.Name(), ".json")
			}
			info, _ := entry.Info()
			var modTime time.Time
			if info != nil {
				modTime = info.ModTime()
			}
			records = append(records, doctorQueueJobRecord{
				Status:  status,
				Job:     job,
				Path:    path,
				ModTime: modTime,
			})
		}
	}
	return records, unreadable, nil
}

func doctorDuplicateQueueJobs(records []doctorQueueJobRecord) []map[string]any {
	byID := map[string][]doctorQueueJobRecord{}
	for _, record := range records {
		byID[record.Job.ID] = append(byID[record.Job.ID], record)
	}
	var out []map[string]any
	for jobID, items := range byID {
		if len(items) < 2 {
			continue
		}
		var statuses []string
		var paths []string
		latestStatus := ""
		var latestTime time.Time
		for _, item := range items {
			statuses = append(statuses, item.Status)
			paths = append(paths, item.Path)
			if latestStatus == "" || item.ModTime.After(latestTime) {
				latestStatus = item.Status
				latestTime = item.ModTime
			}
		}
		out = append(out, map[string]any{
			"job_id":        jobID,
			"statuses":      statuses,
			"paths":         paths,
			"latest_status": latestStatus,
		})
	}
	return out
}

func doctorRunningJobsWithoutLease(records []doctorQueueJobRecord) []map[string]any {
	var out []map[string]any
	for _, record := range records {
		if record.Status != session.QueueStatusRunning {
			continue
		}
		var missing []string
		if strings.TrimSpace(record.Job.ClaimedBy) == "" {
			missing = append(missing, "claimed_by")
		}
		if strings.TrimSpace(record.Job.ClaimedAt) == "" {
			missing = append(missing, "claimed_at")
		}
		if strings.TrimSpace(record.Job.HeartbeatAt) == "" {
			missing = append(missing, "heartbeat_at")
		}
		if record.Job.WorkerPID == 0 {
			missing = append(missing, "worker_pid")
		}
		if strings.TrimSpace(record.Job.ProcessStartID) == "" {
			missing = append(missing, "process_start_id")
		}
		if len(missing) > 0 {
			out = append(out, map[string]any{
				"job_id":  record.Job.ID,
				"path":    record.Path,
				"missing": missing,
			})
		}
	}
	return out
}

func doctorStaleRunningJobs(records []doctorQueueJobRecord, now time.Time) []map[string]any {
	var out []map[string]any
	for _, record := range records {
		if record.Status != session.QueueStatusRunning {
			continue
		}
		referenceName, reference := doctorQueueHeartbeatReference(record.Job)
		if strings.TrimSpace(reference) == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, reference)
		if err != nil {
			out = append(out, map[string]any{
				"job_id":    record.Job.ID,
				"path":      record.Path,
				"field":     referenceName,
				"value":     reference,
				"parse_err": err.Error(),
			})
			continue
		}
		if now.Sub(parsed) > session.QueueRunningStaleAfter {
			out = append(out, map[string]any{
				"job_id":          record.Job.ID,
				"path":            record.Path,
				"field":           referenceName,
				"value":           reference,
				"stale_after_sec": int(session.QueueRunningStaleAfter.Seconds()),
			})
		}
	}
	return out
}

func doctorQueueJobsMissingSessions(sessionRoot string, records []doctorQueueJobRecord) []map[string]any {
	var out []map[string]any
	for _, record := range records {
		sessionID := strings.TrimSpace(record.Job.SessionID)
		if sessionID == "" {
			continue
		}
		path := filepath.Join(sessionRoot, sessionID, "session.json")
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			out = append(out, map[string]any{
				"job_id":     record.Job.ID,
				"status":     record.Status,
				"session_id": sessionID,
				"path":       record.Path,
			})
		}
	}
	return out
}

func doctorQueueHeartbeatReference(job session.QueueJob) (string, string) {
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "heartbeat_at", value: job.HeartbeatAt},
		{name: "claimed_at", value: job.ClaimedAt},
		{name: "updated_at", value: job.UpdatedAt},
	} {
		if strings.TrimSpace(item.value) != "" {
			return item.name, item.value
		}
	}
	return "", ""
}

func doctorQueueStatuses() []string {
	return []string{
		session.QueueStatusQueued,
		session.QueueStatusRunning,
		session.QueueStatusCompleted,
		session.QueueStatusFailed,
	}
}

func sessionRootCandidates(configured string) []sessionRootCandidate {
	var candidates []sessionRootCandidate
	seen := map[string]struct{}{}
	add := func(label, path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		candidates = append(candidates, sessionRootCandidate{Label: label, Path: clean})
	}
	add("configured", configured)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add("home_fallback", filepath.Join(home, ".go-cli-agent", "sessions"))
	}
	add("temp_fallback", filepath.Join(os.TempDir(), "go-cli-agent", "sessions"))
	return candidates
}

func probeSessionRootCandidate(path string, expected fs.FileMode) sessionRootProbeResult {
	result := sessionRootProbeResult{
		Path:         path,
		ExpectedMode: expected,
	}
	if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
		result.CreatedDuringProbe = true
	}
	if err := os.MkdirAll(path, expected); err != nil {
		result.Error = err.Error()
		result.Reason = "mkdir_failed"
		result.Advice = sessionRootCandidateAdvice(path, false)
		return result
	}
	info, err := os.Stat(path)
	if err != nil {
		result.Error = err.Error()
		result.Reason = "stat_failed"
		result.Advice = sessionRootCandidateAdvice(path, false)
		return result
	}
	result.Mode = info.Mode().Perm()
	file, err := os.CreateTemp(path, ".doctor-session-root-*")
	if err != nil {
		result.Error = err.Error()
		result.Reason = "tempfile_failed"
		result.Advice = sessionRootCandidateAdvice(path, false)
		return result
	}
	result.Writable = true
	_ = file.Close()
	_ = os.Remove(file.Name())

	probe, err := sessionDirModeProbe(path, expected)
	if err != nil {
		result.Reason = "mode_probe_failed"
		result.Error = err.Error()
		result.Advice = sessionRootCandidateAdvice(path, true)
		return result
	}
	result.ProbeDir = probe.ProbeDir
	result.ProbeChmodError = probe.ChmodError
	result.SupportsOwnerOnly = probe.SupportsChmod
	switch {
	case !result.SupportsOwnerOnly:
		result.Reason = "filesystem_does_not_honor_posix_permissions"
	default:
		result.Reason = "ready"
	}
	result.Advice = sessionRootCandidateAdvice(path, result.SupportsOwnerOnly)
	return result
}

func sessionRootCandidateAdvice(path string, supportsOwnerOnly bool) []string {
	if supportsOwnerOnly {
		return nil
	}
	advice := []string{
		"Keep the repository where it is if needed, but move session.dir to a filesystem that honors owner-only permissions.",
	}
	if strings.HasPrefix(filepath.Clean(path), "/mnt/") {
		advice = append(advice, "This path looks like a mounted filesystem. On WSL, prefer ~/.go-cli-agent/sessions or /tmp/go-cli-agent/sessions.")
	}
	return advice
}

func sessionRootStrategyAdvice(configuredDir, recommendedDir string) []string {
	if strings.TrimSpace(recommendedDir) == "" {
		return sessionRootCandidateAdvice(configuredDir, false)
	}
	advice := []string{
		fmt.Sprintf("Set session.dir to %s so session logs, artifacts, and control files stay on a writable owner-only filesystem.", recommendedDir),
		"Your workdir can remain in the current repository; only the session store needs to move.",
	}
	if strings.HasPrefix(filepath.Clean(configuredDir), "/mnt/") {
		advice = append(advice, "Mounted filesystems under /mnt often do not honor POSIX owner-only permissions in WSL.")
	}
	return advice
}

func defaultRecommendedDir(result *sessionRootProbeResult) string {
	if result == nil {
		return ""
	}
	return result.Path
}
