// Package ldbc fetches a pinned LDBC SNB dataset artifact, verifies its
// checksums (archive first, content second), extracts it to the canonical CSV
// layout, and returns it as a target.Dataset via the dataset package.
//
// The fetch-and-verify flow:
//  1. Load the pin JSON for the requested scale factor.
//  2. Check the cache: if present and manifest verifies, return it.
//  3. Download the .tar.zst archive from the primary URL (falling back to mirror).
//  4. Verify the archive sha256 before extraction.
//  5. Extract into a temp dir, read the embedded manifest, compute the content
//     checksum, compare to the pin, then rename to the permanent cache path.
//  6. Return a dataset.Set wrapping the verified directory.
//
// Pins live in dataset/ldbc/pins/ as JSON files committed to the repository.
// See notes/Spec/2060/bench/04-datasets-and-generation.md section 4.
package ldbc

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/dataset"
)

//go:embed pins/*.json
var pinFS embed.FS

// Pin describes one pinned LDBC artifact. Committed to the repository as a
// JSON file under dataset/ldbc/pins/.
type Pin struct {
	Name            string `json:"name"`             // stable id, e.g. "snb-sf1"
	Scale           string `json:"scale"`            // LDBC scale label, e.g. "SF1"
	URL             string `json:"url"`              // primary .tar.zst download URL
	Mirror          string `json:"mirror,omitempty"` // fallback URL
	ArchiveChecksum string `json:"archiveChecksum"`  // sha256:<hex> of .tar.zst
	Checksum        string `json:"checksum"`         // content checksum via dataset.Checksum
	NodeCount       int64  `json:"nodeCount"`
	EdgeCount       int64  `json:"edgeCount"`
}

// LoadPin returns the committed pin for the given LDBC scale factor.
// scale should be "SF1", "SF3", "SF10", etc. (case-insensitive).
func LoadPin(scale string) (*Pin, error) {
	name := "snb-" + strings.ToLower(scale)
	data, err := pinFS.ReadFile("pins/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("ldbc: no pin for %s (tried pins/%s.json): %w", scale, name, err)
	}
	var p Pin
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("ldbc: parse pin for %s: %w", scale, err)
	}
	return &p, nil
}

// FetchOptions controls the fetch-and-verify flow.
type FetchOptions struct {
	// CacheDir is where extracted datasets are cached.
	// Default: os.UserCacheDir()/graph-bench/datasets/ldbc/
	CacheDir string
	// HTTPTimeout is the deadline for a single HTTP request. Default 10 minutes.
	HTTPTimeout time.Duration
	// Progress, when non-nil, is called periodically with bytes downloaded so
	// far and the total content-length (-1 when unknown).
	Progress func(done, total int64)
}

func (o *FetchOptions) withDefaults() FetchOptions {
	out := FetchOptions{}
	if o != nil {
		out = *o
	}
	if out.CacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		out.CacheDir = filepath.Join(base, "graph-bench", "datasets", "ldbc")
	}
	if out.HTTPTimeout <= 0 {
		out.HTTPTimeout = 10 * time.Minute
	}
	return out
}

