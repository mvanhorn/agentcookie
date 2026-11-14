package chromeflags

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestDisabledFeaturesMergesAndDedupes(t *testing.T) {
	cases := map[string]string{
		"":                      "MacAppCodeSignClone",
		"Translate":             "Translate,MacAppCodeSignClone",
		"MacAppCodeSignClone,A": "MacAppCodeSignClone,A",
		" A , ,B":               "A,B,MacAppCodeSignClone",
	}
	for in, want := range cases {
		if got := DisabledFeatures(in); got != want {
			t.Errorf("DisabledFeatures(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChromedpDefaultsCarried guards against chromedp changing its default
// --disable-features value: our override replaces it, so it must be a
// superset. It reads chromedp's real default by launching a fake "chrome"
// that records its argv and exits.
func TestChromedpDefaultsCarried(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake chrome is a shell script")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argsFile + "'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(fake))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 5*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(ctx); err == nil {
		t.Fatal("chromedp.Run succeeded against a fake chrome that exits")
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake chrome did not record its args: %v", err)
	}
	var defaults string
	for a := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(a, "--disable-features="); ok {
			defaults = v
		}
	}
	if defaults == "" {
		t.Fatalf("no --disable-features in chromedp's default launch args:\n%s", data)
	}

	got := ChromedpDisabledFeatures()
	for f := range strings.SplitSeq(defaults, ",") {
		if !strings.Contains(","+got+",", ","+f+",") {
			t.Errorf("ChromedpDisabledFeatures() = %q, missing chromedp default %q", got, f)
		}
	}
}
