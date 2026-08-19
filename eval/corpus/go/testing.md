---
tags: [golang, testing]
---

# Testing in Go

## Table-driven by default

```go
func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{name: "seconds", in: "30s", want: 30 * time.Second},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
```

Subtests give you `-run 'TestParseDuration/empty'` and independent failures.
`require` stops the subtest at the first failed assertion, `assert` continues;
use `require` for preconditions and `assert` when several independent checks are
genuinely informative together.

## Helpers and cleanup

`t.Helper()` in any assertion helper, so failures point at the caller's line.
`t.Cleanup(fn)` instead of defer when the setup lives in a helper — it runs at
the end of the test that registered it, including for subtests, and it composes
where defer does not. `t.TempDir()` and `t.Setenv()` clean up and, in the case
of `Setenv`, refuse to run under `t.Parallel()`, which is the correct behaviour
and occasionally an annoying one.

## Parallelism

`t.Parallel()` at the top of a subtest pauses it until the parent returns, then
runs the batch together. Combined with `-race` this is where real concurrency
bugs surface. Anything sharing a fixture has to be safe for it; a package-level
variable mutated by two parallel tests fails intermittently in CI and never
locally.

## What to fake

Prefer the real thing when it is cheap: an in-memory SQLite file, `httptest.NewServer`
for an HTTP dependency, `net.Pipe` for a connection. Interfaces at the seam, per
the repo's abstraction rule, and a hand-written stub is usually clearer than a
generated mock. Mocks that assert on call order test your implementation, and
they fail on every refactor that changes nothing observable.

For the cluster controllers, `envtest` boots a real API server so reconciliation
is tested against real validation and RBAC; see [[operators#Testing]].

## Golden files

`-update` flag, write the output, diff it in CI. Great for anything that
serializes: rendered templates, generated SQL, CLI output. Keep them small
enough to review in a diff, or they become a rubber stamp.

## Fuzzing

```go
func FuzzParse(f *testing.F) {
	f.Add("30s")
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseDuration(s)
		if err != nil {
			return
		}
		round, err := ParseDuration(got.String())
		require.NoError(t, err)
		require.Equal(t, got, round)
	})
}
```

Round-trip and invariant properties are what fuzzing is good at. Failures land
in `testdata/fuzz/` and become regular test cases forever after.

## Benchmarks

`b.ReportAllocs()`, `b.ResetTimer()` after setup, and `benchstat` over ten runs
of old and new — a single run tells you nothing given CPU frequency scaling.
Assign the result to a package-level sink so the compiler cannot eliminate the
work.

## Coverage

Useful as a map of what is untested, useless as a target. `go test -cover
./...` for the number, `-coverprofile` plus `go tool cover -html` to see which
branches nobody exercises. The uncovered error paths are where the real bugs
live, because they are the ones nobody has ever run.