// Fetch runs the fetch-and-verify flow for the given pin and returns a verified
// dataset.Set. Checks the cache first; skips the download if cached data verifies.
func Fetch(ctx context.Context, pin *Pin, opts *FetchOptions) (*dataset.Set, error) {
	o := opts.withDefaults()
	if err := os.MkdirAll(o.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("ldbc: mkdir cache: %w", err)
	}

	cacheDir := filepath.Join(o.CacheDir, pin.Name+"-"+checksumPrefix(pin.Checksum))

	// Check the cache.
	if ds, err := dataset.Open(cacheDir); err == nil {
		return ds, nil
	}

	// Download the archive to a temp file.
	archivePath, err := downloadArchive(ctx, pin, o)
	if err != nil {
		return nil, fmt.Errorf("ldbc: download %s: %w", pin.Name, err)
	}
	defer os.Remove(archivePath)

	// Verify the archive checksum before paying extraction cost.
	if err := verifyFileChecksum(archivePath, pin.ArchiveChecksum); err != nil {
		return nil, fmt.Errorf("ldbc: archive checksum: %w", err)
	}

	// Extract to a temp dir inside the cache dir.
	tmpDir, err := os.MkdirTemp(o.CacheDir, "extract-*")
	if err != nil {
		return nil, fmt.Errorf("ldbc: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := extractTarZst(ctx, archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("ldbc: extract: %w", err)
	}

	// Read the manifest from the extracted directory and verify content checksum.
	m, err := dataset.ReadManifest(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("ldbc: read manifest after extract: %w", err)
	}
	computed, err := dataset.Checksum(tmpDir, m)
	if err != nil {
		return nil, fmt.Errorf("ldbc: compute content checksum: %w", err)
	}
	if computed != pin.Checksum {
		return nil, fmt.Errorf("ldbc: content checksum mismatch: computed %s, pin wants %s", computed, pin.Checksum)
	}

	// Rename to the permanent cache path.
	if err := os.Rename(tmpDir, cacheDir); err != nil {
		if err2 := copyDir(tmpDir, cacheDir); err2 != nil {
			return nil, fmt.Errorf("ldbc: install to cache: %w", err2)
		}
	}

	return dataset.Open(cacheDir)
}

// downloadArchive downloads pin.URL (falling back to pin.Mirror) into a temp
// file and returns its path. The caller removes it.
func downloadArchive(ctx context.Context, pin *Pin, o FetchOptions) (string, error) {
	tmp, err := os.CreateTemp("", "ldbc-*.tar.zst")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	tmp.Close()

	urls := []string{pin.URL}
	if pin.Mirror != "" {
		urls = append(urls, pin.Mirror)
	}

	var lastErr error
	for _, url := range urls {
		if url == "" {
			continue
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return "", err
		}
		dlErr := downloadURL(ctx, url, f, o)
		f.Close()
		if dlErr == nil {
			return name, nil
		}
		lastErr = dlErr
	}
	os.Remove(name)
	return "", fmt.Errorf("all URLs failed (last error: %w)", lastErr)
}

// downloadURL streams url into dst with optional progress reporting.
func downloadURL(ctx context.Context, url string, dst *os.File, o FetchOptions) error {
	client := &http.Client{Timeout: o.HTTPTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 1<<20)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if o.Progress != nil {
				o.Progress(done, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// verifyFileChecksum hashes the file at path and compares to want ("sha256:<hex>").
func verifyFileChecksum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("got %s, want %s", got, want)
	}
	return nil
}

// extractTarZst extracts archivePath into dst using the system zstd and tar.
// zstd is required to be on PATH. On CI runners (Linux) it is installed by
// default; on macOS it is available via Homebrew.
func extractTarZst(ctx context.Context, archivePath, dst string) error {
	// zstd -d -c <archive> | tar -xf - -C <dst>
	zstdCmd := exec.CommandContext(ctx, "zstd", "-d", "-c", archivePath)
	tarCmd := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", dst)
	pipe, err := zstdCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	tarCmd.Stdin = pipe
	if err := zstdCmd.Start(); err != nil {
		return fmt.Errorf("zstd start: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		return fmt.Errorf("tar start: %w", err)
	}
	zstdErr := zstdCmd.Wait()
	tarErr := tarCmd.Wait()
	if zstdErr != nil {
		return fmt.Errorf("zstd: %w", zstdErr)
	}
	if tarErr != nil {
		return fmt.Errorf("tar: %w", tarErr)
	}
	return nil
}

// checksumPrefix returns the first 8 hex characters from a "sha256:<hex>"
// string, for use in directory names.
func checksumPrefix(checksum string) string {
	s := strings.TrimPrefix(checksum, "sha256:")
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ComputePin builds a Pin by hashing a local .tar.zst archive, extracting it
// to a temp directory, and computing the content checksum. The caller supplies
// the name, scale, and URL fields (which are metadata, not derived from the
// archive itself). This is the tooling path: download the archive once, run
// ComputePin, commit the resulting JSON as a pin file.
func ComputePin(ctx context.Context, archivePath, name, scale, url, mirror string) (*Pin, error) {
	// Hash the archive file.
	archiveSum, err := hashFile(archivePath)
	if err != nil {
		return nil, fmt.Errorf("ldbc: hash archive: %w", err)
	}

	// Extract to a temp directory.
	tmpDir, err := os.MkdirTemp("", "ldbc-pin-*")
	if err != nil {
		return nil, fmt.Errorf("ldbc: temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarZst(ctx, archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("ldbc: extract: %w", err)
	}

	// Read the manifest and compute the content checksum.
	m, err := dataset.ReadManifest(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("ldbc: read manifest from extracted archive: %w", err)
	}
	contentSum, err := dataset.Checksum(tmpDir, m)
	if err != nil {
		return nil, fmt.Errorf("ldbc: compute content checksum: %w", err)
	}

	return &Pin{
		Name:            name,
		Scale:           scale,
		URL:             url,
		Mirror:          mirror,
		ArchiveChecksum: "sha256:" + archiveSum,
		Checksum:        contentSum,
		NodeCount:       m.NodeCount,
		EdgeCount:       m.EdgeCount,
	}, nil
}

// hashFile returns the lowercase hex sha256 digest of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
