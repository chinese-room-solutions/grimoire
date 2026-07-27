package pdfconvert

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewExtractor_InvalidPDF(t *testing.T) {
	_, err := NewExtractor([]byte("not a pdf"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPDFParse)
}

func TestNewExtractor_EmptyData(t *testing.T) {
	_, err := NewExtractor(nil)
	require.Error(t, err)
}

func TestExtractor_ExtractPage_RealPDF(t *testing.T) {
	pdfData, err := os.ReadFile("../../testdata/simple.pdf")
	if os.IsNotExist(err) {
		t.Skip("testdata/simple.pdf not found — skipping real PDF test")
	}
	require.NoError(t, err)

	extractor, err := NewExtractor(pdfData)
	require.NoError(t, err)

	numPages := extractor.NumPages()
	require.Positive(t, numPages)
	t.Logf("PDF has %d pages", numPages)

	for i := 1; i <= numPages; i++ {
		text, err := extractor.ExtractPage(i)
		require.NoError(t, err)
		t.Logf("page %d: %d chars", i, len(text))
	}
}

func TestExtractor_ExtractPage_OutOfRange(t *testing.T) {
	pdfData, err := os.ReadFile("../../testdata/simple.pdf")
	if os.IsNotExist(err) {
		t.Skip("testdata/simple.pdf not found — skipping")
	}
	require.NoError(t, err)

	extractor, err := NewExtractor(pdfData)
	require.NoError(t, err)

	// Out-of-range page returns empty string, no error.
	text, err := extractor.ExtractPage(99999)
	require.NoError(t, err)
	require.Empty(t, text)
}
