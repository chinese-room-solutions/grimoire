package ui

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/fence"
	"github.com/chinese-room-solutions/grimoire/internal/frontmatter"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// md renders GitHub-flavored Markdown. Raw HTML in notes is left escaped
// (goldmark's safe default), so previewing a note can't inject markup. Fenced
// code blocks annotated with a language are syntax-highlighted server-side by
// Chroma, emitting CSS classes (see codeHighlightCSS) so the colours follow the
// app theme rather than being baked into inline styles. The wrapper renderer
// records the fence's language as data-lang on the <pre> so wrapCodeBlocks can
// offer a Run button for languages a kernel can execute.
var md = goldmark.New(goldmark.WithExtensions(
	extension.GFM,
	highlighting.NewHighlighting(
		highlighting.WithFormatOptions(
			chromahtml.WithClasses(true),
			chromahtml.PreventSurroundingPre(true), // we emit the <pre> ourselves, with data-lang.
		),
		highlighting.WithWrapperRenderer(codeWrapper),
	),
))

// codeWrapper emits the <pre class="chroma"> around highlighted code, tagging it
// with the fence's language (data-lang) when one was given. The language is lost
// from the rendered HTML otherwise, and the run button needs it.
func codeWrapper(w util.BufWriter, c highlighting.CodeBlockContext, entering bool) {
	if !entering {
		_, _ = w.WriteString("</pre>")
		return
	}
	if lang, ok := c.Language(); ok && len(lang) > 0 {
		_, _ = w.WriteString(`<pre class="chroma" data-lang="` + html.EscapeString(string(lang)) + `">`)
		return
	}
	_, _ = w.WriteString(`<pre class="chroma">`)
}

// NoteLinkScheme prefixes the href of an in-vault wikilink. The webview's click
// handler intercepts anchors with this scheme and opens the target note instead
// of navigating.
const NoteLinkScheme = "grimoire-note:"

// wikilink matches Obsidian-style [[Target]] and [[Target|Alias]] references.
var wikilink = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`)

// Property is a frontmatter key and its value(s) for the properties panel.
type Property = frontmatter.Property

// RenderNote splits a note's frontmatter from its body and returns the parsed
// properties (for the panel), the raw Markdown body (for the editor), and the
// body rendered to HTML (for reading). notePath, when set, lets each runnable
// block re-hydrate its last run from the cache (pass "" when the path is unknown).
func RenderNote(source, notePath string) (props []Property, rawBody, bodyHTML string) {
	props, body := frontmatter.Split(source)
	return props, body, RenderNoteBody(body, notePath)
}

// propIcon picks a Shoelace icon for a frontmatter property by its key, mirroring
// Obsidian's property-type glyphs. Unknown keys get a generic text icon.
func propIcon(key string) string {
	switch strings.ToLower(key) {
	case "tags":
		return "tags"
	case "aliases":
		return "signpost-split"
	case "title":
		return "type"
	case "date", "created", "updated", "modified":
		return "calendar"
	default:
		return "text-left"
	}
}

// RenderMarkdown converts a note's Markdown source to HTML for preview. Code
// blocks render empty output panels (no run history). Use RenderNoteBody when the
// note's path is known so cached run output can be re-hydrated.
func RenderMarkdown(source string) string {
	return renderBody(source, "")
}

// RenderNoteBody is RenderMarkdown for a note at a known vault path, so each
// runnable block whose last run is cached re-hydrates its output panel.
func RenderNoteBody(source, notePath string) string {
	return renderBody(source, notePath)
}

// renderBody renders a note's Markdown to preview HTML, turning [[wikilinks]]
// into in-vault links first, and wraps code blocks (run buttons, kernel badges,
// and — when notePath is set — cached run output).
func renderBody(source, notePath string) string {
	source = wikilink.ReplaceAllStringFunc(source, func(m string) string {
		g := wikilink.FindStringSubmatch(m)
		target, alias := strings.TrimSpace(g[1]), strings.TrimSpace(g[2])
		if alias == "" {
			alias = target
		}
		// Emit a normal Markdown link. The URL is percent-encoded so spaces in
		// note names don't break parsing; the click handler decodes it.
		return "[" + alias + "](" + NoteLinkScheme + url.PathEscape(target) + ")"
	})

	// Per-block data recovered from the source (chroma drops it from the rendered
	// HTML): the {kernel=FAMILY}{version=VER} override and the block's raw source.
	// Overrides are only needed when one is actually present; sources only when a
	// note path lets us look up cached output.
	var overrides []blockFence
	var sources []string
	if strings.Contains(source, "{kernel=") || strings.Contains(source, "{version=") {
		overrides = blockKernels(source)
	}
	if notePath != "" && RunResultLoader != nil {
		sources = blockSources(source)
	}

	var buf bytes.Buffer
	if err := md.Convert([]byte(source), &buf); err != nil {
		// Fall back to the raw text rather than failing the preview.
		return "<pre>" + strings.ReplaceAll(source, "<", "&lt;") + "</pre>"
	}
	return wrapCodeBlocksWithRuns(resolveImageSrcs(renderCallouts(buf.String())), overrides, sources, notePath)
}

// blockFence is a block's per-block kernel override, recovered from its fence
// info string: the kernel family and version, either "" when unset.
type blockFence struct {
	Family  string
	Version string
}

// blockKernels returns each fenced block's {kernel=FAMILY}{version=VER} override
// in document order, so wrapCodeBlocks can re-attach it by block index. It shares
// goldmark's block parsing with the rest of the renderer.
func blockKernels(source string) []blockFence {
	var out []blockFence
	walkFencedBlocks(source, func(info, _ string) {
		out = append(out, blockFence{Family: fence.Kernel(info), Version: fence.Version(info)})
	})
	return out
}

// blockSources returns each fenced block's raw source (the same text the kernel
// runs, and the app hashes for the run-result key) in document order, so a
// reopened block's panel can be re-hydrated from its last run. The reconstruction
// matches the app's extractCodeBlocks, so the two hash the same bytes.
func blockSources(source string) []string {
	var sources []string
	walkFencedBlocks(source, func(_, code string) {
		sources = append(sources, code)
	})
	return sources
}

// walkFencedBlocks visits every fenced code block in document order, calling fn
// with its raw info string and its reconstructed source. Shared by blockKernels
// and blockSources so both index blocks identically.
func walkFencedBlocks(source string, fn func(info, code string)) {
	src := []byte(source)
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		b, ok := n.(*ast.FencedCodeBlock)
		if !ok {
			return ast.WalkContinue, nil
		}
		info := ""
		if b.Info != nil {
			info = string(b.Info.Segment.Value(src))
		}
		var code []byte
		for i := 0; i < b.Lines().Len(); i++ {
			seg := b.Lines().At(i)
			code = append(code, seg.Value(src)...)
		}
		fn(info, string(code))
		return ast.WalkContinue, nil
	})
}

// preBlock matches a rendered code block (<pre>…</pre>).
var preBlock = regexp.MustCompile(`(?s)<pre[ >].*?</pre>`)

// preLang pulls the language out of a block's <pre data-lang="…"> tag (the only
// data-lang in a rendered block).
var preLang = regexp.MustCompile(`<pre[^>]* data-lang="([^"]*)"`)

// wrapCodeBlocks wraps each rendered code block in a relative box and pins a copy
// button to it; blocks tagged with a language a kernel could run also get a Run
// button and an (initially hidden) output panel. The buttons are plain
// server-rendered markup; the webview delegates their clicks (see initCopy /
// initRun). The <pre> scrolls horizontally, so the buttons can't live inside it —
// the non-scrolling wrapper holds them instead. Each block gets a positional id
// so run output can be streamed into its own panel.
// KernelResolver, when set, returns the label and version of the kernel that will
// run a block of the given language with the given per-block family/version
// override. ok is false when the language isn't runnable. The app wires this at
// startup so the render layer can show which kernel a block uses without
// depending on the kernel registry directly.
var KernelResolver func(lang, family, version string) (label, version2 string, ok bool)

// RunResultLoader, when set, returns a block's last run for the note at notePath,
// looked up by the block's source (the app hashes it to the stored key). ok is
// false when the block was never run or its code changed since. The app wires
// this at startup so a reopened note re-hydrates each block's saved output
// without the render layer depending on the run-result store.
var RunResultLoader func(notePath, code string) (RunResult, bool)

// RunResult is a block's persisted last run, mirrored from the app/runs layer so
// the render layer can paint it without importing the store. Items are output
// pieces in stream order; today every item is text, but the MIME tag lets a
// future plotting kernel's image/svg/html items render the same way.
type RunResult struct {
	Items    []RunItem
	ExitCode int
	DurMS    int
	Kernel   string
	RanAt    time.Time
}

// RunItem is one output piece: its MIME type and data.
type RunItem struct {
	MIME string
	Data string
}

// MIME types a run item can carry. Only text is produced today; the image/html
// arms render once a kernel emits them.
const (
	MIMEText = "text/plain"
	MIMEPNG  = "image/png"
	MIMESVG  = "image/svg+xml"
	MIMEHTML = "text/html"
)

func wrapCodeBlocks(rendered string, overrides []blockFence) string {
	return wrapCodeBlocksWithRuns(rendered, overrides, nil, "")
}

// wrapCodeBlocksWithRuns is wrapCodeBlocks with each block's raw source and the
// note path, so a runnable block whose last run is cached re-hydrates its panel
// (saved output + footer + time) instead of starting empty. sources is indexed
// the same as overrides (document order); an empty notePath or nil loader leaves
// every panel empty, as before.
func wrapCodeBlocksWithRuns(rendered string, overrides []blockFence, sources []string, notePath string) string {
	i := -1
	return preBlock.ReplaceAllStringFunc(rendered, func(block string) string {
		i++
		copyBtn := `<sl-icon-button class="g-code-copy" name="copy" label="Copy code"></sl-icon-button>`
		lang := ""
		if m := preLang.FindStringSubmatch(block); m != nil {
			lang = m[1]
		}

		// A per-block kernel override, recovered from the source by position.
		var ov blockFence
		if i < len(overrides) {
			ov = overrides[i]
		}
		// Resolve the kernel that would run this block. A block is runnable only
		// when a kernel actually claims its language — otherwise it gets just a
		// copy button (no Run, no badge), like a plain no-language block.
		var label string
		runnable := false
		if lang != "" && KernelResolver != nil {
			label, _, runnable = KernelResolver(lang, ov.Family, ov.Version)
		}
		if !runnable {
			return `<div class="g-code-block">` + block + copyBtn + `</div>`
		}

		id := strconv.Itoa(i)
		// The override is carried to the run path as data attributes so a run
		// reproduces the same resolution the badge shows.
		kernelAttr := ""
		if ov.Family != "" {
			kernelAttr += ` data-g-kernel="` + html.EscapeString(ov.Family) + `"`
		}
		if ov.Version != "" {
			kernelAttr += ` data-g-version="` + html.EscapeString(ov.Version) + `"`
		}
		// The kernel that will run this block, shown as a badge so the choice is
		// visible before running. The label already carries the version.
		badge := `<span class="g-code-kernel">` + html.EscapeString(label) + `</span>`
		// Run-above (⏩) runs every runnable block from the top through this one;
		// Run (▶) runs just this block. Both reuse the same per-block panel.
		runAboveBtn := `<sl-icon-button class="g-code-run-above" name="fast-forward-fill" label="Run all above and this"></sl-icon-button>`
		runBtn := `<sl-icon-button class="g-code-run" name="play-fill" label="Run"></sl-icon-button>`

		// Re-hydrate the panel from this block's last run, keyed by its source, so
		// reopening the note shows the previous output. A miss (never run, or edited
		// since) leaves the panel empty and hidden.
		panel := `<div class="g-code-output" id="g-code-output-` + id + `" hidden></div>`
		if RunResultLoader != nil && notePath != "" && i < len(sources) {
			if res, ok := RunResultLoader(notePath, sources[i]); ok {
				panel = runResultPanelHTML(id, res)
			}
		}
		return `<div class="g-code-block" data-g-block="` + id + `"` + kernelAttr + `>` + block + badge + runAboveBtn + runBtn + copyBtn + panel + `</div>`
	})
}

