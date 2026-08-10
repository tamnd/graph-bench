// Package snb implements the LDBC SNB workload families on the harness's
// social generator dataset (dataset/gen/social.go): snb-short (IS1-IS7
// shapes), snb-complex (an IC subset), snb-update (IU/ID shapes), snb-mix
// (the interactive operation mix, spec 06 §3.4), and snb-bi (a BI-shaped
// aggregation subset measured under the analytics protocol, spec 07 §2).
//
// Fidelity is "derived" (spec 07 §6): the query SHAPES follow the LDBC SNB
// Interactive v2 and BI specifications, but the data comes from the harness
// `social` generator — Person{id, firstName, lastName, birthday,
// creationDate}, Post{id, content, creationDate, creatorId}, Forum{id,
// title}, with KNOWS(creationDate), HAS_CREATOR, LIKES(creationDate),
// HAS_MEMBER, and CONTAINER_OF — not the official LDBC datagen, and the
// parameters come from harness-curated pools (pools.go). Entities the
// official schema has and this one lacks (Comments/replies, Tags, Places,
// Organisations, moderators, message length/language) force the per-query
// substitutions and omissions disclosed in each workload's doc comment.
//
// Node ids are the dataset's canonical string tokens ("P3", "M17", "F0");
// every parameter, answer, and ORDER BY in this package uses those tokens,
// and the references compare strings byte-wise, matching engine
// lexicographic string ordering.
package snb

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// socialModel is the in-memory view of one social dataset the references
// compute over: straight-line Go over the canonical CSV (ADR-9), never
// another engine. workload.LoadGraph is not reused here because it merges
// every relationship type into one edge set; the SNB references need the
// typed subgraphs (KNOWS-only shortest paths, LIKES with dates), so this
// package loads the per-type CSVs itself via Dataset.RelFiles and
// dataset.ParseHeader.
type socialModel struct {
	persons  []personRec
	personIx map[string]int
	posts    []postRec
	postIx   map[string]int
	forums   []forumRec
	forumIx  map[string]int

	// knows holds each directed KNOWS edge once in each endpoint's list,
	// mirroring what an undirected Cypher match -[:KNOWS]- returns: one row
	// per stored edge per perspective.
	knows   [][]dateEdge
	friends [][]int // distinct adjacent person indices, ascending

	postsOf     [][]int      // person -> authored post indices (HAS_CREATOR)
	postCreator []int        // post -> creator person index
	postLikers  [][]dateEdge // post -> liking persons with like date
	personLikes [][]dateEdge // person -> liked posts with like date

	forumMembers [][]int // forum -> member person indices (HAS_MEMBER)
	postForum    []int   // post -> containing forum index (CONTAINER_OF)
	forumPosts   [][]int // forum -> contained post indices
}

type personRec struct {
	id, firstName, lastName string
	birthday, creationDate  int64
}

type postRec struct {
	id, content  string
	creationDate int64
}

type forumRec struct {
	id, title string
}

// dateEdge is one dated relationship endpoint: other is the peer index
// (a person or a post, per the owning list) and date the edge's
// creationDate.
type dateEdge struct {
	other int
	date  int64
}

var (
	modelMu sync.Mutex
	models  = map[string]*socialModel{}
)

// loadSocial loads (and caches per dataset directory) the social model.
func loadSocial(ds engine.Dataset) (*socialModel, error) {
	key := ds.Dir() + "|" + ds.Checksum()
	modelMu.Lock()
	defer modelMu.Unlock()
	if m, ok := models[key]; ok && ds.Dir() != "" {
		return m, nil
	}
	m, err := buildSocial(ds)
	if err != nil {
		return nil, err
	}
	if ds.Dir() != "" {
		models[key] = m
	}
	return m, nil
}

