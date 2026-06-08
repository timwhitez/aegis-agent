package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ngen/internal/acp"
	"ngen/internal/multica"
	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
	ngentui "ngen/internal/tui"
	"ngen/internal/version"
	"ngen/internal/web"
)

func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootUsage(stderr)
		return 13
	}
	switch args[0] {
	case "help", "-h", "--help":
		printRootUsage(stdout)
		return 0
	case "--version":
		fmt.Fprintf(stdout, "%s %s\n", version.Name, version.Version)
		return 0
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "models":
		return runModels(args[1:], stdout, stderr)
	case "exec":
		return runExec(ctx, args[1:], stdout, stderr, os.Stdin)
	}
	workspaceRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get cwd: %v\n", err)
		return 13
	}
	cfg, err := task.LoadConfig(workspaceRoot)
	if err != nil {
		fmt.Fprintf(stderr, "failed to load config: %v\n", err)
		return 13
	}
	svc := ngenrt.New(workspaceRoot, cfg)

	switch args[0] {
	case "task":
		return runTask(ctx, svc, args[1:], stdout, stderr)
	case "project":
		return runProject(ctx, svc, args[1:], stdout, stderr)
	case "mission":
		return runMission(ctx, svc, args[1:], stdout, stderr)
	case "goal":
		return runGoal(ctx, svc, args[1:], stdout, stderr)
	case "auto":
		return runStreaming(ctx, svc, "auto", args[1:], stdout, stderr)
	case "run":
		return runStreaming(ctx, svc, "run", args[1:], stdout, stderr)
	case "resume":
		return runStreaming(ctx, svc, "resume", args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, svc, args[1:], stdout, stderr)
	case "review":
		return runReview(ctx, svc, args[1:], stdout, stderr)
	case "events":
		return runEvents(ctx, svc, args[1:], stdout, stderr)
	case "handoff":
		return runHandoff(ctx, svc, args[1:], stdout, stderr)
	case "watch":
		return runWatch(ctx, svc, args[1:], stdout, stderr)
	case "scheduler":
		return runScheduler(ctx, svc, args[1:], stdout, stderr)
	case "approval":
		return runApproval(ctx, svc, args[1:], stdout, stderr)
	case "approve":
		return runApprovalDecision(ctx, svc, "approved", args[1:], stdout, stderr)
	case "deny":
		return runApprovalDecision(ctx, svc, "denied", args[1:], stdout, stderr)
	case "input":
		return runInput(ctx, svc, args[1:], stdout, stderr)
	case "worker":
		return runWorker(ctx, svc, args[1:], stdout, stderr)
	case "memory":
		return runMemory(ctx, svc, args[1:], stdout, stderr)
	case "harness":
		return runHarness(ctx, svc, args[1:], stdout, stderr)
	case "acp":
		return runACP(ctx, svc, args[1:], stdout, stderr, os.Stdin)
	case "terminal":
		return runTerminal(ctx, svc, args[1:], stdout, stderr, os.Stdin)
	case "tui":
		return runTUI(ctx, svc, args[1:], stdout, stderr, os.Stdin)
	case "web":
		return runWeb(ctx, svc, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printRootUsage(stderr)
		return 13
	}
}

type multiString []string

func (m *multiString) String() string { return strings.Join(*m, ",") }

