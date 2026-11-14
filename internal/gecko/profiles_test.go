package gecko

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustBrowser(t *testing.T, name string) Browser {
	t.Helper()
	b, ok := Lookup(name)
	if !ok {
		t.Fatalf("no Gecko browser %q", name)
	}
	return b
}

// writeRoot writes profiles.ini under root and creates each named profile dir.
func writeRoot(t *testing.T, root, ini string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The layout of a real Firefox install: the installed build uses the
// [Install] default, while an older profile still carries Default=1.
const installBeatsDefaultINI = `[Profile1]
Name=default
IsRelative=1
Path=Profiles/xznywdor.default
Default=1

[Profile0]
Name=default-release
IsRelative=1
Path=Profiles/izx6uopk.default-release

[General]
StartWithLastProfile=1
Version=2

[Install2656FF1E876E9973]
Default=Profiles/izx6uopk.default-release
Locked=1
`

const twoProfilesNoDefaultINI = `[Profile0]
Name=work
IsRelative=1
Path=Profiles/aaaa.work

[Profile1]
Name=play
IsRelative=1
Path=Profiles/bbbb.play
`

const twoInstallsINI = `[Profile0]
Name=default-release
IsRelative=1
Path=Profiles/aaaa.default-release

[Profile1]
Name=dev-edition-default
IsRelative=1
Path=Profiles/bbbb.dev-edition-default

[Install1111111111111111]
Default=Profiles/aaaa.default-release

[Install2222222222222222]
Default=Profiles/bbbb.dev-edition-default
`

const singleProfileINI = `[Profile0]
Name=default
IsRelative=1
Path=abcd.default
`

func TestResolveProfileInstallDefaultWins(t *testing.T) {
	home := t.TempDir()
	b := mustBrowser(t, "firefox")
	root := b.Roots(home, false)[0]
	writeRoot(t, root, installBeatsDefaultINI, "Profiles/xznywdor.default", "Profiles/izx6uopk.default-release")
	got, err := ResolveProfile(b, home, false, "")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if want := filepath.Join(root, "Profiles", "izx6uopk.default-release"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveProfileSingleProfile(t *testing.T) {
	for _, linux := range []bool{false, true} {
		home := t.TempDir()
		b := mustBrowser(t, "zen")
		root := b.Roots(home, linux)[0]
		writeRoot(t, root, singleProfileINI, "abcd.default")
		got, err := ResolveProfile(b, home, linux, "")
		if err != nil {
			t.Fatalf("linux=%v: %v", linux, err)
		}
		if want := filepath.Join(root, "abcd.default"); got != want {
			t.Errorf("linux=%v: got %q, want %q", linux, got, want)
		}
	}
}

func TestResolveProfileAmbiguityListsCandidates(t *testing.T) {
	type setup func(t *testing.T, home string, b Browser) (linux bool)
	mac := func(ini string, dirs ...string) setup {
		return func(t *testing.T, home string, b Browser) bool {
			writeRoot(t, b.Roots(home, false)[0], ini, dirs...)
			return false
		}
	}
	cases := []struct {
		name      string
		setup     setup
		wantInErr []string
		// byName and byDir each pick one candidate and must resolve it.
		byName, wantByName string
		byDir, want        string
	}{
		{
			name:       "two profiles and no default",
			setup:      mac(twoProfilesNoDefaultINI, "Profiles/aaaa.work", "Profiles/bbbb.play"),
			wantInErr:  []string{"work", "play"},
			byName:     "play",
			wantByName: "Profiles/bbbb.play",
			byDir:      "Profiles/bbbb.play",
			want:       "Profiles/bbbb.play",
		},
		{
			name:       "two installs with different defaults",
			setup:      mac(twoInstallsINI, "Profiles/aaaa.default-release", "Profiles/bbbb.dev-edition-default"),
			wantInErr:  []string{"aaaa.default-release", "bbbb.dev-edition-default"},
			byName:     "dev-edition-default",
			wantByName: "Profiles/bbbb.dev-edition-default",
			byDir:      "aaaa.default-release",
			want:       "Profiles/aaaa.default-release",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			b := mustBrowser(t, "firefox")
			linux := tc.setup(t, home, b)
			root := b.Roots(home, linux)[0]
			_, err := ResolveProfile(b, home, linux, "")
			if err == nil {
				t.Fatal("expected an ambiguity error")
			}
			for _, s := range tc.wantInErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q should list %q", err, s)
				}
			}
			got, err := ResolveProfile(b, home, linux, tc.byName)
			if err != nil {
				t.Fatalf("by name %q: %v", tc.byName, err)
			}
			if want := filepath.Join(root, filepath.FromSlash(tc.wantByName)); got != want {
				t.Errorf("by name %q: got %q, want %q", tc.byName, got, want)
			}
			got, err = ResolveProfile(b, home, linux, tc.byDir)
			if err != nil {
				t.Fatalf("by dir %q: %v", tc.byDir, err)
			}
			if want := filepath.Join(root, filepath.FromSlash(tc.want)); got != want {
				t.Errorf("by dir %q: got %q, want %q", tc.byDir, got, want)
			}
		})
	}
}

func TestResolveProfileNativeAndSnapRootsBothExist(t *testing.T) {
	home := t.TempDir()
	b := mustBrowser(t, "firefox")
	roots := b.Roots(home, true)
	native, snap := roots[0], roots[1]
	writeRoot(t, native, singleProfileINI, "abcd.default")
	writeRoot(t, snap, `[Profile0]
Name=snappy
IsRelative=1
Path=efgh.snappy
`, "efgh.snappy")

	_, err := ResolveProfile(b, home, true, "")
	if err == nil {
		t.Fatal("expected an error when two roots have profiles.ini")
	}
	for _, s := range []string{native, snap} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q should list root %q", err, s)
		}
	}

	got, err := ResolveProfile(b, home, true, "snappy")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if want := filepath.Join(snap, "efgh.snappy"); got != want {
		t.Errorf("by name: got %q, want %q", got, want)
	}
	abs := filepath.Join(native, "abcd.default")
	got, err = ResolveProfile(b, home, true, abs)
	if err != nil {
		t.Fatalf("by absolute dir: %v", err)
	}
	if got != abs {
		t.Errorf("by absolute dir: got %q, want %q", got, abs)
	}
}

func TestResolveProfileNoProfilesINI(t *testing.T) {
	home := t.TempDir()
	_, err := ResolveProfile(mustBrowser(t, "librewolf"), home, false, "")
	if err == nil || !strings.Contains(err.Error(), "profiles.ini") {
		t.Fatalf("got %v, want an error naming profiles.ini", err)
	}
}
