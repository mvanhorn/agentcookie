package browsers

import (
	"reflect"
	"testing"
)

func TestRegistryFamilyAndOwnership(t *testing.T) {
	want := []string{"arc", "atlas", "brave", "chrome", "dia", "edge"}
	for _, family := range []Family{Chromium} {
		got := SupportedNames(family)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("SupportedNames(%q) = %v, want %v", family, got, want)
		}
		got[0] = "changed"
		if SupportedNames(family)[0] != "arc" {
			t.Fatal("names alias registry")
		}
	}
	for _, family := range []Family{"unknown"} {
		if got := SupportedNames(family); len(got) != 0 {
			t.Errorf("SupportedNames(%q) = %v, want no names", family, got)
		}
	}
	for _, name := range want {
		b, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if b.Name != name || b.Family != Chromium {
			t.Errorf("Lookup(%q) = %+v", name, b)
		}
		root := b.SupportDir[0]
		b.SupportDir[0] = "changed"
		b.Name = "changed"
		again, err := Lookup(name)
		if err != nil || again.Name != name || again.SupportDir[0] != root {
			t.Fatalf("Lookup(%q) changed: %+v, %v", name, again, err)
		}
	}
}

func TestLookupDefaultsAndErrors(t *testing.T) {
	for _, name := range []string{"", " \t", " CHROME "} {
		b, err := Lookup(name)
		if err != nil || b.Name != DefaultName {
			t.Errorf("Lookup(%q) = %+v, %v", name, b, err)
		}
	}
	if _, err := Lookup("missing"); err == nil {
		t.Error("unknown browser accepted")
	}
}

func TestLinuxSupportDirCompatibility(t *testing.T) {
	cases := []struct{ in, want []string }{
		{nil, nil},
		{[]string{}, []string{}},
		{[]string{"Google", "Chrome"}, []string{"google-chrome"}},
		{[]string{"Google", "Chrome", "extra"}, []string{"google-chrome"}},
		{[]string{"Google"}, []string{"Google"}},
		{[]string{"Google", "Other"}, []string{"Google", "Other"}},
		{[]string{"Chromium"}, []string{"chromium"}},
		{[]string{"BraveSoftware", "Brave-Browser"}, []string{"BraveSoftware", "Brave-Browser"}},
		{[]string{"Microsoft Edge"}, []string{"microsoft-edge"}},
		{[]string{"Arc", "User Data"}, []string{"Arc", "User Data"}},
		{[]string{"Dia", "User Data"}, []string{"Dia", "User Data"}},
		{[]string{"com.openai.atlas", "browser-data", "host"}, []string{"com.openai.atlas", "browser-data", "host"}},
		{[]string{"Custom", "Browser"}, []string{"Custom", "Browser"}},
	}
	for _, tc := range cases {
		if got := LinuxSupportDir(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("LinuxSupportDir(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestGeckoRegistryMetadataAndOwnership(t *testing.T) {
	want := []string{"firefox", "floorp", "librewolf", "zen"}
	if got := SupportedNames(Gecko); !reflect.DeepEqual(got, want) {
		t.Fatalf("Gecko names %v", got)
	}
	if got := SupportedNames(""); len(got) != 10 {
		t.Fatalf("all names %v", got)
	}
	for _, name := range want {
		b, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if b.Family != Gecko || b.MacRoot == "" || b.LinuxRoot == "" || b.AppBundle == "" || len(b.LinuxInstallDirs) == 0 {
			t.Fatalf("incomplete Gecko metadata: %+v", b)
		}
		install := b.LinuxInstallDirs[0]
		b.LinuxInstallDirs[0] = "changed"
		root := b.LinuxProfileRoots[0]
		b.LinuxProfileRoots[0] = "changed"
		again, _ := Lookup(name)
		if again.LinuxInstallDirs[0] != install || again.LinuxProfileRoots[0] != root {
			t.Fatal("registry metadata aliased")
		}
		if b.KeychainAccount != "" || b.KeychainService != "" {
			t.Fatal("Gecko must not use Keychain")
		}
	}
}
