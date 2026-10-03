package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

type cliApprovalFlags struct {
	planModeID string
	version    int
	revision   string
	latest     bool
	requestID  string
	receipt    bool
}

func registerCLIApprovalFlags(fs *flag.FlagSet) *cliApprovalFlags {
	flags := &cliApprovalFlags{}
	fs.StringVar(&flags.planModeID, "plan-mode-id", "", "ID of the reviewed Plan Mode")
	fs.IntVar(&flags.version, "plan-version", 0, "version of the reviewed plan")
	fs.StringVar(&flags.revision, "expected-revision", "", "revision of the reviewed approval content")
	fs.BoolVar(&flags.latest, "approve-latest", false, "explicitly approve the current content captured when this command runs")
	fs.StringVar(&flags.requestID, "approval-request-id", "", "explicit approval operation identity; keep it for retries")
	fs.BoolVar(&flags.receipt, "approval-receipt", false, "query an approval receipt without executing")
	return flags
}

func (flags *cliApprovalFlags) hasTarget() bool {
	return flags.planModeID != "" || flags.version != 0 || flags.revision != ""
}

func (flags *cliApprovalFlags) validate(approval bool) error {
	if flags.receipt {
		if flags.requestID == "" {
			return session.ErrMissingApprovalRequestID
		}
		if approval || flags.hasTarget() || flags.latest {
			return errors.New("--approval-receipt is a read-only query; do not combine it with approval flags")
		}
		return nil
	}
	if !approval && flags.requestID != "" {
		return errors.New("--approval-request-id requires an approval or --approval-receipt")
	}
	if !approval && (flags.hasTarget() || flags.latest) {
		return errors.New("approval target flags require --approve-plan")
	}
	if flags.latest && flags.hasTarget() {
		return errors.New("--approve-latest cannot be combined with an explicit approval target")
	}
	if flags.hasTarget() && (strings.TrimSpace(flags.planModeID) == "" || flags.version <= 0 || strings.TrimSpace(flags.revision) == "") {
		return errors.New("approval requires --plan-mode-id, --plan-version and --expected-revision together")
	}
	return nil
}

// Basic configuration locates the Store; the core validates the entire receipt
// ledger before a binding can supply the original target for a retry.
func resolveCLIApprovalOperationTarget(ctx context.Context, runner coreRunner, store *session.Store, sessionID string, flags *cliApprovalFlags, stdin io.Reader, stderr io.Writer) (*session.ApprovalTarget, error) {
	if flags.requestID == "" {
		return nil, session.ErrMissingApprovalRequestID
	}
	if !flags.hasTarget() {
		lookup, err := runner.ApprovalReceipt(sessionID, flags.requestID)
		if err == nil {
			target := lookup.Binding.Parameters.Target
			return &target, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	target, err := resolveCLIApprovalTarget(ctx, store, sessionID, flags, stdin, stderr)
	if err == nil && flags.latest {
		fmt.Fprintln(stderr, "Captured approval operation; keep this identity and target for retries:")
		err = json.NewEncoder(stderr).Encode(map[string]any{"approval_request_id": flags.requestID, "target": target})
	}
	return target, err
}

func printCLIApprovalResult(stdout io.Writer, jsonMode bool, result runtime.RunResult, store *session.Store, cause error) error {
	if result.Status == "" && result.Approval != nil && !result.Approval.Lookup.Found {
		result.Status = "not_admitted"
	}
	var current *session.State
	var currentError string
	if store != nil {
		state, err := store.LoadState(result.SessionID)
		if err == nil {
			current = &state
		} else {
			currentError = err.Error()
		}
	}
	exitCode := approvalExitCode(result)
	if cause != nil {
		exitCode = 1
	}
	if jsonMode {
		payload := map[string]any{"session_id": result.SessionID, "status": result.Status, "final_text": result.FinalText, "last_error": result.LastError, "exit_code": exitCode, "approval": result.Approval, "current_state": current}
		if currentError != "" {
			payload["current_state_error"] = currentError
		}
		if cause != nil {
			payload["error"] = cause.Error()
		}
		if err := json.NewEncoder(stdout).Encode(payload); err != nil {
			return err
		}
	} else {
		if result.Approval != nil {
			payload := map[string]any{"session_id": result.SessionID, "approval": result.Approval, "current_state": current}
			if currentError != "" {
				payload["current_state_error"] = currentError
			}
			if err := json.NewEncoder(stdout).Encode(payload); err != nil {
				return err
			}
		}
		if result.FinalText != "" {
			fmt.Fprintln(stdout, result.FinalText)
		}
	}
	if cause != nil {
		return cause
	}
	if exitCode != 0 {
		return ExitError{Code: exitCode}
	}
	return nil
}

func approvalExitCode(result runtime.RunResult) int {
	if result.Approval != nil {
		if result.Approval.RecoveryRequired || result.Approval.Lookup.Receipt.Stage == session.ApprovalReceiptRejected {
			return 1
		}
		if result.Approval.Replay && result.Approval.Lookup.Receipt.Stage == session.ApprovalReceiptAdmitted {
			return 0
		}
	}
	return mapStatusToExitCode(result.Status, result.LastError)
}

func queryCLIApprovalReceipt(runner coreRunner, store *session.Store, id, requestID string, jsonMode bool, stdout io.Writer) error {
	lookup, err := runner.ApprovalReceipt(id, requestID)
	if err != nil {
		return err
	}
	return printCLIApprovalResult(stdout, jsonMode, runtime.RunResult{SessionID: id, Status: string(lookup.Receipt.Stage), Approval: &runtime.ApprovalResult{Lookup: lookup, Replay: true}}, store, nil)
}

func resolveCLIApprovalTarget(ctx context.Context, store *session.Store, sessionID string, flags *cliApprovalFlags, stdin io.Reader, stderr io.Writer) (*session.ApprovalTarget, error) {
	if flags.hasTarget() {
		return &session.ApprovalTarget{PlanModeID: strings.TrimSpace(flags.planModeID), PlanVersion: flags.version, ExpectedRevision: strings.TrimSpace(flags.revision)}, nil
	}
	if !flags.latest && !stdinIsTerminal() {
		return nil, errors.New("reviewed approval target required: provide --plan-mode-id, --plan-version and --expected-revision, or explicitly choose --approve-latest")
	}
	if store == nil {
		return nil, errors.New("approval snapshot store is required")
	}
	snapshot, err := store.LoadApprovalSnapshot(sessionID)
	if err != nil {
		return nil, err
	}
	target := snapshot.Target()
	if !flags.latest {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fmt.Fprintln(stderr, "Review the following approval content:")
		encoder := json.NewEncoder(stderr)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(snapshot); err != nil {
			return nil, err
		}
		fmt.Fprint(stderr, "Approve this reviewed plan and run? [y/N] ")
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return nil, errors.New("plan approval cancelled")
		}
	}
	return &target, nil
}
