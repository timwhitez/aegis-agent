package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type editorTarget int

const (
	editorTargetComposer editorTarget = iota
	editorTargetInput
)

type editorFinishedMsg struct {
	Target  editorTarget
	Content string
	Err     error
}

var errMissingEditor = errors.New("Cannot open external editor: set $VISUAL or $EDITOR before starting NGEN.")

func externalEditorCmd(target editorTarget, seed string) tea.Cmd {
	editorCommand, err := resolveEditorCommand()
	if err != nil {
		return func() tea.Msg {
			return editorFinishedMsg{Target: target, Err: err}
		}
	}
	tempPath, err := writeEditorSeed(seed)
	if err != nil {
		return func() tea.Msg {
			return editorFinishedMsg{Target: target, Err: err}
		}
	}
	command := buildEditorProcess(editorCommand, tempPath)
	return tea.ExecProcess(command, func(runErr error) tea.Msg {
		defer os.Remove(tempPath)
		if runErr != nil {
			return editorFinishedMsg{Target: target, Err: runErr}
		}
		content, err := readEditedContent(tempPath)
		return editorFinishedMsg{Target: target, Content: content, Err: err}
	})
}

func resolveEditorCommand() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("VISUAL"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if raw == "" {
		return nil, errMissingEditor
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return nil, errMissingEditor
	}
	return parts, nil
}

func writeEditorSeed(seed string) (string, error) {
	file, err := os.CreateTemp("", "ngen-tui-editor-*.md")
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err := file.WriteString(seed); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func buildEditorProcess(editorCommand []string, tempPath string) *exec.Cmd {
	argv := append([]string{}, editorCommand...)
	if shouldAppendEditorWaitFlag(argv) {
		argv = append(argv, editorWaitFlag(argv[0]))
	}
	argv = append(argv, tempPath)
	return exec.Command(argv[0], argv[1:]...)
}

func shouldAppendEditorWaitFlag(editorCommand []string) bool {
	if len(editorCommand) == 0 {
		return false
	}
	flag := editorWaitFlag(editorCommand[0])
	if flag == "" {
		return false
	}
	for _, existing := range editorCommand[1:] {
		if existing == flag {
			return false
		}
	}
	return true
}

func editorWaitFlag(program string) string {
	switch strings.ToLower(filepath.Base(strings.TrimSpace(program))) {
	case "code", "code-insiders", "codium", "cursor":
		return "-w"
	case "subl":
		return "--wait"
	default:
		return ""
	}
}

func readEditedContent(tempPath string) (string, error) {
	content, err := os.ReadFile(tempPath)
	if err != nil {
		return "", err
	}
	return trimSingleTrailingNewline(string(content)), nil
}

func trimSingleTrailingNewline(content string) string {
	if strings.HasSuffix(content, "\n") && !strings.HasSuffix(content, "\n\n") {
		return strings.TrimSuffix(content, "\n")
	}
	return content
}
