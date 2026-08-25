package webconsole

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func scanWebAuditLog(file *os.File, expected *webAuditCheckpoint, collectIDs bool) (webAuditScanResult, error) {
	if file == nil {
		return webAuditScanResult{}, fmt.Errorf("audit log file is required")
	}
	if expected != nil {
		if err := validateWebAuditCheckpoint(*expected); err != nil {
			return webAuditScanResult{}, err
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return webAuditScanResult{}, err
	}
	beforeInfo, err := file.Stat()
	if err != nil {
		return webAuditScanResult{}, err
	}
	if !beforeInfo.Mode().IsRegular() {
		return webAuditScanResult{}, errors.New("audit log is not a regular file")
	}
	if beforeInfo.Size() > maxWebAuditLogBytes {
		return webAuditScanResult{}, fmt.Errorf("web audit log exceeds the %d-byte retention limit; stop every web console process and archive the log with its checkpoint before restarting", maxWebAuditLogBytes)
	}
	if expected != nil {
		identity, identityOK := auditFileIdentity(beforeInfo)
		if expected.FileIdentity != "" && (!identityOK || identity != expected.FileIdentity) {
			return webAuditScanResult{}, fmt.Errorf("audit log file identity changed since checkpoint")
		}
		if beforeInfo.Size() < expected.Size {
			return webAuditScanResult{}, fmt.Errorf("audit log was truncated from checkpoint size %d to %d", expected.Size, beforeInfo.Size())
		}
	}

	reader := bufio.NewReaderSize(file, 64<<10)
	seenIDs := map[string]struct{}{}
	var chain [sha256.Size]byte
	var expectedChain [sha256.Size]byte
	var expectedChainCaptured bool
	var expectedRecordCount uint64
	var expectedRecordCountCaptured bool
	if expected != nil {
		decoded, err := decodeAuditDigest(expected.ChainSHA256)
		if err != nil {
			return webAuditScanResult{}, fmt.Errorf("invalid checkpoint chain digest: %w", err)
		}
		expectedChain = decoded
		expectedChainCaptured = expected.Size == 0
		expectedRecordCountCaptured = expected.Size == 0
		if expected.Size == 0 {
			var zero [sha256.Size]byte
			if expectedChain != zero || expected.RecordCount != 0 {
				return webAuditScanResult{}, fmt.Errorf("empty audit checkpoint has non-empty chain or record count")
			}
		}
	}

	var offset int64
	var recordCount uint64
	lastStructuredEpoch := ""
	line := 0
	for {
		raw, readErr := readWebAuditRecord(reader)
		if len(raw) > 0 {
			line++
			lineOffset := offset
			chain = advanceWebAuditChain(chain, raw)
			offset += int64(len(raw))
			if expected != nil && !expectedChainCaptured {
				switch {
				case offset == expected.Size:
					expectedChainCaptured = true
					if chain != expectedChain {
						return webAuditScanResult{}, fmt.Errorf("audit log historical prefix changed since checkpoint at byte %d", expected.Size)
					}
				case offset > expected.Size:
					return webAuditScanResult{}, fmt.Errorf("audit checkpoint size %d is not an audit record boundary", expected.Size)
				}
			}

			trimmed := bytes.TrimSpace(raw)
			if expected != nil && lineOffset >= expected.Size && len(trimmed) == 0 {
				return webAuditScanResult{}, fmt.Errorf("recovered audit tail contains an uncheckpointed blank record at byte %d", lineOffset)
			}
			if len(trimmed) > 0 {
				if auditRecordDecodeObserver != nil {
					auditRecordDecodeObserver()
				}
				var event webAuditEvent
				if err := json.Unmarshal(trimmed, &event); err != nil {
					return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: %w", line, err)
				}
				if err := validateAuditEvent(event); err != nil {
					return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: %w", line, err)
				}
				id := strings.TrimSpace(event.ID)
				if _, ok := seenIDs[id]; ok {
					return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: duplicate audit event id %q", line, id)
				}
				seenIDs[id] = struct{}{}
				epoch, idOffset, structured, err := parseWebAuditStructuredID(id)
				if err != nil {
					return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: %w", line, err)
				}
				if expected != nil && lineOffset >= expected.Size && !structured {
					return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: recovered audit tail must use a structured event id", line)
				}
				if structured {
					if lastStructuredEpoch != "" && epoch != lastStructuredEpoch {
						return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: structured audit event epoch %q differs from earlier epoch %q", line, epoch, lastStructuredEpoch)
					}
					if idOffset != lineOffset {
						return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: structured audit event id offset %d does not match record offset %d", line, idOffset, lineOffset)
					}
					if expected != nil && lineOffset >= expected.Size && epoch != expected.Epoch {
						return webAuditScanResult{}, fmt.Errorf("invalid audit log record %d: recovered audit tail epoch %q does not match checkpoint epoch %q", line, epoch, expected.Epoch)
					}
					lastStructuredEpoch = epoch
				}
				recordCount++
			}
			if expected != nil && offset == expected.Size && !expectedRecordCountCaptured {
				expectedRecordCount = recordCount
				expectedRecordCountCaptured = true
				if expectedRecordCount != expected.RecordCount {
					return webAuditScanResult{}, fmt.Errorf("audit checkpoint record_count %d does not match historical prefix count %d", expected.RecordCount, expectedRecordCount)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return webAuditScanResult{}, fmt.Errorf("read audit log record %d: %w", line+1, readErr)
		}
	}

	if expected != nil {
		if offset < expected.Size {
			return webAuditScanResult{}, fmt.Errorf("audit log was truncated from checkpoint size %d to %d", expected.Size, offset)
		}
		if !expectedChainCaptured {
			return webAuditScanResult{}, fmt.Errorf("audit checkpoint size %d was not reached", expected.Size)
		}
		if !expectedRecordCountCaptured {
			return webAuditScanResult{}, fmt.Errorf("audit checkpoint record count boundary %d was not reached", expected.Size)
		}
		if offset == expected.Size && chain != expectedChain {
			return webAuditScanResult{}, fmt.Errorf("audit log content changed since checkpoint")
		}
	}

	info, err := file.Stat()
	if err != nil {
		return webAuditScanResult{}, err
	}
	if info.Size() != offset {
		return webAuditScanResult{}, fmt.Errorf("audit log changed during validation: scanned %d bytes, stat reports %d", offset, info.Size())
	}
	if !webAuditFileInfoStable(beforeInfo, info) {
		return webAuditScanResult{}, errors.New("audit log identity or metadata changed during validation")
	}
	identity, identityOK := auditFileIdentity(info)
	if expected != nil && expected.FileIdentity != "" && (!identityOK || identity != expected.FileIdentity) {
		return webAuditScanResult{}, fmt.Errorf("audit log file identity changed since checkpoint")
	}
	if expected != nil && lastStructuredEpoch != "" && lastStructuredEpoch != expected.Epoch {
		return webAuditScanResult{}, fmt.Errorf("audit checkpoint epoch %q does not match structural history epoch %q", expected.Epoch, lastStructuredEpoch)
	}

	headDigest, tailOffset, tailDigest, err := auditProbeDigests(file, info.Size())
	if err != nil {
		return webAuditScanResult{}, err
	}
	afterProbeInfo, err := file.Stat()
	if err != nil {
		return webAuditScanResult{}, err
	}
	if !webAuditFileInfoStable(info, afterProbeInfo) {
		return webAuditScanResult{}, errors.New("audit log identity or metadata changed while capturing validation probes")
	}
	epoch := lastStructuredEpoch
	if expected != nil {
		epoch = expected.Epoch
	}
	checkpoint := webAuditCheckpoint{
		SchemaVersion:   webAuditCheckpointSchemaVersion,
		Epoch:           epoch,
		FileIdentity:    identity,
		Size:            afterProbeInfo.Size(),
		RecordCount:     recordCount,
		ChainSHA256:     hex.EncodeToString(chain[:]),
		HeadSHA256:      hex.EncodeToString(headDigest[:]),
		TailOffset:      tailOffset,
		TailSHA256:      hex.EncodeToString(tailDigest[:]),
		ModTimeUnixNano: afterProbeInfo.ModTime().UnixNano(),
	}
	checkpoint.ChangeStamp, _ = auditFileChangeStamp(afterProbeInfo)
	if !collectIDs {
		seenIDs = nil
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return webAuditScanResult{}, err
	}
	return webAuditScanResult{checkpoint: checkpoint, seenIDs: seenIDs}, nil
}

func readWebAuditRecord(reader *bufio.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("audit log reader is required")
	}
	record := make([]byte, 0, 4096)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(record)+len(fragment) > maxWebAuditRecordBytes {
			return nil, fmt.Errorf("audit log record exceeds %d bytes", maxWebAuditRecordBytes)
		}
		record = append(record, fragment...)
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(record) == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("audit log final record is not newline-terminated")
		default:
			return nil, err
		}
	}
}

