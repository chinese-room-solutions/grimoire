package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelativeDate(t *testing.T) {
	ref := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return ref }
	t.Cleanup(func() { now = time.Now })

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"just now", ref.Add(-30 * time.Second), "just now"},
		{"minutes", ref.Add(-5 * time.Minute), "5m ago"},
		{"hours", ref.Add(-3 * time.Hour), "3h ago"},
		{"days", ref.Add(-2 * 24 * time.Hour), "2d ago"},
		{"a week falls back to a date", ref.Add(-8 * 24 * time.Hour), "Jun 6"},
		{"prior year keeps the year", time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC), "Mar 2, 2024"},
		{"future shows the date", ref.Add(48 * time.Hour), "Jun 16"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, RelativeDate(tc.in))
		})
	}
}

func TestAbsoluteAndFullDate(t *testing.T) {
	ref := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return ref }
	t.Cleanup(func() { now = time.Now })

	require.Equal(t, "", AbsoluteDate(time.Time{}))
	require.Equal(t, "Jun 12", AbsoluteDate(time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, "Jun 12, 2024", AbsoluteDate(time.Date(2024, 6, 12, 0, 0, 0, 0, time.UTC)))

	require.Equal(t, "", FullDate(time.Time{}))
	require.Equal(t, "2026-06-12 15:04:09 UTC", FullDate(time.Date(2026, 6, 12, 15, 4, 9, 0, time.UTC)))
}