func (m *multiString) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func runTask(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen task [create|list|get|update|patch] ...")
		return 13
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("task create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		kind := fs.String("kind", "", "")
		preset := fs.String("preset", "", "")
		title := fs.String("title", "", "")
		objective := fs.String("objective", "", "")
		taskFile := fs.String("task-file", "", "")
		permissionMode := fs.String("permission-mode", "", "")
		var constraints multiString
		var criteria multiString
		fs.Var(&constraints, "constraint", "")
		fs.Var(&criteria, "criterion", "")
		if err := fs.Parse(args[1:]); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}

		var file task.TaskFile
		switch {
		case *taskFile != "":
			data, err := os.ReadFile(filepath.Clean(*taskFile))
			if err != nil {
				fmt.Fprintf(stderr, "read task file: %v\n", err)
				return 13
			}
			if err := json.Unmarshal(data, &file); err != nil {
				fmt.Fprintf(stderr, "parse task file: %v\n", err)
				return 13
			}
		default:
			file = task.TaskFile{
				Kind:             task.Kind(*kind),
				PresetID:         task.PresetID(*preset),
				Title:            *title,
				Objective:        *objective,
				Constraints:      constraints,
				PermissionModeID: *permissionMode,
			}
			for i, statement := range criteria {
				file.SuccessCriteria = append(file.SuccessCriteria, task.SuccessCriterion{
					ID:        fmt.Sprintf("SC-%03d", i+1),
					Statement: statement,
				})
			}
		}
		spec, err := svc.Create(ctx, file)
		if err != nil {
			fmt.Fprintf(stderr, "create task: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "%s\n", spec.TaskID)
		return 0
	case "list":
		fs := flag.NewFlagSet("task list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: ngen task list [--json]")
			return 13
		}
		entries, err := svc.ListTasks(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "task list: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, entries)
			return 0
		}
		for _, entry := range entries {
			fmt.Fprintf(stdout, "%s %s %s %s %s\n", entry.TaskID, entry.Kind, entry.Phase, entry.State, entry.CurrentStepID)
		}
		return 0
	case "get":
		fs := flag.NewFlagSet("task get", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen task get TASK-ID [--json]")
			return 13
		}
		view, err := svc.GetTask(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "task get: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return 0
		}
		fmt.Fprintf(stdout, "task=%s kind=%s phase=%s state=%s current_step=%s\n", view.Task.TaskID, view.Task.Kind, view.State.Phase, view.State.State, view.Status.CurrentStepID)
		return 0
	case "update":
		fs := flag.NewFlagSet("task update", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		planFile := fs.String("plan-file", "", "")
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--plan-file": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || strings.TrimSpace(*planFile) == "" {
			fmt.Fprintln(stderr, "usage: ngen task update TASK-ID --plan-file FILE [--json]")
			return 13
		}
		var payload []byte
		var err error
		if strings.TrimSpace(*planFile) == "-" {
			payload, err = io.ReadAll(os.Stdin)
		} else {
			payload, err = os.ReadFile(filepath.Clean(*planFile))
		}
		if err != nil {
			fmt.Fprintf(stderr, "task update: read plan file: %v\n", err)
			return 13
		}
		var update task.PlanUpdate
		if err := json.Unmarshal(payload, &update); err != nil {
			fmt.Fprintf(stderr, "task update: parse plan file: %v\n", err)
			return 13
		}
		view, err := svc.UpdateTaskPlan(ctx, fs.Arg(0), update, task.StepSourceOperator)
		if err != nil {
			fmt.Fprintf(stderr, "task update: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return 0
		}
		fmt.Fprintf(stdout, "%s\n", view.Task.TaskID)
		return 0
	case "patch":
		fs := flag.NewFlagSet("task patch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		patchFile := fs.String("patch-file", "", "")
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--patch-file": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || strings.TrimSpace(*patchFile) == "" {
			fmt.Fprintln(stderr, "usage: ngen task patch TASK-ID --patch-file FILE [--json]")
			return 13
		}
		var payload []byte
		var err error
		if strings.TrimSpace(*patchFile) == "-" {
			payload, err = io.ReadAll(os.Stdin)
		} else {
			payload, err = os.ReadFile(filepath.Clean(*patchFile))
		}
		if err != nil {
			fmt.Fprintf(stderr, "task patch: read patch file: %v\n", err)
			return 13
		}
		var patch task.PlanPatch
		if err := json.Unmarshal(payload, &patch); err != nil {
			fmt.Fprintf(stderr, "task patch: parse patch file: %v\n", err)
			return 13
		}
		view, err := svc.PatchTaskPlan(ctx, fs.Arg(0), patch, task.StepSourceOperator)
		if err != nil {
			fmt.Fprintf(stderr, "task patch: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return 0
		}
		fmt.Fprintf(stdout, "%s\n", view.Task.TaskID)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen task [create|list|get|update|patch] ...")
		return 13
	}
}

func runProject(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen project [get|update|patch] ...")
		return 13
	}
	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("project get", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: ngen project get [--json]")
			return 13
		}
		view, err := svc.GetProject(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "project get: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return 0
		}
		fmt.Fprintf(stdout, "revision=%d current_step=%s active_branches=%s\n", view.Project.Revision, view.Project.CurrentStepID, strings.Join(view.Project.ActiveBranchIDs, ","))
		return 0
	case "update":
		fs := flag.NewFlagSet("project update", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		projectFile := fs.String("project-file", "", "")
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--project-file": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 0 || strings.TrimSpace(*projectFile) == "" {
			fmt.Fprintln(stderr, "usage: ngen project update --project-file FILE [--json]")
			return 13
		}
		var payload []byte
		var err error
		if strings.TrimSpace(*projectFile) == "-" {
			payload, err = io.ReadAll(os.Stdin)
		} else {
			payload, err = os.ReadFile(filepath.Clean(*projectFile))
		}
		if err != nil {
			fmt.Fprintf(stderr, "project update: read project file: %v\n", err)
			return 13
		}
		var update task.ProjectUpdate
		if err := json.Unmarshal(payload, &update); err != nil {
			fmt.Fprintf(stderr, "project update: parse project file: %v\n", err)
			return 13
		}
		view, err := svc.UpdateProject(ctx, update, task.StepSourceOperator)
		if err != nil {
			fmt.Fprintf(stderr, "project update: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return 0
		}
		fmt.Fprintf(stdout, "revision=%d\n", view.Project.Revision)
		return 0
	case "patch":
		fs := flag.NewFlagSet("project patch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		patchFile := fs.String("patch-file", "", "")
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--patch-file": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 0 || strings.TrimSpace(*patchFile) == "" {
			fmt.Fprintln(stderr, "usage: ngen project patch --patch-file FILE [--json]")
			return 13
		}
		var payload []byte
		var err error
		if strings.TrimSpace(*patchFile) == "-" {
			payload, err = io.ReadAll(os.Stdin)
		} else {
			payload, err = os.ReadFile(filepath.Clean(*patchFile))
		}
		if err != nil {
			fmt.Fprintf(stderr, "project patch: read patch file: %v\n", err)
			return 13
		}
		var patch task.ProjectPatch
		if err := json.Unmarshal(payload, &patch); err != nil {
			fmt.Fprintf(stderr, "project patch: parse patch file: %v\n", err)
			return 13
		}
		view, err := svc.PatchProject(ctx, patch, task.StepSourceOperator)
		if err != nil {
			fmt.Fprintf(stderr, "project patch: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return 0
		}
		fmt.Fprintf(stdout, "revision=%d\n", view.Project.Revision)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen project [get|update|patch] ...")
		return 13
	}
}

func runMission(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen mission PROMPT... | [create|get|status|plan|approve|validate|run|pause|resume] ...")
		return 13
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("mission create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		title := fs.String("title", "", "")
		objective := fs.String("objective", "", "")
		rootTask := fs.String("root-task", "", "")
		jsonMode := fs.Bool("json", false, "")
		var criteria multiString
		var constraints multiString
		fs.Var(&criteria, "criterion", "")
		fs.Var(&constraints, "constraint", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--title": true, "--objective": true, "--root-task": true, "--criterion": true, "--constraint": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		positionalObjective := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if strings.TrimSpace(*objective) == "" && positionalObjective != "" {
			*objective = positionalObjective
		}
		if strings.TrimSpace(*title) == "" && positionalObjective != "" {
			*title = missionTitleFromPrompt(positionalObjective)
		}
		if fs.NArg() != 0 && strings.TrimSpace(*objective) != positionalObjective {
			fmt.Fprintln(stderr, "mission create: use either positional PROMPT or --objective, not both")
			return 13
		}
		if strings.TrimSpace(*objective) == "" {
			fmt.Fprintln(stderr, "usage: ngen mission create PROMPT... | --title TITLE --objective OBJECTIVE [--root-task TASK-ID] [--criterion TEXT]... [--json]")
			return 13
		}
		view, err := svc.CreateMission(ctx, task.MissionCreateRequest{
			Title:       *title,
			Objective:   *objective,
			RootTaskID:  *rootTask,
			Criteria:    criteria,
			Constraints: constraints,
		})
		if err != nil {
			fmt.Fprintf(stderr, "mission create: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		fmt.Fprintf(stdout, "%s\n", view.Mission.MissionID)
		return missionExitCode(view.Mission.Status)
	case "get", "status":
		fs := flag.NewFlagSet("mission "+args[0], flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintf(stderr, "usage: ngen mission %s MISSION-ID [--json]\n", args[0])
			return 13
		}
		view, err := svc.MissionStatus(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "mission %s: %v\n", args[0], err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		fmt.Fprintf(stdout, "mission=%s status=%s root_task=%s current_milestone=%s\n", view.Mission.MissionID, view.Mission.Status, view.Mission.RootTaskID, view.Mission.CurrentMilestoneID)
		return missionExitCode(view.Mission.Status)
	case "plan":
		fs := flag.NewFlagSet("mission plan", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen mission plan MISSION-ID [--json]")
			return 13
		}
		plan, err := svc.MissionPlan(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "mission plan: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, plan)
			return 0
		}
		fmt.Fprintf(stdout, "mission=%s features=%d milestones=%d\n", plan.MissionID, len(plan.Features.Features), len(plan.Milestones.Milestones))
		return 0
	case "approve":
		fs := flag.NewFlagSet("mission approve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen mission approve MISSION-ID [--json]")
			return 13
		}
		view, err := svc.ApproveMissionPlan(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "mission approve: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		fmt.Fprintf(stdout, "mission=%s approval=%s status=%s\n", view.Mission.MissionID, view.Mission.PlanApprovalStatus, view.Mission.Status)
		return missionExitCode(view.Mission.Status)
	case "validate":
		fs := flag.NewFlagSet("mission validate", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		milestone := fs.String("milestone", "", "")
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--milestone": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen mission validate MISSION-ID [--milestone MILESTONE-ID] [--json]")
			return 13
		}
		view, err := svc.ValidateMission(ctx, fs.Arg(0), *milestone)
		if err != nil {
			fmt.Fprintf(stderr, "mission validate: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		summary := ""
		if view.LatestValidation != nil {
			summary = view.LatestValidation.Summary
		}
		fmt.Fprintf(stdout, "mission=%s status=%s validation=%s\n", view.Mission.MissionID, view.Mission.Status, summary)
		return missionExitCode(view.Mission.Status)
	case "run":
		fs := flag.NewFlagSet("mission run", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen mission run MISSION-ID [--json]")
			return 13
		}
		view, err := svc.RunMission(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "mission run: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		fmt.Fprintf(stdout, "mission=%s status=%s root_task=%s\n", view.Mission.MissionID, view.Mission.Status, view.Mission.RootTaskID)
		return missionExitCode(view.Mission.Status)
	case "pause":
		fs := flag.NewFlagSet("mission pause", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		reason := fs.String("reason", "", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--reason": true})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || strings.TrimSpace(*reason) == "" {
			fmt.Fprintln(stderr, "usage: ngen mission pause MISSION-ID --reason TEXT")
			return 13
		}
		view, err := svc.PauseMission(ctx, fs.Arg(0), *reason)
		if err != nil {
			fmt.Fprintf(stderr, "mission pause: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "mission=%s status=%s reason=%s\n", view.Mission.MissionID, view.Mission.Status, view.Mission.StatusReasonCode)
		return missionExitCode(view.Mission.Status)
	case "resume":
		fs := flag.NewFlagSet("mission resume", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen mission resume MISSION-ID [--json]")
			return 13
		}
		view, err := svc.ResumeMission(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "mission resume: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		fmt.Fprintf(stdout, "mission=%s status=%s\n", view.Mission.MissionID, view.Mission.Status)
		return missionExitCode(view.Mission.Status)
	default:
		objectiveArgs, jsonMode := parsePromptShortcutArgs(args)
		objective := strings.TrimSpace(strings.Join(objectiveArgs, " "))
		if objective == "" {
			fmt.Fprintln(stderr, "usage: ngen mission PROMPT... [--json] | [create|get|status|plan|approve|validate|run|pause|resume] ...")
			return 13
		}
		view, err := svc.CreateMission(ctx, task.MissionCreateRequest{
			Title:     missionTitleFromPrompt(objective),
			Objective: objective,
			Criteria:  []string{fmt.Sprintf("mission objective is satisfied with evidence: %s", missionTitleFromPrompt(objective))},
		})
		if err != nil {
			fmt.Fprintf(stderr, "mission: %v\n", err)
			return 13
		}
		if jsonMode {
			mustJSONObject(stdout, view)
			return missionExitCode(view.Mission.Status)
		}
		fmt.Fprintf(stdout, "mission=%s root_task=%s status=%s\n", view.Mission.MissionID, view.Mission.RootTaskID, view.Mission.Status)
		return missionExitCode(view.Mission.Status)
	}
}

func runGoal(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	objectiveArgs, jsonMode := parsePromptShortcutArgs(args)
	objective := strings.TrimSpace(strings.Join(objectiveArgs, " "))
	if objective == "" {
		fmt.Fprintln(stderr, "usage: ngen goal PROMPT... [--json]")
		return 13
	}
	view, err := svc.CreateMission(ctx, task.MissionCreateRequest{
		Title:     missionTitleFromPrompt(objective),
		Objective: objective,
		Criteria:  []string{fmt.Sprintf("goal objective is satisfied with evidence: %s", missionTitleFromPrompt(objective))},
	})
	if err != nil {
		fmt.Fprintf(stderr, "goal: %v\n", err)
		return 13
	}
	if jsonMode {
		mustJSONObject(stdout, view)
		return missionExitCode(view.Mission.Status)
	}
	fmt.Fprintf(stdout, "mission=%s root_task=%s status=%s\n", view.Mission.MissionID, view.Mission.RootTaskID, view.Mission.Status)
	return missionExitCode(view.Mission.Status)
}

func parsePromptShortcutArgs(args []string) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	jsonMode := false
	for _, arg := range args {
		if arg == "--json" {
			jsonMode = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, jsonMode
}

func missionTitleFromPrompt(prompt string) string {
	title := strings.Join(strings.Fields(strings.TrimSpace(prompt)), " ")
	if title == "" {
		return "Mission"
	}
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	return strings.TrimSpace(title)
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--json": false})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: ngen version [--json]")
		return 13
	}
	info := version.Current()
	if *jsonMode {
		mustJSONObject(stdout, info)
		return 0
	}
	fmt.Fprintf(stdout, "%s %s\n", info.Name, info.Version)
	return 0
}

func runModels(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "")
	workdir := fs.String("workdir", "", "")
	configPath := fs.String("config", "", "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--json": false, "--workdir": true, "--config": true})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: ngen models --json [--workdir DIR] [--config FILE]")
		return 13
	}
	resolution, err := multica.ResolveConfig(*workdir, *configPath, "")
	if err != nil {
		fmt.Fprintf(stderr, "models: %v\n", err)
		return 13
	}
	models := multica.ListModels(resolution)
	if *jsonMode {
		mustJSONObject(stdout, models)
		return 0
	}
	for _, model := range models {
		marker := ""
		if model.Default {
			marker = " default"
		}
		fmt.Fprintf(stdout, "%s %s%s\n", model.ID, model.Label, marker)
	}
	return 0
}

func runExec(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	outputFormat := fs.String("output-format", "", "")
	inputFormat := fs.String("input-format", "", "")
	configScope := fs.String("config-scope", "", "")
	workdir := fs.String("workdir", "", "")
	configPath := fs.String("config", "", "")
	resume := fs.String("resume", "", "")
	role := fs.String("role", "", "")
	timeoutSeconds := fs.Int("timeout-seconds", 0, "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"--output-format":   true,
		"--input-format":    true,
		"--config-scope":    true,
		"--workdir":         true,
		"--config":          true,
		"--resume":          true,
		"--role":            true,
		"--timeout-seconds": true,
	})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 0 || *outputFormat != "stream-json" || *inputFormat != "stream-json" {
		fmt.Fprintln(stderr, "usage: ngen exec --output-format stream-json --input-format stream-json --workdir DIR [--config-scope daemon] [--config FILE] [--resume TASK-ID] [--role ROLE] [--timeout-seconds N]")
		return 13
	}
	return multica.RunExec(ctx, multica.ExecOptions{
		Workdir:        *workdir,
		ConfigPath:     *configPath,
		ConfigScope:    *configScope,
		ResumeTaskID:   *resume,
		RunRole:        *role,
		TimeoutSeconds: *timeoutSeconds,
	}, stdin, stdout, stderr)
}

func runStreaming(ctx context.Context, svc *ngenrt.Service, verb string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--json": false})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: ngen %s TASK-ID [--json]\n", verb)
		return 13
	}
	taskID := fs.Arg(0)
	var snapshot task.StatusSnapshot
	var events []task.Event
	var err error
	switch verb {
	case "run":
		snapshot, events, err = svc.Run(ctx, taskID)
	case "resume":
		snapshot, events, err = svc.Resume(ctx, taskID)
	case "auto":
		snapshot, events, err = svc.Auto(ctx, taskID)
	default:
		err = fmt.Errorf("unsupported streaming verb: %s", verb)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, err)
		return 13
	}
	if *jsonMode {
		for _, event := range events {
			mustJSONLine(stdout, event)
		}
	} else {
		for _, event := range events {
			fmt.Fprintf(stdout, "%s %s %s\n", event.TS, event.Type, event.Summary)
		}
		fmt.Fprintf(stdout, "state=%s phase=%s reason=%s\n", snapshot.State, snapshot.Phase, snapshot.StatusReasonCode)
	}
	return exitCodeFromState(snapshot.State)
}

func runStatus(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--json": false})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ngen status TASK-ID [--json]")
		return 13
	}
	snapshot, err := svc.Status(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 13
	}
	if *jsonMode {
		mustJSONObject(stdout, snapshot)
	} else {
		fmt.Fprintf(stdout, "task=%s phase=%s state=%s reason=%s\n", snapshot.TaskID, snapshot.Phase, snapshot.State, snapshot.StatusReasonCode)
	}
	return exitCodeFromState(snapshot.State)
}

func runReview(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--json": false})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ngen review TASK-ID [--json]")
		return 13
	}
	report, err := svc.Review(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "review: %v\n", err)
		return 13
	}
	if *jsonMode {
		mustJSONObject(stdout, report)
	} else {
		fmt.Fprintf(stdout, "status=%s summary=%s\n", report.Status, report.Summary)
	}
	if report.Status == "clear" {
		return 0
	}
	return 10
}

