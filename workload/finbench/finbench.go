// Package finbench implements the FinBench-derived transaction
// workloads of spec 06-workloads-oltp.md §4: "fb-read" (the eight
// TCR-shaped reads plus two simple reads) and "fb-write" (two writes),
// both over the harness fin generator's dataset ("fin-10k"). The
// official LDBC FinBench datagen is a Spark cluster job, so the queries
// run the TCR shapes on harness data and every result is labeled
// fidelity "derived" (ADR-7).
//
// FinBench distinctives preserved (spec 06 §4):
//
//   - Temporal windows: every windowed query takes $startTime/$endTime
//     and the curated pools (BuildPools) draw aligned windows from the
//     TRANSFER ts range that produce non-empty answers.
//   - Hub truncation: the spec bounds every traversal expansion at the
//     5000 most-recent edges. The references implement the cap
//     identically (see hubCap in oracle.go); at fin-10k scale windowed
//     expansions stay far below the cap, so the untruncated Cypher
//     texts ask the same question.
//
// Disclosed substitutions against the official TCRs:
//
//   - fb-tcr8 (guarantee risk): the fin schema has no guarantee edge, so
//     per the spec table the query chases loan money instead — the
//     "loan → transfer chain": Loan -DEPOSIT-> Account -TRANSFER*1..2->
//     Account, all inside the window.
//   - fb-tcr11 (recursive path with amount-decay filter): openCypher
//     cannot compare consecutive edge properties inside a
//     variable-length pattern, so the text is an explicit depth-3 chain
//     with a per-hop decay filter (amount_i+1 <= amount_i * $decay).
//
// The four windowed variable-length traversals (tcr1, tcr3, tcr4, tcr8) set
// NeedsPathPredicate, so an engine that cannot filter a variable-length
// expansion by a run-time predicate SKIPs them rather than answering. Pushing
// the window into the expansion is the only correct shape, and the ZuQL texts
// do it: the predicate sits inside the brackets and takes query parameters,
// so a drawn window binds. They once carried a Kùzu-dialect text of the same
// shape, dropped because that filter reads literals only, a query parameter
// in it being rejected by the binder and an enclosing WITH variable out of
// scope, so the text could never be bound to a drawn window. Filtering after
// the expansion instead is not a workaround but a different question, and it
// quietly undercounts: measured against the same reference it reported 792
// reachable accounts where 853 are reachable in the window, because the
// engine keeps one path per endpoint pair and the retained path may be the
// out-of-window one.
package finbench

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(readWorkload())
	workload.Register(writeWorkload())
}

// countRef builds a reference strategy whose answer is one integer row.
func countRef(col string, fn func(g *finGraph, p workload.Params) (int64, error)) *workload.RefStrategy {
	return &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := finFor(ds)
			if err != nil {
				return nil, err
			}
			n, err := fn(g, p)
			if err != nil {
				return nil, err
			}
			return &workload.Answer{Columns: []string{col}, Rows: [][]engine.Value{{n}}}, nil
		},
		Compare: workload.CompareSpec{CoerceNum: true},
	}
}

func readWorkload() *workload.Workload {
	return &workload.Workload{
		Name:     "fb-read",
		Title:    "FinBench-derived transaction reads (TCR shapes on harness data)",
		Family:   "finbench",
		Dataset:  "fin-10k",
		Fidelity: "derived",
		Queries: []*workload.Query{
			tcr1(), tcr2(), tcr3(), tcr4(), tcr5(),
			tcr8(), tcr11(), tcr12(), sr1(), sr2(),
		},
	}
}

