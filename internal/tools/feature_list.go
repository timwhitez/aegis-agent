package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		Description: "Create a feature list for tracking multi-feature tasks",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"features": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"description": map[string]any{"type": "string"},
							"steps": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
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
			data, err := json.MarshalIndent(featureList, "", "  ")
			if err != nil {
				return errorResult("feature_list_create", err), nil
			}

			path := featureListPath(execCtx)
			if err := writeAtomically(path, data, 0o644); err != nil {
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
		Description: "Update feature status and passes count",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string"},
				"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
				"passes": map[string]any{"type": "integer"},
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

			path := featureListPath(execCtx)
			data, err := os.ReadFile(path)
			if err != nil {
				return errorResult("feature_list_update", fmt.Errorf("feature list not found: %w", err)), nil
			}

			var featureList session.FeatureList
			if err := json.Unmarshal(data, &featureList); err != nil {
				return errorResult("feature_list_update", err), nil
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

			data, err = json.MarshalIndent(featureList, "", "  ")
			if err != nil {
				return errorResult("feature_list_update", err), nil
			}

			if err := writeAtomically(path, data, 0o644); err != nil {
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
		Description: "Read current feature list",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			path := featureListPath(execCtx)
			data, err := os.ReadFile(path)
			if err != nil {
				return errorResult("feature_list_read", fmt.Errorf("feature list not found: %w", err)), nil
			}

			var featureList session.FeatureList
			if err := json.Unmarshal(data, &featureList); err != nil {
				return errorResult("feature_list_read", err), nil
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