func runEvents(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "tail" {
		fmt.Fprintln(stderr, "usage: ngen events tail TASK-ID [--json] [--limit N] [--after EVENT-ID]")
		return 13
	}
	fs := flag.NewFlagSet("events tail", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "")
	limit := fs.Int("limit", 20, "")
	after := fs.String("after", "", "")
	if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false, "--limit": true, "--after": true})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ngen events tail TASK-ID [--json] [--limit N] [--after EVENT-ID]")
		return 13
	}
	events, err := svc.TailEventsAfter(fs.Arg(0), *after, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "events tail: %v\n", err)
		return 13
	}
	for _, event := range events {
		if *jsonMode {
			mustJSONLine(stdout, event)
		} else {
			fmt.Fprintf(stdout, "%s %s %s\n", event.TS, event.Type, event.Summary)
		}
	}
	return 0
}

func runHandoff(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	_ = ctx
	if len(args) < 2 || args[0] != "export" {
		fmt.Fprintln(stderr, "usage: ngen handoff export TASK-ID")
		return 13
	}
	if err := svc.Store.ExportHandoff(args[1], stdout); err != nil {
		fmt.Fprintf(stderr, "handoff export: %v\n", err)
		return 13
	}
	return 0
}

func runWatch(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen watch [set|ls|cancel] ...")
		return 13
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("watch set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		interval := fs.Duration("interval", 0, "")
		reason := fs.String("reason", "", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--interval": true, "--reason": true})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen watch set TASK-ID --interval 5m --reason ...")
			return 13
		}
		watch, err := svc.SetWatch(ctx, fs.Arg(0), *interval, *reason)
		if err != nil {
			fmt.Fprintf(stderr, "watch set: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "%s\n", watch.WatchID)
		return 15
	case "ls":
		watches, err := svc.ListWatches(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "watch ls: %v\n", err)
			return 13
		}
		for _, watch := range watches {
			fmt.Fprintf(stdout, "%s %s %s %s\n", watch.WatchID, watch.TaskID, watch.Status, watch.NextWakeAt)
		}
		return 0
	case "cancel":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: ngen watch cancel WATCH-ID")
			return 13
		}
		watch, err := svc.CancelWatch(ctx, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "watch cancel: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "%s %s\n", watch.WatchID, watch.Status)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen watch [set|ls|cancel] ...")
		return 13
	}
}