// runResultPanelHTML renders a block's persisted run into the output-panel markup
// for embedding in a reopened note, reusing the RunResultPanel templ component so
// the hydrated panel matches what a live run leaves behind.
func runResultPanelHTML(blockID string, res RunResult) string {
	var buf bytes.Buffer
	if err := RunResultPanel(blockID, res).Render(context.Background(), &buf); err != nil {
		// A render failure shouldn't break the note; fall back to an empty panel.
		return `<div class="g-code-output" id="g-code-output-` + blockID + `" hidden></div>`
	}
	return buf.String()
}

// VaultFileRoute is the URL prefix under which vault files (a note's referenced
// images) are served, so a relative ![](attachments/x.png) resolves in the
// preview. The web layer mounts a handler at this path.
const VaultFileRoute = "vault-file/"

// imageSrc matches an <img src="…"> attribute in rendered HTML.
var imageSrc = regexp.MustCompile(`(<img[^>]*\bsrc=")([^"]*)(")`)

// resolveImageSrcs rewrites a rendered note's relative image sources to point at
// the vault-file route, so an ![](attachments/x.png) (a vault-relative path)
// loads from the vault. Absolute URLs (http(s), data:, the note scheme, a leading
// slash) are left as-is.
func resolveImageSrcs(html string) string {
	return imageSrc.ReplaceAllStringFunc(html, func(m string) string {
		g := imageSrc.FindStringSubmatch(m)
		src := g[2]
		if src == "" || isAbsoluteURL(src) {
			return m
		}
		return g[1] + VaultFileRoute + src + g[3]
	})
}

// isAbsoluteURL reports whether a src needs no vault rewrite: an external/data URL
// or an already-rooted path.
func isAbsoluteURL(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") ||
		strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "/") ||
		strings.HasPrefix(src, VaultFileRoute)
}

// calloutBlockquote matches a blockquote whose first paragraph opens with an
// Obsidian callout marker "[!type]" (optionally followed by a title on the same
// line), capturing the type, the title, and the remaining body of that paragraph.
// goldmark renders a callout as a plain <blockquote><p>[!type] Title\nbody…</p>;
// we rewrite it into a styled callout box.
var calloutBlockquote = regexp.MustCompile(`(?is)<blockquote>\s*<p>\s*\[!([a-z]+)\]([^\n<]*)\n?(.*?)</p>(.*?)</blockquote>`)

// calloutIcons maps a callout type to a Shoelace icon, with a default for
// unrecognized types. Aliases mirror Obsidian's common set.
var calloutIcons = map[string]string{
	"note": "pencil", "info": "info-circle", "tip": "lightbulb", "hint": "lightbulb",
	"important": "exclamation-circle", "warning": "exclamation-triangle",
	"caution": "exclamation-triangle", "danger": "fire", "error": "x-octagon",
	"success": "check-circle", "check": "check-circle", "done": "check-circle",
	"question": "question-circle", "faq": "question-circle", "example": "list-ul",
	"quote": "chat-quote", "abstract": "card-text", "summary": "card-text", "bug": "bug",
}

// renderCallouts rewrites goldmark's blockquote rendering of Obsidian callouts
// ("> [!note] Title") into styled callout boxes: a header with an icon and title,
// then the body. The title defaults to the capitalized type when omitted. A
// blockquote that isn't a callout is left untouched.
func renderCallouts(html string) string {
	return calloutBlockquote.ReplaceAllStringFunc(html, func(m string) string {
		g := calloutBlockquote.FindStringSubmatch(m)
		typ := strings.ToLower(g[1])
		title := strings.TrimSpace(g[2])
		if title == "" {
			title = strings.ToUpper(typ[:1]) + typ[1:]
		}
		icon := calloutIcons[typ]
		if icon == "" {
			icon = "info-circle"
		}
		// The first paragraph's remaining text (g[3]) plus any following blocks
		// (g[4]) form the body. Wrap the first-paragraph remainder back in a <p> so
		// it keeps paragraph spacing alongside the rest.
		body := strings.TrimSpace(g[3])
		if body != "" {
			body = "<p>" + body + "</p>"
		}
		body += g[4]
		return `<div class="g-callout g-callout-` + typ + `">` +
			`<div class="g-callout-head"><sl-icon name="` + icon + `"></sl-icon>` +
			`<span class="g-callout-title">` + title + `</span></div>` +
			`<div class="g-callout-body">` + body + `</div></div>`
	})
}

//go:embed grimoire.js
var grimoireJS string

//go:embed icon.png
var IconPNG []byte

// State seeds the page with the current vault, models, and index size.
type State struct {
	// HasVault reports whether a vault is bound. When false the page renders the
	// empty state (a vault picker) instead of the workspace, and the per-vault
	// fields below are unset.
	HasVault     bool
	Vault        string
	EmbedModel   string
	ConvertModel string
	// ConvertMaxPixels is the resolved pixel budget for rendered PDF pages
	// (the caller substitutes the default for an unset config), shown in the
	// Vault menu as megapixels.
	ConvertMaxPixels int
	// ConvertPageTimeoutMin is the resolved per-page conversion budget in whole
	// minutes (the caller substitutes the default for an unset config).
	ConvertPageTimeoutMin int
	ChunkCount            int
	IndexConcurrency      int
	// GraphOpen reports that the persisted focused workspace tab is the graph, so
	// the page paints the graph overlay open (the g-graph-open class) on first
	// render instead of the empty prompt — no flash, no JS-timing race on restore.
	GraphOpen bool
	// TrashMode is the soft-delete policy for the Settings control: "all" (trash
	// every delete), "agents" (trash only AI-agent deletes), or "off"
	// (permanent for everyone).
	TrashMode string
	// Recents are the vaults Grimoire knows about, shown as quick-pick rows in the
	// empty state (ignored when HasVault is true).
	Recents []VaultRef
	// Conn seeds the settings menu's MASS connection fields (endpoint, whether a
	// token is set, custom CA path). Global — shown in both states.
	Conn ConnState
	// Version is the running build's version, shown at the foot of the settings
	// menu. Rendered verbatim (a "dev" build says dev); empty drops the line.
	Version string
}

// VaultRef is one vault in the empty-state picker: its display name (the folder's
// base name) and absolute path (what open-vault binds).
type VaultRef struct {
	Name string
	Path string
}

// megapixels formats a pixel count for the Vault menu's resolution input,
// with only the digits the value needs (2500000 → "2.5").
func megapixels(px int) string {
	return strconv.FormatFloat(float64(px)/1e6, 'f', -1, 64)
}

// ConnState seeds the MASS connection controls in the settings menu. The token
// is never carried as a value — only HasToken, so the field shows it is set
// without exposing it.
type ConnState struct {
	Endpoint string
	HasToken bool
	CACert   string
}

// initialSignals is the data-signals blob for the app root. Booleans/numbers stay
// real types here (a hidden input would stringify them, and "false" is truthy in a
// data-attr:loading expression). The Vault-tab string signals are seeded with their
// saved values so the model selects and vault input show the current choice.
func initialSignals(st State) string {
	sig := map[string]any{
		"gBusy":       false,
		"gSearchBusy": false,
		// When the restored focused tab is the graph, mark content present so the
		// empty search prompt doesn't paint behind the graph overlay on first
		// render (the overlay itself is shown via the g-graph-open class).
		"gHasContent":   st.GraphOpen,
		"gPreviewOpen":  false,
		"gPreviewTitle": "",
		"gGraphK":       6,
		"gGraphMinSim":  0.5,
		// Search tuning, surfaced as the session view's top-panel sliders.
		"gSearchK":      10,
		"gSearchMinSim": 0.5,
		"gModel":        st.EmbedModel,
		"gConvertModel": st.ConvertModel,
		// gRunKernel/gRunVersion carry a block's per-run {kernel=FAMILY}{version=VER}
		// override to the run path.
		"gRunKernel":  "",
		"gRunVersion": "",
		// Trash setting: seeded so the control reflects the persisted mode.
		"gTrashMode": st.TrashMode,
	}
	b, err := json.Marshal(sig)
	if err != nil {
		return "{}" // unreachable for this fixed shape; fail safe to no signals.
	}
	return string(b)
}

// sourceLabel formats a hit's provenance line: its note path, and the heading
// the match fell under when one is known.
func sourceLabel(h Hit) string {
	if h.Heading != "" {
		return h.Path + " › " + h.Heading
	}
	return h.Path
}

// snippet trims a chunk to a short preview for the search results list.
func snippet(s string) string {
	const max = 240
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}

func script() string {
	return "<script>" + grimoireJS + "</script>"
}

// codeHighlightCSS is the Chroma syntax-highlighting stylesheet for fenced code
// blocks. The dark theme's rules apply by default; the light theme's rules
// override them under html.sl-theme-light, mirroring the app's theme toggle.
// Both are scoped under .markdown-body so they only touch rendered notes.
var codeHighlightCSS = chromaCSS("dracula", "") +
	chromaCSS("github", "html.sl-theme-light ")

