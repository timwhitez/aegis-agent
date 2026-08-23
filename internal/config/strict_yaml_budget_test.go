package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func configYAMLScalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}
func configYAMLMappingNode(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: items}
}
func configYAMLAliasNode(target *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.AliasNode, Alias: target}
}

func TestRepeatedMergeAliasesAreMemoized(t *testing.T) {
	leaf := configYAMLMappingNode(configYAMLScalarNode("sandbox"), configYAMLScalarNode("bwrap"))
	current := leaf
	for i := 0; i < 14; i++ {
		current = configYAMLMappingNode(
			configYAMLScalarNode("<<"),
			&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{configYAMLAliasNode(current), configYAMLAliasNode(current)}},
		)
	}
	if err := validateConfigYAMLNode(current, reflect.TypeOf(ShellConfig{}), []string{"runtime", "shell"}); err != nil {
		t.Fatalf("valid repeated aliases were rejected: %v", err)
	}
}

func TestAliasCycleFailsClosed(t *testing.T) {
	cycle := &yaml.Node{Kind: yaml.AliasNode}
	cycle.Alias = cycle
	err := validateConfigYAMLNode(cycle, reflect.TypeOf(ShellConfig{}), []string{"runtime", "shell"})
	if err == nil || !strings.Contains(err.Error(), "alias cycle") {
		t.Fatalf("alias cycle did not fail closed: %v", err)
	}
}

func TestUniqueNodeExpansionIsBounded(t *testing.T) {
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Content: make([]*yaml.Node, maxConfigYAMLValidationNodes+1)}
	for i := range sequence.Content {
		sequence.Content[i] = configYAMLScalarNode("value")
	}
	err := validateConfigYAMLNode(sequence, reflect.TypeOf([]string{}), []string{"values"})
	if err == nil || !strings.Contains(err.Error(), "node visits") {
		t.Fatalf("oversized unique YAML graph was not bounded: %v", err)
	}
}

func TestUnknownFieldStillReportsFullPath(t *testing.T) {
	node := configYAMLMappingNode(configYAMLScalarNode("sanbox"), configYAMLScalarNode("bwrap"))
	err := validateConfigYAMLNode(node, reflect.TypeOf(ShellConfig{}), []string{"runtime", "shell"})
	if err == nil || !strings.Contains(err.Error(), "runtime.shell.sanbox") {
		t.Fatalf("unknown field path was lost: %v", err)
	}
}
