package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/graph-bench/target"
)

// Layout constants for a dataset directory: the manifest file and the two
// subdirectories that hold the node and relationship CSV files.
const (
	manifestName = "manifest.json"
	nodesDir     = "nodes"
	relsDir      = "rels"
)

// WriteManifest writes a manifest to manifest.json in a dataset directory as
// indented JSON, the one file a human reads to know what a dataset is.
func WriteManifest(dir string, m *target.Manifest) error {
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	return os.WriteFile(filepath.Join(dir, manifestName), blob, 0o644)
}

// ReadManifest reads and parses the manifest.json in a dataset directory.
func ReadManifest(dir string) (*target.Manifest, error) {
	blob, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var m target.Manifest
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, fmt.Errorf("dataset: parse %s: %w", manifestName, err)
	}
	return &m, nil
}
