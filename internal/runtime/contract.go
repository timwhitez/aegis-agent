package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go-cli-agent/internal/session"
)

const (
	contractSourceUserInstruction = "user_instruction"
	contractTrustExplicitUser     = "explicit_user"
)

func buildSessionContract(meta session.SessionMetadata, messages []session.Message) session.SessionContract {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	contract := session.SessionContract{
		SchemaVersion: 1,
		ContractID:    "contract_" + meta.ID,
		Source:        contractSourceUserInstruction,
		TrustSource:   contractTrustExplicitUser,
		Profile:       contractProfile(meta, messages),
		AgentRole:     meta.AgentRole,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if meta.ProviderOptions.MaxOutputTokens > 0 {
		contract.MaxTurns = 0
	}
	paths := requestedArtifactPaths(meta.Workdir, messages)
	for _, path := range paths {
		artifact := session.RequiredArtifact{
			Path:        path,
			DisplayPath: displayPromptPath(meta.Workdir, path),
			Required:    true,
			Baseline:    artifactSnapshot(path),
			Status: session.ArtifactStatus{
				UpdatedAt: now,
			},
		}
		if activeReviewArtifactRequirement(messages).Active && looksRequestedReviewArtifactPath(path) {
			artifact.ContentValidator = "review_markdown"
		}
		artifact.Status = artifactStatusFromSnapshot(artifact.Baseline, artifact.Baseline, false, 0, "")
		contract.RequiredArtifacts = append(contract.RequiredArtifacts, artifact)
	}
	if len(contract.RequiredArtifacts) > 0 {
		contract.CompletionGates = append(contract.CompletionGates, "required_artifact")
	}
	if activeReviewArtifactRequirement(messages).Active {
		contract.CompletionGates = append(contract.CompletionGates, "review_artifact")
	}
	if req := latestTargetConsistencyRequirement(messages); req.Active {
		contract.CompletionGates = append(contract.CompletionGates, "target_consistency")
		contract.ExactTargetAnchors = append(contract.ExactTargetAnchors, req.TargetLiterals...)
	}
	if req := exactArtifactTemplateRequirement(meta.Workdir, messages); req.Active {
		contract.CompletionGates = append(contract.CompletionGates, "artifact_template")
		contract.ExactTemplateRequirements = append(contract.ExactTemplateRequirements, req.RequiredLines...)
	}
	if req := exactArtifactLiteralRequirement(meta.Workdir, messages); req.Active {
		contract.CompletionGates = append(contract.CompletionGates, "artifact_literal")
		contract.LiteralAnchors = append(contract.LiteralAnchors, req.RequiredLiterals...)
	}
	contract.CompletionGates = sortedUniqueStrings(contract.CompletionGates)
	return contract
}

func contractProfile(meta session.SessionMetadata, messages []session.Message) string {
	if strings.TrimSpace(meta.ParentSessionID) != "" || strings.TrimSpace(meta.QueueJobID) != "" || strings.TrimSpace(meta.AgentRole) != "" {
		return "delegated"
	}
	idx := latestExternalInstructionIndex(messages)
	if idx >= 0 {
		text := messages[idx].Text
		if looksAuditOrReviewTask(text) {
			if strings.Contains(strings.ToLower(text), "audit") || strings.Contains(text, "审计") {
				return "audit"
			}
			return "review"
		}
		lowered := strings.ToLower(text)
		if strings.Contains(lowered, "large project") || strings.Contains(text, "大型项目") || strings.Contains(text, "长期") {
			return "large_project"
		}
	}
	return "default"
}

func artifactSnapshot(path string) session.ArtifactSnapshot {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return session.ArtifactSnapshot{Exists: false}
	}
	snapshot := session.ArtifactSnapshot{
		Exists: true,
		Size:   info.Size(),
		MTime:  info.ModTime().UTC().Format(time.RFC3339Nano),
	}
	if data, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(data)
		snapshot.Hash = hex.EncodeToString(sum[:])
	}
	return snapshot
}