func runScheduler(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "tick" || args[1] != "--once" {
		fmt.Fprintln(stderr, "usage: ngen scheduler tick --once")
		return 13
	}
	resumed, err := svc.SchedulerTick(ctx, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "scheduler tick: %v\n", err)
		return 13
	}
	for _, taskID := range resumed {
		fmt.Fprintln(stdout, taskID)
	}
	return 0
}

func runApproval(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen approval [request|ls] ...")
		return 13
	}
	switch args[0] {
	case "request":
		fs := flag.NewFlagSet("approval request", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		scope := fs.String("scope", "", "")
		reason := fs.String("reason", "", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--scope": true, "--reason": true})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || *scope == "" {
			fmt.Fprintln(stderr, "usage: ngen approval request TASK-ID --scope ... --reason ...")
			return 13
		}
		record, err := svc.RequestApproval(ctx, fs.Arg(0), *scope, *reason)
		if err != nil {
			fmt.Fprintf(stderr, "approval request: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "%s\n", record.ApprovalID)
		return 10
	case "ls":
		fs := flag.NewFlagSet("approval ls", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		owned := fs.Bool("owned", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--owned": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen approval ls TASK-ID [--owned]")
			return 13
		}
		var (
			records []task.ApprovalRecord
			err     error
		)
		if *owned {
			records, err = svc.ListOwnedApprovals(ctx, fs.Arg(0))
		} else {
			records, err = svc.ListApprovals(ctx, fs.Arg(0))
		}
		if err != nil {
			fmt.Fprintf(stderr, "approval ls: %v\n", err)
			return 13
		}
		mustJSONObject(stdout, records)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen approval [request|ls] ...")
		return 13
	}
}

func runApprovalDecision(ctx context.Context, svc *ngenrt.Service, decision string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(decision, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	request := fs.String("request", "", "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--request": true})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 1 || *request == "" {
		fmt.Fprintf(stderr, "usage: ngen %s TASK-ID --request APR-...\n", map[string]string{"approved": "approve", "denied": "deny"}[decision])
		return 13
	}
	record, err := svc.DecideApproval(ctx, fs.Arg(0), *request, decision)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", decision, err)
		return 13
	}
	fmt.Fprintf(stdout, "%s %s\n", record.ApprovalID, record.Status)
	if decision == "approved" {
		return 0
	}
	return 10
}

func runInput(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen input [request|ls|respond] ...")
		return 13
	}
	switch args[0] {
	case "request":
		fs := flag.NewFlagSet("input request", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		field := fs.String("field", "", "")
		prompt := fs.String("prompt", "", "")
		optional := fs.Bool("optional", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--field": true, "--prompt": true, "--optional": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || strings.TrimSpace(*prompt) == "" {
			fmt.Fprintln(stderr, "usage: ngen input request TASK-ID --prompt ... [--field ...] [--optional]")
			return 13
		}
		record, err := svc.RequestInput(ctx, fs.Arg(0), *field, *prompt, !*optional)
		if err != nil {
			fmt.Fprintf(stderr, "input request: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "%s\n", record.RequestID)
		return 10
	case "ls":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: ngen input ls TASK-ID")
			return 13
		}
		records, err := svc.ListInputRequests(ctx, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "input ls: %v\n", err)
			return 13
		}
		mustJSONObject(stdout, records)
		return 0
	case "respond":
		fs := flag.NewFlagSet("input respond", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		requestID := fs.String("request", "", "")
		value := fs.String("value", "", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--request": true, "--value": true})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || strings.TrimSpace(*requestID) == "" {
			fmt.Fprintln(stderr, "usage: ngen input respond TASK-ID --request INP-... --value ...")
			return 13
		}
		record, err := svc.RespondInput(ctx, fs.Arg(0), *requestID, *value)
		if err != nil {
			fmt.Fprintf(stderr, "input respond: %v\n", err)
			return 13
		}
		fmt.Fprintf(stdout, "%s %s\n", record.RequestID, record.Status)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen input [request|ls|respond] ...")
		return 13
	}
}

func runWorker(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen worker [spawn|ls|sync|continue] ...")
		return 13
	}
	switch args[0] {
	case "spawn":
		fs := flag.NewFlagSet("worker spawn", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		role := fs.String("role", "reviewer", "")
		objective := fs.String("objective", "", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--role": true, "--objective": true})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || *objective == "" {
			fmt.Fprintln(stderr, "usage: ngen worker spawn PARENT-TASK-ID --role reviewer --objective ...")
			return 13
		}
		contract, err := svc.SpawnWorker(ctx, fs.Arg(0), *role, *objective)
		if err != nil {
			fmt.Fprintf(stderr, "worker spawn: %v\n", err)
			return 13
		}
		mustJSONObject(stdout, contract)
		return 0
	case "ls":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: ngen worker ls PARENT-TASK-ID")
			return 13
		}
		workers, err := svc.ListWorkers(ctx, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "worker ls: %v\n", err)
			return 13
		}
		mustJSONObject(stdout, workers)
		return 0
	case "sync":
		if len(args) != 3 {
			fmt.Fprintln(stderr, "usage: ngen worker sync PARENT-TASK-ID WORKER-ID")
			return 13
		}
		contract, err := svc.SyncWorker(ctx, args[1], args[2])
		if err != nil {
			fmt.Fprintf(stderr, "worker sync: %v\n", err)
			return 13
		}
		mustJSONObject(stdout, contract)
		return 0
	case "continue":
		if len(args) != 3 {
			fmt.Fprintln(stderr, "usage: ngen worker continue PARENT-TASK-ID WORKER-ID")
			return 13
		}
		contract, err := svc.ContinueWorker(ctx, args[1], args[2])
		if err != nil {
			fmt.Fprintf(stderr, "worker continue: %v\n", err)
			return 13
		}
		mustJSONObject(stdout, contract)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen worker [spawn|ls|sync|continue] ...")
		return 13
	}
}

