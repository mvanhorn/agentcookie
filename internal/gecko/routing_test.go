package gecko

import (
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"testing"
)

func TestRoutingUsesWholeSitesAndStorage(t *testing.T) {
	records := []Record{{Site: "example.com", Container: 0}, {Site: "example.com", Container: 6}, {Site: "unique.test", Container: 6}}
	origins := []Origin{{Origin: "https://ls.example.com:8443", Container: 9}}
	ids := []Identity{{Name: "Work", CookieStoreID: "firefox-container-6"}}
	routes, e := Route(records, origins, ids, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if routes["example.com"].State != protocol.Held || len(routes["example.com"].Containers) != 3 || routes["unique.test"].Container != 6 {
		t.Fatal(routes)
	}
	routes, e = Route(records, origins, ids, []string{"Work"}, nil)
	if e != nil || routes["example.com"].Container != 6 || routes["example.com"].State != protocol.Shipped {
		t.Fatal(routes, e)
	}
	routes, e = Route(records, origins, ids, nil, map[string][]string{"default": {"example.com"}})
	if e != nil || routes["example.com"].Container != 0 || routes["example.com"].State != protocol.Shipped {
		t.Fatal(routes, e)
	}
}
func TestRoutingRejectsUnknownAndDuplicateNames(t *testing.T) {
	ids := []Identity{{Name: "Work", CookieStoreID: "firefox-container-1"}, {Name: "Work", CookieStoreID: "firefox-container-2"}}
	if _, e := Route(nil, nil, ids, []string{"Work"}, nil); e == nil {
		t.Fatal("ambiguous name accepted")
	}
	if _, e := Route(nil, nil, ids, []string{"Missing"}, nil); e == nil {
		t.Fatal("unknown preference accepted")
	}
}
