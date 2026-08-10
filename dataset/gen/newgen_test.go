package gen

import "testing"

// TestURandInvariants checks urand's exact node and edge counts, the id
// space, the canonical (source, target, weight) order, and the GAP weight
// rule.
func TestURandInvariants(t *testing.T) {
	cfg := Config{Kind: "urand", Seed: 42, Scale: 10, EdgeFactor: 8}
	w, m := run(t, cfg)
	wantNodes := int64(1) << uint(cfg.Scale)
	wantEdges := int64(cfg.EdgeFactor) * wantNodes
	if m.Invariants.NodeCount != wantNodes {
		t.Errorf("NodeCount = %d, want %d", m.Invariants.NodeCount, wantNodes)
	}
	if m.Invariants.EdgeCount != wantEdges {
		t.Errorf("EdgeCount = %d, want %d", m.Invariants.EdgeCount, wantEdges)
	}
	rels := w.files["rels/"+relType]
	var prevU, prevV int64 = -1, -1
	for _, row := range rels.rows {
		u, v, wt := mustAtoi(t, row[0]), mustAtoi(t, row[1]), mustAtoi(t, row[3])
		if u < 0 || u >= wantNodes || v < 0 || v >= wantNodes {
			t.Fatalf("edge %d->%d outside id space [0,%d)", u, v, wantNodes)
		}
		if wt < 1 || wt > 255 {
			t.Fatalf("weight %d outside 1..255", wt)
		}
		if u < prevU || (u == prevU && v < prevV) {
			t.Fatalf("edges not sorted: %d->%d after %d->%d", u, v, prevU, prevV)
		}
		prevU, prevV = u, v
	}
}

// TestURandDefaults checks the GAP default edge factor of 16 is applied and
// recorded.
func TestURandDefaults(t *testing.T) {
	_, m := run(t, Config{Kind: "urand", Seed: 1, Scale: 6})
	if m.Invariants.EdgeCount != 16*(1<<6) {
		t.Errorf("EdgeCount = %d, want %d (edgefactor default 16)", m.Invariants.EdgeCount, 16*(1<<6))
	}
	if m.Params["edgeFactor"] != "16" {
		t.Errorf("edgeFactor param = %q, want 16", m.Params["edgeFactor"])
	}
}

// TestFinInvariants checks fin's exact table sizes: the derived person and
// loan counts, one OWN per account, one APPLY and one DEPOSIT per loan, the
// exact transfer volume, and the simulation-clock bound on every timestamp.
func TestFinInvariants(t *testing.T) {
	cfg := Config{Kind: "fin", Seed: 7, Accounts: 500, Days: 5, TxPerDay: 200, HubFrac: 0.02}
	w, m := run(t, cfg)

	accounts, persons, loans := int64(500), int64(250), int64(50)
	if m.Invariants.NodeCount != accounts+persons+loans {
		t.Errorf("NodeCount = %d, want %d", m.Invariants.NodeCount, accounts+persons+loans)
	}
	counts := map[string]int64{
		"nodes/Account": accounts,
		"nodes/Person":  persons,
		"nodes/Loan":    loans,
		"rels/TRANSFER": 5 * 200,
		"rels/WITHDRAW": 5 * 20,
		"rels/OWN":      accounts,
		"rels/APPLY":    loans,
		"rels/DEPOSIT":  loans,
	}
	var total int64
	for file, want := range counts {
		got := int64(len(w.files[file].rows))
		if got != want {
			t.Errorf("%s has %d rows, want %d", file, got, want)
		}
		if file[:5] == "rels/" {
			total += got
		}
	}
	repay := int64(len(w.files["rels/REPAY"].rows))
	if repay < loans || repay > 4*loans {
		t.Errorf("REPAY rows = %d, want within [%d, %d]", repay, loans, 4*loans)
	}
	if m.Invariants.EdgeCount != total+repay {
		t.Errorf("EdgeCount = %d, want %d", m.Invariants.EdgeCount, total+repay)
	}

	// Every TRANSFER timestamp sits inside its day on the simulation clock,
	// days ascend with the row order, and no transfer is a self-loop.
	window := int64(cfg.Days) * 86400
	prevDay := int64(0)
	for i, row := range w.files["rels/TRANSFER"].rows {
		if row[0] == row[1] {
			t.Fatalf("TRANSFER row %d is a self-loop at %s", i, row[0])
		}
		ts := mustAtoi(t, row[4])
		if ts < 0 || ts >= window {
			t.Fatalf("TRANSFER ts %d outside the %d-day window", ts, cfg.Days)
		}
		day := ts / 86400
		if day != int64(i)/int64(cfg.TxPerDay) {
			t.Fatalf("TRANSFER row %d has day %d, want %d (clock is data)", i, day, int64(i)/int64(cfg.TxPerDay))
		}
		if day < prevDay {
			t.Fatalf("TRANSFER days not monotone at row %d", i)
		}
		prevDay = day
	}
}

