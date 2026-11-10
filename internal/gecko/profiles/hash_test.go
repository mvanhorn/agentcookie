package profiles

import (
	"encoding/json"
	"os"
	"testing"
)

// Values generated from Mozilla's CityHash Version 1 reference implementation:
// https://github.com/mozilla/gecko-dev/blob/5836a062726f715fda621338a17b51aff30d0a8c/other-licenses/nsis/Contrib/CityHash/cityhash/city.cpp
// These deliberately differ from newer CityHash releases.
func TestCityHash64Reference(t *testing.T) {
	data, err := os.ReadFile("testdata/cityhash-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Length int
		Hash   uint64
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		b := make([]byte, v.Length)
		for i := range b {
			b[i] = byte(i*37 + 11)
		}
		if got := CityHash64(b); got != v.Hash {
			t.Errorf("length %d: %X, want %X", v.Length, got, v.Hash)
		}
	}
}
func TestInstallHash(t *testing.T) {
	for path, want := range map[string]string{"/usr/lib/firefox": "4F96D1932A9F858E", "/Applications/Firefox.app/Contents/MacOS": "2656FF1E876E9973", "/Applications/Zen.app/Contents/Resources": "7DA302846E837320", "/opt/浏览器/🦊": "BF90A62727C7834D"} {
		if got := InstallHash(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}
