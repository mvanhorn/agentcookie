package chrome

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// boundSessionStoreFile is the per-profile SQLite file where Chrome keeps its
// Device Bound Session Credentials (DBSC) sessions, keyed by schemeful site
// ("https://claude.ai"). A site listed here has a login Chrome keeps alive by
// signing refresh challenges with a key bound to this profile; losing that
// session signs the profile out of the site.
const boundSessionStoreFile = "Device Bound Sessions"

// BoundSessionSites returns the sites (host names, sorted, no scheme) that
// have device-bound sessions in the Chrome profile at profileDir.
//
// A profile with no store has none: it returns an empty list and no error. A
// store that exists but cannot be read returns an error, so a caller guarding
// against sign-outs can treat the status as unknown rather than as "none".
func BoundSessionSites(profileDir string) ([]string, error) {
	path := filepath.Join(profileDir, boundSessionStoreFile)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat device-bound session store: %w", err)
	}
	// Chrome keeps this store in rollback-journal mode, so committed rows are
	// in the main file; immutable=1 reads it while Chrome holds it open.
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&immutable=1", path))
	if err != nil {
		return nil, fmt.Errorf("open device-bound session store: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT key FROM dbsc_session_tbl`)
	if err != nil {
		return nil, fmt.Errorf("read device-bound session store: %w", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("read device-bound session store: %w", err)
		}
		if site := boundSessionSite(key); site != "" {
			seen[site] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read device-bound session store: %w", err)
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

func boundSessionSite(key string) string {
	_, rest, found := strings.Cut(key, "://")
	if !found {
		rest = key
	}
	host, _, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSpace(host))
}
