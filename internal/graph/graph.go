// Package graph builds a similarity graph over notes from their embedding
// centroids: nodes are notes, edges connect each note to its nearest neighbours
// by cosine similarity. It is pure (no I/O) so it is cheap to test — the caller
// supplies the per-note unit vectors (see store.NoteVectors) and gets back a
// node/edge set ready to hand to a force-directed view.
//
// The graph is a symmetric k-nearest-neighbour graph, not a thresholded
// all-pairs graph: each note keeps its top-K most similar neighbours (above a
// floor), and an edge survives if either endpoint chose the other. kNN keeps the
// graph sparse and readable as the vault grows; all-pairs would be O(n²) and
// collapse into a hairball.
package graph

import (
	"path"
	"sort"
	"strings"
)

// Params tunes the graph's density.
type Params struct {
	K             int     // neighbours kept per note (clamped to ≥1).
	MinSimilarity float64 // cosine floor; neighbours below it are dropped.
}

// Node is one note in the graph.
type Node struct {
	ID     string `json:"id"`     // vault-relative path (stable identity).
	Title  string `json:"title"`  // display label (file basename without extension).
	Degree int    `json:"degree"` // number of edges touching this node.
}

// Edge is an undirected similarity link between two notes, A < B by ID so each
// pair appears once. Weight is the cosine similarity in (MinSimilarity, 1].
type Edge struct {
	A      string  `json:"source"`
	B      string  `json:"target"`
	Weight float64 `json:"weight"`
}

// Graph is the built result: every note as a node, plus the surviving edges.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Build constructs the similarity graph from unit-length note vectors (path →
// vector). Vectors are assumed L2-normalized, so cosine similarity is their dot
// product. Output is deterministic: nodes and edges are sorted by ID.
func Build(vectors map[string][]float32, p Params) Graph {
	if p.K < 1 {
		p.K = 1
	}

	// Stable node order so the result (and any layout seeded from it) is
	// reproducible regardless of map iteration order.
	ids := make([]string, 0, len(vectors))
	for id := range vectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Each note's top-K neighbours above the floor. Edge keyed by the ordered
	// pair so a–b and b–a collapse to one entry; keep the max weight seen.
	type pair struct{ a, b string }
	edges := make(map[pair]float64)
	for _, a := range ids {
		neighbours := topK(a, ids, vectors, p)
		for _, n := range neighbours {
			key := pair{a, n.id}
			if a > n.id {
				key = pair{n.id, a}
			}
			if w, ok := edges[key]; !ok || n.sim > w {
				edges[key] = n.sim
			}
		}
	}

	degree := make(map[string]int, len(ids))
	out := Graph{
		Nodes: make([]Node, 0, len(ids)),
		Edges: make([]Edge, 0, len(edges)),
	}
	for key, w := range edges {
		out.Edges = append(out.Edges, Edge{A: key.a, B: key.b, Weight: w})
		degree[key.a]++
		degree[key.b]++
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].A != out.Edges[j].A {
			return out.Edges[i].A < out.Edges[j].A
		}
		return out.Edges[i].B < out.Edges[j].B
	})
	for _, id := range ids {
		out.Nodes = append(out.Nodes, Node{ID: id, Title: title(id), Degree: degree[id]})
	}
	return out
}

// neighbour is a candidate edge target with its similarity to the source.
type neighbour struct {
	id  string
	sim float64
}

// topK returns the up-to-K most similar notes to a (excluding a itself) whose
// similarity clears the floor, highest first.
func topK(a string, ids []string, vectors map[string][]float32, p Params) []neighbour {
	va := vectors[a]
	cand := make([]neighbour, 0, len(ids))
	for _, b := range ids {
		if b == a {
			continue
		}
		sim := dot(va, vectors[b])
		if sim < p.MinSimilarity {
			continue
		}
		cand = append(cand, neighbour{id: b, sim: sim})
	}
	sort.Slice(cand, func(i, j int) bool {
		if cand[i].sim != cand[j].sim {
			return cand[i].sim > cand[j].sim
		}
		return cand[i].id < cand[j].id // tie-break for determinism.
	})
	if len(cand) > p.K {
		cand = cand[:p.K]
	}
	return cand
}

// dot is the cosine similarity of two unit vectors (their dot product). Mismatched
// lengths yield 0 (treated as unrelated) rather than panicking.
func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// title is the display label for a note: its file basename without extension.
func title(id string) string {
	base := path.Base(id)
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