func encodeWebAuditBatch(events []webAuditEvent, state webAuditCheckpoint) (string, [sha256.Size]byte, error) {
	if err := validateWebAuditCheckpoint(state); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	chain, err := decodeAuditDigest(state.ChainSHA256)
	if err != nil {
		return "", [sha256.Size]byte{}, err
	}
	var batch strings.Builder
	cursor := state.Size
	for i := range events {
		events[i].ID = formatWebAuditStructuredID(state.Epoch, cursor)
		if err := validateAuditEvent(events[i]); err != nil {
			return "", [sha256.Size]byte{}, err
		}
		var line bytes.Buffer
		if err := json.NewEncoder(&line).Encode(events[i]); err != nil {
			return "", [sha256.Size]byte{}, err
		}
		if line.Len() > maxWebAuditRecordBytes {
			return "", [sha256.Size]byte{}, fmt.Errorf("encoded audit event exceeds %d bytes", maxWebAuditRecordBytes)
		}
		raw := line.Bytes()
		chain = advanceWebAuditChain(chain, raw)
		batch.Write(raw)
		cursor += int64(len(raw))
	}
	return batch.String(), chain, nil
}

func advanceWebAuditChain(previous [sha256.Size]byte, raw []byte) [sha256.Size]byte {
	hasher := sha256.New()
	_, _ = hasher.Write(previous[:])
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(raw)
	var next [sha256.Size]byte
	copy(next[:], hasher.Sum(nil))
	return next
}

func auditProbeDigests(file *os.File, size int64) ([sha256.Size]byte, int64, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if file == nil {
		return zero, 0, zero, errors.New("audit log file is required")
	}
	if size < 0 {
		return zero, 0, zero, errors.New("audit log size must be non-negative")
	}
	headLength := size
	if headLength > webAuditProbeBytes {
		headLength = webAuditProbeBytes
	}
	head, err := io.ReadAll(io.NewSectionReader(file, 0, headLength))
	if err != nil {
		return zero, 0, zero, err
	}
	tailOffset := size - webAuditProbeBytes
	if tailOffset < 0 {
		tailOffset = 0
	}
	tail, err := io.ReadAll(io.NewSectionReader(file, tailOffset, size-tailOffset))
	if err != nil {
		return zero, 0, zero, err
	}
	return sha256.Sum256(head), tailOffset, sha256.Sum256(tail), nil
}
