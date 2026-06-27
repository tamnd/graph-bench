package ldbc

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestComputePinFromArchive computes a pin from a local LDBC archive and writes it
// as JSON, the tooling path for refreshing a committed pin against the live upstream
// artifact. It is a manual probe, skipped unless GR_PIN_ARCHIVE names a .tar.zst
// file; GR_PIN_NAME, GR_PIN_SCALE, GR_PIN_URL, and GR_PIN_OUT parameterize the
// resulting pin and where it lands. ComputePin runs the same deterministic repack
// Fetch does, so the content checksum it records is the one a later Fetch over the
// same archive will reproduce.
func TestComputePinFromArchive(t *testing.T) {
	archive := os.Getenv("GR_PIN_ARCHIVE")
	if archive == "" {
		t.Skip("set GR_PIN_ARCHIVE to a local .tar.zst to compute a pin")
	}
	name := envOr("GR_PIN_NAME", "snb-sf1")
	scale := envOr("GR_PIN_SCALE", "SF1")
	url := envOr("GR_PIN_URL", "")

	pin, err := ComputePin(context.Background(), archive, name, scale, url, "")
	if err != nil {
		t.Fatalf("compute pin: %v", err)
	}

	blob, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		t.Fatalf("marshal pin: %v", err)
	}
	blob = append(blob, '\n')
	t.Logf("pin:\n%s", blob)

	if out := os.Getenv("GR_PIN_OUT"); out != "" {
		if err := os.WriteFile(out, blob, 0o644); err != nil {
			t.Fatalf("write pin to %s: %v", out, err)
		}
		t.Logf("wrote %s", out)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
