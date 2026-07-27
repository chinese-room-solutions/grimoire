package fence

import "testing"

func TestLang(t *testing.T) {
	tests := []struct{ info, want string }{
		{"", ""},
		{"go", "go"},
		{"Go", "go"},
		{"go title=x", "go"},
		{"go {kernel=go} {version=1.21}", "go"},
		{"  bash", ""}, // a leading space means the first word is empty.
		{"BASH extra", "bash"},
	}
	for _, tc := range tests {
		if got := Lang(tc.info); got != tc.want {
			t.Errorf("Lang(%q) = %q, want %q", tc.info, got, tc.want)
		}
	}
}

func TestKernel(t *testing.T) {
	tests := []struct{ info, want string }{
		{"go", ""},
		{"go {kernel=go}", "go"},
		{"go {kernel=go} {version=1.21}", "go"},
		{"go {kernel=yaegi} title=x", "yaegi"},
		{"go title=x {kernel=go}", "go"},
		{"go {kernel= go }", "go"}, // trimmed.
		{"go {kernel=}", ""},       // empty family.
		{"go {kernel=go", ""},      // unterminated.
		{"go {version=1.21}", ""},  // only a version, no family.
	}
	for _, tc := range tests {
		if got := Kernel(tc.info); got != tc.want {
			t.Errorf("Kernel(%q) = %q, want %q", tc.info, got, tc.want)
		}
	}
}

func TestVersion(t *testing.T) {
	tests := []struct{ info, want string }{
		{"go", ""},
		{"go {version=1.21}", "1.21"},
		{"go {kernel=go} {version=1.21}", "1.21"},
		{"go {version= 0.16.1 }", "0.16.1"}, // trimmed.
		{"go {version=}", ""},               // empty.
		{"go {version=1.21", ""},            // unterminated.
		{"go {kernel=go}", ""},              // only a family, no version.
	}
	for _, tc := range tests {
		if got := Version(tc.info); got != tc.want {
			t.Errorf("Version(%q) = %q, want %q", tc.info, got, tc.want)
		}
	}
}