// tcr1 mirrors TCR1: the k-hop (k=2) transfer-out subgraph of an
// account inside the window, hub-truncated, reported as the distinct
// account count. [Traversal]
func tcr1() *workload.Query {
	return &workload.Query{
		ID:                 "fb-tcr1",
		NeedsPathPredicate: true,
		Class:              engine.Traversal,
		PoolKey:            "fb-tcr1",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account {id: $id})-[es:TRANSFER*1..2]->(b:Account)
WHERE all(t IN es WHERE t.ts >= $startTime AND t.ts < $endTime)
RETURN count(DISTINCT b.id) AS reached`,
			// ZuQL puts the per-hop predicate inside the brackets, where
			// the expansion reads it, so the window is what the walk
			// follows rather than what a later filter throws away. That
			// is the same question the portable text asks and not the
			// same work: filtering after the fact keeps one path per
			// endpoint and can keep the out-of-window one.
			engine.ZuQL: `MATCH (a:Account {id: $id})-[t:TRANSFER*1..2 WHERE t.ts >= $startTime AND t.ts < $endTime]->(b:Account)
RETURN count(DISTINCT b.id) AS reached`,
		},
		Reference: countRef("reached", func(g *finGraph, p workload.Params) (int64, error) {
			id, err := pStr(p, "id")
			if err != nil {
				return 0, err
			}
			s, e, err := window(p)
			if err != nil {
				return 0, err
			}
			return int64(len(g.khopOut(id, s, e, 2))), nil
		}),
	}
}

// tcr2 mirrors TCR2: accounts receiving windowed transfers from more
// than $threshold distinct sources. [Aggregation]
func tcr2() *workload.Query {
	return &workload.Query{
		ID:      "fb-tcr2",
		Class:   engine.Aggregation,
		PoolKey: "fb-tcr2",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (s:Account)-[t:TRANSFER]->(a:Account)
WHERE t.ts >= $startTime AND t.ts < $endTime
WITH a, count(DISTINCT s.id) AS sources
WHERE sources > $threshold
RETURN a.id AS id, sources`,
			engine.ZuQL: `MATCH (s:Account)-[t:TRANSFER]->(a:Account)
WHERE t.ts >= $startTime AND t.ts < $endTime
WITH a, count(DISTINCT s.id) AS sources
WHERE sources > $threshold
RETURN a.id AS id, sources`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := finFor(ds)
				if err != nil {
					return nil, err
				}
				s, e, err := window(p)
				if err != nil {
					return nil, err
				}
				th, err := pInt(p, "threshold")
				if err != nil {
					return nil, err
				}
				return &workload.Answer{
					Columns:   []string{"id", "sources"},
					Rows:      g.fanIn(s, e, th),
					Unordered: true,
				}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// tcr3 mirrors TCR3: the shortest windowed transfer path between two
// accounts, bounded at 4 hops; an unreachable pair answers null. [Traversal]
func tcr3() *workload.Query {
	return &workload.Query{
		ID:                 "fb-tcr3",
		NeedsPathPredicate: true,
		Class:              engine.Traversal,
		PoolKey:            "fb-tcr3",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH p = (a:Account {id: $src})-[es:TRANSFER*1..4]->(b:Account {id: $dst})
WHERE all(t IN es WHERE t.ts >= $startTime AND t.ts < $endTime)
RETURN min(length(p)) AS hops`,
			// The same question asked in the form an engine with a
			// shortest-path operator can answer directly. The portable text
			// above enumerates every windowed path up to four hops and takes
			// the shortest, which is not what the query means — it is what a
			// language without a shortest-path construct forces you to write —
			// and on the smoke-scale fin graph the enumeration does not finish
			// inside fifteen minutes, so the whole workload times out and no
			// FinBench read number gets reported at all.
			//
			// The aggregate is kept rather than returning length(p) directly,
			// because an unreachable pair must answer one row of null: with no
			// grouping key, min over an empty input is exactly that, while a
			// bare RETURN would answer no rows and disagree with the reference
			// about a case the parameter pool does draw.
			engine.Cypher25: `MATCH (a:Account {id: $src}), (b:Account {id: $dst})
MATCH p = shortestPath((a)-[:TRANSFER*1..4]->(b))
WHERE all(t IN relationships(p) WHERE t.ts >= $startTime AND t.ts < $endTime)
RETURN min(length(p)) AS hops`,
			// The window gates the expansion, so the shortest path this
			// finds is the shortest windowed one and not the shortest
			// path that then has to survive a filter. The aggregate is
			// still what returns, for the unreachable pair: ANY SHORTEST
			// matches nothing there, and min over nothing is the one row
			// of null the reference expects.
			engine.ZuQL: `MATCH p = ANY SHORTEST (a:Account {id: $src})-[t:TRANSFER*1..4 WHERE t.ts >= $startTime AND t.ts < $endTime]->(b:Account {id: $dst})
RETURN min(path_length(p)) AS hops`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := finFor(ds)
				if err != nil {
					return nil, err
				}
				src, err := pStr(p, "src")
				if err != nil {
					return nil, err
				}
				dst, err := pStr(p, "dst")
				if err != nil {
					return nil, err
				}
				s, e, err := window(p)
				if err != nil {
					return nil, err
				}
				var hops engine.Value
				if h, ok := g.shortestWin(src, dst, s, e, 4); ok {
					hops = h
				}
				return &workload.Answer{Columns: []string{"hops"}, Rows: [][]engine.Value{{hops}}}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// tcr4 mirrors TCR4: the money loop — distinct accounts m on a windowed
// cycle a -TRANSFER*1..3-> m -TRANSFER-> a (cycle length <= 4 through
// the account). [Subgraph]
func tcr4() *workload.Query {
	return &workload.Query{
		ID:                 "fb-tcr4",
		NeedsPathPredicate: true,
		Class:              engine.Subgraph,
		PoolKey:            "fb-tcr4",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account {id: $id})-[es:TRANSFER*1..3]->(m:Account)-[c:TRANSFER]->(a)
WHERE m.id <> a.id AND c.ts >= $startTime AND c.ts < $endTime
  AND all(t IN es WHERE t.ts >= $startTime AND t.ts < $endTime)
RETURN count(DISTINCT m.id) AS loops`,
			// The same count, expanded hop by hop with the frontier collapsed
			// to distinct nodes between hops.
			//
			// The question is which accounts are reachable, and the compact
			// text above answers it by enumerating every three-hop trail and
			// deduplicating at the end. The two agree — a node reachable by a
			// walk of length k is reachable by a path of length k or less, so
			// the trail set and the reachability set have the same members —
			// but they do not cost the same. The generated transfer graph has
			// an out-degree near 300, so three hops is on the order of 10^7
			// trails from a single account, and Neo4j does not finish one
			// repetition inside four minutes; collapsing to at most 10^3
			// distinct nodes per hop instead answers in about a second.
			//
			// The frontier is spelled with OPTIONAL MATCH so a hop that
			// expands to nothing yields an empty list rather than dropping the
			// row and losing the shorter hops already collected.
			engine.Cypher25: `MATCH (a:Account {id: $id})-[t1:TRANSFER]->(x1:Account)
WHERE t1.ts >= $startTime AND t1.ts < $endTime
WITH a, collect(DISTINCT x1) AS hop1
UNWIND hop1 AS n1
OPTIONAL MATCH (n1)-[t2:TRANSFER]->(x2:Account)
WHERE t2.ts >= $startTime AND t2.ts < $endTime
WITH a, hop1, collect(DISTINCT x2) AS hop2
UNWIND hop2 AS n2
OPTIONAL MATCH (n2)-[t3:TRANSFER]->(x3:Account)
WHERE t3.ts >= $startTime AND t3.ts < $endTime
WITH a, hop1, hop2, collect(DISTINCT x3) AS hop3
WITH a, hop1 + hop2 + hop3 AS reached
UNWIND reached AS m
WITH DISTINCT a, m
WHERE m.id <> a.id
MATCH (m)-[c:TRANSFER]->(a)
WHERE c.ts >= $startTime AND c.ts < $endTime
RETURN count(DISTINCT m.id) AS loops`,
			// The gated expansion answers the reachability half, and the
			// closing edge is a second MATCH rather than a tail on the
			// first: a variable-length pattern that ends on an already
			// bound node does not execute in zu yet. The DISTINCT
			// between them is what the Cypher 25 text collapses its
			// frontier for, and costs the same nothing here.
			engine.ZuQL: `MATCH (a:Account {id: $id})-[t:TRANSFER*1..3 WHERE t.ts >= $startTime AND t.ts < $endTime]->(m:Account)
WITH DISTINCT a, m
WHERE m.id <> a.id
MATCH (m)-[c:TRANSFER]->(a)
WHERE c.ts >= $startTime AND c.ts < $endTime
RETURN count(DISTINCT m.id) AS loops`,
		},
		Reference: countRef("loops", func(g *finGraph, p workload.Params) (int64, error) {
			id, err := pStr(p, "id")
			if err != nil {
				return 0, err
			}
			s, e, err := window(p)
			if err != nil {
				return 0, err
			}
			return g.loops(id, s, e), nil
		}),
	}
}

