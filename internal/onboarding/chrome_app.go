package onboarding

import (
	"fmt"
	"os"
	"path/filepath"
)

// InstallChromeApp writes a macOS app bundle in the chosen directory. It never
// launches a browser or a service. Refusing existing bundles avoids clobbering a
// user's app, symlink, or previously selected destination.
func InstallChromeApp(path, binary, configDir, name string) error {
	script, err := ChromeLauncher(binary, configDir, name)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".app" {
		return fmt.Errorf("absolute .app destination required")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("launcher already exists: %s", path)
		}
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".agentcookie-chrome-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	macos := filepath.Join(staging, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(macos, "launch"), []byte(script), 0700); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleName</key><string>agentcookie Chrome</string><key>CFBundleIdentifier</key><string>dev.agentcookie.chrome</string><key>CFBundleExecutable</key><string>launch</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(staging, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		return err
	}
	return os.Rename(staging, path)
}
