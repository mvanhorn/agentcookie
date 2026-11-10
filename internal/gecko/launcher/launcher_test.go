package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchersQuotePathsAndNeverPreferences(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		home := t.TempDir()
		p, e := Install(Options{Home: home, GOOS: platform, Name: "zen", Executable: "/tmp/browser's name", Profile: "/tmp/profile's name", Port: 9224})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(p); e != nil {
			t.Fatal(e)
		}
		var script string
		if platform == "darwin" {
			script = filepath.Join(p, "Contents", "MacOS", "launch")
		} else {
			script = filepath.Join(home, ".local", "share", "applications", "agentcookie-zen-launch")
		}
		b, e := os.ReadFile(script)
		if e != nil || !strings.Contains(string(b), "--remote-debugging-port=9224") || !strings.Contains(string(b), "'\"'\"'") {
			t.Fatal(string(b), e)
		}
		if strings.Contains(string(b), "prefs.js") {
			t.Fatal("preferences modified")
		}
	}
}