// tcr5 mirrors TCR5: the fan-in/fan-out ratio breach — windowed in/out
// transfer counts and amount sums of an account, and whether the in/out
// amount ratio exceeds $threshold. [Aggregation]
func tcr5() *workload.Query {
	return &workload.Query{
		ID:      "fb-tcr5",
		Class:   engine.Aggregation,
		PoolKey: "fb-tcr5",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account {id: $id})
OPTIONAL MATCH (s:Account)-[i:TRANSFER]->(a)
WHERE i.ts >= $startTime AND i.ts < $endTime
WITH a, count(i) AS inCount, coalesce(sum(i.amount), 0.0) AS inSum
OPTIONAL MATCH (a)-[o:TRANSFER]->(d:Account)
WHERE o.ts >= $startTime AND o.ts < $endTime
RETURN inCount, inSum, count(o) AS outCount, coalesce(sum(o.amount), 0.0) AS outSum,
       (coalesce(sum(o.amount), 0.0) > 0 AND inSum > $threshold * coalesce(sum(o.amount), 0.0)) AS breach`,
			// No coalesce: zu sums an empty group to zero rather than to
			// null, which is what the coalesce is there to produce, so
			// the guard has nothing left to guard. The breach test also
			// moves out of the RETURN, since zu wants a bare aggregate
			// there and reads the expression from the next scope.
			engine.ZuQL: `MATCH (a:Account {id: $id})
OPTIONAL MATCH (s:Account)-[i:TRANSFER]->(a)
WHERE i.ts >= $startTime AND i.ts < $endTime
WITH a, count(i) AS inCount, sum(i.amount) AS inSum
OPTIONAL MATCH (a)-[o:TRANSFER]->(d:Account)
WHERE o.ts >= $startTime AND o.ts < $endTime
WITH inCount, inSum, count(o) AS outCount, sum(o.amount) AS outSum
RETURN inCount, inSum, outCount, outSum,
       (outSum > 0 AND inSum > $threshold * outSum) AS breach`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := finFor(ds)
				if err != nil {
					return nil, err
				}
				id, err := pStr(p, "id")
				if err != nil {
					return nil, err
				}
				s, e, err := window(p)
				if err != nil {
					return nil, err
				}
				th, err := pFloat(p, "threshold")
				if err != nil {
					return nil, err
				}
				inC, inS, outC, outS := g.fanStats(id, s, e)
				breach := outS > 0 && inS > th*outS
				return &workload.Answer{
					Columns: []string{"inCount", "inSum", "outCount", "outSum", "breach"},
					Rows:    [][]engine.Value{{inC, inS, outC, outS, breach}},
				}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true, FloatTol: 1e-6},
		},
	}
}

// tcr8 mirrors TCR8 (guarantee risk) with a disclosed substitution: the
// schema has no guarantee edge, so the query follows the loan → transfer
// chain — the loan's windowed DEPOSIT and then 1..2 windowed transfers —
// and counts the distinct accounts the loan money can reach. [Traversal]
func tcr8() *workload.Query {
	return &workload.Query{
		ID:                 "fb-tcr8",
		NeedsPathPredicate: true,
		Class:              engine.Traversal,
		PoolKey:            "fb-tcr8",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (l:Loan {id: $id})-[d:DEPOSIT]->(a:Account)-[es:TRANSFER*1..2]->(b:Account)
WHERE d.ts >= $startTime AND d.ts < $endTime
  AND all(t IN es WHERE t.ts >= $startTime AND t.ts < $endTime)
RETURN count(DISTINCT b.id) AS reached`,
			engine.ZuQL: `MATCH (l:Loan {id: $id})-[d:DEPOSIT]->(a:Account)-[t:TRANSFER*1..2 WHERE t.ts >= $startTime AND t.ts < $endTime]->(b:Account)
WHERE d.ts >= $startTime AND d.ts < $endTime
RETURN count(DISTINCT b.id) AS reached`,
		},
		Reference: countRef("reached", func(g *finGraph, p workload.Params) (int64, error) {
			id, err := pStr(p, "id")
			if err != nil {
				return 0, err
			}
			s, e, err := window(p)
			if err != nil {
				return 0, err
			}
			return g.loanChain(id, s, e), nil
		}),
	}
}

