package gecko

import (
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type Origin struct {
	Origin    string
	Container uint32
}

// Route selects exactly one container per registrable site, across every
// surface together. Identical cookie values never erase identity ambiguity.
func Route(records []Record, origins []Origin, identities []Identity, preferences []string, overrides map[string][]string) (map[string]protocol.SiteStatus, error) {
	containers := map[string]map[uint32]bool{}
	add := func(site string, n uint32) {
		if containers[site] == nil {
			containers[site] = map[uint32]bool{}
		}
		containers[site][n] = true
	}
	for _, r := range records {
		add(r.Site, r.Container)
	}
	for _, o := range origins {
		u, e := url.Parse(o.Origin)
		if e != nil || u.Hostname() == "" {
			continue
		}
		add(Site(u.Hostname()), o.Container)
	}
	resolve := func(name string) (uint32, error) {
		if name == "default" {
			return 0, nil
		}
		if n, e := strconv.ParseUint(name, 10, 32); e == nil {
			return uint32(n), nil
		}
		var matches []uint32
		for _, id := range identities {
			if id.Name == name {
				n, e := ContainerID(id.CookieStoreID)
				if e != nil {
					return 0, e
				}
				matches = append(matches, n)
			}
		}
		if len(matches) != 1 {
			return 0, fmt.Errorf("container %q resolved to %d identities; use numeric ID", name, len(matches))
		}
		return matches[0], nil
	}
	var prefs []uint32
	all := false
	for _, p := range preferences {
		if p == "all" {
			if len(preferences) != 1 {
				return nil, fmt.Errorf("all must be the only preference")
			}
			all = true
			continue
		}
		n, e := resolve(p)
		if e != nil {
			return nil, e
		}
		prefs = append(prefs, n)
	}
	bySite := map[string]uint32{}
	for name, sites := range overrides {
		n, e := resolve(name)
		if e != nil {
			return nil, e
		}
		for _, site := range sites {
			if strings.Contains(site, "://") {
				u, e := url.Parse(site)
				if e != nil {
					return nil, e
				}
				site = u.Hostname()
			}
			site = Site(site)
			if old, ok := bySite[site]; ok && old != n {
				return nil, fmt.Errorf("site %s assigned to multiple containers", site)
			}
			bySite[site] = n
		}
	}
	out := map[string]protocol.SiteStatus{}
	for site, set := range containers {
		var ids []uint32
		for n := range set {
			ids = append(ids, n)
		}
		slices.Sort(ids)
		status := protocol.SiteStatus{State: protocol.Held, Containers: ids, Reason: "multiple containers; configure browser.containers or browser.container_sites"}
		if n, ok := bySite[site]; ok {
			if set[n] {
				status = protocol.SiteStatus{State: protocol.Shipped, Container: n}
			} else {
				status.Reason = "selected container has no state for this site"
			}
		} else if len(ids) == 1 {
			status = protocol.SiteStatus{State: protocol.Shipped, Container: ids[0]}
		} else {
			order := prefs
			if all {
				order = ids
			}
			for _, n := range order {
				if set[n] {
					status = protocol.SiteStatus{State: protocol.Shipped, Container: n}
					break
				}
			}
		}
		out[site] = status
	}
	return out, nil
}
