package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// SyncGroupCodexAuth provisions the shared login without starting a session.
func SyncGroupCodexAuth(groupPath string) error {
	cfg, err := LoadUserConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	home := cfg.GetGroupCodexConfigDir(groupPath)
	if home == "" {
		return nil
	}
	return applyCodexSharedAuth(home, cfg.Codex.SharedAuthSource)
}

// A symlink keeps token refreshes shared, including atomic replacement of the
// source file. Never copy credentials or overwrite an independent login.
func applyCodexSharedAuth(home, source string) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	source = ExpandPath(source)
	if !filepath.IsAbs(source) || hasParentPathComponent(source) {
		return fmt.Errorf("shared_auth_source must be an absolute path or home-relative path")
	}
	store, err := newHomeSkillStore(home, "Codex")
	if err != nil {
		return err
	}
	dest := filepath.Join(store.home, "auth.json")
	if filepath.Clean(source) == dest {
		return nil
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("shared login source %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("shared login source %s is not a regular file", source)
	}
	configPath := filepath.Join(store.home, "config.toml")
	if data, err := os.ReadFile(configPath); err == nil {
		var cfg struct {
			Store string `toml:"cli_auth_credentials_store"`
		}
		if _, err := toml.Decode(string(data), &cfg); err != nil {
			return fmt.Errorf("read Codex credential storage: %w", err)
		}
		if cfg.Store != "" && cfg.Store != "file" {
			return fmt.Errorf("shared login requires cli_auth_credentials_store = \"file\" in %s", configPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Avoid linking a file to itself through aliased home directories.
	if target, err := os.Stat(dest); err == nil && os.SameFile(info, target) {
		if _, err := os.Readlink(dest); err == nil {
			return nil
		}
		sourceDir, sourceErr := filepath.EvalSymlinks(filepath.Dir(source))
		destDir, destErr := filepath.EvalSymlinks(store.home)
		if sourceErr == nil && destErr == nil && sourceDir == destDir {
			return nil
		}
	}
	if err := os.MkdirAll(store.home, 0700); err != nil {
		return err
	}
	if err := os.Symlink(source, dest); err != nil {
		// Another launch may have created the same link concurrently.
		if target, linkErr := os.Readlink(dest); linkErr == nil && target == source {
			return nil
		}
		return fmt.Errorf("cannot share login at %s; preserve and move any existing credentials before syncing: %w", dest, err)
	}
	return nil
}