// tcr11 mirrors TCR11 (recursive path with amount-decay filter) with a
// disclosed substitution: a fixed depth-3 chain where each hop's amount
// is at most $decay times the previous hop's, counting distinct
// endpoints. [Traversal]
func tcr11() *workload.Query {
	return &workload.Query{
		ID:      "fb-tcr11",
		Class:   engine.Traversal,
		PoolKey: "fb-tcr11",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account {id: $id})-[t1:TRANSFER]->(b:Account)-[t2:TRANSFER]->(c:Account)-[t3:TRANSFER]->(d:Account)
WHERE t1.ts >= $startTime AND t1.ts < $endTime
  AND t2.ts >= $startTime AND t2.ts < $endTime
  AND t3.ts >= $startTime AND t3.ts < $endTime
  AND t2.amount <= t1.amount * $decay
  AND t3.amount <= t2.amount * $decay
RETURN count(DISTINCT d.id) AS reached`,
			engine.ZuQL: `MATCH (a:Account {id: $id})-[t1:TRANSFER]->(b:Account)-[t2:TRANSFER]->(c:Account)-[t3:TRANSFER]->(d:Account)
WHERE t1.ts >= $startTime AND t1.ts < $endTime
  AND t2.ts >= $startTime AND t2.ts < $endTime
  AND t3.ts >= $startTime AND t3.ts < $endTime
  AND t2.amount <= t1.amount * $decay
  AND t3.amount <= t2.amount * $decay
RETURN count(DISTINCT d.id) AS reached`,
		},
		Reference: countRef("reached", func(g *finGraph, p workload.Params) (int64, error) {
			id, err := pStr(p, "id")
			if err != nil {
				return 0, err
			}
			s, e, err := window(p)
			if err != nil {
				return 0, err
			}
			decay, err := pFloat(p, "decay")
			if err != nil {
				return 0, err
			}
			return g.decayReach(id, s, e, decay), nil
		}),
	}
}

// tcr12 mirrors TCR12: the new-account burst — accounts created inside
// the window that made more than $threshold windowed transfers. [Aggregation]
func tcr12() *workload.Query {
	return &workload.Query{
		ID:      "fb-tcr12",
		Class:   engine.Aggregation,
		PoolKey: "fb-tcr12",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account)
WHERE a.createTime >= $startTime AND a.createTime < $endTime
MATCH (a)-[t:TRANSFER]->(:Account)
WHERE t.ts >= $startTime AND t.ts < $endTime
WITH a, count(t) AS burst
WHERE burst > $threshold
RETURN count(a) AS accounts`,
			engine.ZuQL: `MATCH (a:Account)
WHERE a.createTime >= $startTime AND a.createTime < $endTime
MATCH (a)-[t:TRANSFER]->(:Account)
WHERE t.ts >= $startTime AND t.ts < $endTime
WITH a, count(t) AS burst
WHERE burst > $threshold
RETURN count(a) AS accounts`,
		},
		Reference: countRef("accounts", func(g *finGraph, p workload.Params) (int64, error) {
			s, e, err := window(p)
			if err != nil {
				return 0, err
			}
			th, err := pInt(p, "threshold")
			if err != nil {
				return 0, err
			}
			return g.newAccountBurst(s, e, th), nil
		}),
	}
}

