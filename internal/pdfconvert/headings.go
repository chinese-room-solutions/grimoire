package pdfconvert

import "regexp"

// headingRe matches HTML heading tags (<h1>…</h5>, case-insensitive, including inline attributes).
var headingRe = regexp.MustCompile(`(?is)<h[1-5]\b[^>]*>.*?</h[1-5]>`)

// extractHeadings returns all <h1>…<h5> tags from the given HTML text, in document order.
func extractHeadings(html string) []string {
	return headingRe.FindAllString(html, -1)
}
