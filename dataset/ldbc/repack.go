package ldbc

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/target"
)

// repackUpstream detects a raw LDBC SNB datagen tree extracted under dir and, when
// it finds one, rewrites it in place into the canonical nodes/rels + manifest.json
// layout the dataset package reads. It returns true when it repacked, false when
// dir is already canonical (a manifest is present) or is not an LDBC tree.
//
// Why this exists: the LDBC datagen ships pipe-delimited entity files with foreign
// keys merged into the node rows (Comment.CreatorPersonId and so on), one directory
// per entity under graphs/csv/.../initial_snapshot, and no manifest. The canonical
// layout the loader wants is comma-delimited node and relationship files with typed
// headers and a manifest. The pin points at the live upstream archive and records
// its real sha256; this repack is the deterministic bridge between the two, so the
// content checksum the pin records over the repacked output is reproducible from the
// same archive on any machine.
//
// Determinism: entities are processed in a fixed order, part shards in sorted name
// order, rows in file order, and every output column set is fixed, so the bytes a
// run produces depend only on the upstream archive.
//
// Identity: LDBC ids are unique only within an entity, so Person 0, Forum 0, Place 0,
// Tag 0 all coexist. To load them into one global id space without collisions every
// node gets a prefixed :ID (P/F/M/L/O/T/C) for edge wiring, and the raw id rides
// along as an id:int property the queries match on with {id: $param}. Post and Comment
// share the M prefix so the Message super-label resolves through one space and edges
// that leave a message (HAS_CREATOR, REPLY_OF, ...) need not know which kind it is.
func repackUpstream(dir, name string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		return false, nil
	}
	snap, err := findInitialSnapshot(dir)
	if err != nil {
		return false, err
	}
	if snap == "" {
		return false, nil
	}
	rp := &repacker{src: snap, dst: dir}
	if err := rp.run(name); err != nil {
		return false, fmt.Errorf("ldbc: repack: %w", err)
	}
	return true, nil
}

