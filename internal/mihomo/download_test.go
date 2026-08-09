package mihomo

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBinaryAssetName(t *testing.T) {
	if runtime.GOOS == "windows" {
		asset, ver := binaryAsset()
		if !strings.HasPrefix(asset, "mihomo-windows-") || !strings.HasSuffix(asset, ".zip") {
			t.Fatalf("unexpected windows asset: %s", asset)
		}
		if ver != defaultMihomoVersion {
			t.Fatalf("unexpected version: %s", ver)
		}
	}
	if runtime.GOOS == "linux" {
		asset, _ := binaryAsset()
		if !strings.HasPrefix(asset, "mihomo-linux-") || !strings.HasSuffix(asset, ".gz") {
			t.Fatalf("unexpected linux asset: %s", asset)
		}
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mihomo.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("Mihomo/Mihomo.exe")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	payload := []byte("MZ mihomo binary payload")
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write zip payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	dest := filepath.Join(dir, "mihomo.exe")
	if err := extractBinary(archive, "mihomo-windows-amd64-v1.19.29.zip", dest); err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("extracted payload mismatch: %q", got)
	}
}

func TestExtractBinaryFromGzip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mihomo.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create gzip: %v", err)
	}
	gz := gzip.NewWriter(f)
	payload := []byte("ELF mihomo binary payload")
	if _, err := gz.Write(payload); err != nil {
		t.Fatalf("write gzip payload: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close gzip file: %v", err)
	}

	dest := filepath.Join(dir, "mihomo")
	if err := extractBinary(archive, "mihomo-linux-amd64-v1.19.29.gz", dest); err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("extracted payload mismatch: %q", got)
	}
}

func TestDownloadInfoStates(t *testing.T) {
	info := (&Manager{}).DownloadInfo()
	if info["state"] != "idle" {
		t.Fatalf("expected idle state, got %v", info["state"])
	}
	if info["target"] != BinaryTarget() {
		t.Fatalf("unexpected target: %v", info["target"])
	}
}

func TestProgressReader(t *testing.T) {
	var lastN, lastTotal int64
	pr := &progressReader{
		r:          strings.NewReader("hello world"),
		total:      11,
		onProgress: func(downloaded, total int64) { lastN, lastTotal = downloaded, total },
	}
	buf := make([]byte, 3)
	read := 0
	for {
		n, err := pr.Read(buf)
		read += n
		if err != nil {
			break
		}
	}
	if read != 11 || lastN != 11 || lastTotal != 11 {
		t.Fatalf("progress mismatch: read=%d lastN=%d total=%d", read, lastN, lastTotal)
	}
}