func buildSocial(ds engine.Dataset) (*socialModel, error) {
	m := &socialModel{
		personIx: map[string]int{},
		postIx:   map[string]int{},
		forumIx:  map[string]int{},
	}

	// Person nodes.
	if err := eachNodeRow(ds, "Person", func(h header, rec []string) error {
		i := len(m.persons)
		bd, err := h.int64(rec, "birthday")
		if err != nil {
			return err
		}
		cd, err := h.int64(rec, "creationDate")
		if err != nil {
			return err
		}
		p := personRec{
			id:           h.id(rec),
			firstName:    h.str(rec, "firstName"),
			lastName:     h.str(rec, "lastName"),
			birthday:     bd,
			creationDate: cd,
		}
		m.persons = append(m.persons, p)
		m.personIx[p.id] = i
		return nil
	}); err != nil {
		return nil, err
	}

	// Post nodes.
	if err := eachNodeRow(ds, "Post", func(h header, rec []string) error {
		i := len(m.posts)
		cd, err := h.int64(rec, "creationDate")
		if err != nil {
			return err
		}
		p := postRec{id: h.id(rec), content: h.str(rec, "content"), creationDate: cd}
		m.posts = append(m.posts, p)
		m.postIx[p.id] = i
		return nil
	}); err != nil {
		return nil, err
	}

	// Forum nodes.
	if err := eachNodeRow(ds, "Forum", func(h header, rec []string) error {
		i := len(m.forums)
		f := forumRec{id: h.id(rec), title: h.str(rec, "title")}
		m.forums = append(m.forums, f)
		m.forumIx[f.id] = i
		return nil
	}); err != nil {
		return nil, err
	}

	m.knows = make([][]dateEdge, len(m.persons))
	m.postsOf = make([][]int, len(m.persons))
	m.personLikes = make([][]dateEdge, len(m.persons))
	m.postCreator = make([]int, len(m.posts))
	m.postLikers = make([][]dateEdge, len(m.posts))
	m.postForum = make([]int, len(m.posts))
	m.forumMembers = make([][]int, len(m.forums))
	m.forumPosts = make([][]int, len(m.forums))
	for i := range m.postForum {
		m.postForum[i] = -1
	}

	// KNOWS: Person -> Person, dated. Stored once per directed edge in
	// each endpoint's incident list.
	if err := eachRelRow(ds, "KNOWS", func(h header, rec []string) error {
		s, e, err := endpoints(m.personIx, m.personIx, h, rec, "KNOWS")
		if err != nil {
			return err
		}
		cd, err := h.int64(rec, "creationDate")
		if err != nil {
			return err
		}
		m.knows[s] = append(m.knows[s], dateEdge{other: e, date: cd})
		m.knows[e] = append(m.knows[e], dateEdge{other: s, date: cd})
		return nil
	}); err != nil {
		return nil, err
	}

	// HAS_CREATOR: Post -> Person.
	if err := eachRelRow(ds, "HAS_CREATOR", func(h header, rec []string) error {
		s, e, err := endpoints(m.postIx, m.personIx, h, rec, "HAS_CREATOR")
		if err != nil {
			return err
		}
		m.postCreator[s] = e
		m.postsOf[e] = append(m.postsOf[e], s)
		return nil
	}); err != nil {
		return nil, err
	}

	// LIKES: Person -> Post, dated.
	if err := eachRelRow(ds, "LIKES", func(h header, rec []string) error {
		s, e, err := endpoints(m.personIx, m.postIx, h, rec, "LIKES")
		if err != nil {
			return err
		}
		cd, err := h.int64(rec, "creationDate")
		if err != nil {
			return err
		}
		m.personLikes[s] = append(m.personLikes[s], dateEdge{other: e, date: cd})
		m.postLikers[e] = append(m.postLikers[e], dateEdge{other: s, date: cd})
		return nil
	}); err != nil {
		return nil, err
	}

	// HAS_MEMBER: Forum -> Person.
	if err := eachRelRow(ds, "HAS_MEMBER", func(h header, rec []string) error {
		s, e, err := endpoints(m.forumIx, m.personIx, h, rec, "HAS_MEMBER")
		if err != nil {
			return err
		}
		m.forumMembers[s] = append(m.forumMembers[s], e)
		return nil
	}); err != nil {
		return nil, err
	}

	// CONTAINER_OF: Forum -> Post.
	if err := eachRelRow(ds, "CONTAINER_OF", func(h header, rec []string) error {
		s, e, err := endpoints(m.forumIx, m.postIx, h, rec, "CONTAINER_OF")
		if err != nil {
			return err
		}
		m.postForum[e] = s
		m.forumPosts[s] = append(m.forumPosts[s], e)
		return nil
	}); err != nil {
		return nil, err
	}

	// Distinct sorted friend adjacency for the set-based traversals.
	m.friends = make([][]int, len(m.persons))
	for i, edges := range m.knows {
		seen := map[int]struct{}{}
		for _, e := range edges {
			seen[e.other] = struct{}{}
		}
		fs := make([]int, 0, len(seen))
		for f := range seen {
			fs = append(fs, f)
		}
		sort.Ints(fs)
		m.friends[i] = fs
	}
	return m, nil
}

