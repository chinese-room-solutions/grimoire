---
tags: [go, performance]
---

# PGO

The compiler makes better decisions when it has seen the program run. Drop a
`default.pgo` next to `main.go` and the build picks it up automatically — no
flag, no tag, nothing to remember in CI.

## What PGO actually does

- **Inlining.** Hot call sites get a much larger budget, including through
  interface calls that the compiler would otherwise refuse to touch.
- **Devirtualisation.** A call through an interface whose dynamic type is nearly
  always the same becomes a direct call with a type check and a fallback. This
  is where most of the win lives, because it turns an opaque call into something
  the inliner can then eat.
- **Basic block ordering**, so the common path stays in cache.

Expect a few percent on a service that spends its time in Go, and close to
nothing on one that spends its time waiting on the network or the database.
It compounds with everything else and costs one file.

## Getting a profile that means something

Take it from production, under real traffic, or the compiler optimises for a
benchmark's shape rather than the workload's:

```
curl -o cpu.pprof 'http://svc:6060/debug/pprof/profile?seconds=60'
go tool pprof -proto a.pprof b.pprof c.pprof > default.pgo
```

Profiles merge, so collect from several replicas across a normal hour and merge
them. Thirty to sixty seconds each; a five-second sample is mostly noise.

Exposing the profiling endpoint means exposing it on an internal listener, not
on the public mux — registering `net/http/pprof` also registers handlers on
`http.DefaultServeMux`, which is one careless `http.ListenAndServe(addr, nil)`
away from being on the internet; see
[[http-services#Server defaults are wrong]].

## Keeping it fresh

Commit the profile. It is an input to the build and a build must be
reproducible; a profile fetched at build time makes yesterday's binary
unbuildable.

Refresh it monthly, or when the service changes shape. A stale profile is not
dangerous — the compiler treats it as a hint and an unrecognised function is
simply not hot — so the failure mode is losing the benefit, quietly. The
iterative loop everyone worries about (build with a profile, profile the result,
rebuild) converges after one round; the second iteration is not worth the
ceremony.

Measure it like any other change: a benchmark with `-count=10` and `benchstat`,
not a single run and a feeling, see [[testing#Benchmarks]]. Watch build time too
— the extra inlining is not free and I have seen a large binary take a fifth
longer to compile.

## What it will not fix

Allocation. PGO will not remove a single one. If the profile is dominated by the
garbage collector, the fix is fewer allocations — reuse buffers, avoid the
interface boxing that escapes, give the slice a capacity — and no amount of
compiler information changes the number of objects the program asks for. Same
for lock contention: see [[concurrency#sync primitives]] for the actual
remedies. This is a last few percent tool, applied after the algorithmic work,
and reaching for it first is a way to feel busy.
