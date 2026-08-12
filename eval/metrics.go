package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/chinese-room-solutions/grimoire/internal/store"
)

// query is one labelled evaluation query: the text a user would type, a kind
// used to aggregate comparable queries together, and the qrels — vault-relative
// note paths graded 1 (related) to 3 (exactly this note). Any path absent from
// Relevant is graded 0.
type query struct {
	ID       string         `json:"id"`
	Query    string         `json:"query"`
	Kind     string         `json:"kind"`
	Relevant map[string]int `json:"relevant"`
}

// relevantGrade is the lowest grade that counts as a relevant note for the
// set-based metrics (MRR, recall). Grade 1 means "related", not "an answer", so
// it contributes to the graded nDCG but not to whether the search found what
// was asked for.
const relevantGrade = 2

// maxGrade is the highest qrel grade the loader accepts.
const maxGrade = 3

// loadQueries reads and validates the query set.
func loadQueries(path string) ([]query, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading queries: %w", err)
	}
	var qs []query
	if err := json.Unmarshal(data, &qs); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("%s has no queries", path)
	}
	seen := make(map[string]bool, len(qs))
	for i, q := range qs {
		if q.ID == "" {
			return nil, fmt.Errorf("query %d has no id", i)
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("duplicate query id %q", q.ID)
		}
		seen[q.ID] = true
		if q.Query == "" {
			return nil, fmt.Errorf("query %q has no text", q.ID)
		}
		for p, g := range q.Relevant {
			if g < 1 || g > maxGrade {
				return nil, fmt.Errorf("query %q: grade %d for %q outside 1..%d", q.ID, g, p, maxGrade)
			}
		}
	}
	return qs, nil
}

// noteHit is a note-level result: the best-ranked chunk the search returned for
// that note. Hit lists are chunk-level and one note can occupy several slots,
// while the qrels judge notes, so scoring collapses to the first occurrence.
type noteHit struct {
	store.Hit
}

// collapse reduces a chunk-level hit list to note level, keeping each note's
// first (best-ranked) chunk and the order the search produced.
func collapse(hits []store.Hit) []noteHit {
	seen := make(map[string]bool, len(hits))
	out := make([]noteHit, 0, len(hits))
	for _, h := range hits {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		out = append(out, noteHit{Hit: h})
	}
	return out
}

// scores are one query's (or one aggregate's) retrieval metrics. A metric is
// nil when the qrels can't define it — nDCG needs at least one graded note,
// MRR and recall at least one note graded relevantGrade or better — so an
// undefined metric is excluded from the means rather than counted as zero.
type scores struct {
	NDCG10 *float64 `json:"ndcg@10"`
	MRR10  *float64 `json:"mrr@10"`
	R5     *float64 `json:"recall@5"`
	R10    *float64 `json:"recall@10"`
}

// score grades one query's note-level result list.
func score(q query, notes []noteHit) scores {
	var s scores
	if ideal := idealDCG(q.Relevant, resultK); ideal > 0 {
		s.NDCG10 = ptr(dcg(q, notes, resultK) / ideal)
	}
	relevant := 0
	for _, g := range q.Relevant {
		if g >= relevantGrade {
			relevant++
		}
	}
	if relevant == 0 {
		return s
	}
	s.MRR10 = ptr(reciprocalRank(q, notes, resultK))
	s.R5 = ptr(float64(found(q, notes, 5)) / float64(relevant))
	s.R10 = ptr(float64(found(q, notes, resultK)) / float64(relevant))
	return s
}

// dcg is the discounted cumulative gain of the first n notes: each note's grade
// discounted by log2 of its 1-based rank plus one.
func dcg(q query, notes []noteHit, n int) float64 {
	var sum float64
	for i, note := range notes[:min(len(notes), n)] {
		sum += float64(q.Relevant[note.Path]) / math.Log2(float64(i)+2)
	}
	return sum
}

// idealDCG is the DCG of the best possible ranking: every graded note in
// descending grade order.
func idealDCG(qrels map[string]int, n int) float64 {
	grades := make([]int, 0, len(qrels))
	for _, g := range qrels {
		grades = append(grades, g)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	var sum float64
	for i, g := range grades[:min(len(grades), n)] {
		sum += float64(g) / math.Log2(float64(i)+2)
	}
	return sum
}

// reciprocalRank is 1/rank of the first note graded relevantGrade or better
// within the first n, or 0 when none is.
func reciprocalRank(q query, notes []noteHit, n int) float64 {
	for i, note := range notes[:min(len(notes), n)] {
		if q.Relevant[note.Path] >= relevantGrade {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// found counts the notes graded relevantGrade or better among the first n.
func found(q query, notes []noteHit, n int) int {
	hits := 0
	for _, note := range notes[:min(len(notes), n)] {
		if q.Relevant[note.Path] >= relevantGrade {
			hits++
		}
	}
	return hits
}

// aggregate averages each metric over the queries that define it.
func aggregate(all []scores) scores {
	mean := func(of func(scores) *float64) *float64 {
		var sum float64
		var n int
		for _, s := range all {
			if v := of(s); v != nil {
				sum += *v
				n++
			}
		}
		if n == 0 {
			return nil
		}
		return ptr(sum / float64(n))
	}
	return scores{
		NDCG10: mean(func(s scores) *float64 { return s.NDCG10 }),
		MRR10:  mean(func(s scores) *float64 { return s.MRR10 }),
		R5:     mean(func(s scores) *float64 { return s.R5 }),
		R10:    mean(func(s scores) *float64 { return s.R10 }),
	}
}

func ptr(v float64) *float64 { return &v }
