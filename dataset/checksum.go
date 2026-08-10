package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// recipeBlock is the reproduction-relevant slice of a manifest that is folded
// into the content checksum, so two datasets with identical data files but
// different recipes (a different seed or generator) get different checksums.
// It is serialized as canonical JSON (Go's encoder sorts map keys, and the
// field order here is fixed) before hashing.
//
// COMPATIBILITY: this block is carried from v1 unchanged — same fields, same
// JSON names, same value types — so v0.2 checksums remain valid (spec 05 §1).
// GeneratorVersion is an int here even though engine.Manifest records a
// string, and Params are typed values even though engine.Manifest records
// strings; Checksum converts (see recipeFor). CreatedReference existed on the
// v1 manifest but was never set by any generator or repack, so it folds as
// the constant empty string.
type recipeBlock struct {
	Generator        string         `json:"generator"`
	GeneratorVersion int            `json:"generatorVersion"`
	Seed             int64          `json:"seed"`
	Params           map[string]any `json:"params"`
	CreatedReference string         `json:"createdReference"`
	ListDelimiter    string         `json:"listDelimiter"`
	Null             string         `json:"null"`
}

// Checksum computes the canonical content checksum over a dataset directory
// and a manifest, per spec 05 §1. It hashes every node and relationship file
// (in byte-sorted relative-path order, path then contents) and then folds the
// manifest's recipe block as a final canonical-JSON segment. The manifest's
// own checksum field is not part of the hash (it would be circular). The
// result is "sha256:<hex>".
func Checksum(dir string, m *engine.Manifest) (string, error) {
	files, err := dataFiles(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		// Fold the path so a file moving between nodes/ and rels/ changes the
		// hash even if its bytes are identical.
		if _, err := h.Write([]byte(rel)); err != nil {
			return "", err
		}
		f, err := os.Open(filepath.Join(dir, rel))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	recipe, err := recipeFor(m)
	if err != nil {
		return "", err
	}
	blob, err := json.Marshal(recipe)
	if err != nil {
		return "", err
	}
	if _, err := h.Write(blob); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Verify recomputes the checksum over a dataset directory and compares it to
// the manifest's recorded checksum. A mismatch means the bytes on disk are not
// the dataset the manifest claims, which must abort whatever was about to use
// it.
func Verify(dir string, m *engine.Manifest) error {
	got, err := Checksum(dir, m)
	if err != nil {
		return err
	}
	if got != m.Checksum {
		return fmt.Errorf("dataset: checksum mismatch in %s: manifest has %s, files hash to %s", dir, m.Checksum, got)
	}
	return nil
}

// recipeFor builds the v1-typed recipe block from a v0.3 manifest. The version
// string must be a decimal integer (every shipped generator's is), and the
// canonical param strings are re-typed by typedParam so the marshaled bytes
// match what v1 produced for the same recipe.
func recipeFor(m *engine.Manifest) (*recipeBlock, error) {
	gv := 0
	if m.GeneratorVersion != "" {
		n, err := strconv.Atoi(m.GeneratorVersion)
		if err != nil {
			return nil, fmt.Errorf("dataset: generator version %q is not an integer (the checksum recipe folds the v1 integer form): %w", m.GeneratorVersion, err)
		}
		gv = n
	}
	// A nil params map must stay nil so it marshals as JSON null, exactly as
	// v1 marshaled an unset map (the pinned-LDBC manifests have no params).
	var params map[string]any
	if m.Params != nil {
		params = make(map[string]any, len(m.Params))
		for k, v := range m.Params {
			params[k] = typedParam(v)
		}
	}
	return &recipeBlock{
		Generator:        m.Generator,
		GeneratorVersion: gv,
		Seed:             m.Seed,
		Params:           params,
		ListDelimiter:    m.ListDelimiter,
		Null:             m.Null,
	}, nil
}

// typedParam maps a canonical param string back to the typed value v1 stored:
// an integer token becomes int64 (parsed exactly, no float round-trip), any
// other valid JSON token (float, bool, array) becomes its decoded value, and
// everything else stays a string. Generators format param values with the
// param helpers in dataset/gen so this inversion is exact.
func typedParam(s string) any {
	if isInteger(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		switch v.(type) {
		case float64, bool, []any, map[string]any, nil:
			return v
		}
	}
	return s
}

// isInteger reports whether s is a plain decimal integer token.
func isInteger(s string) bool {
	if s == "" {
		return false
	}
	digits := s
	if s[0] == '-' {
		digits = s[1:]
	}
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dataFiles lists the canonical data files under a dataset directory as
// directory-relative paths (nodes/*.csv and rels/*.csv, all shards), sorted by
// a byte-wise comparison so the order does not depend on the filesystem
// listing or the locale.
func dataFiles(dir string) ([]string, error) {
	var files []string
	for _, sub := range []string{nodesDir, relsDir} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".csv" {
				continue
			}
			files = append(files, sub+"/"+e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}