// chromaCSS renders style's token-colour rules as class-based CSS, scoped under
// the markdown body and prefixed by gate (e.g. a theme selector). Chroma emits
// one rule per line, so prefixing the scope per line is reliable. The style's
// own background rules (.bg and the bare .chroma wrapper) are dropped so the
// <pre> keeps the app's panel background; only token colours are kept.
func chromaCSS(style, gate string) string {
	s := styles.Get(style) // Get falls back to a built-in default if absent.
	f := chromahtml.New(chromahtml.WithClasses(true))
	var buf bytes.Buffer
	if err := f.WriteCSS(&buf, s); err != nil {
		return "" // Missing highlight CSS is a cosmetic loss, not a render failure.
	}
	scope := gate + "#app-grimoire .markdown-body "
	var out strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		line = strings.TrimSpace(stripCSSComment(line))
		// Drop blanks and the background rules so the panel background shows
		// through; keep only token-colour selectors (.chroma .<tok>).
		if line == "" || strings.HasPrefix(line, ".bg ") || strings.HasPrefix(line, ".chroma {") {
			continue
		}
		out.WriteString(scope)
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// stripCSSComment removes a leading /* … */ comment that Chroma prefixes to each
// rule, returning the rule alone.
func stripCSSComment(line string) string {
	if rest, ok := strings.CutPrefix(line, "/*"); ok {
		if _, after, found := strings.Cut(rest, "*/"); found {
			return after
		}
	}
	return line
}

// styleBlock is the page's scoped CSS. Sidebar/panel styling follows MASS's
// shell (--mass-* vars, panel borders).
var styleBlock = `<style>
#app-grimoire{--g-tab-top-h:1.85rem;--g-head-h:2.5rem}
#app-grimoire .g-muted{color:var(--mass-text-muted)}
#app-grimoire .g-label{font-size:0.7rem;color:var(--mass-text-muted);display:block;margin-bottom:0.3rem}
#app-grimoire .g-status{font-size:0.72rem;color:var(--mass-text-muted);margin-top:0.35rem;word-break:break-word}
#app-grimoire .g-vault-current{font-size:0.8rem;color:var(--mass-text);background:var(--mass-bg-base);border:1px solid var(--mass-border);border-radius:0.3rem;padding:0.35rem 0.5rem;word-break:break-all;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
/* Vault dropdown panel — mirrors the SDK gear menu's shell (uikit.SettingsShell)
   so the two bottom-bar menus read as one family. Wider than the gear's 220px to
   fit the model selects, and height-capped so the taller content scrolls. */
#app-grimoire .g-menu-panel{display:flex;flex-direction:column;gap:0.75rem;padding:0.85rem;width:248px;max-height:min(72vh,32rem);overflow-y:auto;background:var(--mass-bg-panel);border:1px solid var(--mass-border);border-radius:0.5rem}
/* Trash-mode toggle: a pill track with a dot per stop and a round thumb that
   slides to the active stop (like the editor's "Effort" multi-state toggle),
   coloured by policy — blue for everyone, yellow for agents-only, the usual
   disabled grey for off. The thumb is positioned from --g-trash-i (the active
   stop's index, set by initTrashMode) and coloured from [data-mode]; the dots are
   just visual stops, the whole row is clickable. */
#app-grimoire .g-trash-field{display:flex;flex-direction:column;gap:var(--sl-spacing-3x-small,0.125rem)}
#app-grimoire .g-trash-title{font-size:var(--sl-input-label-font-size-small,0.875rem);color:var(--mass-text-muted)}
#app-grimoire .g-trash-row{display:flex;align-items:center;gap:0.5rem}
#app-grimoire .g-trash-state{font-size:0.8rem;color:var(--mass-text-muted);white-space:nowrap}
/* The track: the thumb diameter plus a tiny --g-pad on every side drives the
   height, so the padding is real vertical room and the thumb sits centred. The
   same --g-pad is the only horizontal edge gap, so an end-position thumb sits
   flush to the rim like a default 2-state switch (no dead space on the ends). */
#app-grimoire .g-trash-mode{--g-thumb:1.15rem;--g-pad:1px;box-sizing:border-box;position:relative;display:flex;align-items:center;width:4.25rem;height:calc(var(--g-thumb) + 2*var(--g-pad));padding:var(--g-pad);background:var(--mass-bg-active);border-radius:999px;cursor:pointer}
/* The thumb's x is a concrete pixel offset initTrashMode measures from the active
   stop's centre, so it lands dead-on at any width without fragile calc(). */
#app-grimoire .g-trash-thumb{position:absolute;top:var(--g-pad);left:0;width:var(--g-thumb);height:var(--g-thumb);border-radius:50%;background:var(--mass-text-muted);box-shadow:0 1px 2px rgba(0,0,0,0.35);transition:transform 0.18s ease,background 0.18s ease;transform:translateX(var(--g-trash-x,0));pointer-events:none}
#app-grimoire .g-trash-mode[data-mode="all"] .g-trash-thumb{background:var(--mass-accent-fill)}
#app-grimoire .g-trash-mode[data-mode="agents"] .g-trash-thumb{background:var(--mass-warning)}
#app-grimoire .g-trash-mode[data-mode="off"] .g-trash-thumb{background:var(--mass-text-muted)}
#app-grimoire .g-trash-stop{flex:1;display:flex;align-items:center;justify-content:center;height:100%;border:0;background:transparent;cursor:pointer;padding:0}
#app-grimoire .g-trash-dot{width:0.3rem;height:0.3rem;border-radius:50%;background:var(--mass-text-muted);opacity:0.55;transition:opacity 0.18s ease}
#app-grimoire .g-trash-stop[aria-checked="true"] .g-trash-dot{opacity:0}
/* Build version, the gear menu's last line: faint label, muted mono value, no
   rule above it — a footnote, not another section. It wraps rather than clips so
   a long version can't widen the 220px menu. */
#app-grimoire .g-version{display:flex;align-items:baseline;justify-content:space-between;gap:0.5rem;font-size:0.72rem;color:var(--mass-text-faint)}
#app-grimoire .g-version-value{color:var(--mass-text-muted);font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;text-align:right;overflow-wrap:anywhere}

/* Sidebar */
/* position:relative so the collapsed sidebar's floated header (book + expand
   toggle) anchors to this content area's top-left. */
#app-grimoire #g-body{position:relative}
#app-grimoire .g-sidebar{width:280px;min-width:220px;background:var(--mass-bg-panel);overflow:hidden}
/* Fixed height (not min-height) so the bottom border sits at the same pixel
   whether expanded (with brand text) or collapsed (icons only) — min-height let
   the row grow/shrink with its content and nudged the border ~1px. */
/* Height matches the workspace tab strip (.g-tabstrip) so the two top bars line up. */
#app-grimoire .g-side-head{padding:0 0.75rem;border-bottom:1px solid var(--mass-border);height:40px;box-sizing:border-box}
/* The book icon doubles as a Home button (back to the base empty prompt). The span
   is the clickable/hover target — a plain element reliably shows the pointer cursor
   — and the inner sl-icon is made inert so its shadow SVG doesn't capture the
   pointer (which kept the cursor from changing). */
#app-grimoire .g-home{display:inline-flex;cursor:pointer;transition:opacity 0.1s}
#app-grimoire .g-home:hover{opacity:0.75}
#app-grimoire .g-home sl-icon{pointer-events:none}
#app-grimoire .g-brand{font-size:1.05rem;font-weight:600}
#app-grimoire .g-sidebar-toggle{margin-left:auto;font-size:1rem;color:var(--mass-text-muted)}
#app-grimoire .g-sidebar-toggle:hover{color:var(--mass-text)}
#app-grimoire .g-sidebar-toggle::part(base){padding:0}

/* Collapsed sidebar: the rail is removed entirely (width 0) — NO empty column.
   The book + expand toggle float over the top-left, ONLY as tall as the tab strip
   (they don't extend below it). The main panel + tab strip fill the full width;
   the strip just reserves room at its start for the icons. Below the strip the
   content runs edge to edge — nothing on the left. The horizontal divider is the
   header's bottom border, continuing the tab strip's own bottom border. */
#app-grimoire .g-sidebar.g-collapsed{width:0;min-width:0;overflow:visible}
#app-grimoire .g-sidebar.g-collapsed .g-tabs{display:none}
#app-grimoire .g-sidebar.g-collapsed + .g-resize-handle{display:none}
#app-grimoire .g-sidebar.g-collapsed .g-side-head{position:absolute;top:0;left:0;width:62px;z-index:20;height:40px;padding:0 0.4rem;gap:0.65rem;box-sizing:border-box}
#app-grimoire .g-sidebar.g-collapsed .g-brand{display:none}
#app-grimoire .g-sidebar.g-collapsed .g-sidebar-toggle{margin-left:0}
/* Thin vertical divider between the icons and the first tab, ONLY within the
   header/tab-strip row (40px tall) — not full height. */
#app-grimoire .g-sidebar.g-collapsed .g-side-head{border-right:1px solid var(--mass-border)}
/* Reserve room at the start of the tab strip for the floated icons. */
#app-grimoire.g-sidebar-collapsed .g-tabstrip{padding-left:62px}

/* Keyboard-shortcuts dialog */
#app-grimoire .g-shortcuts{display:flex;flex-direction:column;gap:1rem}
#app-grimoire .g-shortcuts-group .g-label{margin-bottom:0.4rem}
#app-grimoire .g-shortcut{display:flex;align-items:center;gap:0.75rem;padding:0.2rem 0}
#app-grimoire .g-shortcut-keys{flex-shrink:0;width:9.5rem;display:flex;align-items:center;flex-wrap:wrap;gap:0.2rem}
#app-grimoire .g-shortcut-desc{font-size:0.85rem;color:var(--mass-text)}
#app-grimoire .g-shortcut-plus,#app-grimoire .g-shortcut-then{font-size:0.72rem;color:var(--mass-text-muted)}
#app-grimoire .g-kbd{font-family:var(--sl-font-mono,monospace);font-size:0.72rem;line-height:1;padding:0.2rem 0.4rem;border:1px solid var(--mass-border);border-bottom-width:2px;border-radius:4px;background:var(--mass-bg-panel);color:var(--mass-text);white-space:nowrap}
/* Shared bottom bar: full-width strip below the body. App icon buttons sit on
   the left, the search input row fills the rest on the right. */
/* align-items:flex-end so as the textarea grows multiline, the icon buttons and
   the Search button stay on the same bottom baseline (chat-input convention)
   rather than the icons centering in the taller bar. */
#app-grimoire .g-bottombar{flex-shrink:0;display:flex;align-items:flex-end;gap:1.25rem;padding:0.5rem 1.25rem;border-top:1px solid var(--mass-border);background:var(--mass-bg-panel);min-height:52px}
/* The icon row bottom-aligns with the Search button; a small bottom offset
   centers the glyphs against the buttons' height. gap (not per-button margins)
   spaces the icons evenly regardless of order, and treats the gear's dropdown
   wrapper like any other icon. */
#app-grimoire .g-bottombar-icons{flex-shrink:0;display:flex;align-items:center;gap:0.85rem;padding-bottom:0.3rem}
#app-grimoire .g-bottombar .g-input-row{flex:1;min-width:0}
/* Every icon in the row (the plain foot buttons AND the gear's dropdown wrapper)
   is the same square flex box, and so is each button's internal ::part(base) and
   its glyph — otherwise an sl-icon-button and the gear's sl-dropdown carry
   different intrinsic baselines and the first icon drifts below the rest. The
   glyph itself is the alignment unit: a fixed 1.4rem square centred on the row. */
#app-grimoire .g-bottombar-icons>.g-foot-btn,
#app-grimoire .g-bottombar-icons>sl-dropdown{display:inline-flex;align-items:center;height:1.4rem}
/* Every icon's button and its shadow ::part(base) are pinned to the same square
   flex box so the gear's dropdown trigger and the plain buttons share one
   geometry. */
#app-grimoire .g-bottombar-icons .g-foot-btn,
#app-grimoire .g-bottombar-icons sl-dropdown sl-icon-button[slot="trigger"]{display:inline-flex;align-items:center;justify-content:center;height:1.4rem}
#app-grimoire .g-bottombar-icons .g-foot-btn::part(base),
#app-grimoire .g-bottombar-icons sl-dropdown sl-icon-button::part(base){display:flex;align-items:center;justify-content:center;width:1.4rem;height:1.4rem;padding:0;line-height:1}
#app-grimoire .g-bottombar-icons sl-dropdown sl-icon-button[slot="trigger"]{font-size:1rem !important;color:var(--mass-text-muted)}
/* Icon buttons in the bottom bar: muted glyph, sized to match the row. */
#app-grimoire .g-foot-btn{font-size:1rem;color:var(--mass-text-muted)}
#app-grimoire .g-foot-btn:hover{color:var(--mass-text)}
/* No focus ring on the foot icons: they're one-shot toggles and the hover tint
   is affordance enough. Without this the theme button keeps a focus outline after
   a click (it reads as a stray underline under the glyph). */
#app-grimoire .g-foot-btn::part(base):focus,
#app-grimoire .g-foot-btn::part(base):focus-visible{outline:none;box-shadow:none}

/* Tabs: nav fixed, the active panel scrolls and fills the height. */
#app-grimoire .g-tabs::part(base){height:100%}
#app-grimoire .g-tabs::part(body){flex:1;min-height:0}
#app-grimoire .g-tabs sl-tab-panel{height:100%}
#app-grimoire .g-tabs sl-tab-panel::part(base){height:100%;padding:0.85rem;overflow-y:auto}
#app-grimoire .g-tabs sl-tab::part(base){padding:0.6rem 0.9rem;font-size:0.82rem}

/* Sidebar resize handle (MASS pattern). */
#app-grimoire .g-resize-handle{width:8px;cursor:col-resize;flex-shrink:0;position:relative;z-index:10}
#app-grimoire .g-resize-bar{position:absolute;top:0;bottom:0;left:50%;width:2px;transform:translateX(-50%);background:var(--mass-border);transition:background 0.15s}
#app-grimoire .g-resize-handle:hover .g-resize-bar{background:var(--mass-accent)}

/* Session history list */
/* The lists keep a right inset so row fills and selection rings stop short of
   the overlay scrollbar's floating thumb instead of running beneath it. */
#app-grimoire .g-sessions{display:flex;flex-direction:column;gap:1px;margin-top:0.4rem;overflow-y:auto;flex:1;min-height:0;padding-right:10px}
#app-grimoire .g-sessions-tab{flex:1;min-height:0}
#app-grimoire .g-session{display:flex;align-items:center;gap:0.25rem;padding:0.3rem 0.4rem;border-radius:0.35rem;cursor:pointer;color:var(--mass-text)}
#app-grimoire .g-session:hover{background:var(--mass-bg-base)}
/* Keyboard-navigation selection: a primary inset ring so it reads as focus,
   distinct from the hover fill and the tinted active-item text. Shared by session
   and tree rows. The selected row also takes real DOM focus; the ring is its only
   focus affordance, so the browser's default outline is suppressed to avoid
   doubling up. */
#app-grimoire .g-kbd-sel{box-shadow:inset 0 0 0 1px var(--mass-accent)}
/* While a row is part of the multi-selection its tinted fill stands in for the
   cursor; suppress the ring so multi-select reads as one solid band, not a band
   with a stray bordered row. */
#app-grimoire .g-kbd-sel.g-multi-sel{box-shadow:none}
#app-grimoire .g-session:focus,#app-grimoire .g-session:focus-visible,
#app-grimoire .g-tree-row:focus,#app-grimoire .g-tree-row:focus-visible{outline:none}
/* Rows aren't text-selectable, so Shift+click ranges instead of selecting text. */
#app-grimoire .g-session,#app-grimoire .g-tree-row{user-select:none}
/* Multi-selection (Ctrl/Shift+click) is a tinted fill, distinct from the cursor
   ring; when a row is both, the fill wins and the ring is suppressed (above). The
   rows are squared (no per-row radius) so a contiguous run reads as one band
   rather than a column of separate pills. */
#app-grimoire .g-multi-sel,
#app-grimoire .g-tree-folder-row.g-multi-sel,
#app-grimoire .g-tree-note.g-multi-sel{background:color-mix(in srgb,var(--mass-accent) 22%,transparent);border-radius:0}
/* Round only the outer corners of a contiguous run (tagged in JS) so the band has
   soft ends but its interior rows still butt together seamlessly. */
#app-grimoire .g-multi-sel.g-multi-top{border-top-left-radius:6px;border-top-right-radius:6px}
#app-grimoire .g-multi-sel.g-multi-bot{border-bottom-left-radius:6px;border-bottom-right-radius:6px}
/* The sessions list has a 1px row gap; bridge it for selected rows that aren't a
   run's bottom so the fill reads as one continuous band instead of striped rows. */
#app-grimoire .g-session.g-multi-sel:not(.g-multi-bot){margin-bottom:-1px;padding-bottom:calc(0.3rem + 1px)}
/* Drag-to-move: the folder (or tree root) under the pointer during an internal
   drag is ringed as the drop target; dragged rows dim slightly. */
#app-grimoire .g-drop-target{box-shadow:inset 0 0 0 2px var(--mass-accent);border-radius:0.35rem}
#app-grimoire .g-files.g-drop-target{box-shadow:inset 0 0 0 2px var(--mass-accent)}
#app-grimoire .g-dragging{opacity:0.45}
/* Multi-select action bar, pinned below a list: count + batch buttons, shown by
   JS. A top border sets it off from the list as a footer. */
#app-grimoire .g-actions{display:none;align-items:center;gap:0.4rem;padding:0.4rem 0.1rem 0.1rem;margin-top:0.35rem;border-top:1px solid var(--mass-border);font-size:0.78rem}
#app-grimoire .g-actions.g-actions-open{display:flex}
#app-grimoire .g-actions-count{flex:1;color:var(--mass-text-muted);white-space:nowrap}
#app-grimoire .g-actions sl-button::part(base){font-size:0.74rem}
#app-grimoire .g-actions-clear{color:var(--mass-text-muted);font-size:0.85rem}
/* Active row: tinted title plus a soft accent fill, so "selected" reads clearly
   in both themes (the accent text alone is too close to the body text in light
   mode to stand out). */
#app-grimoire .g-session-active{background:var(--mass-accent-soft)}
#app-grimoire .g-session-active .g-session-title{color:var(--mass-accent)}
#app-grimoire .g-session-main{flex:1;min-width:0;display:flex;flex-direction:column;gap:0.05rem}
#app-grimoire .g-session-title{font-size:0.78rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
#app-grimoire .g-session-meta{font-size:0.66rem;color:var(--mass-text-muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
#app-grimoire .g-session-del{color:var(--mass-text-muted);opacity:0;transition:opacity 0.1s}
#app-grimoire .g-session:hover .g-session-del{opacity:1}
#app-grimoire .g-session-del::part(base){padding:0.1rem}
#app-grimoire .g-session-edit{flex:1;min-width:0;font-size:0.78rem;background:var(--mass-bg-base);color:var(--mass-text);border:1px solid var(--mass-accent);border-radius:0.25rem;padding:0.1rem 0.3rem;outline:none}

/* Sessions and Files tabs share the same column gap and the same top-control
   height (the new-session button vs. the Files icon toolbar), so the filter input
   sits at an identical vertical position on both tabs. */
#app-grimoire .g-sessions-section{gap:0.4rem}
#app-grimoire .g-new-session::part(base){min-height:0;height:var(--g-tab-top-h)}
#app-grimoire .g-new-session::part(label){padding-top:0;padding-bottom:0}

/* Files: vault folder tree */
#app-grimoire .g-files-section{gap:0.4rem}
/* Obsidian-style icon toolbar above the filter, height-matched to the sessions
   new-session button via --g-tab-top-h. */
#app-grimoire .g-files-tools{display:flex;gap:0.4rem;align-items:center;justify-content:center;height:var(--g-tab-top-h)}
#app-grimoire .g-files-tools sl-icon-button{font-size:0.95rem;color:var(--mass-text-muted)}
#app-grimoire .g-files-tools sl-icon-button:hover{color:var(--mass-text)}
#app-grimoire .g-files-tools sl-icon-button::part(base){padding:0.25rem}
#app-grimoire .g-files-section{position:relative}
/* The tree and its drop overlay share this relative wrap (it flex-grows to fill
   the space under the toolbar/filter) so the overlay can cover the tree area
   exactly via inset:0 — no guessed offset that leaves the top row exposed. */
#app-grimoire .g-files-tree-wrap{position:relative;flex:1;min-height:0;display:flex;flex-direction:column}
#app-grimoire .g-files{flex:1;min-height:0;overflow-y:auto;padding-right:10px}
/* While a list is wheel-scrolling (calmHoverWhileScrolling, grimoire.js) its rows
   don't hit-test, so :hover — row semi-selection and the scrollbar's hover state
   with it — stays still instead of hopping under the pointer. */
#app-grimoire .g-scroll-calm *{pointer-events:none}
/* Drop-to-import overlay: hidden until a drag enters the Files tab, then covers
   the whole tree with a dashed target (mirrors pdf2doc's dropzone .dragover
   look). While shown it captures pointer events so the file rows beneath can't be
   clicked mid-drag; the unsupported-format notice variant re-enables clicks so it
   can be dismissed (its document-level handler swallows the dismissing click). */
#app-grimoire .g-files-drop{display:none;position:absolute;inset:0;border:2px dashed var(--mass-accent);border-radius:0.5rem;background:color-mix(in srgb,var(--mass-accent) 10%,var(--mass-bg-panel));color:var(--mass-accent);font-size:0.78rem;align-items:center;justify-content:center;text-align:center;padding:1rem;z-index:5}
#app-grimoire .g-files-section.dragover .g-files-drop{display:flex;pointer-events:auto}
/* Import status line: a slim, non-blocking row above the tree (the tree stays
   browsable during import). Hidden when idle; the JS toggles a progress or notice
   class. The progress bar shows only for a multi-file import. */
#app-grimoire .g-files-status{display:none;flex-direction:column;gap:0.3rem;padding:0.4rem 0.5rem;border-radius:0.35rem;font-size:0.74rem;border:1px solid var(--mass-border);background:var(--mass-bg-base)}
#app-grimoire .g-files-status.g-files-status-active{display:flex}
#app-grimoire .g-files-status-row{display:flex;align-items:center;gap:0.45rem}
#app-grimoire .g-files-status-spinner{font-size:0.85rem;--track-width:2px;flex-shrink:0}
#app-grimoire .g-files-status-text{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--mass-text)}
#app-grimoire .g-files-status.g-files-status-active .g-files-status-close{display:inline-flex}
#app-grimoire .g-files-status-close{flex-shrink:0;color:var(--mass-text-muted);display:none}
#app-grimoire .g-files-status-close::part(base){padding:0.1rem;font-size:0.8rem}
#app-grimoire .g-files-status-bar{display:none;height:3px;border-radius:2px;background:var(--mass-bg-panel);overflow:hidden}
#app-grimoire .g-files-status-fill{height:100%;width:0;background:var(--mass-accent);transition:width 0.2s ease}
/* Multi-file import: reveal the bar. */
#app-grimoire .g-files-status.g-files-status-multi .g-files-status-bar{display:block}
/* Finished state (a notice): swap the spinner for the close button, tint warning
   when something failed/was skipped. */
#app-grimoire .g-files-status.g-files-status-done .g-files-status-spinner{display:none}
#app-grimoire .g-files-status.g-files-status-done .g-files-status-close{display:inline-flex}
#app-grimoire .g-files-status.g-files-status-done .g-files-status-bar{display:none}
#app-grimoire .g-files-status.g-files-status-warn{border-color:color-mix(in srgb,var(--mass-warning) 45%,transparent);background:var(--mass-warning-soft)}
#app-grimoire .g-files-status.g-files-status-warn .g-files-status-text{color:var(--mass-warning);white-space:normal}
/* Clickable preview-opener (hit sources, tree/trash note rows): a bare-button
   reset plus a link-style hover underline. Declared BEFORE the sized rules
   (.g-tree-row, .g-hit-src) at equal specificity — the font:inherit shorthand
   must lose to their font-size/padding, or note rows render at the root 16px. */
#app-grimoire .g-preview-link{background:none;border:none;padding:0;cursor:pointer;text-align:left;font:inherit}
#app-grimoire .g-tree-row{display:flex;align-items:center;gap:0.25rem;width:100%;padding:0.22rem 0.35rem;border-radius:0.35rem;font-size:0.78rem;color:var(--mass-text);cursor:pointer;background:none;border:none;text-align:left;white-space:nowrap;overflow:hidden}
#app-grimoire .g-tree-row:hover{background:var(--mass-bg-base)}
/* The open note's name is tinted, like the active session's title (no fill — the
   fill is reserved for mouse hover). Marked client-side (the server-rendered tree
   doesn't know what's open) and re-applied on every tree re-render. */
#app-grimoire .g-tree-note-active{background:var(--mass-accent-soft);border-radius:0.35rem}
#app-grimoire .g-tree-note-active .g-tree-name{color:var(--mass-accent)}
#app-grimoire .g-tree-icon{font-size:0.85rem;color:var(--mass-text-muted);flex-shrink:0}
#app-grimoire .g-tree-name{overflow:hidden;text-overflow:ellipsis}
#app-grimoire .g-tree-children{padding-left:0.85rem}
/* Disclosure caret: a fixed-width lead slot so folder and file rows align (notes
   get an empty slot of the same width). The chevron rotates when open. */
#app-grimoire .g-tree-caret{flex:0 0 0.8rem;font-size:0.7rem;color:var(--mass-text-muted);display:inline-flex;align-items:center;justify-content:center;transition:transform 0.12s}
#app-grimoire .g-tree-folder[open]>summary .g-tree-caret{transform:rotate(90deg)}
#app-grimoire .g-tree-folder>summary{list-style:none}
#app-grimoire .g-tree-folder>summary::-webkit-details-marker{display:none}
/* Non-note files: shown for context (like Obsidian) but not openable. */
#app-grimoire .g-tree-other{color:var(--mass-text-muted);cursor:default;opacity:0.65}
#app-grimoire .g-tree-other:hover{background:none}
/* Note name takes the row's width so the delete button sits at the far right,
   revealed on hover (like the session row). */
#app-grimoire .g-tree-note .g-tree-name{flex:1;min-width:0}
/* Row hover actions (delete a note/folder, add a note/subfolder, restore/delete a
   trashed note): the same muted reveal-on-hover icon affordance everywhere — a
   shared class so it's defined once. The name flexes so they sit at the row's
   right; per-row :hover reveals them; per-icon hover tints stay below. */
#app-grimoire .g-row-action{color:var(--mass-text-muted);opacity:0;flex-shrink:0;transition:opacity 0.1s}
#app-grimoire .g-row-action::part(base){padding:0.1rem;font-size:0.8rem}
#app-grimoire .g-tree-note:hover .g-row-action,#app-grimoire .g-tree-folder-row:hover .g-row-action,#app-grimoire .g-trash-row:hover .g-row-action{opacity:1}
#app-grimoire .g-tree-folder-row .g-tree-name{flex:1;min-width:0}
/* Inline rename input, sized to sit in the row in place of the name. */
#app-grimoire .g-tree-edit{flex:1;min-width:0;font-size:0.78rem;background:var(--mass-bg-base);color:var(--mass-text);border:1px solid var(--mass-accent);border-radius:0.25rem;padding:0.05rem 0.3rem;outline:none}

/* Trash view: the same file view rendered into #g-files in place of the tree.
   The toolbar Trash button toggles g-files-trashing on the section and lights up
   while browsing the trash. Rows reuse the file-tree row style. */
#app-grimoire .g-files-section.g-files-trashing #g-files-trash{color:var(--mass-accent)}
/* Restore lives in the shared files action bar, shown only in trash mode (Delete
   then means permanent removal). Outside trash mode it's hidden. */
#app-grimoire .g-actions-restore{display:none}
#app-grimoire .g-files-section.g-files-trashing .g-actions-restore{display:inline-flex}
/* Empty trash: a footer service action, shown only in trash mode and only while
   nothing is selected — a selection swaps it for the action bar (Restore/Delete)
   in the same spot, never stacked above it. Centered horizontally. */
#app-grimoire .g-trash-service{display:none;justify-content:center;padding:0.4rem 0.1rem 0.1rem;margin-top:0.35rem;border-top:1px solid var(--mass-border)}
#app-grimoire .g-files-section.g-files-trashing .g-trash-service{display:flex}
#app-grimoire .g-files-section.g-files-trashing:has(.g-actions.g-actions-open) .g-trash-service{display:none}
#app-grimoire .g-trash-service sl-button::part(base){font-size:0.74rem}
/* Trash rows: the shared row + g-row-action affordance, plus a deleted-at stamp
   and per-icon hover tints (restore → accent, delete → danger). */
#app-grimoire .g-trash-date{color:var(--mass-text-muted);font-size:0.68rem;flex-shrink:0}
#app-grimoire .g-trash-restore:hover{color:var(--mass-accent)}
#app-grimoire .g-trash-del:hover{color:var(--mass-danger)}
#app-grimoire .g-trash-empty-msg{padding:0.5rem 0.6rem;font-size:0.78rem;text-align:center}


/* Main conversation area */
/* Panel area below the tab strip: holds the stream, input bar, and the
   preview/graph overlays. position:relative confines those overlays (inset:0)
   to this area so they never cover the tab strip. */
#app-grimoire .g-panel{position:relative;flex:1;min-height:0;display:flex;flex-direction:column}
#app-grimoire .g-stream{overflow-y:auto;padding:1.5rem 2.5rem 0;display:flex;flex-direction:column}
#app-grimoire .g-empty{margin:auto;text-align:center;display:flex;flex-direction:column;align-items:center;gap:0.5rem;max-width:380px}
#app-grimoire .g-empty-title{font-size:1.1rem;font-weight:600;color:var(--mass-text)}
/* Empty state: an opaque cover over the whole main area when no vault is bound,
   so the (inert) workspace behind it never shows through. */
#app-grimoire .g-vault-empty{position:absolute;inset:0;z-index:30;background:var(--mass-bg-base);display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;gap:0.4rem;padding:2rem}
#app-grimoire .g-vault-empty-title{font-size:1.3rem;font-weight:600;color:var(--mass-text)}
#app-grimoire .g-vault-recents{margin-top:1.6rem;width:100%;max-width:460px;text-align:left}
#app-grimoire .g-vault-recent{display:flex;align-items:center;gap:0.5rem;width:100%;padding:0.5rem 0.6rem;border:1px solid var(--mass-border);border-radius:0.4rem;background:var(--mass-bg-panel);color:var(--mass-text);cursor:pointer;margin-bottom:0.35rem;text-align:left}
#app-grimoire .g-vault-recent:hover{border-color:var(--mass-accent);background:var(--mass-bg-hover)}
#app-grimoire .g-vault-recent-name{font-weight:600;flex-shrink:0}
#app-grimoire .g-vault-recent-path{font-size:0.72rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
#app-grimoire #g-conversation{display:flex;flex-direction:column;width:100%;flex:1;min-height:0}
/* Trailing spacer: a scroll container drops its own bottom padding when content
   overflows, so add the gap as a fixed-height final flex child instead. */
#app-grimoire #g-conversation::after{content:"";display:block;flex:0 0 0.2rem}

/* Conversation turns: a query bubble then the results below it. */
#app-grimoire .g-turn{display:flex;flex-direction:column;margin-bottom:1.75rem}
#app-grimoire .g-bubble-user{align-self:flex-end;background:var(--mass-accent-fill);color:var(--mass-fill-text);padding:0.5rem 0.85rem;border-radius:0.9rem 0.9rem 0.2rem 0.9rem;max-width:75%;font-size:0.88rem;word-break:break-word;margin-bottom:0.85rem;cursor:context-menu}
#app-grimoire .g-hit{border:1px solid var(--mass-border);border-radius:0.45rem;padding:0.6rem 0.7rem;background:var(--mass-bg-panel);margin-top:0.5rem}
#app-grimoire .g-hit-src{font-size:0.72rem;color:var(--mass-accent);margin-bottom:0.25rem}
#app-grimoire .g-hit-text{font-size:0.8rem;color:var(--mass-text);white-space:pre-wrap;word-break:break-word}

/* Input bar */
#app-grimoire .g-input-row{display:flex;gap:0.5rem;align-items:flex-end;width:100%}
/* Auto-growing query field: one slim line at rest (matching the small buttons'
   height) that grows to a cap, then scrolls. */
#app-grimoire #g-query-input::part(form-control),#app-grimoire #g-query-input::part(base){min-height:0}
#app-grimoire #g-query-input::part(textarea){padding-top:0.25rem;padding-bottom:0.25rem;min-height:1.25rem;max-height:11rem;overflow-y:auto;line-height:1.25rem}
#app-grimoire #g-query-input::part(base){background:var(--mass-bg-base)}

/* Streaming dots, shown in a turn's answer/results block until content arrives. */
#app-grimoire .g-dots{display:flex;align-items:center;gap:0.45rem;padding:0.25rem 0}
#app-grimoire .g-dots span{width:8px;height:8px;border-radius:50%;background:var(--mass-text-faint);animation:g-wave 1.4s ease-in-out infinite}
#app-grimoire .g-dots span:nth-child(2){animation-delay:0.2s}
#app-grimoire .g-dots span:nth-child(3){animation-delay:0.4s}
@keyframes g-wave{0%,60%,100%{opacity:.15}30%{opacity:.8}}

#app-grimoire .g-preview-link:hover{text-decoration:underline}

/* In-note find: a small bar over the preview (replaces the native Ctrl+F, which
   would scan the whole page — sidebar and hidden conversation included). Matches
   are painted with the CSS Custom Highlight API, so the note's DOM is untouched
   (no <mark> injection to fight a re-render); the current match is brighter. */
#app-grimoire ::highlight(g-find){background:color-mix(in srgb,var(--mass-warning) 30%,var(--mass-bg-base));color:var(--mass-text)}
#app-grimoire ::highlight(g-find-current){background:color-mix(in srgb,var(--mass-warning) 35%,var(--mass-bg-base));color:var(--mass-text);text-decoration:underline var(--mass-warning) 2px}
#app-grimoire .g-find{position:absolute;top:0.55rem;right:1.5rem;z-index:30;display:none;align-items:center;gap:0.4rem;padding:0.3rem 0.5rem;background:var(--mass-bg-panel);border:1px solid var(--mass-border);border-radius:0.4rem;box-shadow:0 2px 8px rgba(0,0,0,0.3)}
#app-grimoire .g-find.g-find-open{display:flex}
#app-grimoire .g-find input{background:var(--mass-bg-base);border:1px solid var(--mass-border);border-radius:0.25rem;color:var(--mass-text);font:inherit;font-size:0.85rem;padding:0.15rem 0.4rem;width:11rem;outline:none}
#app-grimoire .g-find input:focus{border-color:var(--mass-accent)}
#app-grimoire .g-find-count{font-size:0.75rem;color:var(--mass-text-muted);min-width:2.5rem;text-align:center}
#app-grimoire .g-find sl-icon-button::part(base){padding:0.15rem}

/* Note preview panel: slides over the stream */
/* Similarity graph overlay: same full-panel slab as the preview but a notch
   higher so it covers the preview too when open. The canvas fills the body; the
   empty-state note centers over it when there's nothing to draw. */
#app-grimoire .g-graph{position:absolute;inset:0;background:var(--mass-bg-base);z-index:30;display:none;flex-direction:column}
#app-grimoire .g-graph.g-graph-open{display:flex}
#app-grimoire .g-graph-head{display:flex;align-items:center;gap:0.75rem;width:100%;box-sizing:border-box;flex-shrink:0;height:var(--g-head-h);padding:0 1.25rem 0 2rem;border-bottom:1px solid var(--mass-border)}
/* Match the note-header height: shrink the filter input so the controls don't
   stretch the bar taller than the preview header. Drive it through Shoelace's
   own height var so the input text stays vertically centered. */
#app-grimoire .g-graph-filter{--sl-input-height-small:1.6rem}
#app-grimoire .g-graph-title{font-size:0.8rem;color:var(--mass-accent)}
/* Search tuning bar: same compact head as the graph overlay, but it's the
   stream's own top row (not an overlay) so it pins above the scrolling turns. */
#app-grimoire .g-search-head{display:flex;align-items:center;gap:0.75rem;width:100%;box-sizing:border-box;flex-shrink:0;height:var(--g-head-h);padding:0 1.25rem 0 2rem;border-bottom:1px solid var(--mass-border)}
#app-grimoire .g-search-title{font-size:0.8rem;color:var(--mass-accent)}
/* Slider labels carry the hover tip — flag them as such (help cursor + a subtle
   dotted underline) so it's discoverable. */
#app-grimoire .g-ctl-tip{cursor:help;text-decoration:underline dotted;text-underline-offset:2px;text-decoration-color:var(--mass-border)}
#app-grimoire .g-graph-count{font-size:0.72rem;color:var(--mass-text-muted)}
/* k / min-similarity controls, pushed to the right of the head before the ×. */
#app-grimoire .g-graph-controls{margin-left:auto;display:flex;align-items:center;gap:1.25rem}
#app-grimoire .g-graph-ctl{display:flex;align-items:center;gap:0.5rem;font-size:0.72rem}
/* Native range slider, styled in light DOM for a flat rounded-square thumb
   (Shoelace's sl-range hides its circular thumb in shadow DOM, unreshapeable). */
#app-grimoire .g-range{-webkit-appearance:none;appearance:none;width:7rem;height:16px;background:transparent;cursor:pointer;margin:0}
#app-grimoire .g-range::-webkit-slider-runnable-track{height:3px;border-radius:999px;background:var(--mass-bg-active)}
#app-grimoire .g-range::-moz-range-track{height:3px;border-radius:999px;background:var(--mass-bg-active)}
#app-grimoire .g-range::-webkit-slider-thumb{-webkit-appearance:none;appearance:none;margin-top:-6px;width:11px;height:15px;border-radius:4px;background:var(--mass-accent-fill);border:1px solid var(--mass-bg-base)}
#app-grimoire .g-range::-moz-range-thumb{width:11px;height:15px;border-radius:4px;background:var(--mass-accent-fill);border:1px solid var(--mass-bg-base)}
#app-grimoire .g-range:focus-visible{outline:none}
#app-grimoire .g-graph-ctl-val{min-width:1.8rem;text-align:right}
#app-grimoire .g-graph-filter{width:13rem}
#app-grimoire .g-graph-filter::part(base){--sl-input-height-small:1.75rem}
#app-grimoire .g-graph-canvas{flex:1;min-height:0;width:100%;display:block;cursor:grab}
#app-grimoire .g-graph-canvas:active{cursor:grabbing}
#app-grimoire .g-graph-empty{position:absolute;inset:0;display:none;align-items:center;justify-content:center;color:var(--mass-text-muted);font-size:0.85rem;pointer-events:none}
/* Empty message only once loading has finished and the graph is genuinely empty. */
#app-grimoire .g-graph.g-graph-blank:not(.g-graph-loading) .g-graph-empty{display:flex}
/* Loading overlay: centred animated dots shown while the graph fetches + lays
   out (the store opens lazily on a cold start). Plain CSS — no Shoelace spinner,
   so it always paints; the accent dots read clearly on the empty dark canvas. */
#app-grimoire .g-graph-loading{position:absolute;inset:0;display:none;flex-direction:column;align-items:center;justify-content:center;gap:0.75rem;color:var(--mass-text);font-size:0.85rem;pointer-events:none}
#app-grimoire .g-graph.g-graph-loading .g-graph-loading{display:flex}
#app-grimoire .g-graph-dots{display:flex;gap:0.45rem}
#app-grimoire .g-graph-dots span{width:0.55rem;height:0.55rem;border-radius:50%;background:var(--mass-accent);animation:g-graph-dot 1s ease-in-out infinite}
#app-grimoire .g-graph-dots span:nth-child(2){animation-delay:0.15s}
#app-grimoire .g-graph-dots span:nth-child(3){animation-delay:0.3s}
@keyframes g-graph-dot{0%,80%,100%{opacity:0.25;transform:scale(0.7)}40%{opacity:1;transform:scale(1)}}

/* Workspace tab strip: open notes / sessions / graph, focused one highlighted.
   The strip and its tabs share the panel chrome colour (sidebar/input bar) so it
   reads as one surface; tabs are separated by a thin divider, not a contrasting
   strip background. The focused tab takes the content base colour + a primary
   underline. */
/* Height matches the sidebar header (.g-side-head) so the two top bars line up. */
#app-grimoire .g-tabstrip{display:flex;align-items:stretch;height:40px;background:var(--mass-bg-panel);border-bottom:1px solid var(--mass-border);flex-shrink:0}
/* Tabs scroll horizontally; the "+" stays pinned to their right. */
#app-grimoire .g-tabstrip-tabs{display:flex;align-items:stretch;overflow-x:auto;min-width:0}
/* New-tab "+" at the end of the strip (browser convention). */
#app-grimoire .g-tab-new{flex-shrink:0;display:flex;align-items:center;padding:0 0.5rem;color:var(--mass-text-muted);font-size:1rem}
#app-grimoire .g-tab-new:hover{color:var(--mass-text)}
#app-grimoire .g-tab-new::part(base){padding:0 0.35rem}
#app-grimoire .g-tab{position:relative;display:flex;align-items:center;gap:0.4rem;max-width:14rem;padding:0.4rem 0.65rem;border-right:1px solid var(--mass-border);color:var(--mass-text-muted);font-size:0.8rem;cursor:pointer;white-space:nowrap;user-select:none}
#app-grimoire .g-tab:hover{color:var(--mass-text)}
#app-grimoire .g-tab-active{background:var(--mass-bg-base);color:var(--mass-text);box-shadow:inset 0 -2px 0 var(--mass-accent);text-shadow:var(--mass-glow-tab,none)}
#app-grimoire .g-tab-title{overflow:hidden;text-overflow:ellipsis}
/* A provisional (single-click preview) tab shows its title italic, like an IDE; a
   double-click, an edit, or dragging it pins the tab and clears the italic. */
#app-grimoire .g-tab-preview .g-tab-title{font-style:italic}
#app-grimoire .g-tab-close{flex-shrink:0;width:1.1rem;height:1.1rem;line-height:1;display:inline-flex;align-items:center;justify-content:center;border-radius:0.25rem;font-size:1rem;color:var(--mass-text-muted)}
#app-grimoire .g-tab-close:hover{background:var(--mass-border);color:var(--mass-text)}
/* Drag to re-order: the dragged tab dims; an insertion bar marks where it'll land. */
#app-grimoire .g-tab-dragging{opacity:0.4}
#app-grimoire .g-tab-drop-before::before,#app-grimoire .g-tab-drop-after::after{content:"";position:absolute;top:0;bottom:0;width:2px;background:var(--mass-accent)}
#app-grimoire .g-tab-drop-before::before{left:-1px}
#app-grimoire .g-tab-drop-after::after{right:-1px}
/* Right-click bulk-close menu: a small floating panel positioned at the cursor.
   z-index sits above the preview/graph overlays (z-index 30). */
#app-grimoire .g-tab-menu{position:fixed;z-index:60;min-width:11rem;padding:0.25rem;background:var(--mass-bg-panel);border:1px solid var(--mass-border);border-radius:0.4rem;box-shadow:0 4px 14px rgba(0,0,0,0.35);font-size:0.8rem;user-select:none}
#app-grimoire .g-tab-menu-item{padding:0.35rem 0.6rem;border-radius:0.25rem;color:var(--mass-text);cursor:pointer;white-space:nowrap}
#app-grimoire .g-tab-menu-item:hover{background:var(--mass-border)}
#app-grimoire .g-tab-menu-disabled{color:var(--mass-text-muted);opacity:0.5;cursor:default}
#app-grimoire .g-tab-menu-disabled:hover{background:none}
/* Hide the whole app until a reloaded tab/view is restored, so neither the
   default Sessions tab nor the empty home flashes before the restore swaps them
   in (set pre-paint and cleared by JS once restore completes). */
#app-grimoire.g-prepaint-hide{visibility:hidden}
#app-grimoire .g-preview{position:absolute;inset:0;background:var(--mass-bg-base);z-index:25;display:flex;flex-direction:column}
#app-grimoire .g-preview-head{display:flex;align-items:center;gap:0.75rem;box-sizing:border-box;flex-shrink:0;height:var(--g-head-h);padding:0 1.25rem 0 2rem;border-bottom:1px solid var(--mass-border)}
/* The header action icons (save-all/remove-all/edit/close) sit in a tight cluster
   so they read as one control group, not spread across the bar by the header's
   wider gap. */
#app-grimoire .g-preview-actions{display:flex;align-items:center;gap:0.15rem;flex-shrink:0}
/* A trashed note previews read-only: hide the edit pencil + run-save controls. */
#app-grimoire .g-preview.g-preview-readonly #g-edit-toggle,#app-grimoire .g-preview.g-preview-readonly .g-runsaveall-btn{display:none}
#app-grimoire .g-preview-nav{display:flex;gap:0.1rem}
#app-grimoire .g-preview-title{font-size:0.8rem;color:var(--mass-accent);word-break:break-word;flex:1}
#app-grimoire .g-preview-section{color:var(--mass-text-muted)}
/* Modified/created dates in the preview header, beside the title. */
#app-grimoire .g-preview-dates{display:flex;gap:0.9rem;flex:0 0 auto;font-size:0.7rem;color:var(--mass-text-muted);white-space:nowrap}
#app-grimoire .g-preview-close{color:var(--mass-text-muted)}
#app-grimoire .g-preview-close::part(base){padding:0.25rem}
#app-grimoire .g-preview-nav sl-icon-button::part(base){padding:0.25rem}
#app-grimoire .g-preview-nav sl-icon-button[disabled]{opacity:0.3}
#app-grimoire .g-preview-body{overflow-y:auto;padding:1.5rem 2.5rem;flex:1;width:100%}

/* Body editor (raw Markdown), shown in place of the rendered body when editing. */
#app-grimoire .g-editor{display:none;flex:1;min-height:0;flex-direction:column;padding:1.5rem 2.5rem}
#app-grimoire .g-editor.g-editor-open{display:flex}
#app-grimoire .g-editor-text{flex:1;min-height:0;width:100%;resize:none;background:var(--mass-bg-base);color:var(--mass-text);border:1px solid var(--mass-border);border-radius:0.4rem;padding:0.75rem 1rem;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:0.85rem;line-height:1.6;outline:none}
#app-grimoire .g-editor-text:focus{border-color:var(--mass-accent)}
#app-grimoire .g-editor-bar{display:flex;align-items:center;gap:0.6rem;margin-top:0.6rem}
#app-grimoire .g-editor-dirty{flex:1;font-size:0.75rem;color:var(--mass-text-muted)}
#app-grimoire .g-btn,#app-grimoire .g-btn-primary{font-size:0.82rem;padding:0.3rem 0.9rem;border-radius:0.3rem;border:1px solid var(--mass-border);cursor:pointer;background:var(--mass-bg-base);color:var(--mass-text)}
#app-grimoire .g-btn-primary{background:var(--mass-accent-fill);border-color:var(--mass-accent-fill);color:var(--mass-fill-text)}

/* Frontmatter properties panel — always editable, Obsidian-style (no edit mode;
   changes auto-save). A row is: type icon · key input · value chips + add-input ·
   remove. */
#app-grimoire .g-props{margin-bottom:1.25rem;padding:0.5rem 0.75rem;border-bottom:1px solid var(--mass-border);display:flex;flex-direction:column;gap:0.15rem}
#app-grimoire .g-props-list{display:flex;flex-direction:column}
#app-grimoire .g-prop{display:flex;align-items:flex-start;gap:0.5rem;padding:0.15rem 0;font-size:0.82rem}
#app-grimoire .g-prop:hover{background:var(--mass-bg-base);border-radius:0.3rem}
#app-grimoire .g-prop-icon{flex:0 0 1rem;color:var(--mass-text-muted);font-size:0.85rem;margin-top:0.25rem}
#app-grimoire .g-prop-key{flex:0 0 7rem;background:none;border:none;color:var(--mass-text-muted);font:inherit;font-size:0.82rem;padding:0.15rem 0.25rem;border-radius:0.25rem;outline:none}
#app-grimoire .g-prop-key:hover,#app-grimoire .g-prop-key:focus{background:var(--mass-bg-base);color:var(--mass-text)}
#app-grimoire .g-prop-vals{flex:1;display:flex;flex-wrap:wrap;gap:0.3rem;align-items:center}
#app-grimoire .g-chip{display:inline-flex;align-items:center;gap:0.3rem;font-size:0.78rem;background:var(--mass-accent-soft);border-radius:0.3rem;padding:0.05rem 0.45rem;color:var(--mass-accent)}
#app-grimoire .g-chip-del{background:none;border:none;color:inherit;opacity:0.7;cursor:pointer;font-size:0.9rem;line-height:1;padding:0}
#app-grimoire .g-chip-del:hover{opacity:1}
#app-grimoire .g-prop-addval{flex:1;min-width:6rem;font-size:0.8rem;background:none;color:var(--mass-text);border:none;outline:none;padding:0.15rem}
#app-grimoire .g-prop-del{background:none;border:none;color:var(--mass-text-muted);cursor:pointer;opacity:0;font-size:0.9rem}
#app-grimoire .g-prop:hover .g-prop-del{opacity:0.7}
#app-grimoire .g-prop-del:hover{opacity:1;color:var(--mass-text)}
#app-grimoire .g-props-add{align-self:flex-start;display:flex;align-items:center;gap:0.3rem;background:none;border:none;color:var(--mass-text-muted);cursor:pointer;font-size:0.78rem;padding:0.25rem}
#app-grimoire .g-props-add:hover{color:var(--mass-text)}

/* Rendered Markdown */
#app-grimoire .markdown-body{color:var(--mass-text);font-size:0.9rem;line-height:1.65}
#app-grimoire .markdown-body h1,#app-grimoire .markdown-body h2,#app-grimoire .markdown-body h3{margin:1.2em 0 0.5em;font-weight:600;line-height:1.3}
#app-grimoire .markdown-body h1{font-size:1.5rem}
#app-grimoire .markdown-body h2{font-size:1.25rem}
#app-grimoire .markdown-body h3{font-size:1.05rem}
#app-grimoire .markdown-body p{margin:0.6em 0}
#app-grimoire .markdown-body a{color:var(--mass-accent)}
/* Restore list markers: the Shoelace theme reset zeroes list-style globally, so
   without these explicit rules a rendered note's bullets/numbers disappear. */
#app-grimoire .markdown-body ul,#app-grimoire .markdown-body ol{margin:0.6em 0;padding-left:1.5em}
#app-grimoire .markdown-body ul{list-style:disc}
#app-grimoire .markdown-body ul ul{list-style:circle}
#app-grimoire .markdown-body ul ul ul{list-style:square}
#app-grimoire .markdown-body ol{list-style:decimal}
#app-grimoire .markdown-body li{margin:0.2em 0;display:list-item}
#app-grimoire .markdown-body code{background:var(--mass-bg-panel);padding:0.1em 0.35em;border-radius:0.25rem;font-size:0.85em}
#app-grimoire .markdown-body pre{background:var(--mass-bg-panel);padding:0.8rem 1rem;border-radius:0.45rem;overflow-x:auto;border:1px solid var(--mass-border)}
#app-grimoire .markdown-body pre code{background:none;padding:0}
` + codeHighlightCSS + `

/* Code-block controls: copy, and (for runnable blocks) run / run-above, plus the
   kernel badge. All are always visible and fixed in place — no hover-to-reveal,
   so the row doesn't shift under the cursor. The check tint confirms a copy. */
#app-grimoire .g-code-block{position:relative}
#app-grimoire .g-code-copy{position:absolute;top:0.4rem;right:0.4rem;font-size:0.95rem;color:var(--mass-text-muted);background:var(--mass-bg-panel);border-radius:0.25rem;z-index:1}
#app-grimoire .g-code-copy.g-copied{color:var(--mass-success)}
#app-grimoire .g-code-copy::part(base){padding:0.2rem}
/* Run / Run-above buttons: sit left of the copy button. */
#app-grimoire .g-code-run{position:absolute;top:0.4rem;right:2.2rem;font-size:0.95rem;color:var(--mass-text-muted);background:var(--mass-bg-panel);border-radius:0.25rem;z-index:1}
#app-grimoire .g-code-run-above{position:absolute;top:0.4rem;right:4rem;font-size:0.95rem;color:var(--mass-text-muted);background:var(--mass-bg-panel);border-radius:0.25rem;z-index:1}
#app-grimoire .g-code-run::part(base),#app-grimoire .g-code-run-above::part(base){padding:0.2rem}
/* Kernel badge: a chip in the block's top-right showing which kernel will run it,
   fixed left of the run buttons (which start at right:4rem). Muted so it doesn't
   compete with the code. */
#app-grimoire .g-code-kernel{position:absolute;top:0.45rem;right:5.8rem;font-size:0.68rem;line-height:1;padding:0.18rem 0.4rem;color:var(--mass-text-muted);background:var(--mass-bg-base);border:1px solid var(--mass-border);border-radius:0.35rem;user-select:none}
/* Output panel under a runnable block: streamed stdout/stderr + a status footer. */
/* Symmetric vertical padding reserves equal room top and bottom for the corner
   controls (run time top-right, save/discard/remove bottom-right) so they don't
   crowd the output or the panel edge, especially when a block printed little or
   nothing. */
#app-grimoire .g-code-output{position:relative;margin-top:0.4rem;padding:0.9rem 0.75rem;background:var(--mass-bg-base);border:1px solid var(--mass-border);border-radius:0.45rem;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:0.82rem;line-height:1.5;white-space:pre-wrap;word-break:break-word}
/* When the block finished: the time it ran, pinned to the panel's top-right. */
#app-grimoire .g-run-time{position:absolute;top:0.35rem;right:0.5rem;white-space:nowrap;color:var(--mass-text-muted);font-size:0.72rem;opacity:0.75;user-select:none}
#app-grimoire .g-run-time:empty{display:none}
#app-grimoire .g-run-head{display:flex;align-items:center;gap:0.5rem;color:var(--mass-text-muted);margin-bottom:0.4rem}
#app-grimoire .g-run-head:empty{display:none}
/* The gap between output and the status footer rides on the body, so it
   disappears when a block printed nothing (an empty body collapses) — keeping the
   panel's top and bottom padding visually equal in that case. */
#app-grimoire .g-run-body{margin-bottom:0.45rem}
#app-grimoire .g-run-body:empty{display:none}
#app-grimoire .g-run-spinner{font-size:0.9rem;--track-width:2px;--indicator-color:var(--mass-text-muted);--track-color:var(--mass-border)}
#app-grimoire .g-run-out{color:var(--mass-text)}
#app-grimoire .g-run-status{display:flex;align-items:center;gap:0.35rem;color:var(--mass-success);font-size:0.78rem}
#app-grimoire .g-run-status.g-run-fail{color:var(--mass-danger)}
/* The kernel that ran the block, muted so the exit status stays primary. */
#app-grimoire .g-run-kernel{color:var(--mass-text-muted)}
/* A run image (a plotting kernel's output); fits the panel width. */
#app-grimoire .g-run-img{display:block;max-width:100%;margin:0.4rem 0;border-radius:0.3rem}
/* Save control: shown only for an unsaved run (a re-run over already-saved
   output). The first run is auto-saved, so the slot is empty and collapses. It's
   a single floppy icon pinned to the panel's bottom-right corner (the panel is
   position:relative) — a quiet affordance, not a button bar. The warning tint
   signals "unsaved"; the tooltip explains it. */
/* The per-block controls (save floppy + remove trash) cluster in the panel's
   bottom-right corner. The buttons are plain <button>s holding an sl-icon (not
   sl-icon-buttons) — the icon-button's shadow host is a fixed ~30px square that
   centres the glyph and can't be collapsed from light DOM, which kept the glyph
   inset no matter the offset. Bare buttons are glyph-sized, so they reach the
   corner. */
#app-grimoire .g-run-controls{position:absolute;bottom:0.35rem;right:0.4rem;display:inline-flex;align-items:center;gap:0.5rem;line-height:0;z-index:1}
#app-grimoire .g-run-save:empty{display:none}
/* The save slot holds a discard (revert) + save (floppy) pair when a re-run is
   unsaved, so it needs to lay them out side by side. */
#app-grimoire .g-run-save{display:inline-flex;align-items:center;gap:0.5rem;line-height:0}
#app-grimoire .g-run-save-btn,#app-grimoire .g-run-del-btn,#app-grimoire .g-run-discard-btn{display:block;padding:0;margin:0;border:none;background:none;cursor:pointer;font-size:0.95rem;line-height:0}
#app-grimoire .g-run-save-btn{color:var(--mass-warning)}
#app-grimoire .g-run-discard-btn{color:var(--mass-text-muted)}
#app-grimoire .g-run-discard-btn:hover{color:var(--mass-text)}
#app-grimoire .g-run-del-btn{color:var(--mass-text-muted)}
#app-grimoire .g-run-del-btn:hover{color:var(--mass-danger)}
#app-grimoire .g-run-save-btn sl-icon,#app-grimoire .g-run-del-btn sl-icon,#app-grimoire .g-run-discard-btn sl-icon{display:block}
/* The note-level "Discard all" / "Save all" / "Remove all" buttons ride in the
   preview header but only show when relevant: discard-all and save-all while some
   block is unsaved (g-has-unsaved), remove-all while the note has any stored
   output (g-has-results). JS toggles both classes on the preview. */
#app-grimoire .g-rundiscardall-btn,#app-grimoire .g-runsaveall-btn,#app-grimoire .g-rundeleteall-btn{display:none}
#app-grimoire .g-runsaveall-btn{color:var(--mass-warning)}
#app-grimoire .g-rundiscardall-btn{color:var(--mass-text-muted)}
#app-grimoire .g-rundiscardall-btn:hover{color:var(--mass-text)}
#app-grimoire .g-rundeleteall-btn{color:var(--mass-text-muted)}
#app-grimoire .g-rundeleteall-btn:hover{color:var(--mass-danger)}
#app-grimoire .g-preview.g-has-unsaved .g-rundiscardall-btn,#app-grimoire .g-preview.g-has-unsaved .g-runsaveall-btn{display:inline-flex}
#app-grimoire .g-preview.g-has-results .g-rundeleteall-btn{display:inline-flex}
/* The note-level "Run all" play button shows only when the note has a runnable
   block (JS toggles g-has-runnable). Tinted primary as the header's main action. */
#app-grimoire .g-runall-btn{display:none;color:var(--mass-accent)}
#app-grimoire .g-preview.g-has-runnable .g-runall-btn{display:inline-flex}
#app-grimoire .markdown-body blockquote{border-left:3px solid var(--mass-border);margin:0.6em 0;padding-left:1em;color:var(--mass-text-muted)}
/* Obsidian-style callouts: a tinted box with an icon + title header. The accent
   colour is per-type (--cl); the box tints the panel with it. */
/* --cl is the readable accent (text, border); --cl-tint is the soft fill. They're
   separate because a tint mixed from the deep light-theme text colour reads muddy
   (a swampy olive for green) — the *-soft tokens key the fill off a cleaner, mid
   shade while --cl keeps the text legible. */
#app-grimoire .markdown-body .g-callout{--cl:var(--mass-accent);--cl-tint:var(--mass-accent-soft);margin:0.8em 0;border-radius:0.4rem;border:1px solid color-mix(in srgb,var(--cl) 28%,transparent);background:var(--cl-tint);overflow:hidden}
#app-grimoire .markdown-body .g-callout-head{display:flex;align-items:center;gap:0.5rem;padding:0.5rem 0.8rem;font-weight:600;color:var(--cl);background:color-mix(in srgb,var(--cl) 8%,transparent)}
#app-grimoire .markdown-body .g-callout-head sl-icon{font-size:1rem}
#app-grimoire .markdown-body .g-callout-body{padding:0.2em 0.8rem 0.5rem}
#app-grimoire .markdown-body .g-callout-body>*:first-child{margin-top:0.4em}
#app-grimoire .markdown-body .g-callout-body>*:last-child{margin-bottom:0}
#app-grimoire .markdown-body .g-callout-note,#app-grimoire .markdown-body .g-callout-info{--cl:var(--mass-accent);--cl-tint:var(--mass-accent-soft)}
#app-grimoire .markdown-body .g-callout-tip,#app-grimoire .markdown-body .g-callout-hint,#app-grimoire .markdown-body .g-callout-success,#app-grimoire .markdown-body .g-callout-check,#app-grimoire .markdown-body .g-callout-done{--cl:var(--mass-success);--cl-tint:var(--mass-success-soft)}
#app-grimoire .markdown-body .g-callout-warning,#app-grimoire .markdown-body .g-callout-caution,#app-grimoire .markdown-body .g-callout-important{--cl:var(--mass-warning);--cl-tint:var(--mass-warning-soft)}
#app-grimoire .markdown-body .g-callout-danger,#app-grimoire .markdown-body .g-callout-error,#app-grimoire .markdown-body .g-callout-bug{--cl:var(--mass-danger);--cl-tint:var(--mass-danger-soft)}
#app-grimoire .markdown-body .g-callout-question,#app-grimoire .markdown-body .g-callout-faq,#app-grimoire .markdown-body .g-callout-example{--cl:var(--mass-text-muted);--cl-tint:var(--mass-bg-hover)}
#app-grimoire .markdown-body table{border-collapse:collapse;margin:0.6em 0}
#app-grimoire .markdown-body th,#app-grimoire .markdown-body td{border:1px solid var(--mass-border);padding:0.35rem 0.6rem;text-align:left}
#app-grimoire .markdown-body img{max-width:100%}
</style>`

// RenderPage returns the grimoire HTML fragment (without layout wrapper).
// theme and logLevel seed the settings menu's current values. theme is
// normalized so a stale/unknown persisted name can't seed a bogus appTheme
// signal or check a nonexistent picker entry.
func RenderPage(theme, logLevel string, st State) string {
	name := string(uikit.ParseTheme(theme))
	var buf bytes.Buffer
	_ = grimoirePage(settingsMenu(logLevel, name, st), name, st).Render(context.Background(), &buf)
	return buf.String()
}

// settingsMenu composes Grimoire's own gear menu from the SDK pieces plus its
// app-specific controls — the SDK provides the shell and the reusable Log Level
// control; the menu's contents are Grimoire's. The theme has a dedicated palette
// picker in the bottom bar, so the menu seeds the appTheme signal (ThemeSignal)
// without a theme picker of its own. The Trash select governs soft-delete; it
// binds to gTrashMode and posts to /api/trash-mode. The build version closes the
// menu as a plain footer line.
func settingsMenu(logLevel, theme string, st State) string {
	return uikit.ThemeSignal(theme) + uikit.SettingsShell(
		uikit.LogLevelSelect(logLevel),
		trashModeSelect(st.TrashMode),
		uikit.ConnectionSection(st.Conn.Endpoint, st.Conn.HasToken, st.Conn.CACert),
		versionLine(st.Version),
	)
}

// versionLine is the running build's version at the foot of the settings menu:
// one faint label/value row, not a section of its own — the menu is 220px wide.
// The value is shown exactly as the build stamped it ("dev" without ldflags,
// otherwise a git describe). An unset version drops the line.
func versionLine(version string) string {
	if version == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="g-version"><span>Version</span><span class="g-version-value">%s</span></div>`,
		html.EscapeString(version))
}

// trashModeSelect is the soft-delete policy control in the settings menu: a
// three-stop sliding toggle (a pill track with a dot per stop and a thumb that
// slides to the active one) for trashing every delete (restorable from the Files
// tab), only AI-agent deletes, or none. The thumb's position and colour
// (blue / yellow / grey) follow the track's data-mode attribute, which initTrashMode
// (grimoire.js) sets from the current value and updates on click — and which is
// seeded here from the persisted mode so there's no first-paint flash. The current
// label is shown beside the title, mirroring the editor's "Effort (Max)" toggle.
func trashModeSelect(mode string) string {
	if mode == "" {
		mode = string(appconfig.TrashAll)
	}
	type stop struct{ value, state, hint string }
	// Ordered off → agents → everyone, so the default (trash on for all) sits at
	// the right end and "off" at the left, like a volume rising left to right. state
	// names the current setting beside the toggle ("Trash <state>") and is the hint.
	stops := []stop{
		{string(appconfig.TrashOff), "Disabled", "Delete permanently for everyone"},
		{string(appconfig.TrashAgents), "Enabled for agents", "Trash only AI-agent deletes; your own deletes are permanent"},
		{string(appconfig.TrashAll), "Enabled", "Trash every delete (restorable)"},
	}
	var dots strings.Builder
	curState := ""
	for _, s := range stops {
		if s.value == mode {
			curState = s.state
		}
		fmt.Fprintf(&dots,
			`<button type="button" class="g-trash-stop" role="radio" data-value=%q data-state=%q title=%q><span class="g-trash-dot"></span></button>`,
			html.EscapeString(s.value), html.EscapeString(s.state), html.EscapeString(s.hint))
	}
	return fmt.Sprintf(`<div class="g-trash-field">
			<span class="g-trash-title">Trash</span>
			<div class="g-trash-row">
				<div class="g-trash-mode" id="g-trash-mode" role="radiogroup" aria-label="Trash deleted notes" data-mode=%[2]q title="Deleted notes can move to the Trash (in Files) to be restored, or be removed permanently — choose for whom.">
					<span class="g-trash-thumb" aria-hidden="true"></span>
					%[3]s
				</div>
				<span class="g-trash-state" id="g-trash-value">%[1]s</span>
			</div>
		</div>`,
		html.EscapeString(curState),
		html.EscapeString(mode),
		dots.String(),
	)
}

// RenderFullPage returns a complete HTML page with the grimoire UI. theme is a
// registered theme name (built-in "dark"/"light" or a pluggable one); an
// unknown name is normalized to the default. theme and logLevel seed the
// settings.
func RenderFullPage(theme, logLevel string, st State) string {
	name := uikit.ParseTheme(theme)
	return uikit.Layout(appTitle, RenderPage(string(name), logLevel, st), name)
}

// appTitle is the document <title> and Layout heading.
const appTitle = "Grimoire"
