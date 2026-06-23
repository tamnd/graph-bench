package ldbc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadPinSF1 proves the embedded SF1 pin JSON parses without error.
func TestLoadPinSF1(t *testing.T) {
	pin, err := LoadPin("SF1")
	if err != nil {
		t.Fatalf("LoadPin(SF1): %v", err)
	}
	if pin.Name != "snb-sf1" {
		t.Errorf("Name=%q, want snb-sf1", pin.Name)
	}
	if pin.Scale != "SF1" {
		t.Errorf("Scale=%q, want SF1", pin.Scale)
	}
	if !strings.HasPrefix(pin.ArchiveChecksum, "sha256:") {
		t.Errorf("ArchiveChecksum=%q, want sha256: prefix", pin.ArchiveChecksum)
	}
	if !strings.HasPrefix(pin.Checksum, "sha256:") {
		t.Errorf("Checksum=%q, want sha256: prefix", pin.Checksum)
	}
}

// TestLoadPinUnknown proves an unknown scale returns an error.
func TestLoadPinUnknown(t *testing.T) {
	_, err := LoadPin("SF9999")
	if err == nil {
		t.Error("expected error for unknown scale, got nil")
	}
}

// TestChecksumPrefix proves the first 8 characters of the hex portion are
// extracted correctly.
func TestChecksumPrefix(t *testing.T) {
	got := checksumPrefix("sha256:abcdef0123456789")
	if got != "abcdef01" {
		t.Errorf("checksumPrefix=%q, want abcdef01", got)
	}
}

// TestChecksumPrefixShort proves a short hex is returned as-is without panic.
func TestChecksumPrefixShort(t *testing.T) {
	got := checksumPrefix("sha256:abcd")
	if got != "abcd" {
		t.Errorf("checksumPrefix short=%q, want abcd", got)
	}
}

// TestChecksumPrefixNoPrefix proves a bare hex (no sha256: prefix) is handled.
func TestChecksumPrefixNoPrefix(t *testing.T) {
	got := checksumPrefix("abcdef01234567890")
	if got != "abcdef01" {
		t.Errorf("checksumPrefix no-prefix=%q, want abcdef01", got)
	}
}

// TestFetchOptionsWithDefaults proves zero options fill in non-zero defaults.
func TestFetchOptionsWithDefaults(t *testing.T) {
	o := (*FetchOptions)(nil).withDefaults()
	if o.CacheDir == "" {
		t.Error("CacheDir should not be empty after withDefaults")
	}
	if o.HTTPTimeout <= 0 {
		t.Error("HTTPTimeout should be > 0 after withDefaults")
	}
}

// TestFetchOptionsCustomCacheDir proves a non-empty CacheDir is preserved.
func TestFetchOptionsCustomCacheDir(t *testing.T) {
	dir := t.TempDir()
	opts := &FetchOptions{CacheDir: dir}
	o := opts.withDefaults()
	if o.CacheDir != dir {
		t.Errorf("CacheDir=%q, want %q", o.CacheDir, dir)
	}
}

// TestVerifyFileChecksum proves the checker fails on a wrong checksum and
// passes on the correct one, using a temp file.
func TestVerifyFileChecksum(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(tmp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Correct sha256 of "hello"
	correctSum := "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if err := verifyFileChecksum(tmp, correctSum); err != nil {
		t.Errorf("correct checksum should pass: %v", err)
	}
	wrongSum := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := verifyFileChecksum(tmp, wrongSum); err == nil {
		t.Error("wrong checksum should fail, got nil")
	}
}

// TestDownloadURL proves the download function stores content from an HTTP
// server and reports progress.
func TestDownloadURL(t *testing.T) {
	want := "hello from test server"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(want))
	}))
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "out.bin")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var progressCalled bool
	opts := FetchOptions{
		HTTPTimeout: 5 * time.Second,
		Progress: func(done, total int64) {
			progressCalled = true
		},
	}
	if err := downloadURL(context.Background(), srv.URL, f, opts); err != nil {
		f.Close()
		t.Fatalf("downloadURL: %v", err)
	}
	f.Close()

	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !progressCalled {
		t.Error("progress callback was not called")
	}
}

// TestDownloadURL404 proves a non-200 status returns an error.
func TestDownloadURL404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tmp := filepath.Join(t.TempDir(), "out.bin")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = downloadURL(context.Background(), srv.URL, f, FetchOptions{HTTPTimeout: 5 * time.Second})
	if err == nil {
		t.Error("expected error for 404, got nil")
	}
}

// TestCopyDir proves copyDir replicates a directory tree.
func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	subdir := filepath.Join(src, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "subdir", "a.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != "aaa" {
		t.Errorf("got %q, want aaa", got)
	}
}
