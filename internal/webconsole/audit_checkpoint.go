package webconsole

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"aegis-agent/internal/fileutil"

	"golang.org/x/sys/unix"
)

func readWebAuditCheckpoint(logPath string) (webAuditCheckpoint, bool, error) {
	path := webAuditCheckpointPath(logPath)
	file, err := fileutil.OpenFileNoSymlink(path, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return webAuditCheckpoint{}, false, nil
		}
		return webAuditCheckpoint{}, false, err
	}
	defer file.Close()
	if _, err := hardenWebAuditRegularFile(path, file); err != nil {
		return webAuditCheckpoint{}, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWebAuditCheckpointBytes+1))
	if err != nil {
		return webAuditCheckpoint{}, false, err
	}
	if len(data) > maxWebAuditCheckpointBytes {
		return webAuditCheckpoint{}, false, fmt.Errorf("audit checkpoint exceeds %d bytes", maxWebAuditCheckpointBytes)
	}
	if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
		return webAuditCheckpoint{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var checkpoint webAuditCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return webAuditCheckpoint{}, false, fmt.Errorf("invalid audit checkpoint: %w", err)
	}
	if err := ensureAuditCheckpointJSONEOF(decoder); err != nil {
		return webAuditCheckpoint{}, false, fmt.Errorf("invalid audit checkpoint: %w", err)
	}
	if err := validateWebAuditCheckpoint(checkpoint); err != nil {
		return webAuditCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

func writeWebAuditCheckpoint(logPath string, checkpoint webAuditCheckpoint) error {
	if err := validateWebAuditCheckpoint(checkpoint); err != nil {
		return err
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := webAuditCheckpointPath(logPath)
	if beforeWriteAuditCheckpoint != nil {
		if err := beforeWriteAuditCheckpoint(path, data); err != nil {
			return err
		}
	}
	return fileutil.AtomicWriteFileNoSymlink(path, data, 0o600)
}

func validateWebAuditCheckpoint(checkpoint webAuditCheckpoint) error {
	if checkpoint.SchemaVersion != webAuditCheckpointSchemaVersion {
		return fmt.Errorf("unsupported audit checkpoint schema_version %d", checkpoint.SchemaVersion)
	}
	if !validWebAuditEpoch(checkpoint.Epoch) {
		return fmt.Errorf("invalid audit checkpoint epoch %q", checkpoint.Epoch)
	}
	if checkpoint.Size < 0 || checkpoint.Size > maxWebAuditLogBytes {
		return fmt.Errorf("invalid audit checkpoint size %d", checkpoint.Size)
	}
	if checkpoint.RecordCount > uint64(checkpoint.Size) {
		return fmt.Errorf("invalid audit checkpoint record_count %d for size %d", checkpoint.RecordCount, checkpoint.Size)
	}
	chain, err := decodeAuditDigest(checkpoint.ChainSHA256)
	if err != nil {
		return fmt.Errorf("invalid audit checkpoint chain_sha256: %w", err)
	}
	head, err := decodeAuditDigest(checkpoint.HeadSHA256)
	if err != nil {
		return fmt.Errorf("invalid audit checkpoint head_sha256: %w", err)
	}
	tail, err := decodeAuditDigest(checkpoint.TailSHA256)
	if err != nil {
		return fmt.Errorf("invalid audit checkpoint tail_sha256: %w", err)
	}
	expectedTailOffset := checkpoint.Size - webAuditProbeBytes
	if expectedTailOffset < 0 {
		expectedTailOffset = 0
	}
	if checkpoint.TailOffset != expectedTailOffset {
		return fmt.Errorf("invalid audit checkpoint tail_offset %d for size %d; expected %d", checkpoint.TailOffset, checkpoint.Size, expectedTailOffset)
	}
	if checkpoint.Size == 0 {
		var zeroChain [sha256.Size]byte
		emptyDigest := sha256.Sum256(nil)
		if checkpoint.RecordCount != 0 || chain != zeroChain || head != emptyDigest || tail != emptyDigest {
			return errors.New("empty audit checkpoint has non-empty record count or digest state")
		}
	}
	return nil
}

func ensureAuditCheckpointJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeAuditDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		if err == nil {
			err = fmt.Errorf("digest has %d bytes, expected %d", len(decoded), sha256.Size)
		}
		return digest, err
	}
	copy(digest[:], decoded)
	return digest, nil
}

func newWebAuditEpoch() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validWebAuditEpoch(epoch string) bool {
	if len(epoch) != 32 || strings.ToLower(epoch) != epoch {
		return false
	}
	decoded, err := hex.DecodeString(epoch)
	return err == nil && len(decoded) == 16
}

func formatWebAuditStructuredID(epoch string, offset int64) string {
	return fmt.Sprintf("%s%s_%d", webAuditStructuredIDPrefix, epoch, offset)
}

func parseWebAuditStructuredID(id string) (string, int64, bool, error) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, webAuditStructuredIDPrefix) {
		return "", 0, false, nil
	}
	rest := strings.TrimPrefix(id, webAuditStructuredIDPrefix)
	separator := strings.LastIndex(rest, "_")
	if separator <= 0 || separator == len(rest)-1 {
		return "", 0, true, fmt.Errorf("invalid reserved audit event id %q", id)
	}
	epoch := rest[:separator]
	if !validWebAuditEpoch(epoch) {
		return "", 0, true, fmt.Errorf("invalid reserved audit event id epoch %q", epoch)
	}
	offset, err := strconv.ParseInt(rest[separator+1:], 10, 64)
	if err != nil || offset < 0 {
		return "", 0, true, fmt.Errorf("invalid reserved audit event id offset in %q", id)
	}
	return epoch, offset, true, nil
}
