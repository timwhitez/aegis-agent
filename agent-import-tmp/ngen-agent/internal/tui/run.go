package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	ngenrt "ngen/internal/runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
)

func Run(ctx context.Context, service *ngenrt.Service, opts Options, stdin io.Reader, stdout io.Writer) error {
	_ = ctx
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return fmt.Errorf("TERM is set to \"dumb\"; refusing to start the interactive TUI")
	}
	inFile, ok := stdin.(*os.File)
	if !ok || !isatty.IsTerminal(inFile.Fd()) {
		return fmt.Errorf("stdin is not a TTY")
	}
	outFile, ok := stdout.(*os.File)
	if !ok || !isatty.IsTerminal(outFile.Fd()) {
		return fmt.Errorf("stdout is not a TTY")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	if opts.EventLimit <= 0 {
		opts.EventLimit = 500
	}
	model := newModel(NewBackend(service), opts)
	programOptions := []tea.ProgramOption{
		tea.WithInput(stdin),
		tea.WithOutput(stdout),
	}
	if shouldUseAltScreen(service.Config.TUI.AlternateScreen, opts.Inline) {
		programOptions = append(programOptions, tea.WithAltScreen())
	}
	program := tea.NewProgram(model, programOptions...)
	if _, err := program.Run(); err != nil {
		return err
	}
	return nil
}

func shouldUseAltScreen(mode string, inline bool) bool {
	if inline {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "never":
		return false
	case "always":
		return true
	default:
		return strings.TrimSpace(os.Getenv("ZELLIJ")) == ""
	}
}
