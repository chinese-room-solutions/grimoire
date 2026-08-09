package ui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSourceLabel(t *testing.T) {
	tests := []struct {
		hit  Hit
		want string
	}{
		{Hit{Path: "a.md", Heading: "Intro"}, "a.md › Intro"},
		{Hit{Path: "b.md"}, "b.md"},
		// A search spans vaults, so a hit that knows its vault always names it.
		{Hit{Path: "a.md", Heading: "Intro", Vault: "/home/u/notes"}, "notes › a.md › Intro"},
		{Hit{Path: "b.md", Vault: "/home/u/work/"}, "work › b.md"},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, sourceLabel(tc.hit))
	}
}

func TestSnippet(t *testing.T) {
	tests := []struct {
		name, in string
		want     string // exact result, when short enough to spell out
	}{
		{name: "short text is trimmed, not cut", in: "  short  ", want: "short"},
		{name: "long ascii", in: strings.Repeat("x", 500)},
		{name: "long multibyte", in: strings.Repeat("€", 500)},
		// A cut landing mid-character: 239 ascii runes then a 3-byte rune spanning
		// the old 240-byte boundary.
		{name: "cut lands inside a rune", in: strings.Repeat("a", 239) + strings.Repeat("€", 20)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := snippet(tc.in)
			require.True(t, utf8.ValidString(out), "a snippet never splits a rune")
			if tc.want != "" {
				require.Equal(t, tc.want, out)
				return
			}
			require.True(t, strings.HasSuffix(out, "…"))
			require.LessOrEqual(t, utf8.RuneCountInString(out), 241) // 240 runes + the ellipsis.
		})
	}
}