// sr1 is the simple read: account by id. [PointRead]
func sr1() *workload.Query {
	return &workload.Query{
		ID:      "fb-sr1",
		Class:   engine.PointRead,
		PoolKey: "fb-sr1",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account {id: $id})
RETURN a.id AS id, a.createTime AS createTime, a.isBlocked AS isBlocked`,
			engine.ZuQL: `MATCH (a:Account {id: $id})
RETURN a.id AS id, a.createTime AS createTime, a.isBlocked AS isBlocked`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := finFor(ds)
				if err != nil {
					return nil, err
				}
				id, err := pStr(p, "id")
				if err != nil {
					return nil, err
				}
				ans := &workload.Answer{Columns: []string{"id", "createTime", "isBlocked"}}
				if a, ok := g.acc[id]; ok {
					ans.Rows = [][]engine.Value{{workload.IDValue(a.id), a.createTime, a.blocked}}
				}
				return ans, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// sr2 is the simple read: the account's recent windowed outgoing
// transfers, newest first, limit 10. [PointRead]
func sr2() *workload.Query {
	return &workload.Query{
		ID:      "fb-sr2",
		Class:   engine.PointRead,
		PoolKey: "fb-sr2",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Account {id: $id})-[t:TRANSFER]->(b:Account)
WHERE t.ts >= $startTime AND t.ts < $endTime
RETURN b.id AS dst, t.amount AS amount, t.ts AS ts
ORDER BY ts DESC, amount DESC, dst ASC
LIMIT 10`,
			engine.ZuQL: `MATCH (a:Account {id: $id})-[t:TRANSFER]->(b:Account)
WHERE t.ts >= $startTime AND t.ts < $endTime
RETURN b.id AS dst, t.amount AS amount, t.ts AS ts
ORDER BY ts DESC, amount DESC, dst ASC
LIMIT 10`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := finFor(ds)
				if err != nil {
					return nil, err
				}
				id, err := pStr(p, "id")
				if err != nil {
					return nil, err
				}
				s, e, err := window(p)
				if err != nil {
					return nil, err
				}
				return &workload.Answer{
					Columns: []string{"dst", "amount", "ts"},
					Rows:    g.recentOut(id, s, e, 10),
				}, nil
			},
			Compare: workload.CompareSpec{Ordered: true, CoerceNum: true, FloatTol: 1e-6},
		},
	}
}

