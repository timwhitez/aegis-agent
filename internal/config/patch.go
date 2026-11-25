package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"aegis-agent/internal/fileutil"

	"gopkg.in/yaml.v3"
)

// FilePatch describes a field-level settings mutation to apply to an existing
// config YAML file while preserving comments, key order, indentation, and
// untouched keys.
type FilePatch struct {
	DefaultProviderBefore string
	DefaultProvider       *string

	ProviderName   string
	ProviderBefore Provider
	ProviderAfter  Provider
	ProviderFields ProviderFieldMask

	Runtime       RuntimePatch
	RoleProviders map[string]RoleProviderPatch
}

// ProviderFieldMask marks which provider-scoped fields were supplied by the
// mutation request.
type ProviderFieldMask struct {
	APIProvider         bool
	BaseURL             bool
	Model               bool
	ContextWindowTokens bool
	ReasoningMode       bool
	ReasoningSummary    bool
}

// RuntimePatch describes runtime-scoped fields mutated by a settings request.
type RuntimePatch struct {
	GuardrailsModeBefore string
	GuardrailsMode       *string

	MaxTurnsSoftBefore int
	MaxTurnsSoft       *int

	MaxTurnsHardBefore int
	MaxTurnsHard       *int

	ChildBudget *ChildBudgetPatch
}

// ChildBudgetPatch captures the before/after canonical child budget values.
type ChildBudgetPatch struct {
	Before ChildBudgetConfig
	After  ChildBudgetConfig
}

// RoleProviderPatch captures the before/after override for a single role.
type RoleProviderPatch struct {
	Before RoleProviderOverride
	After  RoleProviderOverride
}