func TestRenderNoteBody(t *testing.T) {
	nr := kernelStub() // make go/bash blocks runnable so run buttons render.
	tests := []struct {
		name, in string
		contains []string
	}{
		{"heading", "# Title", []string{"<h1", "Title</h1>"}},
		{"emphasis", "a **bold** word", []string{"<strong>bold</strong>"}},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |", []string{"<table>", "<td>1</td>"}},
		{"raw html is dropped", "<script>alert(1)</script>", nil},
		{"wikilink", "see [[My Note]]", []string{`href="` + NoteLinkScheme + `My%20Note"`, ">My Note</a>"}},
		{"wikilink with alias", "see [[My Note|the note]]", []string{`href="` + NoteLinkScheme + `My%20Note"`, ">the note</a>"}},
		{
			"callout with title",
			"> [!note] Visa\n> Blue card required.",
			[]string{`class="g-callout g-callout-note"`, `name="pencil"`, ">Visa</span>", "Blue card required."},
		},
		{
			"callout without title defaults to the type",
			"> [!warning]\n> Heads up.",
			[]string{"g-callout-warning", `name="exclamation-triangle"`, ">Warning</span>"},
		},
		{
			"unknown callout type gets a default icon",
			"> [!whatever] X\n> body",
			[]string{"g-callout-whatever", `name="info-circle"`},
		},
		{"plain blockquote is not a callout", "> just a quote", []string{"<blockquote>"}},
		{
			"relative image src points at the vault-file route",
			"![alt](attachments/pic.png)",
			[]string{`src="` + VaultFileRoute + `attachments/pic.png"`},
		},
		{"absolute image src is left alone", "![](https://x.com/a.png)", []string{`src="https://x.com/a.png"`}},
		{
			"fenced code with a language is syntax-highlighted and tagged for running",
			"```go\nfunc main() {}\n```",
			[]string{`class="chroma" data-lang="go"`, `<span class="kd">func</span>`, `class="g-code-run"`},
		},
		{
			"language aliases resolve (py)",
			"```py\nimport os\n```",
			[]string{`data-lang="py"`, `<span class="kn">import</span>`},
		},
		{
			"fenced code without a language is plain and not runnable",
			"```\nplain text\n```",
			[]string{`<pre class="chroma">plain text`},
		},
		{
			"plain code block is wrapped with a copy button only",
			"```\nx\n```",
			[]string{`<div class="g-code-block"><pre class="chroma">`, `class="g-code-copy"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderNoteBody(nr, tc.in, "")
			for _, want := range tc.contains {
				require.Contains(t, out, want)
			}
			// goldmark's safe default never emits raw HTML, so a note can't
			// inject markup into the preview.
			require.NotContains(t, out, "<script>")
		})
	}
}

func TestRenderCalloutsNestedQuotes(t *testing.T) {
	nr := kernelStub()
	tests := []struct {
		name, in string
		contains []string
		absent   []string
	}{
		{
			// The inner </blockquote> used to end the callout, leaving the outer quote
			// unclosed and a stray close tag after the box.
			name: "a callout keeps a nested quote and everything after it",
			in:   "> [!note] Title\n> body\n>\n> > nested quote\n>\n> tail\n",
			contains: []string{
				`class="g-callout g-callout-note"`,
				"<blockquote>\n<p>nested quote</p>\n</blockquote>",
				"<p>tail</p>",
			},
		},
		{
			name:     "a callout nested in a callout is a callout too",
			in:       "> [!warning] Outer\n> body\n>\n> > [!tip] Inner\n> > hint\n",
			contains: []string{"g-callout-warning", "g-callout-tip", ">Inner</span>"},
			absent:   []string{"<blockquote>"},
		},
		{
			name:     "a plain quote holding a nested quote is left alone",
			in:       "> plain\n>\n> > nested\n",
			contains: []string{"<blockquote>\n<p>plain</p>\n<blockquote>\n<p>nested</p>\n</blockquote>\n</blockquote>"},
			absent:   []string{"g-callout"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderNoteBody(nr, tc.in, "")
			for _, want := range tc.contains {
				require.Contains(t, out, want)
			}
			for _, no := range tc.absent {
				require.NotContains(t, out, no)
			}
			require.Equal(t, strings.Count(out, "<blockquote>"), strings.Count(out, "</blockquote>"),
				"every quote tag is balanced")
		})
	}
}

func TestWikilinksSkipCode(t *testing.T) {
	// [[…]] is a link in prose and code in code: a bash test, a C++ attribute. The
	// rewrite happens before parsing, so it must skip every code range or the block
	// shows — and runs, and hashes — text the author never wrote.
	// Prose links either side of a fenced block whose code looks like a wikilink.
	const mixed = "see [[A]]\n\n```bash\n[[ -f x ]]\n```\n\nand [[B]]\n"
	nr := kernelStub()
	tests := []struct {
		name, in string
		contains []string
		absent   []string
	}{
		{
			"prose wikilink is rewritten",
			"see [[My Note]]",
			[]string{`href="` + NoteLinkScheme + `My%20Note"`, ">My Note</a>"},
			nil,
		},
		{
			"wikilink in a fenced block is left alone",
			"```bash\nif [[ -f x ]]; then echo hi; fi\n```",
			[]string{"[[", "]]"},
			[]string{NoteLinkScheme},
		},
		{
			"wikilink in an inline code span is left alone",
			"attribute `[[nodiscard]]` applies",
			[]string{"<code>[[nodiscard]]</code>"},
			[]string{NoteLinkScheme},
		},
		{
			"wikilink in an indented block is left alone",
			"para\n\n    [[nodiscard]] int f();\n",
			[]string{"[[nodiscard]] int f();"},
			[]string{NoteLinkScheme},
		},
		{
			// Highlighting splits the code into spans, so the block is checked by what
			// it must not become: a link. Both prose links are still rewritten.
			"prose around a fenced block is still rewritten",
			mixed,
			[]string{`href="` + NoteLinkScheme + `A"`, `href="` + NoteLinkScheme + `B"`, "-f x"},
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderNoteBody(nr, tc.in, "")
			for _, want := range tc.contains {
				require.Contains(t, out, want)
			}
			for _, no := range tc.absent {
				require.NotContains(t, out, no)
			}
		})
	}
	require.Equal(t, 2, strings.Count(RenderNoteBody(nr, mixed, ""), NoteLinkScheme),
		"only the two prose wikilinks become links")
}

func TestBlockSourcesKeepRawCode(t *testing.T) {
	// The re-hydration key is the block's raw source: the loader must be asked for
	// the code as written, not a wikilink-rewritten copy of it, or the key never
	// matches what the app stored.
	nr := kernelStub()
	var asked []string
	nr.RunResult = func(_, code string) (RunResult, bool) {
		asked = append(asked, code)
		return RunResult{}, false
	}

	RenderNoteBody(nr, "```bash\nif [[ -f x ]]; then echo hi; fi\n```\n", "n.md")
	require.Equal(t, []string{"if [[ -f x ]]; then echo hi; fi\n"}, asked)
}

func TestWrapCodeBlocks(t *testing.T) {
	tests := []struct {
		name, in string
		contains []string
		absent   []string
	}{
		{
			"a single pre is wrapped and gets a button",
			"<p>x</p><pre><code>a</code></pre>",
			[]string{`<div class="g-code-block"><pre><code>a</code></pre><sl-icon-button class="g-code-copy"`, "<p>x</p>"},
			nil,
		},
		{
			"each pre is wrapped independently",
			"<pre>a</pre><pre>b</pre>",
			[]string{`<div class="g-code-block"><pre>a</pre>`, `<div class="g-code-block"><pre>b</pre>`},
			nil,
		},
		{
			"html without a pre is untouched",
			"<p>no code here</p>",
			[]string{"<p>no code here</p>"},
			[]string{"g-code-block"},
		},
		{
			"a language block gets run + run-above buttons, an output panel and a block id",
			`<pre class="chroma" data-lang="bash">echo hi</pre>`,
			[]string{
				`<div class="g-code-block" data-g-block="0">`,
				`class="g-code-run"`,
				`class="g-code-run-above"`,
				`id="g-code-output-0"`,
			},
			nil,
		},
		{
			"a plain block gets no run button",
			"<pre>plain</pre>",
			[]string{`<div class="g-code-block"><pre>plain</pre>`},
			[]string{"g-code-run", "g-code-output"},
		},
		{
			"block ids increment across blocks",
			`<pre class="chroma" data-lang="bash">a</pre><pre class="chroma" data-lang="sh">b</pre>`,
			[]string{`data-g-block="0"`, `data-g-block="1"`, `id="g-code-output-0"`, `id="g-code-output-1"`},
			nil,
		},
		{
			// No kernel claims cobol, so the block can't run — but it names a
			// language, so it carries the slot the install CTA fills.
			"an unrunnable language block carries an install slot",
			`<pre class="chroma" data-lang="cobol">DISPLAY 1</pre>`,
			[]string{`data-g-block="0"`, `class="g-code-install" data-g-lang="cobol"`},
			[]string{"g-code-run", "g-code-output"},
		},
		{
			"a plain block carries no install slot",
			"<pre>plain</pre>",
			[]string{`<div class="g-code-block"><pre>plain</pre>`},
			[]string{"g-code-install"},
		},
	}
	// A block is only runnable when a kernel claims its language, which the
	// resolver reports. Treat the languages used above as runnable.
	nr := kernelStub()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := wrapCodeBlocks(nr, tc.in, nil)
			for _, want := range tc.contains {
				require.Contains(t, out, want)
			}
			for _, no := range tc.absent {
				require.NotContains(t, out, no)
			}
		})
	}
}

func TestIndentedBlockDoesNotShiftFencedIndexes(t *testing.T) {
	nr := kernelStub()
	// The app indexes fenced blocks only (extractCodeBlocks), so an indented block
	// before a fence must not consume an id — otherwise a run targets the wrong
	// panel and the stored result never re-attaches.
	out := RenderNoteBody(nr, "    indented\n\n```bash\necho hi\n```\n", "")

	require.Contains(t, out, `<div class="g-code-block"><pre><code>indented`, "the indented block is wrapped without a block id")
	require.Contains(t, out, `data-g-block="0"`, "the fenced block is block 0")
	require.Contains(t, out, `id="g-code-output-0"`)
	require.NotContains(t, out, `data-g-block="1"`)
	require.Equal(t, 1, strings.Count(out, "g-code-run\""), "only the fenced block is runnable")
}

// kernelStub is a renderer whose kernel lookup treats common code languages as
// runnable (returning a label/version) and everything else as not.
func kernelStub() NoteRenderer {
	runnable := map[string]bool{"go": true, "golang": true, "bash": true, "sh": true, "shell": true}
	return NoteRenderer{Kernel: func(lang, family, version string) (string, string, bool) {
		if !runnable[lang] {
			return "", "", false
		}
		if family != "" {
			return family, version, true
		}
		return lang, version, true
	}}
}

func TestWrapCodeBlocksKernelOverride(t *testing.T) {
	nr := kernelStub()
	in := `<pre class="chroma" data-lang="go">a</pre><pre class="chroma" data-lang="go">b</pre>`
	// Only the first block has an override; the second must not get the attributes.
	out := wrapCodeBlocks(nr, in, []blockFence{{Family: "go", Version: "1.21"}, {}})
	require.Contains(t, out, `data-g-block="0" data-g-kernel="go" data-g-version="1.21"`)
	require.Contains(t, out, `data-g-block="1">`)
	require.NotContains(t, out, `data-g-block="1" data-g-kernel`)
}

func TestBlockKernels(t *testing.T) {
	src := "```go {kernel=go} {version=1.21}\nx\n```\n\n```go\ny\n```\n\n```bash\nz\n```\n"
	require.Equal(t, []blockFence{{Family: "go", Version: "1.21"}, {}, {}}, blockKernels(src))
}

func TestBlockSources(t *testing.T) {
	// blockSources returns each fenced block's raw source in document order, the
	// same text the app hashes for the run-result key — so re-hydration looks a
	// block's stored output up under the right key. The fence's trailing newline is
	// kept (matching the app's extractCodeBlocks); BlockHash normalizes it.
	src := "# N\n\n```go\nfmt.Println(1)\n```\n\n```bash\necho hi\n```\n"
	require.Equal(t, []string{"fmt.Println(1)\n", "echo hi\n"}, blockSources(src))
}

// withRunResults returns nr with a run-result lookup that hits for blocks whose
// source is in want and misses otherwise.
func withRunResults(nr NoteRenderer, want map[string]RunResult) NoteRenderer {
	nr.RunResult = func(_, code string) (RunResult, bool) {
		r, ok := want[code]
		return r, ok
	}
	return nr
}

func TestRenderNoteBodyRehydratesStoredOutput(t *testing.T) {
	nr := kernelStub()
	// Block one has a stored result; block two doesn't.
	nr = withRunResults(nr, map[string]RunResult{
		"fmt.Println(1)\n": {
			Items:    []RunItem{{MIME: MIMEText, Data: "1\n"}},
			ExitCode: 0,
			DurMS:    7,
			Kernel:   "Go",
			RanAt:    time.Unix(1_700_000_000, 0),
		},
	})

	src := "```go\nfmt.Println(1)\n```\n\n```go\nfmt.Println(2)\n```\n"
	out := RenderNoteBody(nr, src, "n.md")

	// Block 0 re-hydrates: a visible panel carrying the saved output, exit status,
	// duration, and kernel — not the empty hidden placeholder.
	require.Contains(t, out, `id="g-code-output-0"`)
	require.Contains(t, out, "1\n", "saved output is rendered into the panel")
	require.Contains(t, out, "exit 0", "the status footer is rebuilt from the stored result")
	require.Contains(t, out, "7 ms")
	require.Contains(t, out, "Go", "the kernel that ran it is shown")
	require.NotContains(t, out, `id="g-code-output-0" hidden`, "a hydrated panel isn't hidden")

	// Block 1 had no stored result: its panel is the empty, hidden placeholder.
	require.Contains(t, out, `id="g-code-output-1" hidden`)
}

func TestRenderNoteBodyWithoutPathDoesNotRehydrate(t *testing.T) {
	nr := kernelStub()
	// Even with a loader installed, an empty note path means no re-hydration (the
	// caller didn't know which note it was), so every panel is the empty placeholder.
	nr = withRunResults(nr, map[string]RunResult{
		"fmt.Println(1)\n": {Items: []RunItem{{MIME: MIMEText, Data: "1\n"}}},
	})
	out := RenderNoteBody(nr, "```go\nfmt.Println(1)\n```\n", "")
	require.Contains(t, out, `id="g-code-output-0" hidden`)
	require.NotContains(t, out, ">1\n<")
}

func TestRunResultPanelRendersItemsByMIME(t *testing.T) {
	// The panel renders each output item by its MIME type: text inline, an image as
	// a data-URI <img>. This is the seam a future plotting kernel slots into with no
	// schema change.
	res := RunResult{
		Items: []RunItem{
			{MIME: MIMEText, Data: "plotting…\n"},
			{MIME: MIMEPNG, Data: "QUJD"}, // base64 of "ABC".
		},
		ExitCode: 0,
		RanAt:    time.Unix(1_700_000_000, 0),
	}
	out := runResultPanelHTML("0", res)
	require.Contains(t, out, "plotting…", "text items render inline")
	require.Contains(t, out, `src="data:`+MIMEPNG+`;base64,QUJD"`, "image items render as a data URI")
}

func TestTrashBrowserRendersNoteRows(t *testing.T) {
	var buf strings.Builder
	items := []TrashItem{
		{TrashID: "id1", OriginalPath: "Folder/Gone.md", TrashPath: ".trash/id1/Folder/Gone.md", Name: "Gone", DeletedAt: time.Unix(1_700_000_000, 0)},
	}
	require.NoError(t, TrashBrowser(items).Render(context.Background(), &buf))
	out := buf.String()
	require.Contains(t, out, "Gone")
	// Real note rows so the file view's preview/select/nav work on them.
	require.Contains(t, out, "g-tree-note", "trash rows are note rows")
	require.Contains(t, out, `data-note=".trash/id1/Folder/Gone.md"`, "data-note is the in-trash path (read in place)")
	require.Contains(t, out, `data-trash-id="id1"`, "the trash id keys the restore/delete actions")
	require.Contains(t, out, `data-name="Gone"`, "name for the trash filter")
	// Per-row controls post with the id inlined — no client signal to race.
	require.Contains(t, out, "api/trash/restore-ui?id=id1")
	require.Contains(t, out, "api/trash/delete-ui?id=id1")

	// An empty trash shows the placeholder, not rows.
	buf.Reset()
	require.NoError(t, TrashBrowser(nil).Render(context.Background(), &buf))
	require.Contains(t, buf.String(), "Trash is empty")
}

func TestWrapCodeBlocksKernelBadge(t *testing.T) {
	// The resolver receives (lang, family, version) and returns the label + version
	// shown on the block. ok=false omits the badge (an unrunnable language).
	nr := NoteRenderer{Kernel: func(lang, family, version string) (string, string, bool) {
		if lang != "go" {
			return "", "", false
		}
		if family == "yaegi" {
			return "Go (yaegi) 0.16.1", "0.16.1", true
		}
		return "Go 1.26.3", "1.26.3", true
	}}

	in := `<pre class="chroma" data-lang="go">a</pre>` +
		`<pre class="chroma" data-lang="go">b</pre>` +
		`<pre class="chroma" data-lang="text">c</pre>`
	out := wrapCodeBlocks(nr, in, []blockFence{{}, {Family: "yaegi"}, {}})

	// The badge text is the label (which already carries the version); no tooltip.
	require.Contains(t, out, `<span class="g-code-kernel">Go 1.26.3</span>`)
	require.Contains(t, out, `<span class="g-code-kernel">Go (yaegi) 0.16.1</span>`)
	// The text block isn't runnable, so it gets no badge.
	require.Equal(t, 2, strings.Count(out, "g-code-kernel"))
}

func TestWrapCodeBlocksNoBadgeWithoutResolver(t *testing.T) {
	out := wrapCodeBlocks(NoteRenderer{}, `<pre class="chroma" data-lang="go">a</pre>`, nil)
	require.NotContains(t, out, "g-code-kernel")
}

func TestPropIcon(t *testing.T) {
	tests := []struct {
		key, want string
	}{
		{"tags", "tags"},
		{"aliases", "signpost-split"},
		{"title", "type"},
		{"date", "calendar"},
		{"created", "calendar"},
		{"updated", "calendar"},
		{"modified", "calendar"},
		{"Tags", "tags"}, // case-insensitive
		{"unknown", "text-left"},
		{"", "text-left"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			require.Equal(t, tc.want, propIcon(tc.key))
		})
	}
}

func TestResolveImageSrcs(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"relative", `<img src="a.png">`, `<img src="` + VaultFileRoute + `a.png">`},
		{"relative with subdir", `<img src="attachments/a.png">`, `<img src="` + VaultFileRoute + `attachments/a.png">`},
		{"http left alone", `<img src="http://x/a.png">`, `<img src="http://x/a.png">`},
		{"https left alone", `<img src="https://x/a.png">`, `<img src="https://x/a.png">`},
		{"data uri left alone", `<img src="data:image/png;base64,AAAA">`, `<img src="data:image/png;base64,AAAA">`},
		{"rooted path left alone", `<img src="/a.png">`, `<img src="/a.png">`},
		{"already routed left alone", `<img src="` + VaultFileRoute + `a.png">`, `<img src="` + VaultFileRoute + `a.png">`},
		{"empty src left alone", `<img src="">`, `<img src="">`},
		{"non-img untouched", `<a src="a.png">`, `<a src="a.png">`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveImageSrcs(tc.in))
		})
	}
}

func TestRenderNote(t *testing.T) {
	t.Run("splits frontmatter from body", func(t *testing.T) {
		props, raw, html := RenderNote(NoteRenderer{}, "---\ntitle: Hello\ntags: [a, b]\n---\n# Body\n", "")
		require.NotEmpty(t, props)
		var keys []string
		for _, p := range props {
			keys = append(keys, p.Key)
		}
		require.Contains(t, keys, "title")
		require.Contains(t, keys, "tags")
		require.Contains(t, raw, "# Body")
		require.NotContains(t, raw, "title: Hello") // frontmatter stripped from the body
		require.Contains(t, html, "<h1")
	})

	t.Run("no frontmatter yields no props and the full body", func(t *testing.T) {
		props, raw, html := RenderNote(NoteRenderer{}, "# Just a body\n", "")
		require.Empty(t, props)
		require.Contains(t, raw, "# Just a body")
		require.Contains(t, html, "Just a body</h1>")
	})
}

func TestRenderFullPageThemePicker(t *testing.T) {
	// The picker lists every registered theme; without LoadThemes only the two
	// built-ins are present, which is enough to assert normalization and checking.
	tests := []struct {
		name        string
		theme       string
		wantChecked string // the theme name whose menu item must be checked
	}{
		{"built-in dark checks Carbon", "dark", "dark"},
		{"built-in light checks Cream", "light", "light"},
		{"unknown theme normalizes to dark", "does-not-exist", "dark"},
		{"empty theme normalizes to dark", "", "dark"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page := RenderFullPage(tc.theme, "info", State{})

			// Both built-in themes appear as picker items with their labels.
			require.Contains(t, page, `value="dark"`)
			require.Contains(t, page, `value="light"`)
			require.Contains(t, page, "Carbon")
			require.Contains(t, page, "Cream")

			// The normalized theme's item is the checked one.
			checked := `<sl-menu-item type="checkbox" value="` + tc.wantChecked + `" checked>`
			require.Contains(t, page, checked)

			// The appTheme signal is seeded with the normalized name, never the raw
			// unknown value.
			require.Contains(t, page, `data-bind="appTheme" value="`+tc.wantChecked+`"`)
			if tc.theme != "" && tc.theme != tc.wantChecked {
				require.NotContains(t, page, `value="`+tc.theme+`"`)
			}
		})
	}
}

func TestRenderPageVersion(t *testing.T) {
	// The rendered value marker; it never appears in the page's CSS, so its
	// absence is a real "no version line".
	const marker = `<span class="g-version-value">`

	tests := []struct {
		name    string
		version string
		want    string // rendered value, or "" when the line must be dropped
	}{
		{"unstamped build shows dev verbatim", "dev", "dev"},
		{"git describe sha", "9e33402", "9e33402"},
		{"dirty tree keeps the suffix", "9e33402-dirty", "9e33402-dirty"},
		{"markup in the version is escaped", `<b>x`, "&lt;b&gt;x"},
		{"empty version drops the line", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page := RenderPage("dark", "info", State{Version: tc.version})

			require.Contains(t, page, `id="app-grimoire"`) // the page renders either way
			if tc.want == "" {
				require.NotContains(t, page, marker)
				return
			}
			require.Contains(t, page, marker+tc.want+`</span>`)
			// It closes the gear menu: after the connection section, the last item
			// of the settings dropdown.
			require.Less(t, strings.Index(page, "MASS connection"), strings.Index(page, marker))
		})
	}
}

// TestRenderFullPageExtensionsWindow pins the Extensions dialog's paging and
// sizing contract, which is split between the page CSS and the page script:
// each tab is two content-sized grid rows that only split the panel evenly when
// both overflow (so a short section hands its unused height to the other and
// the dialog shrinks back to content), each section scrolls on its own, and the
// script windows the rows behind a "Show More" row.
func TestRenderFullPageExtensionsWindow(t *testing.T) {
	page := RenderFullPage("dark", "info", State{})

	require.Contains(t, page, ".g-ext-panel{display:grid;grid-template-rows:minmax(0,auto) minmax(0,auto)")
	require.Contains(t, page, "align-content:start")
	require.Contains(t, page, "max-height:max-content")
	require.Contains(t, page, ".g-ext-section{display:flex;flex-direction:column;min-height:0")
	require.Contains(t, page, ".g-ext-rows{display:flex;flex-direction:column;gap:0.25rem;min-height:0;overflow-y:auto}")
	require.Contains(t, page, ".g-ext-more{text-align:center}")

	require.Contains(t, page, "var EXT_PAGE = 5;")
	require.Contains(t, page, `"Show More"`)
	require.Contains(t, page, `icon.setAttribute("name", "chevron-down")`)
}

// TestExtensionListStructure pins the markup the dialog's windowing script
// walks: one .g-ext-section per list, each holding its rows in a single
// .g-ext-rows box (the element that scrolls) so the script can window a
// section's rows and append its "Show More" row beside them.
func TestExtensionListStructure(t *testing.T) {
	var buf strings.Builder
	err := ExtensionList(ExtensionSections{
		Kind:      ExtKindTheme,
		Installed: []ExtensionItem{{ID: "dark", Label: "Carbon", Meta: "dark", Locked: "built-in"}},
		Available: []ExtensionItem{{ID: "neon", Package: "theme-neon", Label: "Neon", Meta: "0.1.0"}},
	}).Render(context.Background(), &buf)
	require.NoError(t, err)
	out := buf.String()

	require.Equal(t, 2, strings.Count(out, `class="g-ext-section"`))
	require.Equal(t, 2, strings.Count(out, `class="g-ext-rows"`))
	require.Equal(t, 2, strings.Count(out, `data-g-ext-filter=`))
	// Every row carries the lowercased haystack the filter matches against.
	require.Contains(t, out, `data-g-ext-filter="carbon dark dark"`)
	require.Contains(t, out, `data-g-ext-filter="neon neon 0.1.0"`)
	// Installed theme rows are the activation surface: the click target attr,
	// the activatable class, and the (initially hidden) active check.
	require.Contains(t, out, `data-g-activate="dark"`)
	require.Contains(t, out, "g-ext-activatable")
	require.Contains(t, out, `class="g-ext-check"`)
	// Available rows are not activatable.
	require.NotContains(t, out, `data-g-activate="neon"`)
}

// TestExtensionRowRemoveShapes pins Remove to the same compact icon button on
// both tabs.
func TestExtensionRowRemoveShapes(t *testing.T) {
	render := func(kind string, it ExtensionItem) string {
		var buf strings.Builder
		require.NoError(t, ExtensionList(ExtensionSections{
			Kind:      kind,
			Installed: []ExtensionItem{it},
		}).Render(context.Background(), &buf))
		return buf.String()
	}

	theme := render(ExtKindTheme, ExtensionItem{ID: "neon", Label: "Neon"})
	require.Contains(t, theme, "circle")
	require.Contains(t, theme, `variant="danger"`)
	require.Contains(t, theme, `content="Remove"`)

	kernel := render(ExtKindKernel, ExtensionItem{ID: "go", Label: "Go", Version: "1.0.0"})
	require.Contains(t, kernel, "circle")
	require.Contains(t, kernel, `variant="danger"`)
	require.Contains(t, kernel, `content="Remove"`)
	require.Contains(t, kernel, `data-g-version="1.0.0"`)
	require.NotContains(t, kernel, "data-g-activate")
}
