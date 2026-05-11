package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go-cli-agent/internal/fileutil"
)

func DefaultEnvFilePath(cwd string) string {
	if envPath := strings.TrimSpace(os.Getenv("GO_CLI_AGENT_ENV_FILE")); envPath != "" {
		return resolveMaybeRelative(cwd, envPath)
	}
	return filepath.Join(cwd, ".env")
}

func LoadEnvFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		key, value, ok := parseEnvAssignment(scanner.Text())
		if !ok {
			continue
		}
		if current := strings.TrimSpace(os.Getenv(key)); current != "" {
			continue
		}
		if !allowedEnvFileKey(key) {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func UpsertEnvFile(path, key, value string) error {
	path = strings.TrimSpace(path)
	key = strings.TrimSpace(key)
	if path == "" {
		return fmt.Errorf("env file path is required")
	}
	if key == "" {
		return fmt.Errorf("env key is required")
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to update symlinked env file: %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("env file is not a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		lines = strings.Split(content, "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	replacement := fmt.Sprintf("%s=%s", key, formatEnvValue(value))
	found := false
	for i, line := range lines {
		lineKey, _, ok := parseEnvAssignment(line)
		if !ok || lineKey != key {
			continue
		}
		lines[i] = replacement
		found = true
	}
	if !found {
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, replacement)
	}

	content := strings.Join(lines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return fileutil.AtomicWriteFileNoSymlink(path, []byte(content), 0o600)
}

func allowedEnvFileKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	if strings.HasPrefix(key, "GO_CLI_AGENT_") {
		return false
	}
	switch key {
	case "PATH", "HOME", "SHELL", "BASH_ENV", "ENV", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES":
		return false
	}
	return strings.HasSuffix(key, "_API_KEY") || strings.HasSuffix(key, "_ACCESS_TOKEN")
}

func parseEnvAssignment(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	parts := strings.SplitN(trimmed, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return "", "", false
	}
	value := strings.TrimSpace(parts[1])
	if len(value) >= 2 {
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return key, value[1 : len(value)-1], true
		}
		if value[0] == '"' && value[len(value)-1] == '"' {
			unquoted, err := strconv.Unquote(value)
			if err == nil {
				return key, unquoted, true
			}
		}
	}
	return key, value, true
}

func formatEnvValue(value string) string {
	if value == "" {
		return ""
	}
	if !strings.ContainsAny(value, " \t\r\n#\"'\\$`") {
		return value
	}
	if !strings.ContainsRune(value, '\'') {
		return "'" + value + "'"
	}
	return strconv.Quote(value)
}
