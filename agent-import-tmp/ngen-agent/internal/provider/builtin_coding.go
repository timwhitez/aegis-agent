package provider

import (
	"fmt"
	"regexp"
	"strings"

	"ngen/internal/task"
)

type builtinFunctionTemplate struct {
	Params     string
	ReturnType string
	Body       string
}

var (
	builtinUndefinedNamePattern = regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`)
	builtinNameTokenPattern     = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)
	builtinTestCallPattern      = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\(`)
	builtinPackagePattern       = regexp.MustCompile(`(?m)^package\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	builtinFuncStartPattern     = regexp.MustCompile(`(?m)^func\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	builtinFunctionTemplates    = map[string]builtinFunctionTemplate{
		"add": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a + b",
		},
		"sum": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a + b",
		},
		"multiply": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a * b",
		},
		"mul": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a * b",
		},
		"product": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a * b",
		},
		"subtract": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a - b",
		},
		"sub": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a - b",
		},
		"minus": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a - b",
		},
		"divide": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a / b",
		},
		"div": {
			Params:     "a, b int",
			ReturnType: "int",
			Body:       "return a / b",
		},
		"concat": {
			Params:     "a, b string",
			ReturnType: "string",
			Body:       "return a + b",
		},
		"join": {
			Params:     "a, b string",
			ReturnType: "string",
			Body:       "return a + b",
		},
	}
)

func generateWorkspaceObservationsBuiltin(input WorkspaceObservationInput) (WorkspaceObservationPlan, error) {
	if input.CommandBudget <= 0 {
		return WorkspaceObservationPlan{
			Summary: "Builtin repair engine observation budget is disabled.",
		}, nil
	}
	if !input.Collection.Truncated && input.Collection.OmittedFileCount == 0 {
		return WorkspaceObservationPlan{
			Summary: "Builtin repair engine does not need extra observation commands.",
		}, nil
	}

	candidates := builtinCandidateFunctionNames(input.Task.Objective, failureSummary(input.RecentVerification), input.Files)
	commands := make([]ObservationCommand, 0, input.CommandBudget)
	for _, name := range candidates {
		if builtinNamePresentInFiles(input.Files, name) {
			continue
		}
		commands = append(commands, ObservationCommand{
			Argv:   []string{"rg", "-n", name, "."},
			Reason: fmt.Sprintf("Locate %s in omitted workspace files before applying a builtin repair.", name),
		})
		if len(commands) >= input.CommandBudget {
			break
		}
	}
	if len(commands) == 0 {
		return WorkspaceObservationPlan{
			Summary: "Builtin repair engine could not identify a focused observation command.",
		}, nil
	}
	return WorkspaceObservationPlan{
		Summary:  "Builtin repair engine requested focused workspace searches.",
		Commands: commands,
	}, nil
}

func generateWorkspaceEditBuiltin(input WorkspaceEditInput) (WorkspaceEditPlan, error) {
	candidates := builtinCandidateFunctionNames(input.Task.Objective, failureSummary(input.RecentVerification), input.Files)
	for _, name := range candidates {
		template, ok := builtinTemplateForName(name)
		if !ok {
			continue
		}
		index, existingContent, found := builtinSourceFileForFunction(input.Files, name)
		if found {
			if start, end, ok := builtinLocateFunction(existingContent, name); ok && strings.Contains(existingContent[start:end], template.Body) {
				continue
			}
		}

		targetPath := "builtin_repair.go"
		currentContent := ""
		if index >= 0 {
			targetPath = input.Files[index].Path
			currentContent = input.Files[index].Content
		} else if fallback := builtinPreferredSourceFile(input.Files); fallback != nil {
			targetPath = fallback.Path
			currentContent = fallback.Content
		}

		nextContent := builtinRepairFileContent(currentContent, builtinPackageName(input.Files), name, template)
		summary := fmt.Sprintf("Builtin repair engine updated %s for %s.", targetPath, name)
		return WorkspaceEditPlan{
			Summary: summary,
			Writes: []WorkspaceWrite{
				{
					Path:    targetPath,
					Content: nextContent,
				},
			},
		}, nil
	}

	return WorkspaceEditPlan{
		Summary: "Builtin repair engine could not infer a safe workspace change.",
	}, nil
}

