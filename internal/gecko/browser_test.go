package gecko

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestFirefoxLinuxRootsIncludeXDG(t *testing.T) {
	home := t.TempDir()
	b := mustBrowser(t, "firefox")

	t.Setenv("XDG_CONFIG_HOME", "")
	want := []string{
		filepath.Join(home, ".mozilla", "firefox"),
		filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
		filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"),
		filepath.Join(home, ".config", "mozilla", "firefox"),
		filepath.Join(home, ".var", "app", "org.mozilla.firefox", "config", "mozilla", "firefox"),
	}
	if got := b.Roots(home, true); !slices.Equal(got, want) {
		t.Errorf("roots with XDG_CONFIG_HOME unset:\n got %q\nwant %q", got, want)
	}

	// A relative XDG_CONFIG_HOME is invalid per the spec and is ignored.
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	if got := b.Roots(home, true); !slices.Equal(got, want) {
		t.Errorf("roots with a relative XDG_CONFIG_HOME:\n got %q\nwant %q", got, want)
	}

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	want[3] = filepath.Join(xdg, "mozilla", "firefox")
	if got := b.Roots(home, true); !slices.Equal(got, want) {
		t.Errorf("roots with XDG_CONFIG_HOME=%s:\n got %q\nwant %q", xdg, got, want)
	}

	// A profile that exists only under the XDG root resolves.
	writeRoot(t, want[3], singleProfileINI, "abcd.default")
	got, err := ResolveProfile(b, home, true, "")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if w := filepath.Join(want[3], "abcd.default"); got != w {
		t.Errorf("got %q, want %q", got, w)
	}

	// Legacy plus XDG roots both holding profiles.ini stays ambiguous.
	writeRoot(t, want[0], singleProfileINI, "abcd.default")
	if _, err := ResolveProfile(b, home, true, ""); err == nil {
		t.Error("expected an error when the legacy and XDG roots both have profiles.ini")
	}
}
