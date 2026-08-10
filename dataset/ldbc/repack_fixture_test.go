package ldbc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
)

// fixtureFiles is a miniature composite-merged-fk upstream tree: one shard per
// entity, pipe-delimited with the datagen's header row, covering merged foreign
// keys (LocationCityId, CreatorPersonId, ParentPostId), an empty foreign key
// (the root Country and TagClass), a standalone edge entity, and a
// list-delimited field (language "en;fr").
var fixtureFiles = map[string]string{
	"dynamic/Person/part-00000.csv":              "creationDate|id|firstName|lastName|gender|birthday|locationIP|browserUsed|language|email|LocationCityId\n2010-01-01|1|Ada|Lovelace|f|1990-01-01|1.2.3.4|Chrome|en|a@x.org|10\n2010-01-02|2|Alan|Turing|m|1991-02-02|5.6.7.8|Safari|en;fr|b@x.org|10\n",
	"dynamic/Post/part-00000.csv":                "creationDate|id|imageFile|locationIP|browserUsed|language|content|length|CreatorPersonId|ContainerForumId|LocationCountryId\n2010-02-01|5||2.3.4.5|Firefox|en|hello world|11|1|7|20\n",
	"dynamic/Comment/part-00000.csv":             "creationDate|id|locationIP|browserUsed|content|length|CreatorPersonId|LocationCountryId|ParentPostId|ParentCommentId\n2010-02-02|6|9.9.9.9|Chrome|nice|4|2|20|5|\n",
	"dynamic/Forum/part-00000.csv":               "creationDate|id|title|ModeratorPersonId\n2010-01-15|7|My Forum|1\n",
	"dynamic/Person_knows_Person/part-00000.csv": "creationDate|Person1Id|Person2Id\n2010-03-01|1|2\n",
	"dynamic/Person_likes_Post/part-00000.csv":   "creationDate|PersonId|PostId\n2010-03-02|2|5\n",
	"static/Place/part-00000.csv":                "id|name|url|type|PartOfPlaceId\n10|CityX|http://c|City|20\n20|CountryY|http://y|Country|\n",
	"static/Tag/part-00000.csv":                  "id|name|url|TypeTagClassId\n30|tagA|http://t|40\n",
	"static/TagClass/part-00000.csv":             "id|name|url|SubclassOfTagClassId\n40|classA|http://tc|\n",
}

// fixtureChecksum is what the v0.2 codebase's repack computes for the fixture
// above. The repack is a deterministic transform folded into every committed
// pin's content checksum, so this constant pins its byte behavior: if it
// drifts, real pins (snb-sf1 and up) would stop verifying.
const fixtureChecksum = "sha256:f3c76a4f237cf3198e0bc5886b89a11b83292c9eb37f23c69ff6726c78d2d090"

// writeFixture materializes the upstream tree under a temp root, nested the way
// the real archive nests it, and returns the root.
func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	snap := filepath.Join(root, "graphs", "csv", "bi", "composite-merged-fk", "initial_snapshot")
	for rel, content := range fixtureFiles {
		path := filepath.Join(snap, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	return root
}

// TestRepackFixture runs the repack over the in-test upstream tree and checks
// the full contract without needing a real archive: the canonical layout, the
// counts, the wiring ids, the v1-compatible content checksum, and the no-op
// second pass.
func TestRepackFixture(t *testing.T) {
	dir := writeFixture(t)

	repacked, err := repackUpstream(dir, "snb-fixture")
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	if !repacked {
		t.Fatal("repack reported no-op on a raw upstream tree")
	}

	// The upstream tree is cleaned away; only the canonical layout remains.
	if _, err := os.Stat(filepath.Join(dir, "graphs")); !os.IsNotExist(err) {
		t.Fatalf("upstream graphs/ tree not cleaned up: %v", err)
	}

	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("open repacked dataset: %v", err)
	}
	m := ds.Manifest()

	// 2 persons + 1 post + 1 comment + 1 forum + 2 places + 1 tag + 1 tag class.
	if m.Invariants.NodeCount != 9 {
		t.Errorf("NodeCount = %d, want 9", m.Invariants.NodeCount)
	}
	// Person IS_LOCATED_IN x2, post HAS_CREATOR/CONTAINER_OF/IS_LOCATED_IN,
	// comment HAS_CREATOR/IS_LOCATED_IN/REPLY_OF, forum HAS_MODERATOR,
	// place IS_PART_OF (root country dropped), tag HAS_TYPE (root class
	// dropped), KNOWS, LIKES.
	if m.Invariants.EdgeCount != 13 {
		t.Errorf("EdgeCount = %d, want 13", m.Invariants.EdgeCount)
	}

	// The byte-compat pin: the checksum the v1 codebase computes for this tree.
	if m.Checksum != fixtureChecksum {
		t.Errorf("checksum = %s, want the v1 value %s", m.Checksum, fixtureChecksum)
	}

	// Spot-check the repacked rows: prefixed wiring ids and the raw id property.
	files, err := ds.NodeFiles("Person")
	if err != nil {
		t.Fatalf("NodeFiles(Person): %v", err)
	}
	blob, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read Person.csv: %v", err)
	}
	want := ":ID,id:int,creationDate:datetime,firstName:string,lastName:string,gender:string,birthday:date,locationIP:string,browserUsed:string,language:string,email:string\nP1,1,2010-01-01,Ada,Lovelace,f,1990-01-01,1.2.3.4,Chrome,en,a@x.org\nP2,2,2010-01-02,Alan,Turing,m,1991-02-02,5.6.7.8,Safari,en;fr,b@x.org\n"
	if string(blob) != want {
		t.Errorf("Person.csv = %q, want %q", blob, want)
	}

	// The comment's REPLY_OF resolves the post parent into the shared M space.
	relFiles, err := ds.RelFiles("REPLY_OF")
	if err != nil {
		t.Fatalf("RelFiles(REPLY_OF): %v", err)
	}
	blob, err = os.ReadFile(relFiles[0])
	if err != nil {
		t.Fatalf("read REPLY_OF.csv: %v", err)
	}
	if string(blob) != ":START_ID,:END_ID\nM6,M5\n" {
		t.Errorf("REPLY_OF.csv = %q", blob)
	}

	// A second repack of the canonical output is the no-op path.
	again, err := repackUpstream(dir, "snb-fixture")
	if err != nil {
		t.Fatalf("second repack: %v", err)
	}
	if again {
		t.Fatal("second repack rewrote an already-canonical directory")
	}
}