func builtinCandidateFunctionNames(objective, failure string, files []WorkspaceFile) []string {
	var names []string
	names = append(names, builtinExtractNames(objective)...)
	names = append(names, builtinExtractNames(failure)...)
	for _, file := range files {
		if !strings.HasSuffix(file.Path, "_test.go") {
			continue
		}
		for _, match := range builtinTestCallPattern.FindAllStringSubmatch(file.Content, -1) {
			if len(match) < 2 {
				continue
			}
			names = append(names, match[1])
		}
	}
	return uniqueNonEmpty(names)
}

func builtinExtractNames(text string) []string {
	var names []string
	for _, match := range builtinUndefinedNamePattern.FindAllStringSubmatch(text, -1) {
		if len(match) >= 2 {
			names = append(names, match[1])
		}
	}
	for _, match := range builtinNameTokenPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		name := match[1]
		if _, ok := builtinTemplateForName(name); ok {
			names = append(names, name)
		}
	}
	return uniqueNonEmpty(names)
}

func builtinTemplateForName(name string) (builtinFunctionTemplate, bool) {
	template, ok := builtinFunctionTemplates[strings.ToLower(strings.TrimSpace(name))]
	return template, ok
}

func builtinNamePresentInFiles(files []WorkspaceFile, name string) bool {
	for _, file := range files {
		if strings.Contains(file.Content, "func "+name+"(") {
			return true
		}
	}
	return false
}

func builtinSourceFileForFunction(files []WorkspaceFile, name string) (int, string, bool) {
	for i, file := range files {
		if !builtinIsSourceFile(file.Path) {
			continue
		}
		if strings.Contains(file.Content, "func "+name+"(") {
			return i, file.Content, true
		}
	}
	return -1, "", false
}

func builtinPreferredSourceFile(files []WorkspaceFile) *WorkspaceFile {
	for i := range files {
		if builtinIsSourceFile(files[i].Path) {
			return &files[i]
		}
	}
	return nil
}

func builtinIsSourceFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

func builtinPackageName(files []WorkspaceFile) string {
	for _, file := range files {
		if match := builtinPackagePattern.FindStringSubmatch(file.Content); len(match) >= 2 {
			return match[1]
		}
	}
	return "main"
}

func builtinRepairFileContent(content, packageName, name string, template builtinFunctionTemplate) string {
	rendered := builtinRenderFunction(name, template)
	if strings.TrimSpace(content) == "" {
		return fmt.Sprintf("package %s\n\n%s\n", packageName, rendered)
	}
	if start, end, ok := builtinLocateFunction(content, name); ok {
		prefix := strings.TrimRight(content[:start], "\n")
		suffix := strings.TrimLeft(content[end:], "\n")
		var out strings.Builder
		if prefix != "" {
			out.WriteString(prefix)
			out.WriteString("\n\n")
		}
		out.WriteString(rendered)
		if suffix != "" {
			out.WriteString("\n\n")
			out.WriteString(suffix)
		}
		if !strings.HasSuffix(out.String(), "\n") {
			out.WriteString("\n")
		}
		return out.String()
	}
	base := strings.TrimRight(content, "\n")
	return base + "\n\n" + rendered + "\n"
}

func builtinRenderFunction(name string, template builtinFunctionTemplate) string {
	return fmt.Sprintf("func %s(%s) %s {\n\t%s\n}", name, template.Params, template.ReturnType, template.Body)
}

func builtinLocateFunction(content, name string) (int, int, bool) {
	loc := regexp.MustCompile(`(?m)^func\s+` + regexp.QuoteMeta(name) + `\s*\(`).FindStringIndex(content)
	if loc == nil {
		return 0, 0, false
	}
	start := loc[0]
	braceOffset := strings.Index(content[start:], "{")
	if braceOffset < 0 {
		return 0, 0, false
	}
	depth := 0
	for i := start + braceOffset; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end := i + 1
				for end < len(content) && content[end] == '\n' {
					end++
				}
				return start, end, true
			}
		}
	}
	return 0, 0, false
}

func failureSummary(report *task.VerificationReport) string {
	if report == nil {
		return ""
	}
	return report.FailureSummary
}