// findInitialSnapshot walks root for the LDBC datagen's initial_snapshot directory,
// the one carrying the dynamic/ and static/ entity trees. It returns "" when there
// is none, which marks dir as not-an-LDBC-tree rather than an error.
func findInitialSnapshot(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "initial_snapshot" &&
			dirExists(filepath.Join(path, "dynamic")) && dirExists(filepath.Join(path, "static")) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// repacker holds the open output writers while a single repack runs.
type repacker struct {
	src, dst    string
	nodes       map[string]*outCSV
	rels        map[string]*outCSV
	nodeHeaders map[string][]string
	relHeaders  map[string][]string
	err         error
}

func (rp *repacker) run(name string) error {
	if err := os.MkdirAll(filepath.Join(rp.dst, "nodes"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(rp.dst, "rels"), 0o755); err != nil {
		return err
	}
	rp.nodes = map[string]*outCSV{}
	rp.rels = map[string]*outCSV{}
	rp.nodeHeaders = map[string][]string{}
	rp.relHeaders = map[string][]string{}

	// Node writers. The :ID is the prefixed wiring id; id:int is the raw LDBC id the
	// queries match. Post and Comment carry a constant Message extra label; Place and
	// Organisation carry the per-row subtype (City/Country, Company/University).
	person := rp.node("Person", ":ID", "id:int", "creationDate:datetime", "firstName:string", "lastName:string", "gender:string", "birthday:date", "locationIP:string", "browserUsed:string", "language:string", "email:string")
	post := rp.node("Post", ":ID", "id:int", "creationDate:datetime", "imageFile:string", "locationIP:string", "browserUsed:string", "language:string", "content:string", "length:int", ":LABEL")
	comment := rp.node("Comment", ":ID", "id:int", "creationDate:datetime", "locationIP:string", "browserUsed:string", "content:string", "length:int", ":LABEL")
	forum := rp.node("Forum", ":ID", "id:int", "creationDate:datetime", "title:string")
	place := rp.node("Place", ":ID", "id:int", "name:string", "url:string", ":LABEL")
	org := rp.node("Organisation", ":ID", "id:int", "name:string", "url:string", ":LABEL")
	tag := rp.node("Tag", ":ID", "id:int", "name:string", "url:string")
	tagClass := rp.node("TagClass", ":ID", "id:int", "name:string", "url:string")

	// Relationship writers. Only the properties a query reads are kept; the rest of
	// the upstream creationDate columns are dropped.
	knows := rp.rel("KNOWS", ":START_ID", ":END_ID", "creationDate:datetime")
	hasInterest := rp.rel("HAS_INTEREST", ":START_ID", ":END_ID")
	likes := rp.rel("LIKES", ":START_ID", ":END_ID", "creationDate:datetime")
	studyAt := rp.rel("STUDY_AT", ":START_ID", ":END_ID", "classYear:int")
	workAt := rp.rel("WORK_AT", ":START_ID", ":END_ID", "workFrom:int")
	hasMember := rp.rel("HAS_MEMBER", ":START_ID", ":END_ID", "joinDate:datetime")
	hasTag := rp.rel("HAS_TAG", ":START_ID", ":END_ID")
	hasCreator := rp.rel("HAS_CREATOR", ":START_ID", ":END_ID")
	containerOf := rp.rel("CONTAINER_OF", ":START_ID", ":END_ID")
	hasModerator := rp.rel("HAS_MODERATOR", ":START_ID", ":END_ID")
	replyOf := rp.rel("REPLY_OF", ":START_ID", ":END_ID")
	isLocatedIn := rp.rel("IS_LOCATED_IN", ":START_ID", ":END_ID")
	isPartOf := rp.rel("IS_PART_OF", ":START_ID", ":END_ID")
	hasType := rp.rel("HAS_TYPE", ":START_ID", ":END_ID")
	isSubclassOf := rp.rel("IS_SUBCLASS_OF", ":START_ID", ":END_ID")
	if rp.err != nil {
		return rp.err
	}

	dyn := filepath.Join(rp.src, "dynamic")
	sta := filepath.Join(rp.src, "static")

	// Persons, with their city as an IS_LOCATED_IN edge.
	if err := forEachRow(filepath.Join(dyn, "Person"), func(g getter) error {
		id := g("id")
		if err := person.row(gid("P", id), id, g("creationDate"), g("firstName"), g("lastName"), g("gender"), g("birthday"), g("locationIP"), g("browserUsed"), g("language"), g("email")); err != nil {
			return err
		}
		return emitEdge(isLocatedIn, gid("P", id), gid("L", g("LocationCityId")))
	}); err != nil {
		return err
	}

	// Posts: a Message-labelled node plus HAS_CREATOR, CONTAINER_OF (forum to post),
	// and IS_LOCATED_IN to the country.
	if err := forEachRow(filepath.Join(dyn, "Post"), func(g getter) error {
		id := g("id")
		m := gid("M", id)
		if err := post.row(m, id, g("creationDate"), g("imageFile"), g("locationIP"), g("browserUsed"), g("language"), g("content"), g("length"), "Message"); err != nil {
			return err
		}
		if err := emitEdge(hasCreator, m, gid("P", g("CreatorPersonId"))); err != nil {
			return err
		}
		if err := emitEdge(containerOf, gid("F", g("ContainerForumId")), m); err != nil {
			return err
		}
		return emitEdge(isLocatedIn, m, gid("L", g("LocationCountryId")))
	}); err != nil {
		return err
	}

	// Comments: a Message-labelled node plus HAS_CREATOR, IS_LOCATED_IN, and a single
	// REPLY_OF to whichever parent (post or comment) the row names.
	if err := forEachRow(filepath.Join(dyn, "Comment"), func(g getter) error {
		id := g("id")
		m := gid("M", id)
		if err := comment.row(m, id, g("creationDate"), g("locationIP"), g("browserUsed"), g("content"), g("length"), "Message"); err != nil {
			return err
		}
		if err := emitEdge(hasCreator, m, gid("P", g("CreatorPersonId"))); err != nil {
			return err
		}
		if err := emitEdge(isLocatedIn, m, gid("L", g("LocationCountryId"))); err != nil {
			return err
		}
		parent := g("ParentPostId")
		if parent == "" {
			parent = g("ParentCommentId")
		}
		return emitEdge(replyOf, m, gid("M", parent))
	}); err != nil {
		return err
	}

	// Forums, with HAS_MODERATOR.
	if err := forEachRow(filepath.Join(dyn, "Forum"), func(g getter) error {
		id := g("id")
		f := gid("F", id)
		if err := forum.row(f, id, g("creationDate"), g("title")); err != nil {
			return err
		}
		return emitEdge(hasModerator, f, gid("P", g("ModeratorPersonId")))
	}); err != nil {
		return err
	}

	// Places, the City/Country/Continent hierarchy carried as the subtype label and
	// the parent as IS_PART_OF.
	if err := forEachRow(filepath.Join(sta, "Place"), func(g getter) error {
		id := g("id")
		l := gid("L", id)
		if err := place.row(l, id, g("name"), g("url"), g("type")); err != nil {
			return err
		}
		return emitEdge(isPartOf, l, gid("L", g("PartOfPlaceId")))
	}); err != nil {
		return err
	}

	// Organisations, Company/University as the subtype label, located in a place via
	// IS_PART_OF (the direction the IC1/IC11 queries traverse).
	if err := forEachRow(filepath.Join(sta, "Organisation"), func(g getter) error {
		id := g("id")
		o := gid("O", id)
		if err := org.row(o, id, g("name"), g("url"), g("type")); err != nil {
			return err
		}
		return emitEdge(isPartOf, o, gid("L", g("LocationPlaceId")))
	}); err != nil {
		return err
	}

	// Tags and tag classes, with the type and subclass hierarchies.
	if err := forEachRow(filepath.Join(sta, "Tag"), func(g getter) error {
		id := g("id")
		t := gid("T", id)
		if err := tag.row(t, id, g("name"), g("url")); err != nil {
			return err
		}
		return emitEdge(hasType, t, gid("C", g("TypeTagClassId")))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(sta, "TagClass"), func(g getter) error {
		id := g("id")
		c := gid("C", id)
		if err := tagClass.row(c, id, g("name"), g("url")); err != nil {
			return err
		}
		return emitEdge(isSubclassOf, c, gid("C", g("SubclassOfTagClassId")))
	}); err != nil {
		return err
	}

	// Standalone edge entities.
	if err := forEachRow(filepath.Join(dyn, "Person_knows_Person"), func(g getter) error {
		return knows.row(gid("P", g("Person1Id")), gid("P", g("Person2Id")), g("creationDate"))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Person_hasInterest_Tag"), func(g getter) error {
		return hasInterest.row(gid("P", g("PersonId")), gid("T", g("TagId")))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Person_likes_Post"), func(g getter) error {
		return likes.row(gid("P", g("PersonId")), gid("M", g("PostId")), g("creationDate"))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Person_likes_Comment"), func(g getter) error {
		return likes.row(gid("P", g("PersonId")), gid("M", g("CommentId")), g("creationDate"))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Person_studyAt_University"), func(g getter) error {
		return studyAt.row(gid("P", g("PersonId")), gid("O", g("UniversityId")), g("classYear"))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Person_workAt_Company"), func(g getter) error {
		return workAt.row(gid("P", g("PersonId")), gid("O", g("CompanyId")), g("workFrom"))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Forum_hasMember_Person"), func(g getter) error {
		return hasMember.row(gid("F", g("ForumId")), gid("P", g("PersonId")), g("creationDate"))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Post_hasTag_Tag"), func(g getter) error {
		return hasTag.row(gid("M", g("PostId")), gid("T", g("TagId")))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Comment_hasTag_Tag"), func(g getter) error {
		return hasTag.row(gid("M", g("CommentId")), gid("T", g("TagId")))
	}); err != nil {
		return err
	}
	if err := forEachRow(filepath.Join(dyn, "Forum_hasTag_Tag"), func(g getter) error {
		return hasTag.row(gid("F", g("ForumId")), gid("T", g("TagId")))
	}); err != nil {
		return err
	}

	if err := rp.closeAll(); err != nil {
		return err
	}
	if err := rp.cleanup(); err != nil {
		return err
	}
	return rp.writeManifest(name)
}

// node opens a node writer under nodes/ and records its header. Errors are deferred
// onto rp.err so the caller can open every writer in a flat block and check once.
func (rp *repacker) node(label string, header ...string) *outCSV {
	if rp.err != nil {
		return nil
	}
	o, err := newOutCSV(filepath.Join(rp.dst, "nodes", label+".csv"), header)
	if err != nil {
		rp.err = err
		return nil
	}
	rp.nodes[label] = o
	rp.nodeHeaders[label] = header
	return o
}

// rel opens a relationship writer under rels/ and records its header.
func (rp *repacker) rel(typ string, header ...string) *outCSV {
	if rp.err != nil {
		return nil
	}
	o, err := newOutCSV(filepath.Join(rp.dst, "rels", typ+".csv"), header)
	if err != nil {
		rp.err = err
		return nil
	}
	rp.rels[typ] = o
	rp.relHeaders[typ] = header
	return o
}

func (rp *repacker) closeAll() error {
	for _, o := range rp.nodes {
		if err := o.close(); err != nil {
			return err
		}
	}
	for _, o := range rp.rels {
		if err := o.close(); err != nil {
			return err
		}
	}
	return nil
}

// cleanup removes the extracted upstream tree, leaving only the canonical output so
// the cached dataset directory holds nothing but nodes/, rels/, and the manifest.
func (rp *repacker) cleanup() error {
	keep := map[string]bool{"nodes": true, "rels": true, "manifest.json": true}
	entries, err := os.ReadDir(rp.dst)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(rp.dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// writeManifest builds the manifest from the open writers' row counts and headers,
// writes it, computes the content checksum over the data files plus the recipe
// block, then writes it again with the checksum filled, the two-pass dance
// dataset.Verify expects (the checksum cannot cover itself).
func (rp *repacker) writeManifest(name string) error {
	m := &target.Manifest{
		Name:          name,
		Kind:          "ldbc",
		Generator:     "ldbc_snb_datagen",
		ListDelimiter: ";",
		Null:          "empty",
		Schema: target.Schema{
			Nodes:         map[string]target.NodeSchema{},
			Relationships: map[string]target.RelSchema{},
		},
	}
	var nodeCount, edgeCount int64
	for label, o := range rp.nodes {
		nodeCount += o.n
		m.Schema.Nodes[label] = target.NodeSchema{
			Files:      []string{"nodes/" + label + ".csv"},
			ID:         "id",
			Labels:     nodeLabels(label),
			Properties: propsOf(rp.nodeHeaders[label]),
		}
	}
	for typ, o := range rp.rels {
		edgeCount += o.n
		st, en := relEnds(typ)
		m.Schema.Relationships[typ] = target.RelSchema{
			Files:      []string{"rels/" + typ + ".csv"},
			Start:      st,
			End:        en,
			Properties: propsOf(rp.relHeaders[typ]),
		}
	}
	m.NodeCount = nodeCount
	m.EdgeCount = edgeCount
	if err := dataset.WriteManifest(rp.dst, m); err != nil {
		return err
	}
	sum, err := dataset.Checksum(rp.dst, m)
	if err != nil {
		return err
	}
	m.Checksum = sum
	return dataset.WriteManifest(rp.dst, m)
}

// nodeLabels returns the label set a node file carries: the file's own label first,
// then the super or subtype labels its :LABEL column adds.
func nodeLabels(label string) []string {
	switch label {
	case "Post", "Comment":
		return []string{label, "Message"}
	case "Place":
		return []string{"Place", "City", "Country", "Continent"}
	case "Organisation":
		return []string{"Organisation", "Company", "University"}
	default:
		return []string{label}
	}
}

// relEnds returns the start and end node labels for a relationship type, for the
// manifest's documentation. An empty string marks a mixed-source end (HAS_TAG leaves
// either a message or a forum, IS_LOCATED_IN either a person or a message).
func relEnds(typ string) (start, end string) {
	switch typ {
	case "KNOWS":
		return "Person", "Person"
	case "HAS_INTEREST":
		return "Person", "Tag"
	case "LIKES":
		return "Person", "Message"
	case "STUDY_AT", "WORK_AT":
		return "Person", "Organisation"
	case "HAS_MEMBER", "HAS_MODERATOR":
		return "Forum", "Person"
	case "HAS_TAG":
		return "", "Tag"
	case "HAS_CREATOR":
		return "Message", "Person"
	case "CONTAINER_OF":
		return "Forum", "Post"
	case "REPLY_OF":
		return "Comment", "Message"
	case "IS_LOCATED_IN":
		return "", "Place"
	case "IS_PART_OF":
		return "", "Place"
	case "HAS_TYPE":
		return "Tag", "TagClass"
	case "IS_SUBCLASS_OF":
		return "TagClass", "TagClass"
	default:
		return "", ""
	}
}

// propsOf parses a canonical header and returns its property columns, the non
// structural ones the manifest's Properties list records.
func propsOf(header []string) []target.Column {
	cols, err := dataset.ParseHeader(header)
	if err != nil {
		return nil
	}
	structural := map[string]bool{"ID": true, "LABEL": true, "TYPE": true, "START_ID": true, "END_ID": true}
	var out []target.Column
	for _, c := range cols {
		if c.Name == "" || structural[c.Type] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// getter resolves an upstream column by name to its field value, or "" when the
// column is absent or the row is short.
type getter func(name string) string

// emitEdge writes one edge unless either endpoint is missing, which is how an empty
// foreign key (a comment with no parent post, a root tag class) drops out.
func emitEdge(o *outCSV, start, end string) error {
	if start == "" || end == "" {
		return nil
	}
	return o.row(start, end)
}

// gid prefixes a raw upstream id into the global wiring id space, or returns "" for
// an empty id so emitEdge skips the edge.
func gid(prefix, raw string) string {
	if raw == "" {
		return ""
	}
	return prefix + raw
}

// forEachRow streams every data row of one upstream entity directory, calling fn
// with a name-indexed accessor. It reads the part-*.csv shards in sorted name order,
// treats the pipe as the delimiter, and skips any line equal to the header (LDBC's
// Spark output repeats the header in each shard). A missing directory is not an
// error: some scale factors omit an entity, and the edges it would feed simply do
// not appear.
func forEachRow(entDir string, fn func(getter) error) error {
	if !dirExists(entDir) {
		return nil
	}
	parts, err := partFiles(entDir)
	if err != nil {
		return err
	}
	var header []string
	cols := map[string]int{}
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		r := csv.NewReader(f)
		r.Comma = '|'
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		for {
			rec, rerr := r.Read()
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				f.Close()
				return fmt.Errorf("read %s: %w", p, rerr)
			}
			if header == nil {
				header = append([]string(nil), rec...)
				for i, n := range rec {
					cols[n] = i
				}
				continue
			}
			if equalRow(rec, header) {
				continue
			}
			row := rec
			g := func(name string) string {
				i, ok := cols[name]
				if !ok || i >= len(row) {
					return ""
				}
				return row[i]
			}
			if err := fn(g); err != nil {
				f.Close()
				return err
			}
		}
		f.Close()
	}
	return nil
}

// partFiles lists the part-*.csv shards of an entity directory in sorted name order,
// skipping the dotfile .crc sidecars and the _SUCCESS marker Spark writes.
func partFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") {
			continue
		}
		if strings.HasPrefix(n, "part-") && strings.HasSuffix(n, ".csv") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Strings(out)
	return out, nil
}

func equalRow(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// outCSV is a counted CSV writer: it tallies the data rows so the manifest can record
// node and edge totals without a second pass.
type outCSV struct {
	f *os.File
	w *csv.Writer
	n int64
}

func newOutCSV(path string, header []string) (*outCSV, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		f.Close()
		return nil, err
	}
	return &outCSV{f: f, w: w}, nil
}

func (o *outCSV) row(fields ...string) error {
	o.n++
	return o.w.Write(fields)
}

func (o *outCSV) close() error {
	o.w.Flush()
	if err := o.w.Error(); err != nil {
		o.f.Close()
		return err
	}
	return o.f.Close()
}
