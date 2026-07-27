// Package frontmatter reads and writes the YAML metadata block at the top of a
// Markdown note (the Obsidian/Jekyll "--- ... ---" convention). Properties are
// kept in file order. It is the single place that understands the frontmatter
// format, shared by the renderer (display) and the app (editing on disk).
package frontmatter

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Property is one frontmatter key and its value(s), preserved in file order. A
// scalar property has a single value; a list property (e.g. tags) has several.
// JSON tags match the wire format the editor posts back.
type Property struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// fence matches a leading YAML frontmatter block: --- ... --- at the very start
// of a note. Anchored at the start so a mid-note "---" rule isn't mistaken for it.
var fence = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)

// Has reports whether source begins with a YAML frontmatter block. Unlike
// checking Split's props, it is true even for an empty or malformed block —
// the fence is there, so the content carries its own frontmatter.
func Has(source string) bool {
	return fence.MatchString(source)
}

// Split separates a leading YAML frontmatter block from the note body. With no
// frontmatter, props is nil and body is the original source.
func Split(source string) (props []Property, body string) {
	m := fence.FindStringSubmatch(source)
	if m == nil {
		return nil, source
	}
	return parse(m[1]), source[len(m[0]):]
}

// parse reads a YAML mapping into ordered properties. A scalar yields one value;
// a sequence yields several. Malformed YAML or a non-mapping document yields nil.
func parse(yamlText string) []Property {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	var props []Property
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		p := Property{Key: key.Value}
		switch val.Kind {
		case yaml.SequenceNode:
			for _, item := range val.Content {
				if item.Value != "" {
					p.Values = append(p.Values, item.Value)
				}
			}
		default:
			if val.Value != "" {
				p.Values = []string{val.Value}
			}
		}
		props = append(props, p)
	}
	return props
}

// Replace rewrites source so its frontmatter is the given properties, leaving the
// body byte-for-byte unchanged. Empty props removes the block entirely. A single
// value renders as a scalar; multiple values render as a YAML sequence — matching
// how Obsidian stores properties.
func Replace(source string, props []Property) string {
	_, body := Split(source)
	block := serialize(props)
	if block == "" {
		return body
	}
	return "---\n" + block + "---\n" + body
}

// ReplaceBody rewrites source so its Markdown body is newBody, leaving the YAML
// frontmatter block (if any) untouched. Used when editing a note's body without
// disturbing its properties.
func ReplaceBody(source, newBody string) string {
	m := fence.FindStringSubmatch(source)
	if m == nil {
		return newBody // no frontmatter: the body is the whole note.
	}
	return m[0] + newBody
}

// serialize renders ordered properties to a YAML mapping body (no fences). Keys
// keep their given order. Returns "" for no properties.
func serialize(props []Property) string {
	if len(props) == 0 {
		return ""
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range props {
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: p.Key}
		var val *yaml.Node
		switch len(p.Values) {
		case 0:
			val = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
		case 1:
			val = &yaml.Node{Kind: yaml.ScalarNode, Value: p.Values[0]}
		default:
			val = &yaml.Node{Kind: yaml.SequenceNode}
			for _, v := range p.Values {
				val.Content = append(val.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
			}
		}
		root.Content = append(root.Content, key, val)
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "" // a malformed tree drops the block rather than corrupting the note.
	}
	s := string(out)
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
