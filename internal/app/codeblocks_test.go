package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractCodeBlocks(t *testing.T) {
	src := "# Note\n\n" +
		"```bash\necho one\n```\n\n" +
		"some prose\n\n" +
		"```\nplain block\n```\n\n" +
		"```python\nprint(2)\n```\n"

	blocks := extractCodeBlocks(src)
	require.Len(t, blocks, 3)

	// Indexes are positional over all fenced blocks, matching the rendered panels.
	require.Equal(t, 0, blocks[0].Index)
	require.Equal(t, "bash", blocks[0].Lang)
	require.Equal(t, "echo one\n", blocks[0].Code)

	// A plain (no-language) block is still indexed, with an empty language.
	require.Equal(t, 1, blocks[1].Index)
	require.Equal(t, "", blocks[1].Lang)

	require.Equal(t, 2, blocks[2].Index)
	require.Equal(t, "python", blocks[2].Lang)
	require.Equal(t, "print(2)\n", blocks[2].Code)
}

func TestExtractCodeBlocksLowercasesAndTrimsInfo(t *testing.T) {
	src := "```Bash title=demo\necho hi\n```\n"
	blocks := extractCodeBlocks(src)
	require.Len(t, blocks, 1)
	// Only the first info word is the language, lowercased.
	require.Equal(t, "bash", blocks[0].Lang)
}

func TestExtractCodeBlocksNone(t *testing.T) {
	require.Empty(t, extractCodeBlocks("# Just a heading\n\nprose only"))
}

func TestExtractCodeBlocksKernelOverride(t *testing.T) {
	src := "```go {kernel=go} {version=1.21}\nfmt.Println(1)\n```\n\n" +
		"```go\nfmt.Println(2)\n```\n"
	blocks := extractCodeBlocks(src)
	require.Len(t, blocks, 2)

	// The first block carries the {kernel=…}{version=…} override; the second none.
	require.Equal(t, "go", blocks[0].Lang)
	require.Equal(t, "go", blocks[0].Kernel)
	require.Equal(t, "1.21", blocks[0].Version)
	require.Equal(t, "go", blocks[1].Lang)
	require.Empty(t, blocks[1].Kernel)
	require.Empty(t, blocks[1].Version)
}
