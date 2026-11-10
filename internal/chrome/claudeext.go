package chrome

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// ClaudeExtensionID is the Chrome Web Store id of Claude in Chrome.
const ClaudeExtensionID = "fcoeoabgfenejglbffodgkkbkcdhcgfn"

const claudeExtTempPrefix = "agentcookie-claudeext-"

var profileDirRE = regexp.MustCompile(`^(Default|Profile [0-9]+)$`)

// IsProfileDirName reports whether name is a Chrome profile folder name
// (Default or "Profile N").
func IsProfileDirName(name string) bool { return profileDirRE.MatchString(name) }

// ClaudeProfile is a Chrome profile whose Claude in Chrome extension is
// signed into a Claude account.
type ClaudeProfile struct {
	Profile     string
	AccountUUID string
}

// QualifyingClaudeProfiles returns every profile under root whose Claude in
// Chrome extension is signed in, but only when those profiles span two or
// more different Claude accounts. Otherwise it returns nil: one account, or
// one profile, has nothing to keep alive. Stores that cannot be read are
// skipped and their errors returned for logging.
func QualifyingClaudeProfiles(root string) ([]ClaudeProfile, []error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, []error{err}
	}
	var (
		found []ClaudeProfile
		errs  []error
	)
	accounts := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || !IsProfileDirName(e.Name()) {
			continue
		}
		store := filepath.Join(root, e.Name(), "Local Extension Settings", ClaudeExtensionID)
		if st, err := os.Stat(store); err != nil || !st.IsDir() {
			continue
		}
		account, err := cachedClaudeAccount(store)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if account == "" {
			continue
		}
		found = append(found, ClaudeProfile{Profile: e.Name(), AccountUUID: account})
		accounts[account] = true
	}
	if len(accounts) < 2 {
		return nil, errs
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Profile < found[j].Profile })
	return found, errs
}

// claudeAccountCache remembers each store's account by a signature of its
// files (name, size, mtime), so an unchanged store is not copied again.
// LevelDB appends to its .log in place, so the signature changes on any
// write, including a sign-in or sign-out.
var (
	claudeAccountMu    sync.Mutex
	claudeAccountCache = map[string]claudeAccountEntry{}
)

type claudeAccountEntry struct {
	sig     string
	account string
}

func cachedClaudeAccount(store string) (string, error) {
	sig, err := storeSignature(store)
	if err != nil {
		return "", err
	}
	claudeAccountMu.Lock()
	e, ok := claudeAccountCache[store]
	claudeAccountMu.Unlock()
	if ok && e.sig == sig {
		return e.account, nil
	}
	account, err := readClaudeAccount(store)
	if err != nil {
		return "", err
	}
	claudeAccountMu.Lock()
	claudeAccountCache[store] = claudeAccountEntry{sig: sig, account: account}
	claudeAccountMu.Unlock()
	return account, nil
}

func storeSignature(store string) (string, error) {
	entries, err := os.ReadDir(store)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries { // ReadDir sorts by name
		if skipLevelDBFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s:%d:%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String(), nil
}

// skipLevelDBFile names files a read-only open never needs: the lock Chrome
// holds and LevelDB's text logs.
func skipLevelDBFile(name string) bool {
	return name == "LOCK" || name == "LOG" || name == "LOG.old"
}

// readClaudeAccount copies the extension's LevelDB into a private temp dir
// (Chrome holds the live store's lock) and reads accountUuid from the copy.
// The copy may hold login data, so it is removed before returning.
func readClaudeAccount(store string) (string, error) {
	tmp, err := os.MkdirTemp("", claudeExtTempPrefix)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	if err := copyLevelDB(store, tmp); err != nil {
		return "", err
	}
	db, err := leveldb.OpenFile(tmp, &opt.Options{ReadOnly: true, ErrorIfMissing: true})
	if err != nil {
		return "", err
	}
	defer db.Close()

	raw, err := db.Get([]byte("accountUuid"), nil)
	if err == leveldb.ErrNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var account string
	if err := json.Unmarshal(raw, &account); err != nil {
		return "", fmt.Errorf("accountUuid is not a JSON string: %w", err)
	}
	return account, nil
}

func copyLevelDB(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || skipLevelDBFile(e.Name()) {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CleanupClaudeExtTemp removes storage copies left behind by a run that was
// killed mid-read.
func CleanupClaudeExtTemp() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), claudeExtTempPrefix+"*"))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}