// PatchFile applies a field-level YAML AST patch to path when the file already
// exists, preserving comments, indentation, key order, and untouched fields.
// When path does not exist yet, it falls back to WriteFile(path, fallbackCfg).
func PatchFile(path string, fallbackCfg *Config, patch FilePatch) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is required")
	}
	existingBytes, _, err := fileutil.ReadRegularFileNoSymlink(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WriteFile(path, fallbackCfg)
		}
		return err
	}
	if len(bytes.TrimSpace(existingBytes)) == 0 {
		return WriteFile(path, fallbackCfg)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(existingBytes, &doc); err != nil {
		return fmt.Errorf("parse existing config yaml: %w", err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		root := &yaml.Node{
			Kind:        yaml.MappingNode,
			Tag:         "!!map",
			HeadComment: strings.TrimRight(string(existingBytes), "\r\n"),
		}
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{root},
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return errors.New("existing config yaml must be a document")
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		root.Kind = yaml.MappingNode
		root.Tag = "!!map"
		root.Value = ""
		root.Style = 0
		root.Content = nil
	}
	if root.Kind != yaml.MappingNode {
		return errors.New("existing config yaml root must be a mapping")
	}

	indent := detectYAMLIndent(root)
	// Capture the blank-line layout before mutating so the re-encoded document
	// keeps the operator's block separators.
	layout := captureBlankLineLayout(&doc, existingBytes)
	changed := applyFilePatch(root, patch)
	if !changed {
		return fileutil.AtomicWriteFileNoSymlink(path, existingBytes, 0o600)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	if err := enc.Encode(&doc); err != nil {
		_ = enc.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileNoSymlink(path, layout.restore(buf.Bytes()), 0o600)
}

func applyFilePatch(root *yaml.Node, patch FilePatch) bool {
	changed := false

	if patch.DefaultProvider != nil {
		val := strings.TrimSpace(*patch.DefaultProvider)
		if _, idx := mappingValueNode(root, "default_provider"); idx >= 0 {
			if setMappingString(root, "default_provider", val) {
				changed = true
			}
		} else if val != "" && val != strings.TrimSpace(patch.DefaultProviderBefore) {
			if setMappingString(root, "default_provider", val) {
				changed = true
			}
		}
	}

	if providerName := strings.TrimSpace(patch.ProviderName); providerName != "" {
		providersNode, _ := mappingValueNode(root, "providers")
		var existingProviderNode *yaml.Node
		if providersNode != nil && providersNode.Kind == yaml.MappingNode {
			existingProviderNode, _ = mappingValueNode(providersNode, providerName)
		}
		if existingProviderNode != nil && existingProviderNode.Kind == yaml.MappingNode {
			if patchExistingProviderNode(existingProviderNode, patch.ProviderBefore, patch.ProviderAfter, patch.ProviderFields) {
				changed = true
			}
		} else if existingProviderNode != nil && existingProviderNode.Kind == yaml.ScalarNode && existingProviderNode.Tag == "!!null" {
			if !ProvidersEqual(patch.ProviderBefore, patch.ProviderAfter) {
				existingProviderNode.Kind = yaml.MappingNode
				existingProviderNode.Tag = "!!map"
				existingProviderNode.Value = ""
				existingProviderNode.Style = 0
				existingProviderNode.Content = nil
				if patchExistingProviderNode(existingProviderNode, patch.ProviderBefore, patch.ProviderAfter, patch.ProviderFields) {
					changed = true
				}
			}
		} else if !ProvidersEqual(patch.ProviderBefore, patch.ProviderAfter) {
			providersNode = ensureMappingChild(root, "providers")
			if pNode, err := encodeProviderNode(patch.ProviderAfter); err == nil {
				setMappingNode(providersNode, providerName, pNode)
				changed = true
			}
		}
	}

	if applyRuntimePatch(root, patch.Runtime) {
		changed = true
	}

	if applyRoleProvidersPatch(root, patch.RoleProviders) {
		changed = true
	}

	return changed
}

func patchExistingProviderNode(node *yaml.Node, before, after Provider, mask ProviderFieldMask) bool {
	changed := false

	if mask.APIProvider {
		if after.APIProvider != "" {
			if _, idx := mappingValueNode(node, "api_provider"); idx >= 0 || after.APIProvider != before.APIProvider {
				if setMappingString(node, "api_provider", after.APIProvider) {
					changed = true
				}
			}
		} else {
			if existing, idx := mappingValueNode(node, "api_provider"); idx >= 0 {
				if after.APIProvider != before.APIProvider || scalarStringValue(existing) != "" {
					if deleteMappingKey(node, "api_provider") {
						changed = true
					}
				}
			}
		}
	}

	if mask.BaseURL {
		if _, idx := mappingValueNode(node, "base_url"); idx >= 0 || after.BaseURL != before.BaseURL || after.BaseURL != "" {
			if setMappingString(node, "base_url", after.BaseURL) {
				changed = true
			}
		}
	}

	if mask.Model {
		if _, idx := mappingValueNode(node, "model"); idx >= 0 || after.Model != before.Model || after.Model != "" {
			if setMappingString(node, "model", after.Model) {
				changed = true
			}
		}
	}

	if mask.ContextWindowTokens {
		if after.ContextWindowTokens > 0 {
			if _, idx := mappingValueNode(node, "context_window_tokens"); idx >= 0 || after.ContextWindowTokens != before.ContextWindowTokens {
				if setMappingInt(node, "context_window_tokens", after.ContextWindowTokens) {
					changed = true
				}
			}
		} else {
			if existing, idx := mappingValueNode(node, "context_window_tokens"); idx >= 0 {
				existingVal, ok := scalarIntValue(existing)
				if after.ContextWindowTokens != before.ContextWindowTokens || !ok || existingVal != 0 {
					if deleteMappingKey(node, "context_window_tokens") {
						changed = true
					}
				}
			}
		}
	}

	if patchOptionalStringField(node, "reasoning_effort", before.ReasoningEffort, after.ReasoningEffort, mask.ReasoningMode || mask.APIProvider) {
		changed = true
	}
	if patchOptionalStringField(node, "reasoning_summary", before.ReasoningSummary, after.ReasoningSummary, mask.ReasoningSummary || mask.APIProvider) {
		changed = true
	}
	if patchOptionalIntPtrField(node, "thinking_budget", before.ThinkingBudget, after.ThinkingBudget, mask.ReasoningMode || mask.APIProvider) {
		changed = true
	}
	if patchOptionalStringField(node, "thinking_mode", before.ThinkingMode, after.ThinkingMode, mask.ReasoningMode || mask.APIProvider) {
		changed = true
	}
	if patchOptionalStringField(node, "thinking_display", before.ThinkingDisplay, after.ThinkingDisplay, mask.ReasoningMode || mask.APIProvider) {
		changed = true
	}
	if patchOptionalBoolPtrField(node, "include_thoughts", before.IncludeThoughts, after.IncludeThoughts, mask.ReasoningMode || mask.APIProvider) {
		changed = true
	}
	if after.OutputConfigEffort != before.OutputConfigEffort {
		if after.OutputConfigEffort != "" {
			if setMappingString(node, "output_config_effort", after.OutputConfigEffort) {
				changed = true
			}
		} else if deleteMappingKey(node, "output_config_effort") {
			changed = true
		}
	}
	if after.MaxOutputTokens != before.MaxOutputTokens {
		if after.MaxOutputTokens > 0 {
			if setMappingInt(node, "max_output_tokens", after.MaxOutputTokens) {
				changed = true
			}
		} else if deleteMappingKey(node, "max_output_tokens") {
			changed = true
		}
	}

	return changed
}

func patchOptionalStringField(node *yaml.Node, key, before, after string, reconcileEmpty bool) bool {
	if after != before {
		if after != "" {
			return setMappingString(node, key, after)
		}
		return deleteMappingKey(node, key)
	}
	if reconcileEmpty && after == "" {
		if existing, idx := mappingValueNode(node, key); idx >= 0 && scalarStringValue(existing) != "" {
			return deleteMappingKey(node, key)
		}
	}
	return false
}

func patchOptionalIntPtrField(node *yaml.Node, key string, before, after *int, reconcileEmpty bool) bool {
	if !intPtrEqual(before, after) {
		if after != nil {
			return setMappingInt(node, key, *after)
		}
		return deleteMappingKey(node, key)
	}
	if reconcileEmpty && after == nil {
		if _, idx := mappingValueNode(node, key); idx >= 0 {
			return deleteMappingKey(node, key)
		}
	}
	return false
}

func patchOptionalBoolPtrField(node *yaml.Node, key string, before, after *bool, reconcileEmpty bool) bool {
	if !boolPtrEqual(before, after) {
		if after != nil {
			return setMappingBool(node, key, *after)
		}
		return deleteMappingKey(node, key)
	}
	if reconcileEmpty && after == nil {
		if _, idx := mappingValueNode(node, key); idx >= 0 {
			return deleteMappingKey(node, key)
		}
	}
	return false
}

func applyRuntimePatch(root *yaml.Node, patch RuntimePatch) bool {
	changed := false
	runtimeNode, _ := mappingValueNode(root, "runtime")
	getRuntimeNode := func() *yaml.Node {
		if runtimeNode == nil || runtimeNode.Kind != yaml.MappingNode {
			runtimeNode = ensureMappingChild(root, "runtime")
		}
		return runtimeNode
	}

	if patch.GuardrailsMode != nil {
		val := *patch.GuardrailsMode
		if runtimeNode != nil && runtimeNode.Kind == yaml.MappingNode {
			if _, idx := mappingValueNode(runtimeNode, "guardrails_mode"); idx >= 0 {
				if setMappingString(runtimeNode, "guardrails_mode", val) {
					changed = true
				}
			} else if val != patch.GuardrailsModeBefore {
				if setMappingString(runtimeNode, "guardrails_mode", val) {
					changed = true
				}
			}
		} else if val != patch.GuardrailsModeBefore {
			if setMappingString(getRuntimeNode(), "guardrails_mode", val) {
				changed = true
			}
		}
	}

	if patch.MaxTurnsSoft != nil {
		val := *patch.MaxTurnsSoft
		if runtimeNode != nil && runtimeNode.Kind == yaml.MappingNode {
			if _, idx := mappingValueNode(runtimeNode, "max_turns_soft"); idx >= 0 {
				if setMappingInt(runtimeNode, "max_turns_soft", val) {
					changed = true
				}
			} else if val != patch.MaxTurnsSoftBefore {
				if setMappingInt(runtimeNode, "max_turns_soft", val) {
					changed = true
				}
			}
		} else if val != patch.MaxTurnsSoftBefore {
			if setMappingInt(getRuntimeNode(), "max_turns_soft", val) {
				changed = true
			}
		}
	}

	if patch.MaxTurnsHard != nil {
		val := *patch.MaxTurnsHard
		if runtimeNode != nil && runtimeNode.Kind == yaml.MappingNode {
			if _, idx := mappingValueNode(runtimeNode, "max_turns_hard"); idx >= 0 {
				if setMappingInt(runtimeNode, "max_turns_hard", val) {
					changed = true
				}
			} else if val != patch.MaxTurnsHardBefore {
				if setMappingInt(runtimeNode, "max_turns_hard", val) {
					changed = true
				}
			}
		} else if val != patch.MaxTurnsHardBefore {
			if setMappingInt(getRuntimeNode(), "max_turns_hard", val) {
				changed = true
			}
		}
	}

	if patch.ChildBudget != nil {
		cb := patch.ChildBudget
		var existingCB *yaml.Node
		if runtimeNode != nil && runtimeNode.Kind == yaml.MappingNode {
			existingCB, _ = mappingValueNode(runtimeNode, "child_budget")
		}
		same := cb.Before.MaxActiveRuntimeSec == cb.After.MaxActiveRuntimeSec &&
			cb.Before.MaxElapsedSec == cb.After.MaxElapsedSec &&
			cb.Before.MaxTurnsPerAttempt == cb.After.MaxTurnsPerAttempt &&
			cb.Before.ActiveRuntimeCheckpointMS == cb.After.ActiveRuntimeCheckpointMS
		needsWrite := !same
		if !needsWrite && existingCB != nil && existingCB.Kind == yaml.MappingNode {
			if childBudgetNodeNeedsSync(existingCB, cb.After) {
				needsWrite = true
			}
		}
		if needsWrite {
			cbNode := ensureMappingChild(getRuntimeNode(), "child_budget")
			if setMappingInt(cbNode, "max_active_runtime_sec", cb.After.MaxActiveRuntimeSec) {
				changed = true
			}
			if setMappingInt(cbNode, "max_elapsed_sec", cb.After.MaxElapsedSec) {
				changed = true
			}
			if setMappingInt(cbNode, "max_turns_per_attempt", cb.After.MaxTurnsPerAttempt) {
				changed = true
			}
			if setMappingInt(cbNode, "active_runtime_checkpoint_ms", cb.After.ActiveRuntimeCheckpointMS) {
				changed = true
			}
			if deleteMappingKey(cbNode, "max_wall_clock_sec") {
				changed = true
			}
			if deleteMappingKey(cbNode, "max_turns") {
				changed = true
			}
		}
	}

	return changed
}

func childBudgetNodeNeedsSync(cbNode *yaml.Node, want ChildBudgetConfig) bool {
	if _, idx := mappingValueNode(cbNode, "max_wall_clock_sec"); idx >= 0 {
		return true
	}
	if _, idx := mappingValueNode(cbNode, "max_turns"); idx >= 0 {
		return true
	}
	checks := []struct {
		key  string
		want int
	}{
		{"max_active_runtime_sec", want.MaxActiveRuntimeSec},
		{"max_elapsed_sec", want.MaxElapsedSec},
		{"max_turns_per_attempt", want.MaxTurnsPerAttempt},
		{"active_runtime_checkpoint_ms", want.ActiveRuntimeCheckpointMS},
	}
	for _, check := range checks {
		node, idx := mappingValueNode(cbNode, check.key)
		if idx < 0 {
			continue
		}
		got, ok := scalarIntValue(node)
		if !ok || got != check.want {
			return true
		}
	}
	return false
}

func applyRoleProvidersPatch(root *yaml.Node, patches map[string]RoleProviderPatch) bool {
	if len(patches) == 0 {
		return false
	}
	changed := false
	roles := []string{"planner", "generator", "evaluator", "explorer"}
	for _, role := range roles {
		rp, ok := patches[role]
		if !ok || rp.Before == rp.After {
			continue
		}
		rpNode := ensureMappingChild(root, "role_providers")
		roleNode, roleIdx := mappingValueNode(rpNode, role)
		if roleIdx < 0 || roleNode.Kind != yaml.MappingNode {
			if rp.After == (RoleProviderOverride{}) {
				if roleIdx >= 0 && deleteMappingKey(rpNode, role) {
					changed = true
				}
				continue
			}
			roleNode = ensureMappingChild(rpNode, role)
		}
		if patchRoleStringField(roleNode, "provider", rp.Before.Provider, rp.After.Provider) {
			changed = true
		}
		if patchRoleStringField(roleNode, "api_provider", rp.Before.APIProvider, rp.After.APIProvider) {
			changed = true
		}
		if patchRoleStringField(roleNode, "base_url", rp.Before.BaseURL, rp.After.BaseURL) {
			changed = true
		}
		if patchRoleStringField(roleNode, "model", rp.Before.Model, rp.After.Model) {
			changed = true
		}
		if patchRoleStringField(roleNode, "reasoning_effort", rp.Before.ReasoningEffort, rp.After.ReasoningEffort) {
			changed = true
		}
		if patchRoleStringField(roleNode, "output_config_effort", rp.Before.OutputConfigEffort, rp.After.OutputConfigEffort) {
			changed = true
		}
		if rp.After.MaxOutputTokens != rp.Before.MaxOutputTokens {
			if rp.After.MaxOutputTokens > 0 {
				if setMappingInt(roleNode, "max_output_tokens", rp.After.MaxOutputTokens) {
					changed = true
				}
			} else if deleteMappingKey(roleNode, "max_output_tokens") {
				changed = true
			}
		}
		if len(roleNode.Content) == 0 {
			if deleteMappingKey(rpNode, role) {
				changed = true
			}
		}
	}
	if rpNode, idx := mappingValueNode(root, "role_providers"); idx >= 0 && rpNode.Kind == yaml.MappingNode && len(rpNode.Content) == 0 {
		deleteMappingKey(root, "role_providers")
	}
	return changed
}

func patchRoleStringField(roleNode *yaml.Node, key, before, after string) bool {
	if after == before {
		return false
	}
	if after != "" {
		return setMappingString(roleNode, key, after)
	}
	return deleteMappingKey(roleNode, key)
}

func encodeProviderNode(p Provider) (*yaml.Node, error) {
	if p.RequestTimeoutSec > 0 && p.TimeoutSec == p.RequestTimeoutSec {
		p.TimeoutSec = 0
	}
	var node yaml.Node
	if err := node.Encode(p); err != nil {
		return nil, err
	}
	return &node, nil
}

// ProvidersEqual reports whether two Provider structs have identical persisted values.
func ProvidersEqual(a, b Provider) bool {
	return a.APIProvider == b.APIProvider &&
		a.APIKeyEnv == b.APIKeyEnv &&
		a.BaseURL == b.BaseURL &&
		a.Model == b.Model &&
		a.ContextWindowTokens == b.ContextWindowTokens &&
		a.TimeoutSec == b.TimeoutSec &&
		a.RequestTimeoutSec == b.RequestTimeoutSec &&
		a.StreamIdleTimeoutMS == b.StreamIdleTimeoutMS &&
		a.Retry == b.Retry &&
		a.AnthropicVersion == b.AnthropicVersion &&
		a.WireAPI == b.WireAPI &&
		floatPtrEqual(a.Temperature, b.Temperature) &&
		floatPtrEqual(a.TopP, b.TopP) &&
		a.MaxOutputTokens == b.MaxOutputTokens &&
		a.ReasoningEffort == b.ReasoningEffort &&
		a.ReasoningSummary == b.ReasoningSummary &&
		a.TextVerbosity == b.TextVerbosity &&
		intPtrEqual(a.ThinkingBudget, b.ThinkingBudget) &&
		a.ThinkingMode == b.ThinkingMode &&
		a.ThinkingDisplay == b.ThinkingDisplay &&
		boolPtrEqual(a.IncludeThoughts, b.IncludeThoughts) &&
		a.OutputConfigEffort == b.OutputConfigEffort &&
		boolPtrEqual(a.PromptCache, b.PromptCache) &&
		boolPtrEqual(a.Store, b.Store) &&
		boolPtrEqual(a.SendMetadata, b.SendMetadata) &&
		boolPtrEqual(a.RawSidecar, b.RawSidecar)
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func detectYAMLIndent(node *yaml.Node) int {
	if node == nil {
		return 2
	}
	if indent := findNestedIndent(node); indent >= 2 && indent <= 8 {
		return indent
	}
	return 2
}

func findNestedIndent(node *yaml.Node) int {
	if node == nil || node.Style&yaml.FlowStyle != 0 {
		return 0
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			val := node.Content[i+1]
			if val.Style&yaml.FlowStyle == 0 && (val.Kind == yaml.MappingNode || val.Kind == yaml.SequenceNode) && len(val.Content) > 0 {
				diff := val.Content[0].Column - key.Column
				if diff >= 2 && diff <= 8 {
					return diff
				}
			}
			if nested := findNestedIndent(val); nested > 0 {
				return nested
			}
		}
	} else if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if nested := findNestedIndent(item); nested > 0 {
				return nested
			}
		}
	}
	return 0
}

func mappingValueNode(m *yaml.Node, key string) (*yaml.Node, int) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i+1], i
		}
	}
	return nil, -1
}

