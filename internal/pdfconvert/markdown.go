package pdfconvert

import (
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)

// mdConverter is configured once: the structured HTML the model emits uses
// tables and strikethrough (<del>) on top of CommonMark constructs, so the
// GFM table + strikethrough plugins are added to the base set. Reused across
// conversions — the converter is safe for sequential use.
var mdConverter = converter.NewConverter(
	converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		strikethrough.NewStrikethroughPlugin(),
		table.NewTablePlugin(),
	),
)

// HTMLToMarkdown converts the combined structured HTML into GitHub-flavored
// Markdown. Tags with no Markdown equivalent (math, chem, sup/sub, forms) fall
// back to their text content, so the result stays readable even where it can't
// be fully expressed in Markdown.
//
// On a conversion error it returns the error and an empty string; callers treat
// Markdown as a best-effort companion to the HTML, never a hard failure.
func HTMLToMarkdown(html string) (string, error) {
	if html == "" {
		return "", nil
	}
	return mdConverter.ConvertString(html)
}
