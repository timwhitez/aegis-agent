package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"go-cli-agent/internal/session"
)

func featureListPath(execCtx ExecContext) string {
	return filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "feature_list.json")
}

func defFeatureListCreate() Definition {
	return Definition{
		Name:        "feature_list_create",
		Description: "Create an initializer-mode feature roadmap for a new project or explicit multi-feature bootstrap. Use this early in init mode to capture features and steps before scaffolding; use todo_write or task_create for normal implementation tracking outside initialization.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"features": map[string]any{
					"type":        "array",
					"description": "Ordered feature roadmap items to create.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"description": map[string]any{"type": "string", "description": "Short feature description."},
							"steps": map[string]any{
								"type":        "array",
								"description": "Optional implementation or validation steps for this feature.",
								"items":       map[string]any{"type": "string"},
							},
						},
						"required": []string{"description"},
					},
				},
			},
			"required": []string{"features"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Features []struct {
					Description string   `json:"description"`
					Steps       []string `json:"steps"`
				} `json:"features"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("feature_list_create", err), nil
			}

			now := time.Now().UTC().Format(time.RFC3339)
			var features []session.Feature
			for i, f := range input.Features {
				features = append(features, session.Feature{
					ID:          fmt.Sprintf("feature_%04d", i+1),
					Description: f.Description,
					Steps:       f.Steps,
					Status:      "pending",
					Passes:      0,
					CreatedAt:   now,
					UpdatedAt:   now,
				})
			}

			featureList := session.FeatureList{Features: features}
			path := featureListPath(execCtx)
			if err := execCtx.Store.SaveFeatureList(execCtx.SessionID, featureList); err != nil {
				return errorResult("feature_list_create", err), nil
			}

			return session.ToolResult{
				Name:          "feature_list_create",
				LLMOutput:     fmt.Sprintf("Created feature list with %d features at %s", len(features), path),
				DisplayOutput: fmt.Sprintf("Created feature list with %d features", len(features)),
			}, nil
		},
	}
}

func defFeatureListUpdate() Definition {
	return Definition{
		Name:        "feature_list_update",
		Description: "Update one initializer feature's status or pass count. Use this while working through the bootstrap roadmap created by feature_list_create, and mark a feature completed only after its setup and needed verification are done.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string", "description": "Feature ID from feature_list_create, for example feature_0001."},
				"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}, "description": "Optional new feature status."},
				"passes": map[string]any{"type": "integer", "description": "Optional count of completed verification or implementation passes."},
			},
			"required": []string{"id"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				ID     string `json:"id"`
				Status string `json:"status,omitempty"`
				Passes *int   `json:"passes,omitempty"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("feature_list_update", err), nil
			}

			featureList, err := execCtx.Store.LoadFeatureList(execCtx.SessionID)
			if err != nil {
				return errorResult("feature_list_update", fmt.Errorf("feature list not found: %w", err)), nil
			}

			found := false
			for i := range featureList.Features {
				if featureList.Features[i].ID == input.ID {
					found = true
					if input.Status != "" {
						featureList.Features[i].Status = input.Status
					}
					if input.Passes != nil {
						featureList.Features[i].Passes = *input.Passes
					}
					featureList.Features[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
					break
				}
			}

			if !found {
				return errorResult("feature_list_update", fmt.Errorf("feature not found: %s", input.ID)), nil
			}

			if err := execCtx.Store.SaveFeatureList(execCtx.SessionID, featureList); err != nil {
				return errorResult("feature_list_update", err), nil
			}

			return session.ToolResult{
				Name:          "feature_list_update",
				LLMOutput:     fmt.Sprintf("Updated feature %s", input.ID),
				DisplayOutput: fmt.Sprintf("Updated feature %s", input.ID),
			}, nil
		},
	}
}

func defFeatureListRead() Definition {
	return Definition{
		Name:        "feature_list_read",
		Description: "Read the initializer feature roadmap created for this session. Use before updating bootstrap feature status or when resuming init-mode setup.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			featureList, err := execCtx.Store.LoadFeatureList(execCtx.SessionID)
			if err != nil {
				return errorResult("feature_list_read", fmt.Errorf("feature list not found: %w", err)), nil
			}

			output, _ := json.MarshalIndent(featureList, "", "  ")
			return session.ToolResult{
				Name:          "feature_list_read",
				LLMOutput:     string(output),
				DisplayOutput: string(output),
			}, nil
		},
	}
}
