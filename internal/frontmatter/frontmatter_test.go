package frontmatter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantProps []Property
		wantBody  string
		wantHas   bool
	}{
		{
			name: "parses ordered properties and returns the body",
			src:  "---\ntitle: NATS\ntags:\n  - system-design\n  - component\naliases:\n  - NATS JetStream\n---\n# NATS\n\nbody",
			wantProps: []Property{
				{Key: "title", Values: []string{"NATS"}},
				{Key: "tags", Values: []string{"system-design", "component"}},
				{Key: "aliases", Values: []string{"NATS JetStream"}},
			},
			wantBody: "# NATS\n\nbody",
			wantHas:  true,
		},
		{
			name:     "no frontmatter leaves source untouched",
			src:      "# Note\n\ntext",
			wantBody: "# Note\n\ntext",
		},
		{
			name:     "mid-note --- is not frontmatter",
			src:      "# Title\n\ntext\n\n---\n\nmore",
			wantBody: "# Title\n\ntext\n\n---\n\nmore",
		},
		{
			name:     "malformed yaml drops the block",
			src:      "---\n: : bad\n---\nbody",
			wantBody: "body",
			wantHas:  true,
		},
		{
			name:     "a four-dash line does not close the block",
			src:      "---\ntitle: x\n----\nbody",
			wantBody: "---\ntitle: x\n----\nbody",
		},
		{
			name:     "a horizontal rule does not close the block",
			src:      "---\ntitle: x\n----------\nbody",
			wantBody: "---\ntitle: x\n----------\nbody",
		},
		{
			name:     "a ---junk line does not close the block",
			src:      "---\ntitle: x\n---junk\nbody",
			wantBody: "---\ntitle: x\n---junk\nbody",
		},
		{
			name:     "an empty block is frontmatter with no properties",
			src:      "---\n---\nbody",
			wantBody: "body",
			wantHas:  true,
		},
		{
			name:      "the closing fence may end the note",
			src:       "---\ntitle: x\n---",
			wantProps: []Property{{Key: "title", Values: []string{"x"}}},
			wantHas:   true,
		},
		{
			name:      "trailing spaces on the fences are tolerated",
			src:       "--- \ntitle: x\n---\t\nbody",
			wantProps: []Property{{Key: "title", Values: []string{"x"}}},
			wantBody:  "body",
			wantHas:   true,
		},
		{
			name:      "CRLF block",
			src:       "---\r\ntitle: x\r\ntags:\r\n  - a\r\n---\r\nbody",
			wantProps: []Property{{Key: "title", Values: []string{"x"}}, {Key: "tags", Values: []string{"a"}}},
			wantBody:  "body",
			wantHas:   true,
		},
		{
			name:     "CRLF empty block",
			src:      "---\r\n---\r\nbody",
			wantBody: "body",
			wantHas:  true,
		},
		{
			name:     "CRLF four-dash line does not close the block",
			src:      "---\r\ntitle: x\r\n----\r\nbody",
			wantBody: "---\r\ntitle: x\r\n----\r\nbody",
		},
		{
			name:      "a rule after the block stays in the body",
			src:       "---\ntitle: x\n---\nbody\n\n----\nmore",
			wantProps: []Property{{Key: "title", Values: []string{"x"}}},
			wantBody:  "body\n\n----\nmore",
			wantHas:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props, body := Split(tt.src)
			require.Equal(t, tt.wantProps, props)
			require.Equal(t, tt.wantBody, body)
			require.Equal(t, tt.wantHas, Has(tt.src))
			// ReplaceBody agrees with Split on where the block ends.
			require.Equal(t, tt.src, ReplaceBody(tt.src, body))
		})
	}
}

func TestReplace(t *testing.T) {
	body := "# NATS\n\nLightweight messaging.\n"

	t.Run("rewrites frontmatter and keeps the body verbatim", func(t *testing.T) {
		src := "---\ntitle: old\n---\n" + body
		out := Replace(src, []Property{
			{Key: "title", Values: []string{"NATS"}},
			{Key: "tags", Values: []string{"a", "b"}},
		})
		props, gotBody := Split(out)
		require.Equal(t, []Property{
			{Key: "title", Values: []string{"NATS"}},
			{Key: "tags", Values: []string{"a", "b"}},
		}, props)
		require.Equal(t, body, gotBody)
	})

	t.Run("adds frontmatter to a note that had none", func(t *testing.T) {
		out := Replace(body, []Property{{Key: "tags", Values: []string{"x"}}})
		props, gotBody := Split(out)
		require.Equal(t, []Property{{Key: "tags", Values: []string{"x"}}}, props)
		require.Equal(t, body, gotBody)
	})

	t.Run("empty properties removes the block", func(t *testing.T) {
		src := "---\ntitle: x\n---\n" + body
		require.Equal(t, body, Replace(src, nil))
	})

	t.Run("a single value stays scalar, multiple become a sequence", func(t *testing.T) {
		out := Replace(body, []Property{
			{Key: "one", Values: []string{"solo"}},
			{Key: "many", Values: []string{"a", "b"}},
		})
		require.Contains(t, out, "one: solo")
		require.Contains(t, out, "many:\n    - a\n    - b")
	})
}

func TestReplaceBody(t *testing.T) {
	t.Run("swaps the body, keeps the frontmatter block verbatim", func(t *testing.T) {
		src := "---\ntitle: x\ntags:\n  - a\n---\n# Old\n\nold body"
		out := ReplaceBody(src, "# New\n\nnew body")
		props, body := Split(out)
		require.Equal(t, []Property{
			{Key: "title", Values: []string{"x"}},
			{Key: "tags", Values: []string{"a"}},
		}, props)
		require.Equal(t, "# New\n\nnew body", body)
	})

	t.Run("no frontmatter: the body is the whole note", func(t *testing.T) {
		require.Equal(t, "# New", ReplaceBody("# Old\n\ntext", "# New"))
	})
}
