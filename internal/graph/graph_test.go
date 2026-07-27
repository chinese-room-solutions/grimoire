package graph

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// edgeSet keys an undirected edge for membership assertions.
func edgeSet(g Graph) map[[2]string]float64 {
	m := make(map[[2]string]float64, len(g.Edges))
	for _, e := range g.Edges {
		m[[2]string{e.A, e.B}] = e.Weight
	}
	return m
}

func TestBuild_Empty(t *testing.T) {
	g := Build(nil, Params{K: 6, MinSimilarity: 0.3})
	require.Empty(t, g.Nodes)
	require.Empty(t, g.Edges)
}

func TestBuild_NodeForEveryNote(t *testing.T) {
	// Three mutually-orthogonal notes: with a positive floor no edges survive,
	// but every note still appears as a node (an isolated point in the view).
	vecs := map[string][]float32{
		"a.md": {1, 0, 0},
		"b.md": {0, 1, 0},
		"c.md": {0, 0, 1},
	}
	g := Build(vecs, Params{K: 6, MinSimilarity: 0.1})
	require.Len(t, g.Nodes, 3)
	require.Empty(t, g.Edges)
	require.Equal(t, "a", g.Nodes[0].Title) // basename without extension.
}

func TestBuild_ClusterConnectsOutlierStaysIsolated(t *testing.T) {
	// a/b/c point in nearly the same direction (a tight cluster); z is far.
	vecs := map[string][]float32{
		"a.md": unit(1, 0.1, 0),
		"b.md": unit(1, 0.0, 0),
		"c.md": unit(0.9, 0.2, 0),
		"z.md": unit(0, 0, 1),
	}
	g := Build(vecs, Params{K: 6, MinSimilarity: 0.5})
	es := edgeSet(g)
	// The cluster is fully connected; z joins nothing.
	require.Contains(t, es, [2]string{"a.md", "b.md"})
	require.Contains(t, es, [2]string{"a.md", "c.md"})
	require.Contains(t, es, [2]string{"b.md", "c.md"})
	for pair := range es {
		require.NotContains(t, pair, "z.md")
	}
	// z has degree 0; cluster members have degree 2.
	deg := degrees(g)
	require.Equal(t, 0, deg["z.md"])
	require.Equal(t, 2, deg["a.md"])
}

func TestBuild_ThresholdDropsWeakEdges(t *testing.T) {
	// a–b are similar (~0.8); a–c weak (~0.3). A 0.5 floor keeps a–b, drops a–c.
	vecs := map[string][]float32{
		"a.md": unit(1, 0, 0),
		"b.md": unit(1, 0.7, 0),
		"c.md": unit(0.3, 1, 0),
	}
	g := Build(vecs, Params{K: 6, MinSimilarity: 0.5})
	es := edgeSet(g)
	require.Contains(t, es, [2]string{"a.md", "b.md"})
	require.NotContains(t, es, [2]string{"a.md", "c.md"})
}

func TestBuild_KClampsNeighbours_ButUnionKeepsMutualChoices(t *testing.T) {
	// Four near-identical notes. K=1 means each picks its single best neighbour,
	// but the *union* of those directed picks can still leave a node degree>1
	// when others choose it. The point: no node initiates more than K edges.
	vecs := map[string][]float32{
		"a.md": unit(1, 0.01, 0),
		"b.md": unit(1, 0.02, 0),
		"c.md": unit(1, 0.03, 0),
		"d.md": unit(1, 0.04, 0),
	}
	g := Build(vecs, Params{K: 1, MinSimilarity: 0.5})
	// Every node has at least one edge (nobody is isolated in a dense cluster).
	deg := degrees(g)
	for id := range vecs {
		require.GreaterOrEqual(t, deg[id], 1, "node %s should be connected", id)
	}
	// Edges are undirected and de-duplicated: weight in (floor, 1].
	for _, e := range g.Edges {
		require.Less(t, e.A, e.B) // canonical ordering A<B.
		require.Greater(t, e.Weight, 0.5)
		require.LessOrEqual(t, e.Weight, 1.0+1e-9)
	}
}

func TestBuild_KDefaultsToOneWhenNonPositive(t *testing.T) {
	vecs := map[string][]float32{"a.md": unit(1, 0, 0), "b.md": unit(1, 0, 0)}
	g := Build(vecs, Params{K: 0, MinSimilarity: 0.5})
	require.Len(t, g.Edges, 1)
}

// degrees re-derives node degree by ID for assertions.
func degrees(g Graph) map[string]int {
	m := make(map[string]int, len(g.Nodes))
	for _, n := range g.Nodes {
		m[n.ID] = n.Degree
	}
	return m
}

// unit returns the L2-normalized form of the given components (Build assumes
// unit vectors, matching store.NoteVectors' output).
func unit(vals ...float32) []float32 {
	var sum float64
	for _, v := range vals {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return vals
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(vals))
	for i, v := range vals {
		out[i] = v * inv
	}
	return out
}
