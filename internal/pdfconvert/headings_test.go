package pdfconvert

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractHeadings(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "no headings",
			html: "<p>Just a paragraph.</p><p>Another.</p>",
			want: nil,
		},
		{
			name: "single heading",
			html: "<h1>Title</h1><p>Some text.</p>",
			want: []string{"<h1>Title</h1>"},
		},
		{
			name: "multiple levels",
			html: "<h1>Chapter 1</h1><p>Intro.</p><h2>Section 1.1</h2><p>Body.</p><h3>Sub</h3>",
			want: []string{"<h1>Chapter 1</h1>", "<h2>Section 1.1</h2>", "<h3>Sub</h3>"},
		},
		{
			name: "heading with attributes",
			html: `<h2 id="sec1" class="main">Section</h2><p>x</p>`,
			want: []string{`<h2 id="sec1" class="main">Section</h2>`},
		},
		{
			name: "case insensitive tags",
			html: "<H1>Upper</H1><h2>Lower</h2>",
			want: []string{"<H1>Upper</H1>", "<h2>Lower</h2>"},
		},
		{
			name: "ignores h6 (out of allowed range)",
			html: "<h5>Included</h5><h6>Excluded</h6>",
			want: []string{"<h5>Included</h5>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHeadings(tt.html)
			if tt.want == nil {
				require.Empty(t, got)
			} else {
				require.Equal(t, tt.want, got)
			}
		})
	}
}
