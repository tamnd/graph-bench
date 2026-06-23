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

	"github.com/tamnd/graph-bench/target"
)

// recipeBlock is the reproduction-relevant slice of a manifest that is folded
// into the content checksum, so two datasets with identical data files but
// different recipes (a different seed or generator) get different checksums. It
// is serialized as canonical JSON (Go's encoder sorts map keys, and the field
// order here is fixed) before hashing.
type recipeBlock struct {
	Generator        string         `json:"generator"`
	GeneratorVersion int            `json:"generatorVersion"`
	Seed             int64          `json:"seed"`
	Params           map[string]any `json:"params"`
	CreatedReference string         `json:"createdReference"`
	ListDelimiter    string         `json:"listDelimiter"`
	Null             string         `json:"null"`
}

// Checksum computes the canonical content checksum over a dataset directory and
// a manifest, per spec doc 04 section 3.4. It hashes every node and relationship
// file (in byte-sorted relative-path order, path then contents) and then folds
// the manifest's recipe block as a final canonical-JSON segment. The manifest's
// own checksum field is not part of the hash (it would be circular). The result
// is "sha256:<hex>".
func Checksum(dir string, m *target.Manifest) (string, error) {
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
	recipe := recipeBlock{
		Generator:        m.Generator,
		GeneratorVersion: m.GeneratorVersion,
		Seed:             m.Seed,
		Params:           m.Params,
		CreatedReference: m.CreatedReference,
		ListDelimiter:    m.ListDelimiter,
		Null:             m.Null,
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

// Verify recomputes the checksum over a dataset directory and compares it to the
// manifest's recorded checksum. A mismatch means the bytes on disk are not the
// dataset the manifest claims, which must abort whatever was about to use it.
func Verify(dir string, m *target.Manifest) error {
	got, err := Checksum(dir, m)
	if err != nil {
		return err
	}
	if got != m.Checksum {
		return fmt.Errorf("dataset: checksum mismatch in %s: manifest has %s, files hash to %s", dir, m.Checksum, got)
	}
	return nil
}

// dataFiles lists the canonical data files under a dataset directory as
// directory-relative paths (nodes/*.csv and rels/*.csv, all shards), sorted by a
// byte-wise comparison so the order does not depend on the filesystem listing or
// the locale.
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
