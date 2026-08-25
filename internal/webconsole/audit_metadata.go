package webconsole

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func auditFileIdentity(info os.FileInfo) (string, bool) {
	if info == nil || info.Sys() == nil {
		return "", false
	}
	value := reflect.ValueOf(info.Sys())
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", false
	}
	dev, devOK := auditReflectInteger(value.FieldByName("Dev"))
	ino, inoOK := auditReflectInteger(value.FieldByName("Ino"))
	if !devOK || !inoOK {
		return "", false
	}
	return fmt.Sprintf("%d:%d", dev, ino), true
}

func auditFileChangeStamp(info os.FileInfo) (string, bool) {
	if info == nil || info.Sys() == nil {
		return "", false
	}
	value := reflect.ValueOf(info.Sys())
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", false
	}
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		for field.Kind() == reflect.Pointer {
			if field.IsNil() {
				return "", false
			}
			field = field.Elem()
		}
		if field.Kind() != reflect.Struct {
			continue
		}
		sec, secOK := auditReflectInteger(field.FieldByName("Sec"))
		nsec, nsecOK := auditReflectInteger(field.FieldByName("Nsec"))
		if secOK && nsecOK {
			return fmt.Sprintf("%d:%d", sec, nsec), true
		}
	}
	sec, secOK := auditReflectInteger(value.FieldByName("Ctime"))
	nsec, nsecOK := auditReflectInteger(value.FieldByName("Ctimensec"))
	if secOK && nsecOK {
		return fmt.Sprintf("%d:%d", sec, nsec), true
	}
	return "", false
}

func auditReflectInteger(value reflect.Value) (int64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		raw := value.Uint()
		if raw > math.MaxInt64 {
			return 0, false
		}
		return int64(raw), true
	default:
		return 0, false
	}
}

func hardenWebAuditRegularFile(path string, file *os.File) (os.FileInfo, error) {
	if file == nil {
		return nil, errors.New("audit managed file is required")
	}
	verify := func() (os.FileInfo, error) {
		fileInfo, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if !fileInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("audit managed path is not a regular file: %s", path)
		}
		pathInfo, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("audit managed path is not the opened regular file: %s", path)
		}
		if !os.SameFile(fileInfo, pathInfo) {
			return nil, fmt.Errorf("audit managed path was replaced while open: %s", path)
		}
		return fileInfo, nil
	}
	info, err := verify()
	if err != nil {
		return nil, err
	}
	if webAuditModeNeedsHardening(info.Mode()) {
		if err := file.Chmod(0o600); err != nil {
			return nil, fmt.Errorf("harden audit managed file permissions for %s: %w", path, err)
		}
		info, err = verify()
		if err != nil {
			return nil, err
		}
		if webAuditModeNeedsHardening(info.Mode()) {
			return nil, fmt.Errorf("audit managed file permissions remain unsafe for %s: %s", path, info.Mode())
		}
	}
	return info, nil
}

func webAuditModeNeedsHardening(mode os.FileMode) bool {
	return mode.Perm() != 0o600 || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0
}

func ensureWebAuditFileStillAtPath(path string, file *os.File) error {
	_, err := hardenWebAuditRegularFile(path, file)
	return err
}

func webAuditFileInfoStable(before, after os.FileInfo) bool {
	if before == nil || after == nil || !before.Mode().IsRegular() || !after.Mode().IsRegular() {
		return false
	}
	if !os.SameFile(before, after) ||
		before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) ||
		before.Mode() != after.Mode() {
		return false
	}
	beforeIdentity, beforeIdentityOK := auditFileIdentity(before)
	afterIdentity, afterIdentityOK := auditFileIdentity(after)
	if beforeIdentityOK != afterIdentityOK || (beforeIdentityOK && beforeIdentity != afterIdentity) {
		return false
	}
	beforeStamp, beforeStampOK := auditFileChangeStamp(before)
	afterStamp, afterStampOK := auditFileChangeStamp(after)
	if beforeStampOK != afterStampOK || (beforeStampOK && beforeStamp != afterStamp) {
		return false
	}
	return true
}

// auditOptionalMetadataMatches treats missing host metadata as a real capability
// state. A checkpoint written on a filesystem without an identity/ctime field
// can use the fast path only while the current filesystem exposes the same
// absence. Capability gain or loss forces a full scan and checkpoint refresh.
func auditOptionalMetadataMatches(stored, actual string, actualOK bool) bool {
	if stored == "" {
		return !actualOK
	}
	return actualOK && stored == actual
}

func rejectAuditSymlinkAncestors(path string) error {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	current := volume
	if strings.HasPrefix(rest, separator) {
		current += separator
		rest = strings.TrimPrefix(rest, separator)
	}
	if current == "" {
		current = "."
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" {
			continue
		}
		if current == separator || strings.HasSuffix(current, separator) {
			current += part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to append through symlinked audit path: %s", current)
		}
	}
	return nil
}

func webAuditCheckpointPath(logPath string) string { return logPath + webAuditCheckpointSuffix }
func webAuditLockPath(logPath string) string       { return logPath + webAuditLockSuffix }
func webAuditManagedPaths(logPath string) []string {
	return []string{logPath, webAuditCheckpointPath(logPath), webAuditLockPath(logPath)}
}

func webAuditLogPath(sessionRoot string) string {
	if sessionRoot == "" {
		return webAuditLogName
	}
	return filepath.Join(filepath.Dir(sessionRoot), webAuditLogName)
}
