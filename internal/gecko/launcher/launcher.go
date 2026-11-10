// Package launcher writes an opt-in launcher; it never modifies preferences,
// restarts a browser, or installs a background service.
package launcher

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Options struct {
	Home, GOOS, Name, Executable, Profile string
	Port                                  int
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func Install(o Options) (string, error) {
	if o.Home == "" || !filepath.IsAbs(o.Executable) || !filepath.IsAbs(o.Profile) || o.Port < 1 || o.Port > 65535 {
		return "", fmt.Errorf("absolute executable/profile and valid port required")
	}
	if o.Name != "zen" && o.Name != "firefox" && o.Name != "librewolf" && o.Name != "floorp" {
		return "", fmt.Errorf("unsupported Gecko browser")
	}
	name := strings.ToUpper(o.Name[:1]) + o.Name[1:] + " (agentcookie)"
	script := "#!/bin/sh\nexec " + shellQuote(o.Executable) + " --remote-debugging-port=" + fmt.Sprint(o.Port) + " -profile " + shellQuote(o.Profile) + " \"$@\"\n"
	var path string
	switch o.GOOS {
	case "darwin":
		path = filepath.Join(o.Home, "Applications", name+".app")
		macos := filepath.Join(path, "Contents", "MacOS")
		if e := os.MkdirAll(macos, 0700); e != nil {
			return "", e
		}
		if e := os.WriteFile(filepath.Join(macos, "launch"), []byte(script), 0700); e != nil {
			return "", e
		}
		var escaped bytes.Buffer
		_ = xml.EscapeText(&escaped, []byte(name))
		plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleName</key><string>` + escaped.String() + `</string><key>CFBundleIdentifier</key><string>dev.agentcookie.gecko.` + o.Name + `</string><key>CFBundleExecutable</key><string>launch</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
		if e := os.WriteFile(filepath.Join(path, "Contents", "Info.plist"), []byte(plist), 0600); e != nil {
			return "", e
		}
	case "linux":
		dir := filepath.Join(o.Home, ".local", "share", "applications")
		if e := os.MkdirAll(dir, 0700); e != nil {
			return "", e
		}
		scriptPath := filepath.Join(dir, "agentcookie-"+o.Name+"-launch")
		if e := os.WriteFile(scriptPath, []byte(script), 0700); e != nil {
			return "", e
		}
		path = filepath.Join(dir, "agentcookie-"+o.Name+".desktop")
		quoted := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(scriptPath)
		desktop := "[Desktop Entry]\nType=Application\nName=" + name + "\nExec=\"" + quoted + "\" %u\nTerminal=false\nCategories=Network;WebBrowser;\n"
		if e := os.WriteFile(path, []byte(desktop), 0600); e != nil {
			return "", e
		}
	default:
		return "", fmt.Errorf("gecko launcher supports macOS and Linux only")
	}
	return path, nil
}
