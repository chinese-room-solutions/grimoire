package frontmatter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	t.Run("parses ordered properties and returns the body", func(t *testing.T) {
		src := "---\ntitle: NATS\ntags:\n  - system-design\n  - component\naliases:\n  - NATS JetStream\n---\n# NATS\n\nbody"
		props, body := Split(src)
		require.Equal(t, []Property{
			{Key: "title", Values: []string{"NATS"}},
			{Key: "tags", Values: []string{"system-design", "component"}},
			{Key: "aliases", Values: []string{"NATS JetStream"}},
		}, props)
		require.Equal(t, "# NATS\n\nbody", body)
	})

	t.Run("no frontmatter leaves source untouched", func(t *testing.T) {
		props, body := Split("# Note\n\ntext")
		require.Nil(t, props)
		require.Equal(t, "# Note\n\ntext", body)
	})

	t.Run("mid-note --- is not frontmatter", func(t *testing.T) {
		src := "# Title\n\ntext\n\n---\n\nmore"
		props, body := Split(src)
		require.Nil(t, props)
		require.Equal(t, src, body)
	})

	t.Run("malformed yaml drops the block", func(t *testing.T) {
		props, body := Split("---\n: : bad\n---\nbody")
		require.Nil(t, props)
		require.Equal(t, "body", body)
	})
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