func ensureMappingChild(parent *yaml.Node, key string) *yaml.Node {
	if val, idx := mappingValueNode(parent, key); idx >= 0 {
		if val.Kind == yaml.MappingNode {
			return val
		}
		val.Kind = yaml.MappingNode
		val.Tag = "!!map"
		val.Value = ""
		val.Style = 0
		val.Content = nil
		return val
	}
	child := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		child,
	)
	return child
}

func setMappingNode(m *yaml.Node, key string, val *yaml.Node) {
	if _, idx := mappingValueNode(m, key); idx >= 0 {
		m.Content[idx+1] = val
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		val,
	)
}

func setMappingString(m *yaml.Node, key, value string) bool {
	if existing, idx := mappingValueNode(m, key); idx >= 0 {
		if existing.Kind == yaml.ScalarNode && existing.Tag == "!!str" && existing.Value == value {
			return false
		}
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		existing.Content = nil
		if value == "" {
			if existing.Style != yaml.SingleQuotedStyle && existing.Style != yaml.DoubleQuotedStyle {
				existing.Style = yaml.DoubleQuotedStyle
			}
		} else if existing.Style != yaml.SingleQuotedStyle && existing.Style != yaml.DoubleQuotedStyle {
			existing.Style = 0
		}
		return true
	}
	style := yaml.Style(0)
	if value == "" {
		style = yaml.DoubleQuotedStyle
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style},
	)
	return true
}