func runMemory(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen memory [show|promote] ...")
		return 13
	}
	switch args[0] {
	case "show":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: ngen memory show")
			return 13
		}
		data, err := svc.MemoryMarkdown(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "memory show: %v\n", err)
			return 13
		}
		_, _ = stdout.Write(data)
		return 0
	case "promote":
		fs := flag.NewFlagSet("memory promote", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		summary := fs.String("summary", "", "")
		kind := fs.String("kind", task.MemoryKindTaskNote, "")
		jsonMode := fs.Bool("json", false, "")
		var refs multiString
		fs.Var(&refs, "ref", "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--summary": true, "--kind": true, "--ref": true, "--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 || strings.TrimSpace(*summary) == "" {
			fmt.Fprintln(stderr, "usage: ngen memory promote TASK-ID --summary TEXT [--kind KIND] [--ref REF]... [--json]")
			return 13
		}
		entry, err := svc.PromoteMemory(ctx, fs.Arg(0), task.MemoryPromotion{
			Kind:    *kind,
			Summary: *summary,
			Refs:    []string(refs),
		}, task.MemorySourceOperator)
		if err != nil {
			fmt.Fprintf(stderr, "memory promote: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, entry)
			return 0
		}
		fmt.Fprintf(stdout, "%s\n", entry.EntryID)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen memory [show|promote] ...")
		return 13
	}
}

