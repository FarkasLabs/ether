package transcode

import (
	"github.com/braedonsaunders/ether/internal/config"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAdoptedOrphanRemovalFailureRetainsBudgetAndRetries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permission fixture requires POSIX permissions")
	}
	dir := t.TempDir()
	downloads := filepath.Join(dir, "downloads")
	if err := os.MkdirAll(downloads, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(downloads, "dl-0123456789abcdef.mp4")
	if err := os.WriteFile(file, make([]byte, 16), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := (&Manager{tempDir: dir}).NewDownloadRemuxer(config.Downloads{DiskBudgetBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleDownloadAge)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	d.adopted[file] = old
	if err := os.Chmod(downloads, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(downloads, 0700)
	// Verify this user cannot bypass the fixture (for example, root).
	probe := filepath.Join(downloads, "permission-probe")
	if err := os.WriteFile(probe, nil, 0600); err == nil {
		os.Remove(probe)
		t.Skip("user bypasses directory write permissions")
	}
	d.expireStaleAdopted(time.Now())
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("file should survive failed unlink: %v", err)
	}
	if got := d.UsedBytes(); got != 16 {
		t.Fatalf("failed unlink leaves 16 bytes on disk but UsedBytes=%d", got)
	}
	if _, ok := d.adopted[file]; !ok {
		t.Fatal("failed unlink forgot orphan; cannot retry")
	}
	if err := os.Chmod(downloads, 0700); err != nil {
		t.Fatal(err)
	}
	d.expireStaleAdopted(time.Now())
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("retry did not delete orphan: %v", err)
	}
	if got := d.UsedBytes(); got != 0 {
		t.Fatalf("successful retry retained %d budget bytes", got)
	}
	if _, ok := d.adopted[file]; ok {
		t.Fatal("successful retry kept orphan entry")
	}
}
