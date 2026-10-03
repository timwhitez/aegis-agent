package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"aegis-agent/internal/session"
)

type cliApprovalFlags struct {
	planModeID string
	version    int
	revision   string
	latest     bool
}

func registerCLIApprovalFlags(fs *flag.FlagSet) *cliApprovalFlags {
	flags := &cliApprovalFlags{}
	fs.StringVar(&flags.planModeID, "plan-mode-id", "", "ID of the reviewed Plan Mode")
	fs.IntVar(&flags.version, "plan-version", 0, "version of the reviewed plan")
	fs.StringVar(&flags.revision, "expected-revision", "", "revision of the reviewed approval content")
	fs.BoolVar(&flags.latest, "approve-latest", false, "explicitly approve the current content captured when this command runs")
	return flags
}

func (flags *cliApprovalFlags) hasTarget() bool {
	return flags.planModeID != "" || flags.version != 0 || flags.revision != ""
}

func (flags *cliApprovalFlags) validate(approval bool) error {
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
