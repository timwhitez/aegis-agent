package config

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxConfigYAMLValidationDepth = 64
	maxConfigYAMLValidationNodes = 100000
)

// configYAMLAlias avoids recursively calling Config.UnmarshalYAML when the
// validated node is decoded into the existing layered configuration.
type configYAMLAlias Config

type configYAMLValidationKey struct {
	node   *yaml.Node
	target reflect.Type
	merge  bool
}

type configYAMLValidationState struct {
	visited   int
	active    map[configYAMLValidationKey]struct{}
	validated map[configYAMLValidationKey]struct{}
}

// UnmarshalYAML rejects unknown keys before applying a user-authored layer.
// yaml.v3's ordinary Unmarshal path silently ignores unknown struct fields,
// which is unsafe for misspelled policy, sandbox, hook, and provider settings.
func (cfg *Config) UnmarshalYAML(node *yaml.Node) error {
	if cfg == nil {
		return fmt.Errorf("configuration target is nil")
	}
	if err := validateConfigYAMLNode(node, reflect.TypeOf(Config{}), nil); err != nil {
		return err
	}
	return node.Decode((*configYAMLAlias)(cfg))
}

func validateConfigYAMLNode(node *yaml.Node, target reflect.Type, path []string) error {
	state := &configYAMLValidationState{
		active:    make(map[configYAMLValidationKey]struct{}),
		validated: make(map[configYAMLValidationKey]struct{}),
	}
	return validateConfigYAMLNodeState(node, target, path, 0, state)
}

func validateConfigYAMLNodeState(node *yaml.Node, target reflect.Type, path []string, depth int, state *configYAMLValidationState) (retErr error) {
	if node == nil {
		return nil
	}
	if depth > maxConfigYAMLValidationDepth {
		return configYAMLDepthError(path)
	}
	if state == nil {
		return fmt.Errorf("configuration YAML validation state is nil")
	}
	state.visited++
	if state.visited > maxConfigYAMLValidationNodes {
		displayPath := configYAMLDisplayPath(path)
		return fmt.Errorf("configuration YAML validation exceeds %d node visits at %s", maxConfigYAMLValidationNodes, displayPath)
	}

	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	key := configYAMLValidationKey{node: node, target: target}
	if _, ok := state.validated[key]; ok {
		return nil
	}
	if _, ok := state.active[key]; ok {
		return fmt.Errorf("configuration YAML alias cycle detected at %s", configYAMLDisplayPath(path))
	}
	state.active[key] = struct{}{}
	defer func() {
		delete(state.active, key)
		if retErr == nil {
			state.validated[key] = struct{}{}
		}
	}()

	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		return validateConfigYAMLNodeState(node.Content[0], target, path, depth+1, state)
	}
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil {
			return fmt.Errorf("configuration YAML alias is missing its target at %s", configYAMLDisplayPath(path))
		}
		return validateConfigYAMLNodeState(node.Alias, target, path, depth+1, state)
	}
	if node.Tag == "!!null" {
		return nil
	}

	switch target.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		fields := configYAMLFields(target)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			keyName := strings.TrimSpace(keyNode.Value)
			if keyName == "<<" {
				if err := validateConfigYAMLMergeState(valueNode, target, path, depth+1, state); err != nil {
					return err
				}
				continue
			}
			fieldType, ok := fields[keyName]
			fieldPath := appendConfigYAMLPath(path, keyName)
			if !ok {
				location := ""
				if keyNode.Line > 0 {
					location = fmt.Sprintf(" at line %d", keyNode.Line)
				}
				return fmt.Errorf("unknown configuration field %q%s", strings.Join(fieldPath, "."), location)
			}
			if err := validateConfigYAMLNodeState(valueNode, fieldType, fieldPath, depth+1, state); err != nil {
				return err
			}
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyName := strings.TrimSpace(node.Content[i].Value)
			if keyName == "<<" {
				if err := validateConfigYAMLMergeState(node.Content[i+1], target, path, depth+1, state); err != nil {
					return err
				}
				continue
			}
			if err := validateConfigYAMLNodeState(node.Content[i+1], target.Elem(), appendConfigYAMLPath(path, keyName), depth+1, state); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return nil
		}
		for i, child := range node.Content {
			itemPath := appendConfigYAMLPath(path, fmt.Sprintf("[%d]", i))
			if err := validateConfigYAMLNodeState(child, target.Elem(), itemPath, depth+1, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfigYAMLMergeState(node *yaml.Node, target reflect.Type, path []string, depth int, state *configYAMLValidationState) (retErr error) {
	if node == nil {
		return nil
	}
	if depth > maxConfigYAMLValidationDepth {
		return configYAMLDepthError(path)
	}
	if state == nil {
		return fmt.Errorf("configuration YAML validation state is nil")
	}
	state.visited++
	if state.visited > maxConfigYAMLValidationNodes {
		return fmt.Errorf("configuration YAML validation exceeds %d node visits at %s", maxConfigYAMLValidationNodes, configYAMLDisplayPath(path))
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	key := configYAMLValidationKey{node: node, target: target, merge: true}
	if _, ok := state.validated[key]; ok {
		return nil
	}
	if _, ok := state.active[key]; ok {
		return fmt.Errorf("configuration YAML alias cycle detected at %s", configYAMLDisplayPath(path))
	}
	state.active[key] = struct{}{}
	defer func() {
		delete(state.active, key)
		if retErr == nil {
			state.validated[key] = struct{}{}
		}
	}()

	switch node.Kind {
	case yaml.AliasNode:
		if node.Alias == nil {
			return fmt.Errorf("configuration YAML alias is missing its target at %s", configYAMLDisplayPath(path))
		}
		return validateConfigYAMLMergeState(node.Alias, target, path, depth+1, state)
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateConfigYAMLMergeState(child, target, path, depth+1, state); err != nil {
				return err
			}
		}
		return nil
	default:
		return validateConfigYAMLNodeState(node, target, path, depth+1, state)
	}
}

func configYAMLDepthError(path []string) error {
	return fmt.Errorf("configuration YAML nesting exceeds %d levels at %s", maxConfigYAMLValidationDepth, configYAMLDisplayPath(path))
}

func configYAMLDisplayPath(path []string) string {
	displayPath := strings.Join(path, ".")
	if displayPath == "" {
		return "<root>"
	}
	return displayPath
}

func configYAMLFields(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("yaml")
		name, options, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if strings.Contains(","+options+",", ",inline,") {
			inlineType := field.Type
			for inlineType.Kind() == reflect.Pointer {
				inlineType = inlineType.Elem()
			}
			if inlineType.Kind() == reflect.Struct {
				for inlineName, inlineFieldType := range configYAMLFields(inlineType) {
					fields[inlineName] = inlineFieldType
				}
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		fields[name] = field.Type
	}
	return fields
}

func appendConfigYAMLPath(path []string, component string) []string {
	next := make([]string, 0, len(path)+1)
	next = append(next, path...)
	if strings.HasPrefix(component, "[") && len(next) > 0 {
		next[len(next)-1] += component
		return next
	}
	next = append(next, component)
	return next
}