func setMappingInt(m *yaml.Node, key string, value int) bool {
	formatted := strconv.Itoa(value)
	if existing, idx := mappingValueNode(m, key); idx >= 0 {
		if got, ok := scalarIntValue(existing); ok && got == value {
			return false
		}
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!int"
		existing.Value = formatted
		existing.Style = 0
		existing.Content = nil
		return true
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: formatted},
	)
	return true
}

func setMappingBool(m *yaml.Node, key string, value bool) bool {
	formatted := strconv.FormatBool(value)
	if existing, idx := mappingValueNode(m, key); idx >= 0 {
		if existing.Kind == yaml.ScalarNode && existing.Tag == "!!bool" && existing.Value == formatted {
			return false
		}
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!bool"
		existing.Value = formatted
		existing.Style = 0
		existing.Content = nil
		return true
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: formatted},
	)
	return true
}

func deleteMappingKey(m *yaml.Node, key string) bool {
	if _, idx := mappingValueNode(m, key); idx >= 0 {
		m.Content = append(m.Content[:idx], m.Content[idx+2:]...)
		return true
	}
	return false
}

func scalarStringValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func scalarIntValue(node *yaml.Node) (int, bool) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return 0, false
	}
	var out int
	if err := node.Decode(&out); err != nil {
		return 0, false
	}
	return out, true
}
