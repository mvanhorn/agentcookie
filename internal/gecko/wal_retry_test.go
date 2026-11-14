package gecko

import (
	"os"
	"testing"
)

func TestReadCookiesRetriesWhenWALDisappearsMidCopy(t *testing.T) {
	path := writeFixture(t, 17, []mozRow{row("a", ".example.com", realExpiryMS)})
	if err := os.WriteFile(path+"-wal", nil, 0o600); err != nil {
		t.Fatalf("write -wal: %v", err)
	}
	calls := 0
	beforeWALCopy = func() {
		calls++
		if calls == 1 {
			if err := os.Remove(path + "-wal"); err != nil {
				t.Errorf("remove -wal: %v", err)
			}
		}
	}
	t.Cleanup(func() { beforeWALCopy = nil })

	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	if calls != 1 {
		t.Errorf("beforeWALCopy ran %d times, want 1 (second attempt sees no -wal)", calls)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("got %v, want the one cookie", byName(t, got))
	}
}