func runHarness(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ngen harness eval TASK-ID [--json]")
		return 13
	}
	switch args[0] {
	case "eval":
		fs := flag.NewFlagSet("harness eval", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		jsonMode := fs.Bool("json", false, "")
		if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--json": false})); err != nil {
			fmt.Fprintln(stderr, err)
			return 13
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: ngen harness eval TASK-ID [--json]")
			return 13
		}
		eval, err := svc.HarnessEvaluation(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "harness eval: %v\n", err)
			return 13
		}
		if *jsonMode {
			mustJSONObject(stdout, eval)
			return 0
		}
		fmt.Fprintf(stdout, "task=%s action=%s provider=%s verification=%s review=%s completion=%s\n", eval.TaskID, eval.RuntimeAction, eval.ProviderMode, eval.VerificationStatus, eval.ReviewStatus, eval.CompletionStatus)
		return 0
	default:
		fmt.Fprintln(stderr, "usage: ngen harness eval TASK-ID [--json]")
		return 13
	}
}

func runACP(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	_ = stderr
	if len(args) != 1 || args[0] != "serve" {
		fmt.Fprintln(stderr, "usage: ngen acp serve")
		return 13
	}
	server := acp.Server{Service: svc}
	if err := server.Serve(ctx, stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "acp serve: %v\n", err)
		return 13
	}
	return 0
}

