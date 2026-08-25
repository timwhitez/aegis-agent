package webconsole

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
)

const (
	webAuditLogName                       = "webconsole-audit.jsonl"
	webAuditCheckpointSuffix              = ".checkpoint.json"
	webAuditLockSuffix                    = ".lock"
	webAuditCheckpointSchemaVersion       = 1
	webAuditStructuredIDPrefix            = "audit_v2_"
	maxWebAuditLogBytes             int64 = 64 << 20
	maxWebAuditRecordBytes                = 4 << 20
	maxWebAuditCheckpointBytes            = 64 << 10
	webAuditProbeBytes              int64 = 64 << 10
)

type webAuditEvent struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Time          string         `json:"time"`
	Data          map[string]any `json:"data,omitempty"`
}

type pendingWebAuditEvent struct {
	eventType string
	data      map[string]any
}

type webAuditCheckpoint struct {
	SchemaVersion   int    `json:"schema_version"`
	Epoch           string `json:"epoch"`
	FileIdentity    string `json:"file_identity,omitempty"`
	Size            int64  `json:"size"`
	RecordCount     uint64 `json:"record_count"`
	ChainSHA256     string `json:"chain_sha256"`
	HeadSHA256      string `json:"head_sha256"`
	TailOffset      int64  `json:"tail_offset"`
	TailSHA256      string `json:"tail_sha256"`
	ModTimeUnixNano int64  `json:"mod_time_unix_nano"`
	ChangeStamp     string `json:"change_stamp,omitempty"`
}

type webAuditScanResult struct {
	checkpoint webAuditCheckpoint
	seenIDs    map[string]struct{}
}

var beforeOpenAuditLog func(path string) error
var beforeWriteAuditCheckpoint func(path string, data []byte) error
var auditRecordDecodeObserver func()

// PrepareAuditLog establishes or recovers the audit checkpoint without
// constructing queue workers or the stale-session reaper. The CLI calls this
// before New so no background durable mutation can start ahead of audit
// validation.
func PrepareAuditLog(cfg *config.Config, configPath string) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	serviceCfg, err := config.Clone(cfg)
	if err != nil {
		return err
	}
	store := runtime.NewStoreView(serviceCfg).Store()
	service := &Service{
		cfg:        serviceCfg,
		configPath: configPath,
		store:      store,
	}
	return service.InitializeAuditLog()
}

func (s *Service) appendAuditEvent(eventType string, data map[string]any) error {
	return s.appendAuditEvents(pendingWebAuditEvent{eventType: eventType, data: data})
}

