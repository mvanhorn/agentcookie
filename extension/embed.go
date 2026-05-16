// Package extension embeds the agentcookie Chrome extension at build time so
// the wizard can unpack it into ~/.agentcookie/extension/ at install time
// without a separate download step.
package extension

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed manifest.json background.js stealth.js
var assets embed.FS

// Install copies the embedded extension files into dir. The directory is
// created if it does not exist. Existing files are overwritten (the wizard
// always wants the current binary's bundled extension version, not a stale
// copy from a previous install).
func Install(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create extension dir %s: %w", dir, err)
	}
	return fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		out := filepath.Join(dir, path)
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		return nil
	})
}