func runTerminal(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: ngen terminal TASK-ID")
		return 13
	}
	session, err := svc.StartSession(ctx, args[0], "terminal")
	if err != nil {
		fmt.Fprintf(stderr, "terminal: %v\n", err)
		return 13
	}
	fmt.Fprintf(stdout, "session=%s task=%s\n", session.SessionID, session.TaskID)
	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprint(stdout, "> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			break
		}
		if strings.HasPrefix(line, "/status") {
			snapshot, err := svc.Status(ctx, session.TaskID)
			if err != nil {
				fmt.Fprintf(stderr, "status: %v\n", err)
				return 13
			}
			mustJSONObject(stdout, snapshot)
			continue
		}
		if strings.HasPrefix(line, "/review") {
			report, err := svc.Review(ctx, session.TaskID)
			if err != nil {
				fmt.Fprintf(stderr, "review: %v\n", err)
				return 13
			}
			mustJSONObject(stdout, report)
			continue
		}
		updated, snapshot, events, err := svc.PromptSession(ctx, session.SessionID, line)
		if err != nil {
			fmt.Fprintf(stderr, "prompt: %v\n", err)
			return 13
		}
		session = updated
		for _, event := range events {
			fmt.Fprintf(stdout, "%s %s %s\n", event.TS, event.Type, event.Summary)
		}
		mustJSONObject(stdout, snapshot)
	}
	return 0
}

