package gecko

import (
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"testing"
)

func TestDestinationRoutingDoesNotFallBackAfterLogout(t *testing.T) {
	identities := []Identity{{Name: "Personal", CookieStoreID: "firefox-container-1"}, {Name: "Work", CookieStoreID: "firefox-container-6"}}
	records := []Record{{Site: "example.com", Container: 6}}
	routes, err := RouteContainers(records, nil, identities, []string{"Personal"})
	if err != nil {
		t.Fatal(err)
	}
	if routes["example.com"].State != protocol.Excluded {
		t.Fatalf("cross-account route after logout: %+v", routes)
	}
	routes, err = RouteContainers(records, nil, identities, []string{"Work"})
	if err != nil || routes["example.com"].Container != 6 || routes["example.com"].State != protocol.Shipped {
		t.Fatalf("work lost: %+v %v", routes, err)
	}
	routes, err = RouteContainers([]Record{{Site: "example.com", Container: 1}, {Site: "example.com", Container: 6}}, nil, identities, []string{"Work", "Personal"})
	if err != nil || routes["example.com"].Container != 6 {
		t.Fatalf("preferences not ordered: %+v %v", routes, err)
	}
	routes, err = Route(records, nil, identities, []string{"Personal"}, nil)
	if err != nil || routes["example.com"].State != protocol.Shipped {
		t.Fatal("legacy unique-site semantics changed")
	}
}
