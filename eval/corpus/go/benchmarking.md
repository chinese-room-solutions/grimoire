---
tags: [golang, benchmarking, performance]
---

# Benchmarking in Go

The mechanics are in [[testing#Benchmarks]]; this is the methodology, because
a benchmark that produces a number is easy and a benchmark that produces a
*decision* is not.

## The harness

Since 1.24:

```go
func BenchmarkIndexLookup(b *testing.B) {
	idx := buildIndex(b)
	for b.Loop() {
		_, _ = idx.Lookup("query-42")
	}
}
```

`b.Loop()` replaces the `for i := 0; i < b.N; i++` idiom and quietly fixes
its two footguns: parameters of the benchmark function stay alive (no more
accidental dead-code elimination of the entire body because the compiler
proved the result unused), and per-iteration setup stops needing
`b.StopTimer`/`b.StartTimer` scaffolding, which never measured what people
hoped — the timer toggling itself cost more than the code under test on small
bodies. `_ =` for the result is fine here; the point is the side effect of
the call staying reachable.

On older modules: classic loop, package-level sink variable for results,
`b.ReportAllocs()` always, `b.ResetTimer()` after any setup that must stay.

## The statistics

One run means nothing. Frequency scaling, thermal states, and noisy
neighbours on a shared CI box put 10-30% wobble on numbers that people then
ship decisions about. The minimum viable protocol:

```
go test -bench . -count=10 -benchmem ./... | tee old.txt
# change the code
go test -bench . -count=10 -benchmem ./... | tee new.txt
benchstat old.txt new.txt
```

`benchstat` reports the delta with a p-value. If the confidence interval
straddles zero, the honest summary is "no measurable change", and the change
should ship or not on simplicity grounds, not performance folklore. `-count=10`
is the floor; on a laptop, 20 is better, and on shared CI runners the numbers
are directional at best — run the deciding comparisons locally, twice, at
different times of day.

## What to measure

- **Allocations first, time second.** `b.ReportAllocs()` output (allocs/op,
  B/op) is deterministic and reproducible in a way nanoseconds are not. Most
  "make it faster" work in Go services is actually "allocate less" work:
  escapes to heap, interface boxing, per-request buffers. The same framing as
  [[pgo#What it will not fix]] — the profiler, not the compiler, is what
  removes allocations.
- **Real shapes.** A lookup benchmark over a 10-element map tells you nothing
  about the 100k-entry index. Benchmark at production scale, and if the
  interesting cost is a corner (cold cache, worst-case key), write the
  benchmark that produces the corner.
- **End-to-end occasionally.** Micro-benchmarks optimise the function; the
  service still waits on the database. PGO profiles from production traffic,
  per [[pgo]], are the antidote to over-tuning code that is 0.1% of request
  time.

## Parallel benchmarks

`b.RunParallel(func(pb *testing.PB) { for pb.Next() { ... } })` exercises
contended paths — the mutex-guarded map, the singleflight cache. Throughput
under parallelism surfaces lock contention that a serial benchmark literally
cannot see, and results scale sub-linearly the moment the mutex is the
bottleneck. Watch CPU: a parallel benchmark pegged at 200% on an 8-core box
is telling you the serialization point directly.

## Traps

- **Timer pollution.** Setup inside the measured region, including `fmt`
  debugging left in. With `b.Loop()` this class mostly disappears; the
  remaining version is setup that depends on the iteration.
- **Compiler folding.** Constant arguments propagate and the whole call
  becomes a constant. Vary the input across iterations (index by `b.N` or a
  rotating counter) when the function is pure.
- **Comparing across machines.** Absolute numbers are machine-local; only
  deltas on the same box, same session, are meaningful. Commit the benchstat
  output alongside the change so the next person inherits the baseline, not a
  folklore "about 30% faster" comment in a merged PR.
- **Winning the wrong benchmark.** The fastest implementation that complicates
  the code everyone touches daily is usually a net loss; the benchmark is one
  input into a judgement that also weighs [[error-handling]] and readability.

A profile next to the benchmark closes the loop: `go tool pprof` on the
benchmark's cpu/allocs profile shows *why* the number moved, which is the part
worth writing down.
