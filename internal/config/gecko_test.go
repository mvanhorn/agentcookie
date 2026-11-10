package config

import (
	"testing"
)

func TestGeckoEffectiveSourceNeverComputesChromiumPath(t *testing.T) {
	cfg, e := EffectiveSource(&SourceConfig{Browser: BrowserRef{Name: "zen", Profile: "Personal"}}, SourceOverrides{})
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Chrome.DBPath != "" || cfg.Browser.Profile != "Personal" || cfg.Browser.BiDiPort != 9224 || cfg.Browser.CompanionPort != 9225 {
		t.Fatalf("%+v", cfg)
	}
	if _, e = EffectiveSource(&SourceConfig{Browser: BrowserRef{Name: "zen"}, Chrome: ChromeRef{DBPath: "/fixture/Chrome/Cookies"}}, SourceOverrides{}); e == nil {
		t.Fatal("wrong-family DB path accepted")
	}
	if _, e = SourceBrowserCookiesPath("zen", "Default"); e == nil {
		t.Fatal("Gecko resolved as Chromium")
	}
}
