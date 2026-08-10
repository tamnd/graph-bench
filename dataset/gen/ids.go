package gen

import "strconv"

// idSpace hands out one contiguous block of integer identifiers per label,
// packed end to end from zero.
//
// Multi-table generators need identifiers unique across labels, not just
// within one: Person 0 and Forum 0 are different nodes, and an engine with a
// flat identifier space cannot tell them apart. An earlier version disambiguated
// with string prefixes ("P0", "F0") and a later one with a fixed billion-wide
// stride per label. Both are wrong here, for different reasons:
//
//   - Prefixes are not what LDBC SNB and FinBench specify — both define int64
//     identifiers — and an engine whose bulk loader takes integer endpoints
//     cannot load a prefixed dataset at all, silently reducing a three-engine
//     comparison to two.
//   - A fixed wide stride keeps the ids integral but makes them sparse, and an
//     engine that sizes structures by the largest identifier rather than by the
//     node count then pays for the holes. zu does exactly that: `zu copy`
//     rejects ids past the 32-bit range outright, and well before that limit it
//     slows to a crawl allocating for identifiers no node occupies.
//
// Packing the blocks tightly satisfies both: every id is an integer, the
// labels stay disjoint, and the id space is exactly as large as the node count.
// Block bases follow the order the generator requests them, which is fixed in
// code and so reproducible.
type idSpace struct{ next int64 }

// block reserves count identifiers and returns the base of the reservation.
func (s *idSpace) block(count int64) int64 {
	base := s.next
	s.next += count
	return base
}

// labeler returns a function rendering the i'th identifier of a freshly
// reserved block of count identifiers.
func (s *idSpace) labeler(count int64) func(int64) string {
	base := s.block(count)
	return func(i int64) string { return strconv.FormatInt(base+i, 10) }
}