func artifactStatusFromSnapshot(current, baseline session.ArtifactSnapshot, touched bool, turn int, writer string) session.ArtifactStatus {
	status := session.ArtifactStatus{
		Present:          current.Exists,
		TouchedBySession: touched,
		LastWriteTurn:    turn,
		LastWriterTool:   writer,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
	if !baseline.Exists && current.Exists {
		status.ChangedFromBaseline = true
	} else if baseline.Exists && current.Exists {
		status.ChangedFromBaseline = baseline.Hash != "" && current.Hash != "" && baseline.Hash != current.Hash
		if !status.ChangedFromBaseline && baseline.Size != current.Size {
			status.ChangedFromBaseline = true
		}
	}
	return status
}

func refreshArtifactStatuses(artifacts []session.RequiredArtifact) []session.RequiredArtifact {
	out := append([]session.RequiredArtifact(nil), artifacts...)
	for i := range out {
		current := artifactSnapshot(out[i].Path)
		status := artifactStatusFromSnapshot(current, out[i].Baseline, out[i].Status.TouchedBySession, out[i].Status.LastWriteTurn, out[i].Status.LastWriterTool)
		status.ValidationStatus = out[i].Status.ValidationStatus
		status.ValidationIssues = append([]string(nil), out[i].Status.ValidationIssues...)
		out[i].Status = status
	}
	return out
}

func markArtifactWrite(artifacts []session.RequiredArtifact, path, toolName string, turn int) ([]session.RequiredArtifact, bool) {
	cleanPath := cleanAbsPath(path)
	if cleanPath == "" {
		return artifacts, false
	}
	out := append([]session.RequiredArtifact(nil), artifacts...)
	changed := false
	for i := range out {
		if cleanAbsPath(out[i].Path) != cleanPath {
			continue
		}
		current := artifactSnapshot(out[i].Path)
		out[i].Status = artifactStatusFromSnapshot(current, out[i].Baseline, true, turn, toolName)
		out[i].Status.ValidationStatus = "not_validated"
		changed = true
	}
	return out, changed
}

func cleanAbsPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func (r *Runner) refreshContractFromMessages(meta session.SessionMetadata, phase string) error {
	return refreshContractForSession(r.store, func(eventType string, data map[string]any) {
		r.emit(meta.ID, eventType, phase, data)
	}, meta)
}

func refreshContractForSession(store *session.Store, emit func(string, map[string]any), meta session.SessionMetadata) error {
	messages, err := store.LoadMessages(meta.ID)
	if err != nil {
		return err
	}
	next := buildSessionContract(meta, messages)
	existing, err := store.LoadContract(meta.ID)
	if err == nil {
		next.CreatedAt = existing.CreatedAt
		if contractsEquivalent(existing, next) {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveContract(meta.ID, next); err != nil {
		return err
	}
	if err := store.SaveArtifactTracker(meta.ID, next.RequiredArtifacts); err != nil {
		return err
	}
	_ = store.AppendContractHistory(meta.ID, next)
	eventType := "contract.created"
	if existing.ContractID != "" {
		eventType = "contract.updated"
	}
	if emit != nil {
		emit(eventType, map[string]any{
			"contract_id":        next.ContractID,
			"profile":            next.Profile,
			"required_artifacts": len(next.RequiredArtifacts),
			"completion_gates":   append([]string(nil), next.CompletionGates...),
		})
		if len(next.RequiredArtifacts) > 0 {
			emit("artifact.required", map[string]any{
				"contract_id": next.ContractID,
				"artifacts":   requiredArtifactEventPayload(next.RequiredArtifacts),
				"count":       len(next.RequiredArtifacts),
			})
		}
	}
	_ = writeSessionSummary(store, meta.ID)
	_ = writeLongRunCheckpoint(store, meta.ID)
	return nil
}

func requiredArtifactEventPayload(artifacts []session.RequiredArtifact) []map[string]any {
	out := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, map[string]any{
			"path":              artifact.Path,
			"display_path":      artifact.DisplayPath,
			"required":          artifact.Required,
			"baseline_exists":   artifact.Baseline.Exists,
			"content_validator": artifact.ContentValidator,
		})
	}
	return out
}

func contractsEquivalent(a, b session.SessionContract) bool {
	if a.Profile != b.Profile || a.AgentRole != b.AgentRole || strings.Join(a.CompletionGates, "\x00") != strings.Join(b.CompletionGates, "\x00") {
		return false
	}
	if len(a.RequiredArtifacts) != len(b.RequiredArtifacts) {
		return false
	}
	for i := range a.RequiredArtifacts {
		if cleanAbsPath(a.RequiredArtifacts[i].Path) != cleanAbsPath(b.RequiredArtifacts[i].Path) {
			return false
		}
	}
	return strings.Join(a.ExactTargetAnchors, "\x00") == strings.Join(b.ExactTargetAnchors, "\x00") &&
		strings.Join(a.ExactTemplateRequirements, "\x00") == strings.Join(b.ExactTemplateRequirements, "\x00") &&
		strings.Join(a.LiteralAnchors, "\x00") == strings.Join(b.LiteralAnchors, "\x00")
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
