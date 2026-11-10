// Package gecko reads Firefox-family cookies without accessing a credential store.
package gecko

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"golang.org/x/net/publicsuffix"
)

type Partition struct {
	TopLevelSite         string                     `json:"topLevelSite"`
	HasCrossSiteAncestor bool                       `json:"hasCrossSiteAncestor"`
	Unknown              map[string]json.RawMessage `json:"-"`
}

func (p *Partition) UnmarshalJSON(b []byte) error {
	type plain Partition
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = Partition(v)
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	delete(all, "topLevelSite")
	delete(all, "hasCrossSiteAncestor")
	p.Unknown = all
	return nil
}

type Cookie struct {
	Domain           string                     `json:"domain"`
	Name             string                     `json:"name"`
	Value            string                     `json:"value"`
	Path             string                     `json:"path"`
	HostOnly         bool                       `json:"hostOnly"`
	Secure           bool                       `json:"secure"`
	HTTPOnly         bool                       `json:"httpOnly"`
	Session          bool                       `json:"session"`
	ExpirationDate   float64                    `json:"expirationDate"`
	SameSite         string                     `json:"sameSite"`
	StoreID          string                     `json:"storeId"`
	PartitionKey     *Partition                 `json:"partitionKey,omitempty"`
	FirstPartyDomain string                     `json:"firstPartyDomain,omitempty"`
	Unknown          map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON preserves future isolation fields, rather than silently flattening them.
func (c *Cookie) UnmarshalJSON(b []byte) error {
	type plain Cookie
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Cookie(p)
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	for _, k := range []string{"domain", "name", "value", "path", "hostOnly", "secure", "httpOnly", "session", "expirationDate", "sameSite", "storeId", "partitionKey", "firstPartyDomain"} {
		delete(all, k)
	}
	c.Unknown = all
	return nil
}

type Record struct {
	Cookie    chrome.Cookie `json:"cookie"`
	Container uint32        `json:"container"`
	Partition *Partition    `json:"partition,omitempty"`
	FPD       string        `json:"fpd,omitempty"`
	Site      string        `json:"site"`
}
type Identity struct {
	Name          string `json:"name"`
	CookieStoreID string `json:"cookieStoreId"`
}

func StoreID(n uint32) string {
	if n == 0 {
		return "firefox-default"
	}
	return "firefox-container-" + strconv.FormatUint(uint64(n), 10)
}
func ContainerID(s string) (uint32, error) {
	if s == "firefox-default" {
		return 0, nil
	}
	if !strings.HasPrefix(s, "firefox-container-") {
		return 0, fmt.Errorf("unsupported cookie store %q", s)
	}
	n, e := strconv.ParseUint(strings.TrimPrefix(s, "firefox-container-"), 10, 32)
	if e != nil || n == 0 {
		return 0, fmt.Errorf("invalid container %q", s)
	}
	return uint32(n), nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func Site(host string) string {
	host = strings.ToLower(strings.Trim(strings.TrimPrefix(host, "."), "[]"))
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return host
	}
	s, e := publicsuffix.EffectiveTLDPlusOne(host)
	if e != nil {
		return host
	}
	return s
}
func MapCookie(c Cookie, now time.Time) (Record, error) {
	var r Record
	for k, v := range c.Unknown {
		if string(v) != "null" && string(v) != "\"\"" && string(v) != "0" && string(v) != "false" {
			return r, fmt.Errorf("unknown cookie isolation field %s", k)
		}
	}
	n, e := ContainerID(c.StoreID)
	if e != nil {
		return r, e
	}
	site := Site(c.Domain)
	if c.Domain == "" || c.Path == "" || !strings.HasPrefix(c.Path, "/") {
		return r, fmt.Errorf("invalid cookie host/path")
	}
	if c.FirstPartyDomain != "" && strings.ToLower(c.FirstPartyDomain) != site {
		return r, fmt.Errorf("firstPartyDomain not representable")
	}
	ss, ok := map[string]int{"no_restriction": 0, "lax": 1, "strict": 2, "unspecified": -1}[c.SameSite]
	if !ok {
		return r, fmt.Errorf("unknown sameSite %q", c.SameSite)
	}
	host := c.Domain
	if c.HostOnly {
		host = strings.TrimPrefix(host, ".")
	} else if !strings.HasPrefix(host, ".") {
		host = "." + host
	}
	out := chrome.Cookie{HostKey: host, Name: c.Name, Value: c.Value, Path: c.Path, IsSecure: boolInt(c.Secure), IsHTTPOnly: boolInt(c.HTTPOnly), SameSite: ss, Priority: 1, LastAccessUTC: now.UnixMicro() + 11644473600000000, SourceScheme: 1, SourcePort: 80}
	if c.Secure {
		out.SourceScheme = 2
		out.SourcePort = 443
	}
	if !c.Session {
		v := c.ExpirationDate*1e6 + 11644473600000000
		if math.IsNaN(v) || math.IsInf(v, 0) || v >= float64(math.MaxInt64) || v < 0 {
			return r, fmt.Errorf("cookie expiration overflow")
		}
		out.ExpiresUTC = int64(v)
		out.HasExpires = 1
		out.IsPersistent = 1
	}
	if c.PartitionKey != nil {
		p := c.PartitionKey
		for field, value := range p.Unknown {
			if string(value) != "null" && string(value) != "\"\"" && string(value) != "0" && string(value) != "false" {
				return r, fmt.Errorf("unknown partition isolation field %s", field)
			}
		}
		u, e := url.Parse(p.TopLevelSite)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || !c.Secure {
			return r, fmt.Errorf("partition not representable")
		}
		out.TopFrameSiteKey = p.TopLevelSite
		out.HasCrossSiteAncestor = boolInt(p.HasCrossSiteAncestor)
	}
	if strings.HasPrefix(c.Name, "__Secure-") && !c.Secure {
		return r, fmt.Errorf("invalid __Secure cookie")
	}
	if strings.HasPrefix(c.Name, "__Host-") && (!c.Secure || !c.HostOnly || c.Path != "/") {
		return r, fmt.Errorf("invalid __Host cookie")
	}
	return Record{Cookie: out, Container: n, Partition: c.PartitionKey, FPD: c.FirstPartyDomain, Site: site}, nil
}

type Attributes struct {
	Container        uint32
	Partition        *Partition
	FirstPartyDomain string
}

func ParseAttributes(raw string) (Attributes, error) {
	var a Attributes
	if raw == "" {
		return a, nil
	}
	if !strings.HasPrefix(raw, "^") {
		return a, fmt.Errorf("invalid origin attributes")
	}
	seen := map[string]bool{}
	for pair := range strings.SplitSeq(raw[1:], "&") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 || seen[kv[0]] {
			return a, fmt.Errorf("invalid or duplicate attribute")
		}
		seen[kv[0]] = true
		v, e := url.QueryUnescape(kv[1])
		if e != nil {
			return a, e
		}
		switch kv[0] {
		case "userContextId":
			n, e := strconv.ParseUint(v, 10, 32)
			if e != nil {
				return a, e
			}
			a.Container = uint32(n)
		case "privateBrowsingId":
			if v != "0" {
				return a, fmt.Errorf("private browsing not representable")
			}
		case "firstPartyDomain":
			a.FirstPartyDomain = v
		case "partitionKey":
			if v == "" {
				continue
			}
			if !strings.HasPrefix(v, "(") || !strings.HasSuffix(v, ")") {
				return a, fmt.Errorf("invalid partitionKey delimiters")
			}
			parts := strings.Split(v[1:len(v)-1], ",")
			if len(parts) < 2 || len(parts) > 3 || (parts[0] != "http" && parts[0] != "https") {
				return a, fmt.Errorf("invalid partitionKey")
			}
			a.Partition = &Partition{TopLevelSite: parts[0] + "://" + parts[1]}
			if len(parts) == 3 {
				if parts[2] != "f" && parts[2] != "t" {
					return a, fmt.Errorf("invalid partition ancestor")
				}
				a.Partition.HasCrossSiteAncestor = parts[2] == "t"
			}
		default:
			if v != "" {
				return a, fmt.Errorf("unknown origin attribute %s", kv[0])
			}
		}
	}
	return a, nil
}