func runTUI(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inline := fs.Bool("inline", false, "")
	pollMS := fs.Int("poll-ms", svc.Config.TUI.PollIntervalMS, "")
	eventLimit := fs.Int("event-limit", svc.Config.TUI.EventLimit, "")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"--inline": false, "--poll-ms": true, "--event-limit": true})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "usage: ngen tui [TASK-ID] [--inline] [--poll-ms N] [--event-limit N]")
		return 13
	}
	taskID := ""
	if fs.NArg() == 1 {
		taskID = fs.Arg(0)
	}

	if err := ngentui.Run(ctx, svc, ngentui.Options{
		TaskID:       taskID,
		Inline:       *inline,
		PollInterval: time.Duration(*pollMS) * time.Millisecond,
		EventLimit:   *eventLimit,
		ProviderMode: svc.Config.Provider.Mode,
		SimpleMode:   true,
	}, stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "tui: %v\n", err)
		return 13
	}
	return 0
}

func runWeb(ctx context.Context, svc *ngenrt.Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, "usage: ngen web serve [--listen ADDR] [--token-env ENV] [--allow-unauthenticated]")
		return 13
	}
	fs := flag.NewFlagSet("web serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "127.0.0.1:8765", "")
	tokenEnv := fs.String("token-env", "NGEN_WEB_TOKEN", "")
	allowUnauthenticated := fs.Bool("allow-unauthenticated", false, "")
	if err := fs.Parse(normalizeFlagArgs(args[1:], map[string]bool{"--listen": true, "--token-env": true, "--allow-unauthenticated": false})); err != nil {
		fmt.Fprintln(stderr, err)
		return 13
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: ngen web serve [--listen ADDR] [--token-env ENV] [--allow-unauthenticated]")
		return 13
	}
	token := ""
	if strings.TrimSpace(*tokenEnv) != "" {
		token = os.Getenv(strings.TrimSpace(*tokenEnv))
	}
	if token == "" && !*allowUnauthenticated && webListenRequiresToken(*listen) {
		fmt.Fprintf(stderr, "web serve refuses unauthenticated non-loopback listen address %q; set %s or pass --allow-unauthenticated\n", *listen, strings.TrimSpace(*tokenEnv))
		return 13
	}
	httpServer := &http.Server{
		Addr:    *listen,
		Handler: web.Server{Service: svc, Token: token}.Handler(),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(stdout, "ngen web listening on http://%s\n", *listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "web serve: %v\n", err)
		return 13
	}
	return 0
}

func webListenRequiresToken(addr string) bool {
	host := strings.TrimSpace(addr)
	if parsedHost, _, err := net.SplitHostPort(addr); err == nil {
		host = parsedHost
	} else if strings.HasPrefix(addr, ":") {
		host = ""
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback()
}

func mustJSONObject(w io.Writer, value any) {
	data, _ := json.MarshalIndent(value, "", "  ")
	_, _ = fmt.Fprintln(w, string(data))
}

func mustJSONLine(w io.Writer, value any) {
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintln(w, string(data))
}

func exitCodeFromState(state task.StateName) int {
	switch state {
	case task.StateDone:
		return 0
	case task.StateBlocked:
		return 10
	case task.StateFailed:
		return 11
	case task.StateAborted:
		return 12
	case task.StateWaiting:
		return 15
	default:
		return 0
	}
}

func missionExitCode(status string) int {
	switch strings.TrimSpace(status) {
	case task.MissionStatusDone:
		return 0
	case task.MissionStatusBlocked:
		return 10
	case task.MissionStatusPaused:
		return 15
	default:
		return 0
	}
}

func printRootUsage(w io.Writer) {
	io.WriteString(w, strings.TrimSpace(`
usage:
  ngen task [create|list|get|update|patch] ...
  ngen project [get|update|patch] ...
  ngen mission PROMPT... [--json] | [create|get|status|plan|approve|validate|run|pause|resume] ...
  ngen goal PROMPT... [--json]
  ngen --version
  ngen version [--json]
  ngen models --json [--workdir DIR] [--config FILE]
  ngen exec --output-format stream-json --input-format stream-json --workdir DIR [--config-scope daemon]
  ngen auto TASK-ID [--json]
  ngen run TASK-ID [--json]
  ngen resume TASK-ID [--json]
  ngen status TASK-ID [--json]
  ngen review TASK-ID [--json]
  ngen events tail TASK-ID [--json] [--after EVENT-ID]
  ngen handoff export TASK-ID
  ngen watch [set|ls|cancel] ...
  ngen scheduler tick --once
  ngen approval [request|ls] ...
  ngen approve TASK-ID --request APR-...
  ngen deny TASK-ID --request APR-...
  ngen input [request|ls|respond] ...
  ngen worker [spawn|ls|sync|continue] ...
  ngen memory [show|promote] ...
  ngen harness eval TASK-ID [--json]
  ngen acp serve
  ngen terminal TASK-ID
  ngen tui [TASK-ID] [--inline] [--poll-ms N] [--event-limit N]
  ngen web serve [--listen ADDR] [--token-env ENV]
`)+"\n")
}

func normalizeFlagArgs(args []string, valuedFlags map[string]bool) []string {
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if hasValue, ok := valuedFlags[arg]; ok {
			flags = append(flags, arg)
			if hasValue && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

func MustWorkspace() (string, error) {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if workspaceRoot == "" {
		return "", errors.New("empty workspace root")
	}
	return workspaceRoot, nil
}
