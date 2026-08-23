package config

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxConfigYAMLValidationDepth = 64

// configYAMLAlias avoids recursively calling Config.UnmarshalYAML when the
// validated node is decoded into the existing layered configuration.
type configYAMLAlias Config

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
	return validateConfigYAMLNodeDepth(node, target, path, 0)
}

func validateConfigYAMLNodeDepth(node *yaml.Node, target reflect.Type, path []string, depth int) error {
	if node == nil {
		return nil
	}
	if depth > maxConfigYAMLValidationDepth {
		displayPath := strings.Join(path, ".")
		if displayPath == "" {
			displayPath = "<root>"
		}
		return fmt.Errorf("configuration YAML nesting exceeds %d levels at %s", maxConfigYAMLValidationDepth, displayPath)
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		return validateConfigYAMLNodeDepth(node.Content[0], target, path, depth+1)
	}
	if node.Kind == yaml.AliasNode {
		return validateConfigYAMLNodeDepth(node.Alias, target, path, depth+1)
	}

	for target.Kind() == reflect.Pointer {
		target = target.Elem()
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
			key := strings.TrimSpace(keyNode.Value)
			if key == "<<" {
				if err := validateConfigYAMLMergeDepth(valueNode, target, path, depth+1); err != nil {
					return err
				}
				continue
			}
			fieldType, ok := fields[key]
			fieldPath := appendConfigYAMLPath(path, key)
			if !ok {
				location := ""
				if keyNode.Line > 0 {
					location = fmt.Sprintf(" at line %d", keyNode.Line)
				}
				return fmt.Errorf("unknown configuration field %q%s", strings.Join(fieldPath, "."), location)
			}
			if err := validateConfigYAMLNodeDepth(valueNode, fieldType, fieldPath, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.TrimSpace(node.Content[i].Value)
			if key == "<<" {
				if err := validateConfigYAMLMergeDepth(node.Content[i+1], target, path, depth+1); err != nil {
					return err
				}
				continue
			}
			if err := validateConfigYAMLNodeDepth(node.Content[i+1], target.Elem(), appendConfigYAMLPath(path, key), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return nil
		}
		for i, child := range node.Content {
			itemPath := appendConfigYAMLPath(path, fmt.Sprintf("[%d]", i))
			if err := validateConfigYAMLNodeDepth(child, target.Elem(), itemPath, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfigYAMLMergeDepth(node *yaml.Node, target reflect.Type, path []string, depth int) error {
	if node == nil {
		return nil
	}
	if depth > maxConfigYAMLValidationDepth {
		return validateConfigYAMLNodeDepth(node, target, path, depth)
	}
	if node.Kind == yaml.AliasNode {
		return validateConfigYAMLNodeDepth(node.Alias, target, path, depth+1)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := validateConfigYAMLMergeDepth(child, target, path, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return validateConfigYAMLNodeDepth(node, target, path, depth+1)
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