// TestFinDefaults checks the documented defaults are applied and recorded in
// the manifest params.
func TestFinDefaults(t *testing.T) {
	_, m := run(t, Config{Kind: "fin", Seed: 1, Accounts: 100, Days: 1, TxPerDay: 10})
	want := map[string]string{
		"accounts": "100", "persons": "50", "loans": "10",
		"days": "1", "txPerDay": "10", "hubFrac": "0.01",
	}
	for k, v := range want {
		if m.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, m.Params[k], v)
		}
	}
}

// TestLBInvariants checks lb's node count, the power-law link bounds, the
// distinct ascending targets, and the payload shape.
func TestLBInvariants(t *testing.T) {
	cfg := Config{Kind: "lb", Seed: 3, N: 400}
	w, m := run(t, cfg)
	if m.Invariants.NodeCount != cfg.N {
		t.Errorf("NodeCount = %d, want %d", m.Invariants.NodeCount, cfg.N)
	}
	if m.Invariants.EdgeCount != int64(len(w.files["rels/LINK"].rows)) {
		t.Errorf("EdgeCount = %d, want %d", m.Invariants.EdgeCount, len(w.files["rels/LINK"].rows))
	}
	if m.Invariants.EdgeCount < cfg.N {
		t.Errorf("EdgeCount = %d, want >= %d (min 1 link per object)", m.Invariants.EdgeCount, cfg.N)
	}
	perSource := map[string]int{}
	seen := map[string]bool{}
	for _, row := range w.files["rels/LINK"].rows {
		key := row[0] + ">" + row[1]
		if row[0] == row[1] {
			t.Fatalf("self-link at %s", row[0])
		}
		if seen[key] {
			t.Fatalf("duplicate link %s", key)
		}
		seen[key] = true
		perSource[row[0]]++
		if len(row[5]) != lbPayloadLen {
			t.Fatalf("link payload length %d, want %d", len(row[5]), lbPayloadLen)
		}
	}
	for src, deg := range perSource {
		if deg < lbMinLinks || int64(deg) > cfg.N-1 {
			t.Fatalf("object %s has %d links, outside [%d, %d]", src, deg, lbMinLinks, cfg.N-1)
		}
	}
	for _, row := range w.files["nodes/Obj"].rows {
		if len(row[4]) != lbPayloadLen {
			t.Fatalf("object payload length %d, want %d", len(row[4]), lbPayloadLen)
		}
	}
}

// TestSocialInvariants checks social's exact table sizes and the wiring
// rules: one creator and one containing forum per post, 1..3 forums per
// person, and at most 5 likes per post.
func TestSocialInvariants(t *testing.T) {
	cfg := Config{Kind: "social", Seed: 9, Persons: 200, AvgFriends: 8, PostsPerPerson: 4}
	w, m := run(t, cfg)

	persons, posts, forums := int64(200), int64(800), int64(10)
	if m.Invariants.NodeCount != persons+posts+forums {
		t.Errorf("NodeCount = %d, want %d", m.Invariants.NodeCount, persons+posts+forums)
	}
	if got := int64(len(w.files["nodes/Person"].rows)); got != persons {
		t.Errorf("Person rows = %d, want %d", got, persons)
	}
	if got := int64(len(w.files["nodes/Post"].rows)); got != posts {
		t.Errorf("Post rows = %d, want %d", got, posts)
	}
	if got := int64(len(w.files["nodes/Forum"].rows)); got != forums {
		t.Errorf("Forum rows = %d, want %d", got, forums)
	}
	if got := int64(len(w.files["rels/HAS_CREATOR"].rows)); got != posts {
		t.Errorf("HAS_CREATOR rows = %d, want %d (one creator per post)", got, posts)
	}
	if got := int64(len(w.files["rels/CONTAINER_OF"].rows)); got != posts {
		t.Errorf("CONTAINER_OF rows = %d, want %d (one forum per post)", got, posts)
	}
	members := int64(len(w.files["rels/HAS_MEMBER"].rows))
	if members < persons || members > 3*persons {
		t.Errorf("HAS_MEMBER rows = %d, want within [%d, %d]", members, persons, 3*persons)
	}
	likes := int64(len(w.files["rels/LIKES"].rows))
	if likes > 5*posts {
		t.Errorf("LIKES rows = %d, want <= %d", likes, 5*posts)
	}

	// The KNOWS mean tracks AvgFriends loosely: the power-law rescale should
	// land within a factor of two.
	knows := int64(len(w.files["rels/KNOWS"].rows))
	mean := float64(knows) / float64(persons)
	if mean < float64(cfg.AvgFriends)/2 || mean > float64(cfg.AvgFriends)*2 {
		t.Errorf("mean KNOWS degree = %.1f, want near %d", mean, cfg.AvgFriends)
	}

	// HAS_CREATOR wiring is deterministic: post i belongs to person
	// i/PostsPerPerson.
	for i, row := range w.files["rels/HAS_CREATOR"].rows {
		// Read the expected id out of the Person table rather than
		// recomputing it, so the assertion checks the wiring and not the
		// id layout the generator happens to use.
		wantCreator := w.files["nodes/Person"].rows[i/cfg.PostsPerPerson][0]
		if row[1] != wantCreator {
			t.Fatalf("post %d creator = %s, want %s", i, row[1], wantCreator)
		}
	}
}
