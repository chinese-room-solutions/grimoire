package ui

import (
	"fmt"
	"time"
)

// now is the clock used for relative dates; a var so tests can pin it.
var now = time.Now

// RelativeDate renders t as a short, human relative string ("just now", "2h
// ago", "3d ago", "Jun 12", "Jun 12, 2024"). A zero time yields "". Recent times
// read relatively; older ones fall back to an absolute calendar date so the label
// stays meaningful at any age.
func RelativeDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now().Sub(t)
	switch {
	case d < 0:
		return AbsoluteDate(t) // future (clock skew): just show the date.
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return AbsoluteDate(t)
	}
}

// AbsoluteDate renders t as a calendar date, omitting the year when it's the
// current year ("Jun 12" vs "Jun 12, 2024"). A zero time yields "".
func AbsoluteDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if t.Year() == now().Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// FullDate renders t as a precise local timestamp for tooltips, down to the
// second and including the timezone ("2026-06-14 15:04:09 CEST"). A zero time
// yields "".
func FullDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05 MST")
}