// Write markers: fb-w1 tags its transfer with a ts far beyond any
// generated simulation clock value, and fb-w2 creates an account id no
// generated scale reaches. Teardown runs unparameterized (verify.verifyWrite
// executes it as bare text), so it has to address the written data by these
// literals alone — which is why the marker, not the endpoints, is what
// identifies fb-w1's edge.
const (
	w1MarkTs = int64(9_000_000_000)
	w2ID     = int64(9_000_001)
)

func writeWorkload() *workload.Workload {
	return &workload.Workload{
		Name:     "fb-write",
		Title:    "FinBench-derived transaction writes",
		Family:   "finbench",
		Dataset:  "fin-10k",
		Fidelity: "derived",
		Queries:  []*workload.Query{w1(), w2()},
	}
}

// w1 adds a transfer edge between two existing accounts (spec table
// fb-w1). The teardown deletes the marker edge so repetitions are
// stationary. [Write]
//
// The endpoints come from a curated pool rather than a literal pair, because
// which ids exist is a property of the dataset: an earlier version hardcoded
// "A0" and "A1" and silently matched nothing once the generators moved to
// canonical integer ids, turning a write benchmark into a no-op that still
// reported a latency. The marker ts is what identifies the written edge, so
// the teardown can find it without knowing the endpoints.
func w1() *workload.Query {
	return &workload.Query{
		ID:    "fb-w1",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (s:Account {id: $src}), (d:Account {id: $dst})
CREATE (s)-[:TRANSFER {amount: $amount, ts: $ts}]->(d)`,
		},
		PoolKey: "fb-w1",
		Params:  workload.NewPoolSource(nil),
		PostCondition: `MATCH (:Account {id: $src})-[t:TRANSFER {ts: $ts}]->(:Account {id: $dst})
RETURN count(t) = 1`,
		Teardown: `MATCH ()-[t:TRANSFER {ts: 9000000000}]->() DELETE t`,
	}
}

// w2 adds an account (spec table fb-w2: "add account"). The teardown
// detach-deletes it. [Write]
func w2() *workload.Query {
	return &workload.Query{
		ID:    "fb-w2",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `CREATE (:Account {id: $id, createTime: $createTime, isBlocked: $isBlocked})`,
		},
		Params: workload.Fixed{P: workload.Params{
			"id": w2ID, "createTime": int64(0), "isBlocked": false,
		}},
		PostCondition: `MATCH (a:Account {id: $id}) RETURN count(a) = 1`,
		Teardown:      `MATCH (a:Account {id: 9000001}) DETACH DELETE a`,
	}
}
