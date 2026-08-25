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

func ensureWebAuditFileStillAtPath(path string, file *os.File) error {
	if file == nil {
		return errors.New("audit log file is required")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("audit log path is no longer the opened regular file: %s", path)
	}
	if !os.SameFile(fileInfo, pathInfo) {
		return fmt.Errorf("audit log path was replaced while open: %s", path)
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
