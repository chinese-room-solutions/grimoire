---
tags: [golang, types]
---

# Generics

Type parameters landed in 1.18. Three years on, the useful summary is: they are
for containers and small algorithms, and they are usually the wrong answer for
business logic.

## Syntax

```go
func Map[T, U any](s []T, f func(T) U) []U {
	out := make([]U, len(s))
	for i, v := range s {
		out[i] = f(v)
	}
	return out
}

type Number interface {
	~int | ~int64 | ~float64
}

func Sum[T Number](s []T) T {
	var total T
	for _, v := range s {
		total += v
	}
	return total
}
```

The `~` means "any type whose underlying type is this", so a
`type Celsius float64` satisfies the constraint. Without the tilde, only the
exact predeclared type matches, which is almost never what you want.

Constraints are interfaces. A constraint with method requirements can be used as
an ordinary interface too; one with a type union cannot be used as a variable
type at all.

## Inference

Type arguments are inferred from the function arguments, so `Map(xs, f)` works
but a function with a type parameter only in the return type must be called
explicitly: `Zero[int]()`. Inference does not flow through a returned function's
type, and it does not look at assignment context.

Methods cannot have their own type parameters. This is the rule that kills the
fluent chained-transform API everyone tries to write first — you can have
`Map[T, U]` as a function, but not `(*Stream[T]).Map[U]`.

## The standard library

- `slices` — `Sort`, `SortFunc`, `BinarySearch`, `Contains`, `Index`, `Insert`,
  `Delete`, `Clone`, `Equal`, `Max`, `Min`.
- `maps` — `Keys`, `Values` (iterators since 1.23), `Clone`, `Equal`,
  `DeleteFunc`.
- `cmp` — `Ordered`, `Compare`, `Or`.
- `sync.OnceValue` / `OnceValues`, the typed version of the pattern in
  [[concurrency#sync primitives]].

`slices.SortFunc` with a comparison returning an int replaced most `sort.Slice`
calls; it is faster because there is no reflection and no interface boxing.

Combined with range-over-func iterators (`iter.Seq[T]`), the composition story
finally works without allocating intermediate slices at every step.

## When to use them

Good: data structures (a typed set, an LRU cache, a work queue), small pure
helpers over slices and maps, a result or option type if your team wants one,
and cutting `interface{}` plus a type assertion out of a hot path.

Bad: as a substitute for an interface. If the behaviour differs per type, that
is polymorphism and an interface expresses it more clearly and compiles faster.
Also bad: constraints with five type parameters. When the signature needs a
diagram, the abstraction is in the wrong place — the repo rule about
abstractions belonging at the seams applies here exactly.

## Costs

The compiler uses GC shape stenciling: one instantiation per distinct pointer
shape, with a dictionary passed at runtime for the rest. So generic code over
pointer types is not specialized per type, and a generic function can be
*slower* than the concrete one it replaced because of dictionary indirection and
lost inlining. Benchmark before converting anything that matters.

Build times and error messages both get worse. The messages in particular go
from "cannot use x (type Foo) as type Bar" to a paragraph about constraint
satisfaction that takes a minute to parse.
