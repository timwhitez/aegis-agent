package config

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlPathSeparator joins the structural path of a mapping key. It is a control
// character so a path cannot collide with a real key.
const yamlPathSeparator = "\x1f"

// blankLineLayout records where an existing YAML file separated blocks with
// blank lines, keyed by the structural path of the following mapping key.
//
// gopkg.in/yaml.v3 preserves comments, key order and indentation when a parsed
// Node tree is re-encoded, but it never emits blank lines. Without this layout a
// single-field settings save would silently strip every blank separator from the
// operator's file.
type blankLineLayout struct {
	// blanks maps a structural mapping path to the number of blank lines that
	// separated it from the previous block in the source file.
	blanks map[string]int
}

// captureBlankLineLayout records the blank-line separators of the document as it
// exists in raw. It must be called before the document is mutated because it
// relies on the line numbers of the freshly parsed tree.
func captureBlankLineLayout(doc *yaml.Node, raw []byte) blankLineLayout {
	layout := blankLineLayout{blanks: map[string]int{}}
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return layout
	}
	lines := splitYAMLLines(raw)
	walkYAMLMappingPaths(doc.Content[0], nil, func(path string, key *yaml.Node) {
		count := blankLinesBeforeKey(lines, key.Line)
		if count > 0 {
			if _, exists := layout.blanks[path]; !exists {
				layout.blanks[path] = count
			}
		}
	})
	return layout
}

// restore re-inserts the recorded blank-line separators into encoded, which must
// be the re-encoded form of the same document. Paths that no longer exist in the
// encoded document (deleted blocks) are skipped, and new blocks keep whatever
// spacing the encoder produced.
func (l blankLineLayout) restore(encoded []byte) []byte {
	if len(l.blanks) == 0 {
		return encoded
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(encoded, &doc); err != nil {
		return encoded
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return encoded
	}
	lines := splitYAMLLines(encoded)
	insertBefore := map[int]int{}
	walkYAMLMappingPaths(doc.Content[0], nil, func(path string, key *yaml.Node) {
		want, ok := l.blanks[path]
		if !ok || want <= 0 {
			return
		}
		index := key.Line - 1 // 0-based index of the key line
		// Head comment lines sit directly above the key and belong to the same
		// block, so the separator goes above them rather than between the
		// comment and its key.
		for index > 0 && strings.HasPrefix(strings.TrimSpace(lines[index-1]), "#") {
			index--
		}
		if index <= 0 || index >= len(lines) {
			return
		}
		insertBefore[index] = want
	})
	if len(insertBefore) == 0 {
		return encoded
	}

	var out strings.Builder
	out.Grow(len(encoded) + len(insertBefore))
	for i, line := range lines {
		if want := insertBefore[i]; want > 0 && !(i > 0 && strings.TrimSpace(lines[i-1]) == "") {
			for j := 0; j < want; j++ {
				out.WriteString("\n")
			}
		}
		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	return []byte(out.String())
}

// walkYAMLMappingPaths visits every mapping key in document order, reporting the
// structural path of that key. Sequence entries contribute their index so paths
// stay unique.
func walkYAMLMappingPaths(node *yaml.Node, path []string, visit func(path string, key *yaml.Node)) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			walkYAMLMappingPaths(child, path, visit)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode, valueNode := node.Content[i], node.Content[i+1]
			child := appendYAMLPath(path, keyNode.Value)
			visit(joinYAMLPath(child), keyNode)
			walkYAMLMappingPaths(valueNode, child, visit)
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			walkYAMLMappingPaths(item, appendYAMLPath(path, strconv.Itoa(i)), visit)
		}
	}
}

func appendYAMLPath(path []string, part string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = part
	return out
}

func joinYAMLPath(path []string) string {
	return strings.Join(path, yamlPathSeparator)
}

// blankLinesBeforeKey counts the blank lines that separate the block starting at
// line (1-based) from the previous block. Comment lines directly above the key
// are part of that block and are skipped first. A block at the very start of the
// file has no previous block, so its preceding blank lines are not a separator.
func blankLinesBeforeKey(lines []string, line int) int {
	index := line - 2 // 0-based index of the line directly above
	for index >= 0 && strings.HasPrefix(strings.TrimSpace(lines[index]), "#") {
		index--
	}
	count := 0
	for index >= 0 && strings.TrimSpace(lines[index]) == "" {
		count++
		index--
	}
	if index < 0 {
		// Ran past the start of the file: these blanks led the document rather
		// than separating two blocks.
		return 0
	}
	return count
}

func splitYAMLLines(raw []byte) []string {
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}
