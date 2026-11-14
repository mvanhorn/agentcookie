package cli

import (
	"path/filepath"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func ck(host, name, value string) chrome.Cookie {
	return chrome.Cookie{HostKey: host, Name: name, Value: value, Path: "/", IsSecure: 1}
}

func TestPlanProfileSync_Ownership(t *testing.T) {
	ours := ck(".example.com", "sid", "old")
	state := &profileSyncState{Written: map[string]uint64{
		profileSyncKey(ours): profileSyncValueHash(ours),
	}}
	src := []chrome.Cookie{
		ck(".new.com", "a", "1"),          // missing in target -> add
		ck(".example.com", "sid", "new"),  // ours, source changed -> update
		ck(".same.com", "x", "v"),         // identical -> unchanged
		ck(".mine.com", "tok", "src-val"), // target set its own -> keep
		ck(".claude.ai", "sessionKey", "acct-a"),
		ck("api.anthropic.com", "s", "z"),
	}
	dst := []chrome.Cookie{
		ours,
		ck(".same.com", "x", "v"),
		ck(".mine.com", "tok", "target-val"),
		ck(".claude.ai", "sessionKey", "acct-b"),
	}
	p := planProfileSync(src, dst, state, profileSyncDefaultExcludes)

	if p.added != 1 || p.updated != 1 || p.unchanged != 1 || len(p.targetOwned) != 1 || p.excluded != 2 {
		t.Fatalf("plan counts: added=%d updated=%d unchanged=%d owned=%d excluded=%d",
			p.added, p.updated, p.unchanged, len(p.targetOwned), p.excluded)
	}
	got := map[string]string{}
	for _, c := range p.write {
		got[c.HostKey+"/"+c.Name] = c.Value
	}
	if got[".new.com/a"] != "1" || got[".example.com/sid"] != "new" || len(got) != 2 {
		t.Errorf("write set = %v", got)
	}
}

func TestHostInDomains(t *testing.T) {
	cases := map[string]bool{
		".claude.ai": true, "claude.ai": true, "www.claude.ai": true,
		"notclaude.ai": false, "claude.ai.evil.com": false,
	}
	for h, want := range cases {
		if got := hostInDomains(h, []string{"claude.ai"}); got != want {
			t.Errorf("hostInDomains(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestProfileSyncState_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s", "chrome-profile-1.json")
	st, err := loadProfileSyncState(path)
	if err != nil || len(st.Written) != 0 {
		t.Fatalf("load missing: %v %v", st, err)
	}
	st.Written["k"] = 42
	if err := saveProfileSyncState(path, st); err != nil {
		t.Fatal(err)
	}
	again, err := loadProfileSyncState(path)
	if err != nil || again.Written["k"] != 42 {
		t.Fatalf("reload: %v %v", again, err)
	}
}

func TestRunProfileSync_RejectsBadProfiles(t *testing.T) {
	defer func(f, to string) { profileSyncFrom, profileSyncTo = f, to }(profileSyncFrom, profileSyncTo)
	for _, tc := range []struct{ from, to string }{
		{"Default", ""}, {"Default", "Default"}, {"Default", "../x"}, {"Default", "Profile 1/../../etc"},
	} {
		profileSyncFrom, profileSyncTo = tc.from, tc.to
		if err := runProfileSync(profileSyncCmd, nil); err == nil {
			t.Errorf("from=%q to=%q: expected error", tc.from, tc.to)
		}
	}
}

func TestPlanProfileSync_PartitionedRowsStayDistinct(t *testing.T) {
	plain := ck(".site.com", "c", "src")
	part := plain
	part.TopFrameSiteKey = "https://top.example"
	part.HasCrossSiteAncestor = 1
	// Target has only the partitioned row, set by itself.
	dstPart := part
	dstPart.Value = "target-own"
	p := planProfileSync([]chrome.Cookie{plain, part}, []chrome.Cookie{dstPart}, &profileSyncState{Written: map[string]uint64{}}, nil)
	if p.added != 1 || len(p.targetOwned) != 1 || len(p.write) != 1 || p.write[0].TopFrameSiteKey != "" {
		t.Fatalf("added=%d owned=%d write=%#v", p.added, len(p.targetOwned), p.write)
	}
}

func TestProfileSyncLock_Exclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := profileSyncLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profileSyncLock(path); err == nil {
		t.Fatal("second lock succeeded, want error")
	}
	unlock()
	again, err := profileSyncLock(path)
	if err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	again()
}
