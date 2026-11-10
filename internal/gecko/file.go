package gecko

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/gecko/profilelock"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxSessionSize = 64 << 20

// DecodeMozLZ4 decodes a bounded Mozilla LZ4 block without external dependencies.
func DecodeMozLZ4(b []byte) ([]byte, error) {
	if len(b) < 12 || len(b) > maxSessionSize || string(b[:8]) != "mozLz40\x00" {
		return nil, fmt.Errorf("invalid mozlz4 header/size")
	}
	size := int(binary.LittleEndian.Uint32(b[8:12]))
	if size > maxSessionSize {
		return nil, fmt.Errorf("mozlz4 declared size exceeds cap")
	}
	out := make([]byte, 0, size)
	b = b[12:]
	length := func(n int) (int, error) {
		if n != 15 {
			return n, nil
		}
		for {
			if len(b) == 0 {
				return 0, io.ErrUnexpectedEOF
			}
			v := int(b[0])
			b = b[1:]
			n += v
			if n > size {
				return 0, fmt.Errorf("LZ4 length exceeds cap")
			}
			if v != 255 {
				return n, nil
			}
		}
	}
	for len(b) > 0 {
		token := b[0]
		b = b[1:]
		n, e := length(int(token >> 4))
		if e != nil {
			return nil, e
		}
		if n > len(b) || n > size-len(out) {
			return nil, fmt.Errorf("invalid LZ4 literals")
		}
		out = append(out, b[:n]...)
		b = b[n:]
		if len(b) == 0 {
			break
		}
		if len(b) < 2 {
			return nil, io.ErrUnexpectedEOF
		}
		off := int(binary.LittleEndian.Uint16(b))
		b = b[2:]
		if off == 0 || off > len(out) {
			return nil, fmt.Errorf("invalid LZ4 offset")
		}
		n, e = length(int(token & 15))
		if e != nil {
			return nil, e
		}
		n += 4
		if n > size-len(out) {
			return nil, fmt.Errorf("LZ4 match exceeds size")
		}
		for range n {
			out = append(out, out[len(out)-off])
		}
	}
	if len(out) != size {
		return nil, fmt.Errorf("LZ4 size mismatch")
	}
	return out, nil
}

type FileResult struct {
	Cookies    []Record
	Identities []Identity
	Dropped    map[string]int
	Complete   bool
	Reason     string
}

