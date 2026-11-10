// Package catalog inventories storage origins by directory names only. No live
// SQLite, LevelDB, metadata or other database content is opened.
package catalog

import (
	"encoding/json"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Entry struct {
	Origin             string    `json:"origin"`
	Container          uint32    `json:"container"`
	Attributes         string    `json:"attributes,omitempty"`
	FirstSeen          time.Time `json:"first_seen"`
	LastSeen           time.Time `json:"last_seen"`
	LastRead           time.Time `json:"last_read"`
	LastReadGeneration uint64    `json:"last_read_generation"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Dirty              bool      `json:"dirty"`
	Present            bool      `json:"present"`
	Representable      bool      `json:"representable"`
}

func Key(origin string, n uint32, attrs string) string {
	return origin + "|" + strconv.FormatUint(uint64(n), 10) + "|" + attrs
}
func (e *Entry) Key() string { return Key(e.Origin, e.Container, e.Attributes) }

type Catalog struct {
	Entries map[string]*Entry `json:"entries"`
	Listed  bool              `json:"listed"`
}

func New() *Catalog { return &Catalog{Entries: map[string]*Entry{}} }
func ParseDirectory(name string) (origin string, container uint32, attrs, reason string, err error) {
	base := name
	if i := strings.IndexByte(name, '^'); i >= 0 {
		base = name[:i]
		attrs = name[i:]
	}
	parts := strings.SplitN(base, "+++", 2)
	if len(parts) != 2 || (parts[0] != "http" && parts[0] != "https") {
		err = fmt.Errorf("unsupported origin directory %q", name)
		return
	}
	authority := parts[1]
	if strings.HasPrefix(authority, "[") {
		i := strings.IndexByte(authority, ']')
		if i < 0 {
			err = fmt.Errorf("malformed IPv6 origin")
			return
		}
		host := strings.ReplaceAll(authority[1:i], "+", ":")
		if net.ParseIP(host) == nil {
			err = fmt.Errorf("invalid IPv6 origin")
			return
		}
		tail := authority[i+1:]
		authority = "[" + host + "]"
		if tail != "" {
			if tail[0] != '+' {
				err = fmt.Errorf("invalid origin port")
				return
			}
			authority += ":" + tail[1:]
		}
	} else {
		authority = strings.ReplaceAll(authority, "+", ":")
	}
	u, e := url.Parse(parts[0] + "://" + authority)
	if e != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		err = fmt.Errorf("invalid origin directory")
		return
	}
	if u.Port() != "" {
		p, e := strconv.ParseUint(u.Port(), 10, 16)
		if e != nil || p == 0 {
			err = fmt.Errorf("invalid origin port")
			return
		}
	}
	origin = u.String()
	a, e := gecko.ParseAttributes(attrs)
	container = a.Container
	// Preserve routing identity even if an earlier future attribute made the
	// entry unreadable. Unknown isolation must not make it appear default-scoped.
	if values, parseErr := url.ParseQuery(strings.TrimPrefix(attrs, "^")); parseErr == nil {
		if ids := values["userContextId"]; len(ids) == 1 {
			if n, e := strconv.ParseUint(ids[0], 10, 32); e == nil {
				container = uint32(n)
			}
		}
	}

	if e != nil {
		reason = e.Error()
	} else if a.Partition != nil {
		reason = "partitioned storage not representable"
	} else if a.FirstPartyDomain != "" && a.FirstPartyDomain != gecko.Site(u.Hostname()) {
		reason = "firstPartyDomain storage not representable"
	}
	return
}
func (c *Catalog) Discover(profile string, now time.Time) error {
	entries, e := os.ReadDir(filepath.Join(profile, "storage", "default"))
	if os.IsNotExist(e) {
		entries = nil
		e = nil
	}
	if e != nil {
		c.Listed = false
		return e
	}
	c.Listed = true
	for _, x := range c.Entries {
		x.Present = false
	}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		origin, n, attrs, reason, e := ParseDirectory(d.Name())
		if e != nil {
			origin = "unreadable:" + d.Name()
			reason = e.Error()
		}
		k := Key(origin, n, attrs)
		x := c.Entries[k]
		if x == nil {
			x = &Entry{Origin: origin, Container: n, Attributes: attrs, FirstSeen: now, Status: "active", Dirty: true}
			c.Entries[k] = x
		}
		x.Present = true
		x.LastSeen = now
		x.Representable = reason == ""
		if reason != "" {
			x.Status = "unreadable"
			x.Reason = reason
		} else if x.Status == "removed" {
			x.Status = "active"
			x.Dirty = true
		}
	}
	return nil
}

// Observe only dirties existing entries; cookie/navigation hints are not an inventory.
func (c *Catalog) Observe(host string, n uint32) {
	site := gecko.Site(host)
	for _, e := range c.Entries {
		u, err := url.Parse(e.Origin)
		if err == nil && e.Container == n && gecko.Site(u.Hostname()) == site && e.Status != "removed" {
			e.Dirty = true
		}
	}
}
func (c *Catalog) Due(now time.Time) []*Entry {
	var a []*Entry
	for _, e := range c.Entries {
		if e.Status != "removed" && e.Representable && (e.Dirty || e.LastRead.IsZero() || now.Sub(e.LastRead) >= 10*time.Minute || !e.Present) {
			a = append(a, e)
		}
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].LastRead.Equal(a[j].LastRead) {
			return a[i].Key() < a[j].Key()
		}
		return a[i].LastRead.Before(a[j].LastRead)
	})
	return a
}
func (c *Catalog) ReadResult(key string, generation uint64, empty bool, err error, now time.Time) {
	e := c.Entries[key]
	if e == nil {
		return
	}
	if err != nil {
		e.Status = "unreadable"
		e.Reason = err.Error()
		e.Dirty = true
		return
	}
	e.LastRead = now
	e.LastReadGeneration = generation
	e.Dirty = false
	e.Reason = ""
	e.Status = "active"
	if empty && !e.Present {
		e.Status = "removed"
	}
}
func (c *Catalog) Unread() int {
	n := 0
	for _, e := range c.Entries {
		if e.Status != "removed" && (e.LastRead.IsZero() || e.Status == "unreadable" || e.Dirty) {
			n++
		}
	}
	return n
}
func Load(path string) (*Catalog, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return New(), nil
	}
	if e != nil {
		return nil, e
	}
	var c Catalog
	if e = json.Unmarshal(b, &c); e != nil {
		return nil, e
	}
	if c.Entries == nil {
		c.Entries = map[string]*Entry{}
	}
	c.Listed = false
	return &c, nil
}
func (c *Catalog) Save(path string) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".catalog-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