func (s *Service) appendAuditEvents(pending ...pendingWebAuditEvent) error {
	if s == nil || s.store == nil || len(pending) == 0 {
		return nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()

	now := time.Now().UTC()
	events := make([]webAuditEvent, 0, len(pending))
	for _, item := range pending {
		if s.beforeAppendAuditEvent != nil {
			if err := s.beforeAppendAuditEvent(item.eventType, item.data); err != nil {
				return err
			}
		}
		events = append(events, webAuditEvent{
			SchemaVersion: 1,
			Type:          item.eventType,
			Time:          now.Format(time.RFC3339Nano),
			Data:          item.data,
		})
	}
	path := webAuditLogPath(s.store.Root())
	if err := s.ensureWebAuditManagedPathsDistinct(path); err != nil {
		return err
	}
	return appendWebAuditEventsAtPath(path, events)
}

func appendWebAuditEventsAtPath(path string, events []webAuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	return withWebAuditFileLock(path, func() error {
		file, err := openAuditLogNoSymlink(path)
		if err != nil {
			return err
		}
		defer file.Close()

		state, err := loadWebAuditState(path, file, false)
		if err != nil {
			return err
		}
		encoded, nextChain, err := encodeWebAuditBatch(events, state)
		if err != nil {
			return err
		}
		if state.Size > maxWebAuditLogBytes-int64(len(encoded)) {
			return fmt.Errorf("web audit log reached the %d-byte retention limit; stop every web console process and archive %s together with %s before retrying", maxWebAuditLogBytes, path, webAuditCheckpointPath(path))
		}
		offset := state.Size
		actualOffset, err := file.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		if actualOffset != offset {
			return fmt.Errorf("audit log changed before append: size %d became %d", offset, actualOffset)
		}
		if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
			return err
		}
		written, writeErr := io.WriteString(file, encoded)
		if writeErr == nil && written != len(encoded) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			rollbackErr := file.Truncate(offset)
			if rollbackErr == nil {
				rollbackErr = file.Sync()
			}
			if rollbackErr != nil {
				return errors.Join(writeErr, fmt.Errorf("restore and sync audit log after failed batch append: %w", rollbackErr))
			}
			return writeErr
		}
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync appended audit records: %w", err)
		}

		// A complete write followed by a successful file sync is the audit
		// commit point. From here onward the business mutation must not be told
		// to roll back merely because acceleration metadata could not be
		// refreshed: that would leave a durable audit event describing an action
		// that the caller subsequently undid. Any post-commit verification or
		// checkpoint failure therefore leaves the previous checkpoint as the
		// recovery anchor and degrades the next append/startup to a full
		// validation of the structural tail.
		info, err := file.Stat()
		if err != nil {
			return nil
		}
		expectedSize := offset + int64(len(encoded))
		if info.Size() != expectedSize {
			return nil
		}
		if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
			return nil
		}
		headDigest, tailOffset, tailDigest, err := auditProbeDigests(file, info.Size())
		if err != nil {
			return nil
		}
		afterProbeInfo, err := file.Stat()
		if err != nil || !webAuditFileInfoStable(info, afterProbeInfo) {
			return nil
		}
		if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
			return nil
		}

		next := state
		next.FileIdentity, _ = auditFileIdentity(afterProbeInfo)
		next.Size = afterProbeInfo.Size()
		next.RecordCount += uint64(len(events))
		next.ChainSHA256 = hex.EncodeToString(nextChain[:])
		next.HeadSHA256 = hex.EncodeToString(headDigest[:])
		next.TailOffset = tailOffset
		next.TailSHA256 = hex.EncodeToString(tailDigest[:])
		next.ModTimeUnixNano = afterProbeInfo.ModTime().UnixNano()
		next.ChangeStamp, _ = auditFileChangeStamp(afterProbeInfo)
		_ = writeWebAuditCheckpoint(path, next)
		return nil
	})
}

// InitializeAuditLog performs startup/recovery full validation before any
// background Web service component or listener is allowed to mutate state.
func (s *Service) InitializeAuditLog() error {
	if s == nil || s.store == nil {
		return nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	path := webAuditLogPath(s.store.Root())
	if err := s.ensureWebAuditManagedPathsDistinct(path); err != nil {
		return err
	}
	return initializeWebAuditLogAtPath(path)
}

func initializeWebAuditLogAtPath(path string) error {
	return ensureWebAuditLogAtPath(path, true)
}

func (s *Service) ensureAuditLogWritable() error {
	if s == nil || s.store == nil {
		return nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	path := webAuditLogPath(s.store.Root())
	if err := s.ensureWebAuditManagedPathsDistinct(path); err != nil {
		return err
	}
	return ensureWebAuditLogAtPath(path, false)
}

func (s *Service) ensureWebAuditManagedPathsDistinct(logPath string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	configPath := strings.TrimSpace(s.configPath)
	if configPath == "" {
		configPath = config.PersistPath("", cwd)
	}
	envPath := config.DefaultEnvFilePath(cwd)
	for _, managedPath := range webAuditManagedPaths(logPath) {
		if same, err := sameWebPath(configPath, managedPath); err != nil {
			return err
		} else if same {
			return newWebSettingsValidationError("config file and audit log must be separate (including checkpoint and lock sidecars): %s", configPath)
		}
		if same, err := sameWebPath(envPath, managedPath); err != nil {
			return err
		} else if same {
			return newWebSettingsValidationError("API key env file and audit log must be separate (including checkpoint and lock sidecars): %s", envPath)
		}
	}
	return nil
}
