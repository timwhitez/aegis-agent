package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aegis-agent/internal/fileutil"
	"golang.org/x/sys/unix"
)

const (
	toolOutputArtifactReservationPrefix = ".tool-output-reservation-"
	toolOutputArtifactInflightPrefix    = ".tool-output-inflight-"
	toolOutputArtifactReservationSchema = 1
	toolOutputArtifactReservationMax    = 64 << 10
)

type toolOutputArtifactReservation struct {
	SchemaVersion   int    `json:"schema_version"`
	TempName        string `json:"temp_name"`
	ArtifactKey     string `json:"artifact_key,omitempty"`
	ReservedBytes   int    `json:"reserved_bytes"`
	WrittenBytes    int    `json:"written_bytes"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	WorkerPID       int    `json:"worker_pid"`
	ProcessIdentity string `json:"process_identity,omitempty"`
}

// ToolOutputArtifactStream accepts an unbounded source stream while persisting
// only a quota-reserved prefix. Write intentionally consumes every input byte
// even after a storage failure or quota stop so stdout/stderr draining cannot
// change command execution semantics. Storage errors are retained and reported
// by Close together with exact raw/persisted/omitted accounting.
type ToolOutputArtifactStream struct {
	store           *Store
	artifactRoot    string
	artifactKey     string
	quota           ToolOutputArtifactQuota
	reservationPath string
	tempPath        string
	streamID        string
	reservationFile *os.File
	outputFile      *os.File
	digest          hash.Hash

	mu             sync.Mutex
	reservation    toolOutputArtifactReservation
	rawBytes       int
	persistedBytes int
	stopReason     string
	firstErr       error
	contentFailed  bool
	publishBlocked bool
	outputClosed   bool
	closed         bool
	finalResult    ToolOutputArtifactResult
	finalErr       error
}

// beforeToolOutputArtifactStreamOperation is test-only failure injection. The
// supported operation names are create, reserve, write, sync, close, rename,
// and release.
var beforeToolOutputArtifactStreamOperation func(operation, path string) error

// BeginToolOutputArtifactStream reserves one artifact slot and returns a
// writer that persists only the prefix allowed by the shared session quota.
func (s *Store) BeginToolOutputArtifactStream(sessionID, artifactRoot, artifactKey string, quota ToolOutputArtifactQuota) (*ToolOutputArtifactStream, ToolOutputArtifactResult, error) {
	result := ToolOutputArtifactResult{}
	if err := validateStoreID("session", sessionID); err != nil {
		result.Reason = ToolOutputArtifactReasonCreateFailed
		return nil, result, err
	}
	if quota.FileMaxBytes <= 0 || quota.SessionMaxBytes <= 0 || quota.MaxFiles <= 0 {
		result.Reason = ToolOutputArtifactReasonCreateFailed
		return nil, result, fmt.Errorf("tool output artifact quota values must be positive: %#v", quota)
	}
	resolvedRoot, err := s.resolveToolOutputArtifactRoot(sessionID, artifactRoot)
	if err != nil {
		result.Reason = ToolOutputArtifactReasonCreateFailed
		return nil, result, err
	}
	artifactRoot = resolvedRoot
	lockPath := filepath.Join(artifactRoot, toolOutputArtifactLockName)
	var stream *ToolOutputArtifactStream
	err = s.withPrivateFileLock(lockPath, func() error {
		if err := rejectSymlinkPathAncestors(artifactRoot); err != nil {
			return err
		}
		usageBytes, usageFiles, err := scanToolOutputArtifactUsage(artifactRoot)
		if err != nil {
			return err
		}
		if usageFiles >= quota.MaxFiles {
			result.Reason = ToolOutputArtifactReasonSessionFiles
			return nil
		}
		if usageBytes >= quota.SessionMaxBytes {
			result.Reason = ToolOutputArtifactReasonSessionBytes
			return nil
		}

		streamID, err := newToolOutputArtifactStreamID()
		if err != nil {
			return err
		}
		reservationPath := filepath.Join(artifactRoot, toolOutputArtifactReservationPrefix+streamID+".json")
		tempPath := filepath.Join(artifactRoot, toolOutputArtifactInflightPrefix+streamID+".tmp")
		if beforeToolOutputArtifactStreamOperation != nil {
			if err := beforeToolOutputArtifactStreamOperation("create", tempPath); err != nil {
				return err
			}
		}
		reservationFile, err := fileutil.OpenFileNoSymlink(reservationPath, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		cleanupReservation := true
		defer func() {
			if cleanupReservation {
				_ = unix.Flock(int(reservationFile.Fd()), unix.LOCK_UN)
				_ = reservationFile.Close()
				_ = removeToolOutputArtifactInternal(reservationPath)
			}
		}()
		if err := reservationFile.Chmod(0o600); err != nil {
			return err
		}
		if err := unix.Flock(int(reservationFile.Fd()), unix.LOCK_SH); err != nil {
			return err
		}
		outputFile, err := fileutil.OpenFileNoSymlink(tempPath, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		cleanupOutput := true
		defer func() {
			if cleanupOutput {
				_ = outputFile.Close()
				_ = removeToolOutputArtifactInternal(tempPath)
			}
		}()
		if err := outputFile.Chmod(0o600); err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		reservation := toolOutputArtifactReservation{
			SchemaVersion: toolOutputArtifactReservationSchema,
			TempName:      filepath.Base(tempPath),
			ArtifactKey:   artifactKey,
			CreatedAt:     now,
			UpdatedAt:     now,
			WorkerPID:     os.Getpid(),
		}
		if identity, ok := hostProcessIdentity(os.Getpid()); ok {
			reservation.ProcessIdentity = identity
		}
		if err := writeToolOutputArtifactReservation(reservationFile, reservation); err != nil {
			return err
		}
		stream = &ToolOutputArtifactStream{
			store:           s,
			artifactRoot:    artifactRoot,
			artifactKey:     artifactKey,
			quota:           quota,
			reservationPath: reservationPath,
			tempPath:        tempPath,
			streamID:        streamID,
			reservationFile: reservationFile,
			outputFile:      outputFile,
			digest:          sha256.New(),
			reservation:     reservation,
		}
		cleanupReservation = false
		cleanupOutput = false
		return nil
	})
	if err != nil {
		if result.Reason == "" {
			result.Reason = ToolOutputArtifactReasonCreateFailed
		}
		return nil, result, err
	}
	return stream, result, nil
}

func (w *ToolOutputArtifactStream) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("tool output artifact stream is closed")
	}
	if len(payload) == 0 {
		return 0, nil
	}
	w.rawBytes += len(payload)
	_, _ = w.digest.Write(payload)
	if w.firstErr != nil || w.stopReason == ToolOutputArtifactReasonFileBytes || w.stopReason == ToolOutputArtifactReasonSessionBytes {
		return len(payload), nil
	}

	fileRemaining := w.quota.FileMaxBytes - w.persistedBytes
	if fileRemaining <= 0 {
		w.stopReason = ToolOutputArtifactReasonFileBytes
		return len(payload), nil
	}
	want := len(payload)
	if want > fileRemaining {
		want = fileRemaining
	}
	granted, reason, err := w.reserve(w.persistedBytes + want)
	if err != nil {
		w.recordFailure(ToolOutputArtifactReasonReservationFail, err)
		return len(payload), nil
	}
	if reason != "" {
		w.stopReason = reason
	}
	if granted <= w.persistedBytes {
		return len(payload), nil
	}
	writable := granted - w.persistedBytes
	if writable > want {
		writable = want
	}
	data := payload[:writable]
	if beforeToolOutputArtifactStreamOperation != nil {
		if err := beforeToolOutputArtifactStreamOperation("write", w.tempPath); err != nil {
			w.recordFailure(ToolOutputArtifactReasonWriteFailed, err)
			return len(payload), nil
		}
	}
	if beforeToolOutputArtifactWrite != nil {
		if err := beforeToolOutputArtifactWrite(w.tempPath, data); err != nil {
			w.recordFailure(ToolOutputArtifactReasonWriteFailed, err)
			return len(payload), nil
		}
	}
	written, writeErr := w.outputFile.Write(data)
	if written > 0 {
		w.persistedBytes += written
		w.reservation.WrittenBytes = w.persistedBytes
	}
	if writeErr != nil {
		w.recordFailure(ToolOutputArtifactReasonWriteFailed, writeErr)
	} else if written != len(data) {
		w.recordFailure(ToolOutputArtifactReasonWriteFailed, io.ErrShortWrite)
	}
	if writable < len(payload) && w.stopReason == "" {
		if w.persistedBytes >= w.quota.FileMaxBytes {
			w.stopReason = ToolOutputArtifactReasonFileBytes
		} else {
			w.stopReason = ToolOutputArtifactReasonSessionBytes
		}
	}
	return len(payload), nil
}

// reserve grows this stream's durable byte reservation under the session-wide
// quota lock. It returns the total bytes currently granted to this stream.
func (w *ToolOutputArtifactStream) reserve(target int) (int, string, error) {
	if target <= w.reservation.ReservedBytes {
		return w.reservation.ReservedBytes, "", nil
	}
	lockPath := filepath.Join(w.artifactRoot, toolOutputArtifactLockName)
	reason := ""
	err := w.store.withPrivateFileLock(lockPath, func() error {
		usageBytes, _, err := scanToolOutputArtifactUsage(w.artifactRoot)
		if err != nil {
			return err
		}
		fileTarget := target
		if fileTarget > w.quota.FileMaxBytes {
			fileTarget = w.quota.FileMaxBytes
			reason = ToolOutputArtifactReasonFileBytes
		}
		additionalWanted := fileTarget - w.reservation.ReservedBytes
		if additionalWanted <= 0 {
			return nil
		}
		availableSession := w.quota.SessionMaxBytes - usageBytes
		if availableSession <= 0 {
			reason = ToolOutputArtifactReasonSessionBytes
			return nil
		}
		if additionalWanted > availableSession {
			additionalWanted = availableSession
			reason = ToolOutputArtifactReasonSessionBytes
		}
		next := w.reservation
		next.ReservedBytes += additionalWanted
		next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if beforeToolOutputArtifactStreamOperation != nil {
			if err := beforeToolOutputArtifactStreamOperation("reserve", w.reservationPath); err != nil {
				return err
			}
		}
		if err := writeToolOutputArtifactReservation(w.reservationFile, next); err != nil {
			return err
		}
		w.reservation = next
		return nil
	})
	return w.reservation.ReservedBytes, reason, err
}

func (w *ToolOutputArtifactStream) Close() (ToolOutputArtifactResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.finalResult, w.finalErr
	}
	w.closed = true
	result := ToolOutputArtifactResult{RawBytes: w.rawBytes, OmittedBytes: w.rawBytes, Reason: w.stopReason}

	if beforeToolOutputArtifactStreamOperation != nil {
		if err := beforeToolOutputArtifactStreamOperation("sync", w.tempPath); err != nil {
			w.recordFailure(ToolOutputArtifactReasonSyncFailed, err)
		}
	}
	if !w.publishBlocked {
		if err := w.outputFile.Sync(); err != nil {
			w.recordFailure(ToolOutputArtifactReasonSyncFailed, err)
		}
	}
	if beforeToolOutputArtifactStreamOperation != nil {
		if err := beforeToolOutputArtifactStreamOperation("close", w.tempPath); err != nil {
			w.recordFailure(ToolOutputArtifactReasonCloseFailed, err)
		}
	}
	closeErr := w.closeOutput()
	if closeErr != nil {
		w.recordFailure(ToolOutputArtifactReasonCloseFailed, closeErr)
	}

	canPublish := w.persistedBytes > 0 && !w.publishBlocked
	if canPublish {
		published, publishErr := w.publish()
		if published == "" {
			w.recordFailure(ToolOutputArtifactReasonRenameFailed, publishErr)
			canPublish = false
		} else {
			result.Filename = filepath.Base(published)
			result.AbsolutePath = published
			result.PersistedBytes = w.persistedBytes
			result.OmittedBytes = w.rawBytes - w.persistedBytes
			if publishErr != nil {
				w.recordPublishedFailure(ToolOutputArtifactReasonReservationFail, publishErr)
			}
		}
	}
	if !canPublish {
		if err := w.cleanupInternals(); err != nil {
			w.recordFailure(ToolOutputArtifactReasonReservationFail, err)
		}
		result.Filename = ""
		result.AbsolutePath = ""
		result.PersistedBytes = 0
		result.OmittedBytes = w.rawBytes
	}
	if w.stopReason != "" {
		result.Reason = w.stopReason
	}
	result.Complete = result.AbsolutePath != "" && result.PersistedBytes == result.RawBytes && !w.contentFailed
	result.Truncated = result.AbsolutePath != "" && !result.Complete
	result.Recoverable = result.Complete
	if result.Complete && w.firstErr == nil {
		result.Reason = ""
	}
	w.finalResult = result
	w.finalErr = w.firstErr
	return result, w.firstErr
}

func (w *ToolOutputArtifactStream) publish() (string, error) {
	digest := hex.EncodeToString(w.digest.Sum(nil))
	filename := safeToolOutputArtifactComponent(w.artifactKey) + "-" + digest[:24] + "-" + w.streamID[:8] + ".txt"
	targetPath := filepath.Join(w.artifactRoot, filename)
	lockPath := filepath.Join(w.artifactRoot, toolOutputArtifactLockName)
	published := ""
	err := w.store.withPrivateFileLock(lockPath, func() error {
		if beforeToolOutputArtifactStreamOperation != nil {
			if err := beforeToolOutputArtifactStreamOperation("rename", targetPath); err != nil {
				return err
			}
		}
		if err := fileutil.RenamePathNoSymlink(w.tempPath, targetPath); err != nil {
			return err
		}
		// The temporary file was created and explicitly chmodded owner-only;
		// rename preserves the inode and mode. From this point the artifact is
		// published even if reservation cleanup needs later crash recovery.
		published = targetPath
		var cleanupErr error
		if beforeToolOutputArtifactStreamOperation != nil {
			cleanupErr = beforeToolOutputArtifactStreamOperation("release", w.reservationPath)
		}
		return errors.Join(cleanupErr, w.releaseReservation())
	})
	return published, err
}

func (w *ToolOutputArtifactStream) cleanupInternals() error {
	lockPath := filepath.Join(w.artifactRoot, toolOutputArtifactLockName)
	return w.store.withPrivateFileLock(lockPath, func() error {
		closeErr := w.closeOutput()
		removeErr := removeToolOutputArtifactInternal(w.tempPath)
		releaseErr := w.releaseReservation()
		return errors.Join(closeErr, removeErr, releaseErr)
	})
}

func (w *ToolOutputArtifactStream) closeOutput() error {
	if w.outputClosed || w.outputFile == nil {
		return nil
	}
	w.outputClosed = true
	return w.outputFile.Close()
}

func (w *ToolOutputArtifactStream) releaseReservation() error {
	var releaseErr error
	if w.reservationFile != nil {
		if err := unix.Flock(int(w.reservationFile.Fd()), unix.LOCK_UN); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
		if err := w.reservationFile.Close(); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
		w.reservationFile = nil
	}
	return errors.Join(releaseErr, removeToolOutputArtifactInternal(w.reservationPath))
}

func (w *ToolOutputArtifactStream) recordFailure(reason string, err error) {
	if err == nil {
		return
	}
	if w.firstErr == nil {
		w.firstErr = err
	} else {
		w.firstErr = errors.Join(w.firstErr, err)
	}
	switch reason {
	case ToolOutputArtifactReasonWriteFailed:
		w.contentFailed = true
		if !w.publishBlocked {
			w.stopReason = reason
		}
	case ToolOutputArtifactReasonReservationFail, ToolOutputArtifactReasonSyncFailed, ToolOutputArtifactReasonCloseFailed, ToolOutputArtifactReasonRenameFailed:
		if !w.publishBlocked {
			w.stopReason = reason
		}
		w.publishBlocked = true
	default:
		if w.stopReason == "" {
			w.stopReason = reason
		}
	}
}

func (w *ToolOutputArtifactStream) recordPublishedFailure(reason string, err error) {
	if err == nil {
		return
	}
	if w.firstErr == nil {
		w.firstErr = err
	} else {
		w.firstErr = errors.Join(w.firstErr, err)
	}
	if w.stopReason == "" {
		w.stopReason = reason
	}
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
	activeTemps := make(map[string]struct{})

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), toolOutputArtifactReservationPrefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return 0, 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return 0, 0, fmt.Errorf("invalid tool output artifact reservation: %s", path)
		}
		file, err := fileutil.OpenFileNoSymlink(path, unix.O_RDWR, 0o600)
		if err != nil {
			return 0, 0, err
		}
		lockErr := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		reservation, readErr := readToolOutputArtifactReservation(file)
		if lockErr == nil {
			_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
			_ = file.Close()
			if readErr == nil && validToolOutputArtifactTempName(reservation.TempName) {
				if err := removeToolOutputArtifactInternal(filepath.Join(root, reservation.TempName)); err != nil {
					return 0, 0, err
				}
			}
			if err := removeToolOutputArtifactInternal(path); err != nil {
				return 0, 0, err
			}
			continue
		}
		_ = file.Close()
		if !errors.Is(lockErr, unix.EWOULDBLOCK) && !errors.Is(lockErr, unix.EAGAIN) {
			return 0, 0, lockErr
		}
		if readErr != nil {
			return 0, 0, readErr
		}
		if reservation.SchemaVersion != toolOutputArtifactReservationSchema || reservation.ReservedBytes < 0 || !validToolOutputArtifactTempName(reservation.TempName) {
			return 0, 0, fmt.Errorf("invalid active tool output artifact reservation: %s", path)
		}
		if err := addToolOutputArtifactUsage(&totalBytes, int64(reservation.ReservedBytes), root); err != nil {
			return 0, 0, err
		}
		files++
		activeTemps[reservation.TempName] = struct{}{}
	}

	entries, err = os.ReadDir(root)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == toolOutputArtifactLockName || strings.HasPrefix(name, toolOutputArtifactReservationPrefix) {
			continue
		}
		path := filepath.Join(root, name)
		info, err := entry.Info()
		if err != nil {
			return 0, 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, 0, fmt.Errorf("refusing symlink in tool output artifact root: %s", path)
		}
		if !info.Mode().IsRegular() {
			return 0, 0, fmt.Errorf("unexpected non-regular entry in tool output artifact root: %s", path)
		}
		if strings.HasPrefix(name, toolOutputArtifactInflightPrefix) {
			if _, active := activeTemps[name]; active {
				continue
			}
			if err := removeToolOutputArtifactInternal(path); err != nil {
				return 0, 0, err
			}
			continue
		}
		if err := addToolOutputArtifactUsage(&totalBytes, info.Size(), root); err != nil {
			return 0, 0, err
		}
		files++
	}
	if totalBytes > int64(^uint(0)>>1) {
		return 0, 0, fmt.Errorf("tool output artifact usage exceeds platform int range in %s", root)
	}
	return int(totalBytes), files, nil
}

func addToolOutputArtifactUsage(total *int64, amount int64, root string) error {
	if amount < 0 || *total > int64(^uint(0)>>1)-amount {
		return fmt.Errorf("tool output artifact usage overflow in %s", root)
	}
	*total += amount
	return nil
}

func readToolOutputArtifactReservation(file *os.File) (toolOutputArtifactReservation, error) {
	var reservation toolOutputArtifactReservation
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return reservation, err
	}
	data, err := io.ReadAll(io.LimitReader(file, toolOutputArtifactReservationMax+1))
	if err != nil {
		return reservation, err
	}
	if len(data) > toolOutputArtifactReservationMax {
		return reservation, errors.New("tool output artifact reservation exceeds size limit")
	}
	if err := json.Unmarshal(data, &reservation); err != nil {
		return reservation, err
	}
	return reservation, nil
}

func writeToolOutputArtifactReservation(file *os.File, reservation toolOutputArtifactReservation) error {
	data, err := json.Marshal(reservation)
	if err != nil {
		return err
	}
	if len(data) > toolOutputArtifactReservationMax {
		return errors.New("tool output artifact reservation exceeds size limit")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Chmod(0o600)
}

func validToolOutputArtifactTempName(name string) bool {
	return filepath.Base(name) == name && strings.HasPrefix(name, toolOutputArtifactInflightPrefix) && strings.HasSuffix(name, ".tmp")
}

func removeToolOutputArtifactInternal(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	err := fileutil.RemoveFileNoSymlink(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func newToolOutputArtifactStreamID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
