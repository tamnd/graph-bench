package gen

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// socialWindow is the simulation window for every creationDate: five years of
// seconds, drawn uniformly.
const socialWindow = int64(5 * 365 * 86400)

// socialBirthdayMax bounds the uniform birthday draw: epoch seconds up to
// 2005-01-01, so birthdays land in 1970..2004.
const socialBirthdayMax = int64(1104537600)

// Social is the SNB-shaped generator: a small multi-table property graph of
// Person, Post, and Forum nodes for smoke-scale runs of the snb workloads
// (spec 05 §2). It exists because the official LDBC SNB datagen is a Spark
// cluster job (ADR-6); real audit-grade scales come from the pinned datagen
// artifacts (dataset/ldbc), and this generator covers the shapes the smoke
// tier needs: a power-law friendship graph, authored posts, likes, and forum
// membership.
//
// Shape, deterministic from Config.Seed:
//
//   - Persons persons with names from fixed pools, a uniform birthday, and a
//     uniform creationDate over the five-year window.
//   - Persons*PostsPerPerson posts; post i is authored by person
//     i/PostsPerPerson (HAS_CREATOR, and mirrored in the creatorId property),
//     contained in one uniform forum (CONTAINER_OF), and liked by 0..5
//     distinct uniform persons (LIKES).
//   - max(1, Persons/20) forums; every person joins 1..3 distinct uniform
//     forums (HAS_MEMBER).
//   - KNOWS out-degrees are power-law (exponent 2.0 over [1, 10*AvgFriends])
//     rescaled so the mean is approximately AvgFriends; targets are distinct,
//     never the source, emitted in ascending order.
//
// Each label takes a packed block of integer ids (see idSpace), so the three
// tables stay disjoint in one flat id space without leaving holes in it.
type Social struct{}

// Name returns "social".
func (Social) Name() string { return "social" }

// Version is the algorithm version.
func (Social) Version() string { return "1" }

// socialFirstNames and socialLastNames are the fixed pools person names are
// drawn from; part of the reproduction recipe.
var (
	socialFirstNames = []string{"Ada", "Alan", "Barbara", "Claude", "Donald", "Edsger", "Frances", "Grace", "John", "Katherine", "Ken", "Leslie", "Margaret", "Niklaus", "Robin", "Tony"}
	socialLastNames  = []string{"Allen", "Backus", "Dijkstra", "Hamilton", "Hoare", "Hopper", "Kay", "Knuth", "Lamport", "Liskov", "Lovelace", "McCarthy", "Milner", "Shannon", "Thompson", "Wirth"}
)

