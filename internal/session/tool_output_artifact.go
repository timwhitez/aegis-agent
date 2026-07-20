package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go-cli-agent/internal/fileutil"
)

const (
	ToolOutputArtifactReasonFileBytes       = "artifact_file_max_bytes"
	ToolOutputArtifactReasonSessionBytes    = "artifact_session_max_bytes"
	ToolOutputArtifactReasonSessionFiles    = "artifact_max_files"
	toolOutputArtifactReasonExistingPartial = "existing_partial_artifact"
	toolOutputArtifactLockName              = ".quota.lock"
)

type ToolOutputArtifactQuota struct {
	FileMaxBytes    int
	SessionMaxBytes int
	MaxFiles        int
}

type ToolOutputArtifactResult struct {
	Filename       string
	AbsolutePath   string
	RawBytes       int
	PersistedBytes int
	OmittedBytes   int
	Complete       bool
	Truncated      bool
	Recoverable    bool
	Reason         string
}

var beforeToolOutputArtifactWrite func(path string, payload []byte) error

func (s *Store) WriteToolOutputArtifact(sessionID, artifactRoot, artifactKey string, payload []byte, quota ToolOutputArtifactQuota) (ToolOutputArtifactResult, error) {
	result := ToolOutputArtifactResult{RawBytes: len(payload), OmittedBytes: len(payload)}
	if err := validateStoreID("session", sessionID); err != nil {
		return result, err
	}
	if quota.FileMaxBytes <= 0 || quota.SessionMaxBytes <= 0 || quota.MaxFiles <= 0 {
		return result, fmt.Errorf("tool output artifact quota values must be positive: %#v", quota)
	}
	artifactRoot = strings.TrimSpace(artifactRoot)
	if artifactRoot == "" {
		artifactRoot = filepath.Join(s.SessionDir(sessionID), "artifacts", "tool-outputs")
	}
	artifactRoot = filepath.Clean(artifactRoot)
	filename := toolOutputArtifactFilename(artifactKey, payload)
	targetPath := filepath.Join(artifactRoot, filename)
	lockPath := filepath.Join(artifactRoot, toolOutputArtifactLockName)

	err := s.withPrivateFileLock(lockPath, func() error {
		if err := rejectSymlinkPathAncestors(artifactRoot); err != nil {
			return err
		}
		if existing, found, err := readExistingToolOutputArtifact(targetPath); err != nil {
			return err
		} else if found {
			if err := fileutil.ChmodPathNoSymlink(targetPath, 0o600); err != nil {
				return err
			}
			if len(existing) > len(payload) || !bytes.Equal(existing, payload[:len(existing)]) {
				return fmt.Errorf("tool output artifact collision at %s", targetPath)
			}
			result.Filename = filename
			result.AbsolutePath = targetPath
			result.PersistedBytes = len(existing)
			result.OmittedBytes = len(payload) - len(existing)
			result.Complete = len(existing) == len(payload)
			result.Truncated = !result.Complete
			result.Recoverable = result.Complete
			if !result.Complete {
				result.Reason = toolOutputArtifactReasonExistingPartial
			}
			return nil
		}

		usageBytes, usageFiles, err := scanToolOutputArtifactUsage(artifactRoot)
		if err != nil {
			return err
		}
		if usageFiles >= quota.MaxFiles {
			result.Reason = ToolOutputArtifactReasonSessionFiles
			return nil
		}
		remainingSessionBytes := quota.SessionMaxBytes - usageBytes
		if remainingSessionBytes <= 0 {
			result.Reason = ToolOutputArtifactReasonSessionBytes
			return nil
		}

		persistedBytes := len(payload)
		result.Reason = ""
		if persistedBytes > quota.FileMaxBytes {
			persistedBytes = quota.FileMaxBytes
			result.Reason = ToolOutputArtifactReasonFileBytes
		}
		if persistedBytes > remainingSessionBytes {
			persistedBytes = remainingSessionBytes
			result.Reason = ToolOutputArtifactReasonSessionBytes
		}
		if persistedBytes <= 0 {
			if result.Reason == "" {
				result.Reason = ToolOutputArtifactReasonSessionBytes
			}
			return nil
		}

		data := payload[:persistedBytes]
		if beforeToolOutputArtifactWrite != nil {
			if err := beforeToolOutputArtifactWrite(targetPath, data); err != nil {
				return err
			}
		}
		if err := fileutil.AtomicWriteFileNoSymlink(targetPath, data, 0o600); err != nil {
			return err
		}
		result.Filename = filename
		result.AbsolutePath = targetPath
		result.PersistedBytes = persistedBytes
		result.OmittedBytes = len(payload) - persistedBytes
		result.Complete = persistedBytes == len(payload)
		result.Truncated = !result.Complete
		result.Recoverable = result.Complete
		return nil
	})
	return result, err
}

func readExistingToolOutputArtifact(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("refusing to use symlinked tool output artifact: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("tool output artifact is not a regular file: %s", path)
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func scanToolOutputArtifactUsage(root string) (int, int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	totalBytes := int64(0)
	files := 0
	for _, entry := range entries {
		if entry.Name() == toolOutputArtifactLockName {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, 0, fmt.Errorf("refusing symlink in tool output artifact root: %s", filepath.Join(root, entry.Name()))
		}
		if !info.Mode().IsRegular() {
			return 0, 0, fmt.Errorf("unexpected non-regular entry in tool output artifact root: %s", filepath.Join(root, entry.Name()))
		}
		if info.Size() < 0 || totalBytes > int64(^uint(0)>>1)-info.Size() {
			return 0, 0, fmt.Errorf("tool output artifact usage overflow in %s", root)
		}
		totalBytes += info.Size()
		files++
	}
	if totalBytes > int64(^uint(0)>>1) {
		return 0, 0, fmt.Errorf("tool output artifact usage exceeds platform int range in %s", root)
	}
	return int(totalBytes), files, nil
}

func toolOutputArtifactFilename(key string, payload []byte) string {
	component := safeToolOutputArtifactComponent(key)
	digest := sha256.Sum256(payload)
	return component + "-" + hex.EncodeToString(digest[:6]) + ".txt"
}

func safeToolOutputArtifactComponent(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
		if builder.Len() >= 48 {
			break
		}
	}
	component := strings.Trim(builder.String(), "-")
	if component == "" || component == "." || component == ".." {
		return "output"
	}
	return component
}
