package gecko

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCookieProvenance(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		c := Cookie{Domain: "example.com", Name: "sid", Value: "x", Path: "/", Secure: true, HostOnly: true, Session: true, SameSite: "unspecified", StoreID: "firefox-container-6", PartitionKey: &Partition{TopLevelSite: "https://other.test", HasCrossSiteAncestor: ancestor}}
		r, err := MapCookie(c, time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		if r.Container != 6 || r.Cookie.HostKey != "example.com" || r.Cookie.SameSite != -1 || r.Cookie.ExpiresUTC != 0 || r.Cookie.TopFrameSiteKey != "https://other.test" || r.Cookie.HasCrossSiteAncestor != boolInt(ancestor) {
			t.Fatalf("bad mapping: %+v", r)
		}
		c.Secure = false
		if _, err = MapCookie(c, time.Now()); err == nil {
			t.Fatal("insecure partition accepted")
		}
	}
}
func TestCookieFailsClosed(t *testing.T) {
	base := `{"domain":"example.com","name":"sid","value":"x","path":"/","hostOnly":false,"session":true,"sameSite":"lax","storeId":"firefox-default", "futureIsolation":"secret"}`
	var c Cookie
	if err := json.Unmarshal([]byte(base), &c); err != nil {
		t.Fatal(err)
	}
	if _, err := MapCookie(c, time.Now()); err == nil {
		t.Fatal("unknown isolation accepted")
	}
	c.Unknown = nil
	c.FirstPartyDomain = "example.com"
	if _, err := MapCookie(c, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.FirstPartyDomain = "other.test"
	if _, err := MapCookie(c, time.Now()); err == nil {
		t.Fatal("cross-party FPI accepted")
	}
}
func TestAttributes(t *testing.T) {
	a, err := ParseAttributes("^userContextId=6&partitionKey=%28https%2Cexample.com%29")
	if err != nil || a.Container != 6 || a.Partition.TopLevelSite != "https://example.com" {
		t.Fatalf("%+v %v", a, err)
	}
	for _, s := range []string{"^userContextId=1&userContextId=2", "^privateBrowsingId=1", "^future=1", "^userContextId=-1"} {
		if _, err := ParseAttributes(s); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestUnknownPartitionFieldFailsClosed(t *testing.T) {
	var c Cookie
	if err := json.Unmarshal([]byte(`{"domain":"example.com","name":"sid","value":"x","path":"/","hostOnly":true,"session":true,"secure":true,"sameSite":"lax","storeId":"firefox-default","partitionKey":{"topLevelSite":"https://other.test","futureIsolation":"value"}}`), &c); err != nil {
		t.Fatal(err)
	}
	if _, err := MapCookie(c, time.Now()); err == nil {
		t.Fatal("nested isolation flattened")
	}
	if _, err := ParseAttributes("^partitionKey=https,example.com"); err == nil {
		t.Fatal("malformed partition accepted")
	}
}