// Generate emits the social graph. See Generator.Generate.
func (g Social) Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error) {
	persons := int64(cfg.Persons)
	if persons == 0 {
		persons = 1000
	}
	if persons < 2 {
		return nil, fmt.Errorf("social: Persons must be >= 2, got %d", persons)
	}
	avgFriends := cfg.AvgFriends
	if avgFriends == 0 {
		avgFriends = 20
	}
	if avgFriends < 1 {
		return nil, fmt.Errorf("social: AvgFriends must be >= 1, got %d", avgFriends)
	}
	postsPerPerson := cfg.PostsPerPerson
	if postsPerPerson == 0 {
		postsPerPerson = 10
	}
	if postsPerPerson < 1 {
		return nil, fmt.Errorf("social: PostsPerPerson must be >= 1, got %d", postsPerPerson)
	}
	forums := persons / 20
	if forums < 1 {
		forums = 1
	}
	posts := persons * int64(postsPerPerson)
	// One packed id block per label, so the three tables stay disjoint in a
	// flat id space without leaving holes in it (see idSpace).
	var ids idSpace
	personID := ids.labeler(persons)
	postID := ids.labeler(posts)
	forumID := ids.labeler(forums)

	prng := NewPRNG(cfg.Seed)
	date := func() string { return strconv.FormatInt(prng.Int63n(socialWindow), 10) }

	// Node tables.
	person, err := w.NodeFile("Person", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "firstName", Type: "STRING"},
		{Name: "lastName", Type: "STRING"},
		{Name: "birthday", Type: "INT64"},
		{Name: "creationDate", Type: "INT64"},
	})
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < persons; i++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		row := []string{
			personID(i),
			socialFirstNames[prng.Int63n(int64(len(socialFirstNames)))],
			socialLastNames[prng.Int63n(int64(len(socialLastNames)))],
			strconv.FormatInt(prng.Int63n(socialBirthdayMax), 10),
			date(),
		}
		if err := person.Write(row); err != nil {
			return nil, err
		}
	}
	if err := person.Close(); err != nil {
		return nil, err
	}

	post, err := w.NodeFile("Post", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "content", Type: "STRING"},
		{Name: "creationDate", Type: "INT64"},
		{Name: "creatorId", Type: "INT64"},
	})
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < posts; i++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		creator := i / int64(postsPerPerson)
		row := []string{postID(i), randString(prng, 32), date(), strconv.FormatInt(creator, 10)}
		if err := post.Write(row); err != nil {
			return nil, err
		}
	}
	if err := post.Close(); err != nil {
		return nil, err
	}

	forum, err := w.NodeFile("Forum", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "title", Type: "STRING"},
	})
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < forums; i++ {
		if err := forum.Write([]string{forumID(i), "forum-" + strconv.FormatInt(i, 10)}); err != nil {
			return nil, err
		}
	}
	if err := forum.Close(); err != nil {
		return nil, err
	}

	dateHeader := append(relHeader(), engine.Column{Name: "creationDate", Type: "INT64"})

	// KNOWS: the power-law friendship graph, rescaled to the requested mean.
	maxF := int64(10 * avgFriends)
	if maxF > persons-1 {
		maxF = persons - 1
	}
	cdf := powerLawCDF(1, int(maxF), 2.0)
	degScale := float64(avgFriends) / powerLawMean(cdf, 1)
	knows, err := w.RelFile("KNOWS", "Person", "Person", dateHeader)
	if err != nil {
		return nil, err
	}
	chosen := make(map[int64]struct{}, maxF)
	for u := int64(0); u < persons; u++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		deg := int64(math.Round(float64(drawDegree(cdf, 1, prng.Float64())) * degScale))
		if deg < 1 {
			deg = 1
		}
		if deg > persons-1 {
			deg = persons - 1
		}
		for k := range chosen {
			delete(chosen, k)
		}
		for int64(len(chosen)) < deg {
			v := prng.Int63n(persons)
			if v == u {
				continue
			}
			if _, dup := chosen[v]; dup {
				continue
			}
			chosen[v] = struct{}{}
		}
		for v := int64(0); v < persons; v++ {
			if _, ok := chosen[v]; !ok {
				continue
			}
			if err := knows.Write([]string{personID(u), personID(v), "KNOWS", date()}); err != nil {
				return nil, err
			}
		}
	}
	if err := knows.Close(); err != nil {
		return nil, err
	}

	// HAS_CREATOR: authorship, deterministic from the post id.
	hasCreator, err := w.RelFile("HAS_CREATOR", "Post", "Person", relHeader())
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < posts; i++ {
		creator := i / int64(postsPerPerson)
		if err := hasCreator.Write([]string{postID(i), personID(creator), "HAS_CREATOR"}); err != nil {
			return nil, err
		}
	}
	if err := hasCreator.Close(); err != nil {
		return nil, err
	}

	// LIKES: 0..5 distinct uniform persons per post.
	likes, err := w.RelFile("LIKES", "Person", "Post", dateHeader)
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < posts; i++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		k := prng.Int63n(6)
		for kk := range chosen {
			delete(chosen, kk)
		}
		for int64(len(chosen)) < k {
			chosen[prng.Int63n(persons)] = struct{}{}
		}
		for v := int64(0); v < persons; v++ {
			if _, ok := chosen[v]; !ok {
				continue
			}
			if err := likes.Write([]string{personID(v), postID(i), "LIKES", date()}); err != nil {
				return nil, err
			}
		}
	}
	if err := likes.Close(); err != nil {
		return nil, err
	}

	// HAS_MEMBER: every person joins 1..3 distinct forums.
	hasMember, err := w.RelFile("HAS_MEMBER", "Forum", "Person", relHeader())
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < persons; i++ {
		k := 1 + prng.Int63n(3)
		if k > forums {
			k = forums
		}
		for kk := range chosen {
			delete(chosen, kk)
		}
		for int64(len(chosen)) < k {
			chosen[prng.Int63n(forums)] = struct{}{}
		}
		for f := int64(0); f < forums; f++ {
			if _, ok := chosen[f]; !ok {
				continue
			}
			if err := hasMember.Write([]string{forumID(f), personID(i), "HAS_MEMBER"}); err != nil {
				return nil, err
			}
		}
	}
	if err := hasMember.Close(); err != nil {
		return nil, err
	}

	// CONTAINER_OF: every post lives in exactly one forum.
	containerOf, err := w.RelFile("CONTAINER_OF", "Forum", "Post", relHeader())
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < posts; i++ {
		if err := containerOf.Write([]string{forumID(prng.Int63n(forums)), postID(i), "CONTAINER_OF"}); err != nil {
			return nil, err
		}
	}
	if err := containerOf.Close(); err != nil {
		return nil, err
	}

	m := &engine.Manifest{
		Name:             fmt.Sprintf("social-p%d", persons),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Scale:            fmt.Sprintf("p%d", persons),
		Params: map[string]string{
			"persons":        iparam(persons),
			"avgFriends":     iparam(avgFriends),
			"postsPerPerson": iparam(postsPerPerson),
			"forums":         iparam(forums),
		},
		Invariants: engine.Invariants{NodeCount: persons + posts + forums},
	}
	return w.Finalize(m)
}

// powerLawMean computes the mean of the discrete distribution a powerLawCDF
// table encodes, used to rescale draws to a requested average degree.
func powerLawMean(cdf []float64, minDeg int) float64 {
	var mean, prev float64
	for i, c := range cdf {
		mean += float64(minDeg+i) * (c - prev)
		prev = c
	}
	return mean
}

// sid renders a prefixed id ("P3", "M17", "F0"), keeping the three node
// tables disjoint in one global :ID space.
