package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v4.2.5", "v4.2.6", -1},
		{"v4.2.6", "v4.2.5", 1},
		{"v4.2.6", "v4.2.6", 0},
		{"4.2.6", "v4.2.6", 0},      // prefix-insensitive
		{"v4.2.6", "v5.0.0", -1},    // major
		{"v4.3.0", "v4.2.9", 1},     // minor
		{"v4.2.6-rc1", "v4.2.6", 0}, // pre-release metadata dropped
		{"dev", "v4.2.6", -1},       // dev is always older
		{"v4.2.6", "dev", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	txt := "abc123  vaults3-linux-amd64.tar.gz\ndef456  vaults3-darwin-arm64.tar.gz\n"
	got, err := checksumFor(txt, "vaults3-darwin-arm64.tar.gz")
	if err != nil || got != "def456" {
		t.Fatalf("checksumFor = (%q,%v), want def456", got, err)
	}
	if _, err := checksumFor(txt, "vaults3-windows-amd64.tar.gz"); err == nil {
		t.Fatal("expected error for missing checksum entry")
	}
}

func TestExtractBinary(t *testing.T) {
	// Build a tar.gz containing a couple files.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string][]byte{
		"vaults3-linux-amd64":     []byte("SERVER-BINARY-BYTES"),
		"vaults3-cli-linux-amd64": []byte("cli"),
		"README.md":               []byte("readme"),
	}
	for name, data := range files {
		tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0755})
		tw.Write(data)
	}
	tw.Close()
	gz.Close()

	got, err := extractBinary(buf.Bytes(), "vaults3-linux-amd64")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if string(got) != "SERVER-BINARY-BYTES" {
		t.Fatalf("extracted %q, want SERVER-BINARY-BYTES", got)
	}
	if _, err := extractBinary(buf.Bytes(), "nope"); err == nil {
		t.Fatal("expected error for missing binary in archive")
	}
}

func TestCheckDetectsNewer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v4.2.6","assets":[{"name":"checksums.txt","browser_download_url":"x"}]}`)
	}))
	defer srv.Close()

	u := New("v4.2.5")
	u.apiBase = srv.URL // point the GitHub API at the stub
	u.client = srv.Client()

	st := u.Check(context.Background())
	if st.Error != "" {
		t.Fatalf("Check error: %s", st.Error)
	}
	if st.Latest != "v4.2.6" || !st.UpdateAvailable {
		t.Fatalf("status = %+v, want latest v4.2.6 + update available", st)
	}
	if got := u.LastStatus(); got.Latest != "v4.2.6" {
		t.Fatalf("LastStatus not recorded: %+v", got)
	}
}

func TestCheckUpToDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v4.2.6","assets":[]}`)
	}))
	defer srv.Close()

	u := New("v4.2.6")
	u.apiBase = srv.URL
	u.client = srv.Client()

	st := u.Check(context.Background())
	if st.UpdateAvailable {
		t.Fatalf("should not report an update when already current: %+v", st)
	}
}

// sanity: ensure a real archive's sha256 matches what checksumFor would expect.
func TestChecksumRoundTrip(t *testing.T) {
	data := []byte("release-archive-content")
	sum := sha256.Sum256(data)
	line := fmt.Sprintf("%s  vaults3-linux-amd64.tar.gz", hex.EncodeToString(sum[:]))
	got, err := checksumFor(line, "vaults3-linux-amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(sum[:]) {
		t.Fatal("checksum mismatch in round trip")
	}
}
