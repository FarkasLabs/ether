package app

import (
	"os"
	"path/filepath"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// preUpgradeFiles reads the settings and credential key that match the
// database as it is before this start, for a pre-migrate snapshot. Missing
// files are skipped (a fresh install has neither).
func preUpgradeFiles(dataDir string) []store.SnapshotFile {
	var out []store.SnapshotFile
	for _, name := range []string{"config.json", config.CredentialKeyFile} {
		b, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			continue
		}
		out = append(out, store.SnapshotFile{Name: name, Data: b})
	}
	return out
}