// ReadFile holds Mozilla's lock through the complete copy and recovery. It never
// opens original databases with SQLite and is never deletion-authoritative.
func ReadFile(ctx context.Context, profile string, now time.Time) (*FileResult, error) {
	lock, e := profilelock.Acquire(profile)
	if e != nil {
		return nil, e
	}
	defer lock.Close()
	temp, e := os.MkdirTemp("", "agentcookie-gecko-*")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(temp)
	for _, name := range []string{"cookies.sqlite", "cookies.sqlite-wal", "cookies.sqlite-shm", "cookies.sqlite-journal"} {
		src := filepath.Join(profile, name)
		st, e := os.Lstat(src)
		if os.IsNotExist(e) && name != "cookies.sqlite" {
			continue
		}
		if e != nil {
			return nil, e
		}
		if !st.Mode().IsRegular() || st.Size() > 256<<20 {
			return nil, fmt.Errorf("unsafe/oversized store %s", name)
		}
		if e = copyPrivate(ctx, src, filepath.Join(temp, name)); e != nil {
			return nil, e
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(temp, "cookies.sqlite")}
	db, e := sql.Open("sqlite3", u.String()+"?mode=rw&_busy_timeout=1000")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var check string
	if e = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); e != nil {
		return nil, e
	}
	if check != "ok" {
		return nil, fmt.Errorf("cookie database integrity check failed")
	}
	var version int
	if e = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); e != nil {
		return nil, e
	}
	if version < 15 {
		return nil, fmt.Errorf("unsupported Gecko cookie schema %d", version)
	}
	rows, e := db.QueryContext(ctx, "PRAGMA table_info(moz_cookies)")
	if e != nil {
		return nil, e
	}
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
			rows.Close()
			return nil, e
		}
		cols[name] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, name := range []string{"host", "name", "value", "path", "expiry", "isSecure", "isHttpOnly", "sameSite", "originAttributes"} {
		if !cols[name] {
			return nil, fmt.Errorf("missing cookie column %s", name)
		}
	}
	scheme := "0"
	if cols["schemeMap"] {
		scheme = "schemeMap"
	}
	partitionFlag, browserElement := "0", "0"
	if cols["isPartitionedAttributeSet"] {
		partitionFlag = "isPartitionedAttributeSet"
	}
	if cols["inBrowserElement"] {
		browserElement = "inBrowserElement"
	}
	rows, e = db.QueryContext(ctx, "SELECT host,name,value,path,expiry,isSecure,isHttpOnly,sameSite,originAttributes,"+scheme+","+partitionFlag+","+browserElement+" FROM moz_cookies ORDER BY host,name,path,originAttributes")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := &FileResult{Dropped: map[string]int{}, Reason: "file mode: session snapshot may lag; cookies incomplete; storage unsupported"}
	for rows.Next() {
		var c Cookie
		var expiry int64
		var secure, httpOnly, sameSite, schemeMap, partitioned, inBrowserElement int
		var raw string
		if e = rows.Scan(&c.Domain, &c.Name, &c.Value, &c.Path, &expiry, &secure, &httpOnly, &sameSite, &raw, &schemeMap, &partitioned, &inBrowserElement); e != nil {
			return nil, e
		}
		a, e := ParseAttributes(raw)
		if e != nil {
			result.Dropped[e.Error()]++
			continue
		}
		if inBrowserElement != 0 || partitioned < 0 || partitioned > 1 || (partitioned == 1 && a.Partition == nil) || schemeMap < 0 || schemeMap > 3 {
			result.Dropped["inconsistent cookie isolation flags"]++
			continue
		}
		c.HostOnly = !strings.HasPrefix(c.Domain, ".")
		c.Secure = secure == 1
		c.HTTPOnly = httpOnly == 1
		c.SameSite = map[int]string{0: "no_restriction", 1: "lax", 2: "strict", 256: "unspecified"}[sameSite]
		c.StoreID = StoreID(a.Container)
		c.PartitionKey = a.Partition
		c.FirstPartyDomain = a.FirstPartyDomain
		c.ExpirationDate = float64(expiry)
		if version >= 16 {
			c.ExpirationDate /= 1000
		}
		if (secure != 0 && secure != 1) || (httpOnly != 0 && httpOnly != 1) {
			result.Dropped["invalid cookie boolean"]++
			continue
		}
		r, e := MapCookie(c, now)
		if e != nil {
			result.Dropped[e.Error()]++
			continue
		}
		switch schemeMap {
		case 1:
			r.Cookie.SourceScheme = 1
			r.Cookie.SourcePort = 80
		case 2:
			r.Cookie.SourceScheme = 2
			r.Cookie.SourcePort = 443
		default:
			r.Cookie.SourceScheme = 0
			r.Cookie.SourcePort = -1
		}
		result.Cookies = append(result.Cookies, r)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	b, e := readBounded(filepath.Join(profile, "sessionstore.jsonlz4"), maxSessionSize)
	if e == nil {
		b, e = DecodeMozLZ4(b)
		if e != nil {
			return nil, e
		}
		session, e := sessionCookies(b, now, result.Dropped)
		if e != nil {
			return nil, e
		}
		result.Cookies = append(result.Cookies, session...)
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	b, e = readBounded(filepath.Join(profile, "containers.json"), 1<<20)
	if e == nil {
		var doc struct {
			Identities []struct {
				Name          string `json:"name"`
				UserContextID uint32 `json:"userContextId"`
				Public        bool   `json:"public"`
			} `json:"identities"`
		}
		if e = json.Unmarshal(b, &doc); e != nil {
			return nil, e
		}
		for _, v := range doc.Identities {
			if v.Public {
				result.Identities = append(result.Identities, Identity{Name: v.Name, CookieStoreID: StoreID(v.UserContextID)})
			}
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	sort.Slice(result.Cookies, func(i, j int) bool {
		a, b := result.Cookies[i], result.Cookies[j]
		return fmt.Sprint(a.Container, a.Cookie.HostKey, a.Cookie.Name, a.Cookie.Path, a.Cookie.TopFrameSiteKey) < fmt.Sprint(b.Container, b.Cookie.HostKey, b.Cookie.Name, b.Cookie.Path, b.Cookie.TopFrameSiteKey)
	})
	return result, nil
}
func copyPrivate(ctx context.Context, src, dst string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	ce := out.Close()
	if e == nil {
		e = ce
	}
	return e
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds cap", filepath.Base(path))
	}
	return b, nil
}
func sessionCookies(b []byte, now time.Time, dropped map[string]int) ([]Record, error) {
	type sc struct {
		Host             string          `json:"host"`
		Name             string          `json:"name"`
		Value            string          `json:"value"`
		Path             string          `json:"path"`
		Secure           bool            `json:"secure"`
		HTTPOnly         bool            `json:"httponly"`
		SameSite         int             `json:"sameSite"`
		OriginAttributes json.RawMessage `json:"originAttributes"`
	}
	var doc struct {
		Cookies []sc `json:"cookies"`
		Windows []struct {
			Cookies []sc `json:"cookies"`
		} `json:"windows"`
	}
	if e := json.Unmarshal(b, &doc); e != nil {
		return nil, e
	}
	all := doc.Cookies
	for _, w := range doc.Windows {
		all = append(all, w.Cookies...)
	}
	var out []Record
	for _, v := range all {
		raw := ""
		if len(v.OriginAttributes) > 0 {
			if e := json.Unmarshal(v.OriginAttributes, &raw); e != nil {
				var fields map[string]json.RawMessage
				if e = json.Unmarshal(v.OriginAttributes, &fields); e != nil {
					return nil, e
				}
				var keys []string
				for k := range fields {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					var text string
					if json.Unmarshal(fields[k], &text) != nil {
						text = string(fields[k])
					}
					if raw != "" {
						raw += "&"
					}
					raw += k + "=" + url.QueryEscape(text)
				}
				if raw != "" {
					raw = "^" + raw
				}
			}
		}
		a, e := ParseAttributes(raw)
		if e != nil {
			dropped[e.Error()]++
			continue
		}
		c := Cookie{Domain: v.Host, Name: v.Name, Value: v.Value, Path: v.Path, HostOnly: !strings.HasPrefix(v.Host, "."), Secure: v.Secure, HTTPOnly: v.HTTPOnly, Session: true, SameSite: map[int]string{0: "no_restriction", 1: "lax", 2: "strict", 256: "unspecified"}[v.SameSite], StoreID: StoreID(a.Container), PartitionKey: a.Partition, FirstPartyDomain: a.FirstPartyDomain}
		r, e := MapCookie(c, now)
		if e != nil {
			dropped[e.Error()]++
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
