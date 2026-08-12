package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func envWritebackEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DS2API_ENV_WRITEBACK")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (s *Store) IsEnvWritebackEnabled() bool {
	return envWritebackEnabled()
}

func (s *Store) HasEnvConfigSource() bool {
	rawCfg := strings.TrimSpace(os.Getenv("DS2API_CONFIG_JSON"))
	return rawCfg != ""
}

func (s *Store) ConfigPath() string {
	return s.path
}

func writeConfigFile(path string, cfg Config) error {
	persistCfg := cfg.Clone()
	persistCfg.ClearAccountTokens()
	if err := saveMihomoSubscriptionsFile(persistCfg.Mihomo); err != nil {
		return err
	}
	b, err := json.MarshalIndent(persistCfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigBytes(path, b)
}

func writeConfigBytes(path string, b []byte) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func(cause error) error {
		if removeErr := os.Remove(tmpName); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("%w; remove config temp file: %v", cause, removeErr)
		}
		return cause
	}
	if err := tmp.Chmod(0o600); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			err = fmt.Errorf("%w; close config temp file: %v", err, closeErr)
		}
		return cleanup(fmt.Errorf("chmod config temp file: %w", err))
	}
	if _, err := tmp.Write(b); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			err = fmt.Errorf("%w; close config temp file: %v", err, closeErr)
		}
		return cleanup(fmt.Errorf("write config temp file: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			err = fmt.Errorf("%w; close config temp file: %v", err, closeErr)
		}
		return cleanup(fmt.Errorf("sync config temp file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return cleanup(fmt.Errorf("close config temp file: %w", err))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return cleanup(fmt.Errorf("replace config file: %w", err))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod config file: %w", err)
	}
	return nil
}
