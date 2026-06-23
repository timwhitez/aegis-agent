package filechanges

import "strings"

// ShellRedirectTarget is a file written by a shell output redirect.
type ShellRedirectTarget struct {
	Path   string
	Append bool
}

// CollectShellRedirectTargets extracts files written by output redirects in a
// shell command, ignoring here-doc bodies and fd-only targets like /dev/null.
func CollectShellRedirectTargets(command string) []ShellRedirectTarget {
	tokens := tokenizeShellCommand(stripShellHereDocBodies(command))
	var out []ShellRedirectTarget
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if !isShellOutputRedirect(token) {
			continue
		}
		if i+1 >= len(tokens) {
			break
		}
		target := cleanShellRedirectTarget(tokens[i+1])
		if target == "" {
			i++
			continue
		}
		out = append(out, ShellRedirectTarget{
			Path:   target,
			Append: strings.Contains(token, ">>"),
		})
		i++
	}
	return out
}

type shellHereDocDelimiter struct {
	value     string
	stripTabs bool
}

func stripShellHereDocBodies(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	lines := strings.SplitAfter(command, "\n")
	var out strings.Builder
	var pending []shellHereDocDelimiter
	for _, line := range lines {
		lineText := strings.TrimRight(line, "\r\n")
		if len(pending) > 0 {
			target := lineText
			if pending[0].stripTabs {
				target = strings.TrimLeft(target, "\t")
			}
			if target == pending[0].value {
				pending = pending[1:]
			}
			continue
		}
		out.WriteString(line)
		pending = append(pending, collectShellHereDocDelimiters(line)...)
	}
	return out.String()
}

func collectShellHereDocDelimiters(line string) []shellHereDocDelimiter {
	var out []shellHereDocDelimiter
	quote := byte(0)
	escaping := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaping {
			escaping = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaping = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch != '<' || i+1 >= len(line) || line[i+1] != '<' {
			continue
		}
		if i+2 < len(line) && line[i+2] == '<' {
			i += 2
			continue
		}
		cursor := i + 2
		stripTabs := false
		if cursor < len(line) && line[cursor] == '-' {
			stripTabs = true
			cursor++
		}
		for cursor < len(line) && (line[cursor] == ' ' || line[cursor] == '\t') {
			cursor++
		}
		value, end := readShellHereDocDelimiter(line, cursor)
		if value != "" {
			out = append(out, shellHereDocDelimiter{value: value, stripTabs: stripTabs})
		}
		if end > i {
			i = end - 1
		}
	}
	return out
}

func readShellHereDocDelimiter(line string, start int) (string, int) {
	var out strings.Builder
	quote := byte(0)
	escaping := false
	for i := start; i < len(line); i++ {
		ch := line[i]
		if escaping {
			out.WriteByte(ch)
			escaping = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaping = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				out.WriteByte(ch)
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == ';' || ch == '|' || ch == '&' || ch == '<' || ch == '>' {
			return out.String(), i
		}
		out.WriteByte(ch)
	}
	return out.String(), len(line)
}

func tokenizeShellCommand(command string) []string {
	var tokens []string
	var current strings.Builder
	quote := byte(0)
	escaping := false
	expectRedirectTarget := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
		expectRedirectTarget = false
	}
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaping {
			current.WriteByte(ch)
			escaping = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaping = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				current.WriteByte(ch)
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			flush()
			continue
		}
		if redirect, end := readShellOutputRedirect(command, i); redirect != "" {
			flush()
			tokens = append(tokens, redirect)
			expectRedirectTarget = true
			i = end
			continue
		}
		if ch == '&' && expectRedirectTarget && current.Len() == 0 {
			current.WriteByte(ch)
			continue
		}
		if ch == ';' || ch == '|' || ch == '&' {
			flush()
			expectRedirectTarget = false
			continue
		}
		current.WriteByte(ch)
	}
	flush()
	return tokens
}

func readShellOutputRedirect(source string, index int) (string, int) {
	if index < 0 || index >= len(source) {
		return "", index
	}
	first := source[index]
	prefix := ""
	cursor := index
	if (first >= '0' && first <= '9') || first == '&' {
		if index+1 >= len(source) || source[index+1] != '>' {
			return "", index
		}
		prefix = string(first)
		cursor++
	} else if first != '>' {
		return "", index
	}
	if cursor >= len(source) || source[cursor] != '>' {
		return "", index
	}
	if cursor+1 < len(source) && source[cursor+1] == '>' {
		return prefix + ">>", cursor + 1
	}
	if cursor+1 < len(source) && source[cursor+1] == '|' {
		return prefix + ">|", cursor + 1
	}
	return prefix + ">", cursor
}

func isShellOutputRedirect(token string) bool {
	if token == ">" || token == ">>" || token == ">|" {
		return true
	}
	if len(token) < 2 {
		return false
	}
	prefix := token[:len(token)-1]
	if strings.HasSuffix(token, ">>") {
		prefix = token[:len(token)-2]
	}
	if strings.HasSuffix(token, ">|") {
		prefix = token[:len(token)-2]
	}
	if prefix == "&" {
		return true
	}
	if prefix == "" {
		return false
	}
	for _, ch := range prefix {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return strings.HasSuffix(token, ">") || strings.HasSuffix(token, ">>") || strings.HasSuffix(token, ">|")
}

func cleanShellRedirectTarget(target string) string {
	value := strings.TrimSpace(target)
	if value == "" || value == "-" || strings.HasPrefix(value, "&") || strings.HasPrefix(value, "(") {
		return ""
	}
	if value == "/dev/null" || strings.HasPrefix(value, "/dev/fd/") {
		return ""
	}
	return value
}