// bfs returns breadth-first distances over the distinct-friend (undirected
// KNOWS) adjacency from src, -1 for unreachable. maxDepth < 0 means
// unbounded.
func (m *socialModel) bfs(src, maxDepth int) []int {
	dist := make([]int, len(m.persons))
	for i := range dist {
		dist[i] = -1
	}
	dist[src] = 0
	queue := []int{src}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		if maxDepth >= 0 && dist[u] == maxDepth {
			continue
		}
		for _, v := range m.friends[u] {
			if dist[v] == -1 {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}
	return dist
}

// ---- CSV scanning ----------------------------------------------------

// header maps a canonical CSV header to column indices: properties by name,
// structural columns (:ID, :START_ID, :END_ID) by type.
type header struct {
	byName map[string]int
	byType map[string]int
}

func (h header) id(rec []string) string    { return rec[h.byType["ID"]] }
func (h header) start(rec []string) string { return rec[h.byType["START_ID"]] }
func (h header) end(rec []string) string   { return rec[h.byType["END_ID"]] }
func (h header) str(rec []string, name string) string {
	return rec[h.byName[name]]
}

func (h header) int64(rec []string, name string) (int64, error) {
	i, ok := h.byName[name]
	if !ok {
		return 0, fmt.Errorf("snb: no column %q", name)
	}
	v, err := strconv.ParseInt(rec[i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("snb: column %q: %w", name, err)
	}
	return v, nil
}

// eachNodeRow scans every shard of one node label, calling fn per data row.
func eachNodeRow(ds engine.Dataset, label string, fn func(h header, rec []string) error) error {
	files, err := ds.NodeFiles(label)
	if err != nil {
		return fmt.Errorf("snb: node files for %q: %w", label, err)
	}
	return eachFileRow(files, fn)
}

// eachRelRow scans every shard of one relationship type, calling fn per row.
func eachRelRow(ds engine.Dataset, typ string, fn func(h header, rec []string) error) error {
	files, err := ds.RelFiles(typ)
	if err != nil {
		return fmt.Errorf("snb: rel files for %q: %w", typ, err)
	}
	return eachFileRow(files, fn)
}

func eachFileRow(files []string, fn func(h header, rec []string) error) error {
	for _, path := range files {
		if err := scanFile(path, fn); err != nil {
			return err
		}
	}
	return nil
}

func scanFile(path string, fn func(h header, rec []string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("snb: open %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true

	first, err := r.Read()
	if err != nil {
		return fmt.Errorf("snb: read header of %s: %w", path, err)
	}
	cols, err := dataset.ParseHeader(first)
	if err != nil {
		return fmt.Errorf("snb: %s: %w", path, err)
	}
	h := header{byName: map[string]int{}, byType: map[string]int{}}
	for i, c := range cols {
		if c.Name != "" {
			h.byName[c.Name] = i
		}
		if dataset.IsStructural(c) {
			h.byType[c.Type] = i
		}
	}

	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("snb: read %s: %w", path, err)
		}
		if err := fn(h, rec); err != nil {
			return err
		}
	}
}

// endpoints resolves a rel row's start and end tokens through the given id
// maps.
func endpoints(startIx, endIx map[string]int, h header, rec []string, typ string) (int, int, error) {
	s, ok := startIx[h.start(rec)]
	if !ok {
		return 0, 0, fmt.Errorf("snb: %s edge references unknown start id %q", typ, h.start(rec))
	}
	e, ok := endIx[h.end(rec)]
	if !ok {
		return 0, 0, fmt.Errorf("snb: %s edge references unknown end id %q", typ, h.end(rec))
	}
	return s, e, nil
}

// ---- reference plumbing ----------------------------------------------

// pstr extracts a required id parameter as the string token the model's
// indexes key on, accepting either spelling of an id.
//
// The pools bind an integer id as int64 so the engines match their integer id
// property (workload.IDValue); the model keys on the raw CSV token. Insisting
// on a Go string here would reject exactly the typing that makes the queries
// work.
func pstr(p workload.Params, key string) (string, error) {
	if _, ok := p[key]; !ok {
		return "", fmt.Errorf("snb: missing parameter %q (pools not bound? call snb.Bind)", key)
	}
	s, ok := workload.Token(p, key)
	if !ok {
		return "", fmt.Errorf("snb: parameter %q is %T, want an id", key, p[key])
	}
	return s, nil
}

// pint extracts a required integer parameter.
func pint(p workload.Params, key string) (int64, error) {
	v, ok := p[key]
	if !ok {
		return 0, fmt.Errorf("snb: missing parameter %q (pools not bound? call snb.Bind)", key)
	}
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("snb: parameter %q is %T, want integer", key, v)
	}
}

// row is shorthand for one answer row.
func row(vs ...engine.Value) []engine.Value { return vs }

// idv types a model id for an answer column. The model holds ids as the raw
// CSV token because that is what its indexes key on, but a reference row is
// compared against what an engine returns, and an engine stores an integer id
// as an integer. See workload.IDValue.
func idv(id string) engine.Value { return workload.IDValue(id) }

// sortKey orders rows by one column; desc flips it.
type sortKey struct {
	col  int
	desc bool
}

// sortRows sorts rows by the given key sequence. Values compare as int64 or
// string (the only types this package's answers carry in sort positions).
func sortRows(rows [][]engine.Value, keys ...sortKey) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, k := range keys {
			c := cmpVal(rows[i][k.col], rows[j][k.col])
			if c == 0 {
				continue
			}
			if k.desc {
				return c > 0
			}
			return c < 0
		}
		return false
	})
}

func cmpVal(a, b engine.Value) int {
	switch x := a.(type) {
	case int64:
		y := b.(int64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case string:
		return strings.Compare(x, b.(string))
	default:
		panic(fmt.Sprintf("snb: unsortable value type %T", a))
	}
}

// limit truncates rows to at most n.
func limit(rows [][]engine.Value, n int) [][]engine.Value {
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}

// person and post resolve a token to its index, erroring on unknown ids
// (a pool should never hand out an id the dataset lacks).
func (m *socialModel) person(id string) (int, error) {
	i, ok := m.personIx[id]
	if !ok {
		return 0, fmt.Errorf("snb: unknown person id %q", id)
	}
	return i, nil
}

func (m *socialModel) post(id string) (int, error) {
	i, ok := m.postIx[id]
	if !ok {
		return 0, fmt.Errorf("snb: unknown post id %q", id)
	}
	return i, nil
}

func (m *socialModel) forum(id string) (int, error) {
	i, ok := m.forumIx[id]
	if !ok {
		return 0, fmt.Errorf("snb: unknown forum id %q", id)
	}
	return i, nil
}
