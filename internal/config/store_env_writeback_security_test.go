package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfigBytesUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfigBytes(path, []byte(`{"keys":["secret"]}`)); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected config mode 0600, got %o", got)
	}
}
