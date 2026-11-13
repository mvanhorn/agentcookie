package envfile

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRenderRoundTrip(t *testing.T) {
	for _, v := range []string{
		"abc", "a=b==", "a b", `C:\dir\`, `"tok"`, `'tok'`, `x"y'z`, "x\ry", "", "it's", `'it'`, "#x", " lead",
	} {
		line, err := RenderLine("K", v)
		if err != nil {
			t.Fatalf("render %q: %v", v, err)
		}
		got, err := ParseBytes([]byte(line + "\n"))
		if err != nil {
			t.Fatalf("parse rendered %q: %v", line, err)
		}
		if got["K"] != v {
			t.Errorf("round trip %q: wrote %q, read %q", v, line, got["K"])
		}
	}
}

func TestRenderValueForms(t *testing.T) {
	cases := map[string]string{
		"abc":      "abc",
		"a b":      "a b",
		`C:\dir\`:  `'C:\dir\'`,
		`"tok"`:    `'"tok"'`,
		`'it'`:     `"'it'"`,
		`x"y'z`:    `x"y'z`,
		"a=b==":    "a=b==",
		`$(id)`:    `$(id)`,
		"x\ry":     "x\ry",
		"":         "",
		`"`:        `"`,
		`it's C:\`: "",
	}
	for in, want := range cases {
		got, err := RenderValue(in)
		if want == "" && in != "" {
			if err == nil {
				t.Errorf("RenderValue(%q) = %q, want a refusal", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("RenderValue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRenderRefusals(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{`'a"b'`, ErrUnquotable},
		{`it's C:\`, ErrUnquotable},
		{"a\nb", ErrNewline},
		{"tok\r", ErrTrailingCR},
	}
	for _, tc := range cases {
		_, err := RenderValue(tc.in)
		if !errors.Is(err, tc.want) {
			t.Errorf("RenderValue(%q) error = %v, want %v", tc.in, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), tc.in) {
			t.Errorf("refusal for %q echoes the value: %v", tc.in, err)
		}
	}
}

func TestRenderLineLengthLimit(t *testing.T) {
	// "K=" plus the value: the longest line the readers accept is one byte
	// under the limit.
	fits := strings.Repeat("a", MaxLineBytes-3)
	line, err := RenderLine("K", fits)
	if err != nil {
		t.Fatalf("line of %d bytes refused: %v", MaxLineBytes-1, err)
	}
	if got, err := ParseBytes([]byte(line + "\n")); err != nil || got["K"] != fits {
		t.Fatalf("longest accepted line does not read back: %v", err)
	}
	if _, err := ParseBytes([]byte("K=" + fits + "a\n")); err == nil {
		t.Fatal("reader accepted a line at the limit; the writer limit is off by one")
	}
	if _, err := RenderLine("K", fits+"a"); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("line at the limit: err = %v, want ErrLineTooLong", err)
	}
	// Quoting counts toward the limit.
	if _, err := RenderLine("K", `"`+strings.Repeat("a", MaxLineBytes-6)+`"`); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("quoted line at the limit: err = %v, want ErrLineTooLong", err)
	}
}

func TestRenderShellValue(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	// zsh, the macOS login shell, expands more than sh in assignments.
	shells := []string{sh}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		shells = append(shells, zsh)
	}
	for _, v := range []string{
		"abc", "a b", `$(id)`, "x;y", `"tok"`, `'it'`, `C:\x\`, "a=b", "~", "x\ry", "`id`", "", "it's", "=ls", "==x",
	} {
		rv, err := RenderShellValue(v)
		if err != nil {
			t.Fatalf("RenderShellValue(%q): %v", v, err)
		}
		for _, shell := range shells {
			out, err := exec.Command(shell, "-c", `eval "$1"; printf %s "$K"`, "sh", "K="+rv).Output()
			if err != nil {
				t.Fatalf("%s eval K=%s: %v", shell, rv, err)
			}
			if string(out) != v {
				t.Errorf("%s read K=%s as %q, want %q", shell, rv, out, v)
			}
		}
		got, err := ParseBytes([]byte("K=" + rv + "\n"))
		if err != nil || got["K"] != v {
			t.Errorf("reader read K=%s as %q (%v), want %q", rv, got["K"], err, v)
		}
	}
	for _, v := range []string{"a b", `$(id)`, "x;y", `"tok"`} {
		if rv, _ := RenderShellValue(v); rv != "'"+v+"'" {
			t.Errorf("RenderShellValue(%q) = %s, want single quotes", v, rv)
		}
	}
	for _, v := range []string{`it's $HOME`, "a\nb", "it's `id`", `it's "x"`} {
		if rv, err := RenderShellValue(v); err == nil {
			t.Errorf("RenderShellValue(%q) = %s, want a refusal", v, rv)
		}
	}
}

func TestParseStrictInvalidKeyDoesNotEcho(t *testing.T) {
	_, err := ParseBytes([]byte("OK=1\nsk-live+SECRET=1\n"))
	if err == nil {
		t.Fatal("want an error for an invalid key")
	}
	if !strings.Contains(err.Error(), "line 2") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error = %q, want line 2 and no key text", err)
	}
}

func TestParseLenientKeys(t *testing.T) {
	entries, err := Parse(strings.NewReader("OK=1\nsk-live+SECRET=1\n"), Options{LenientKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Key != "sk-live+SECRET" || entries[1].Line != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := Parse(strings.NewReader("K =1\n"), Options{LenientKeys: true}); err == nil {
		t.Fatal("lenient mode still rejects whitespace around '='")
	}
}

func TestParseBOMCRLFAndContinuation(t *testing.T) {
	entries, err := Parse(strings.NewReader("\ufeffA=1\r\nLONG=part1\\\npart2\r\nB=\"x\"\r\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := ToMap(entries)
	if got["A"] != "1" || got["LONG"] != "part1part2" || got["B"] != "x" || len(got) != 3 {
		t.Fatalf("got %q", got)
	}
	if !entries[1].Continued || entries[1].Line != 2 || entries[0].Continued {
		t.Fatalf("continuation not reported on the right entry: %+v", entries)
	}
}

func TestParseErrorsNameLines(t *testing.T) {
	cases := map[string]string{
		"A=1\nNOPE\n":       "line 2: missing '='",
		"A=1\nK = v\n":      "line 2: whitespace around '='",
		"A=1\nK=a\\\nb\\\n": "line 3: unterminated backslash continuation (started on line 2)",
		"A=" + strings.Repeat("x", MaxLineBytes) + "\n": "line 1: scan",
	}
	for in, want := range cases {
		_, err := ParseBytes([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parse error = %v, want %q", err, want)
		}
	}
}
