//go:build linux

package gecko

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/gecko/profilelock"
	"golang.org/x/sys/unix"
)

// This fixture uses real SQLite cache spills/checkpoints with deterministic
// process barriers instead of replacing SQLite's VFS. While paused, the writer
// retains Mozilla's actual fcntl lock and has either partially updated main DB
// pages protected by a hot rollback journal, or reset/reused a WAL generation.
// IN_OPEN instrumentation proves ReadFile never opens any database or sidecar.
func TestLockedSQLiteWriterRefusedBeforeDatabaseOpen(t *testing.T) {
	for _, mode := range []string{"rollback-spill", "wal-reset"} {
		t.Run(mode, func(t *testing.T) {
			p := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockedSQLiteWriterHelper$")
			cmd.Env = append(os.Environ(), "AGENTCOOKIE_LOCKED_WRITER_PROFILE="+p, "AGENTCOOKIE_LOCKED_WRITER_MODE="+mode)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				input.Close()
				if err := cmd.Wait(); err != nil {
					t.Errorf("writer fixture: %v: %s", err, stderr.String())
				}
			}()
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil {
				t.Fatalf("writer readiness: %v: %s", err, stderr.String())
			}
			var phase struct {
				Mode                                                      string
				DirtyMain, HotJournal, WALTruncated, WALGenerationChanged bool
			}
			if err = json.Unmarshal([]byte(line), &phase); err != nil {
				t.Fatal(line, err)
			}
			if phase.Mode != mode || (mode == "rollback-spill" && (!phase.DirtyMain || !phase.HotJournal)) ||
				(mode == "wal-reset" && (!phase.WALTruncated || !phase.WALGenerationChanged)) {
				t.Fatalf("writer did not reach the required unsafe-copy phase: %+v", phase)
			}
			fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
			if err != nil {
				t.Fatal("database-open instrumentation unavailable", err)
			}
			defer unix.Close(fd)
			if _, err = unix.InotifyAddWatch(fd, p, unix.IN_OPEN); err != nil {
				t.Fatal(err)
			}
			for range 10 {
				if _, err = ReadFile(context.Background(), p, time.Now()); !errors.Is(err, profilelock.ErrBrowserRunning) {
					t.Fatalf("locked %s reader must refuse before acquisition: %v", mode, err)
				}
			}
			opened := readOpenEvents(t, fd)
			if !opened[".parentlock"] {
				t.Fatal("instrumentation missed profile-lock open")
			}
			for name := range opened {
				if name != ".parentlock" {
					t.Fatalf("reader opened locked profile file: %s", name)
				}
			}
			// Positive control: prove this watch detects database opens, not just locks.
			file, err := os.Open(filepath.Join(p, "cookies.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
			if !readOpenEvents(t, fd)["cookies.sqlite"] {
				t.Fatal("instrumentation failed database-open positive control")
			}
		})
	}
}

func readOpenEvents(t *testing.T, fd int) map[string]bool {
	t.Helper()
	result := map[string]bool{}
	buffer := make([]byte, 64*1024)
	for {
		n, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EAGAIN) {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return result
		}
		for offset := 0; offset < n; {
			if n-offset < unix.SizeofInotifyEvent {
				t.Fatal("truncated inotify event")
			}
			mask := binary.NativeEndian.Uint32(buffer[offset+4 : offset+8])
			length := int(binary.NativeEndian.Uint32(buffer[offset+12 : offset+16]))
			end := offset + unix.SizeofInotifyEvent + length
			if end > n || mask&unix.IN_Q_OVERFLOW != 0 {
				t.Fatal("invalid/overflowed open trace")
			}
			if mask&unix.IN_OPEN != 0 {
				name := strings.TrimRight(string(buffer[offset+unix.SizeofInotifyEvent:end]), "\x00")
				result[name] = true
			}
			offset = end
		}
	}
}

func TestLockedSQLiteWriterHelper(t *testing.T) {
	p := os.Getenv("AGENTCOOKIE_LOCKED_WRITER_PROFILE")
	if p == "" {
		return
	}
	mode := os.Getenv("AGENTCOOKIE_LOCKED_WRITER_MODE")
	lock, err := profilelock.Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	path := filepath.Join(p, "cookies.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`PRAGMA page_size=1024; PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; PRAGMA cache_size=1; PRAGMA cache_spill=ON;
  CREATE TABLE a(value TEXT); CREATE TABLE b(value TEXT);
  INSERT INTO a VALUES('old'); INSERT INTO b VALUES('old')`)
	phase := struct {
		Mode                                                      string
		DirtyMain, HotJournal, WALTruncated, WALGenerationChanged bool
	}{Mode: mode}
	var tx *sql.Tx
	switch mode {
	case "rollback-spill":
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		tx, err = db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		// Updating only a and forcing a page spill freezes the same mixed-page
		// interval as a forwarding VFS pause before the transaction's b update.
		if _, err = tx.Exec("UPDATE a SET value=?", strings.Repeat("new", 8000)); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		phase.DirtyMain = !bytes.Equal(before, after)
		journal, err := os.ReadFile(path + "-journal")
		if err != nil {
			t.Fatal(err)
		}
		phase.HotJournal = len(journal) >= 28 && !bytes.Equal(journal[:8], make([]byte, 8))
	case "wal-reset":
		exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE a SET value='generation-one'")
		oldWAL, err := os.ReadFile(path + "-wal")
		if err != nil || len(oldWAL) < 32 {
			t.Fatal("missing original WAL generation", err)
		}
		var busy, log, checkpointed int
		if err = db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil || busy != 0 {
			t.Fatal("WAL reset failed", err)
		}
		stat, err := os.Stat(path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		phase.WALTruncated = stat.Size() == 0
		exec("UPDATE a SET value='generation-two'; UPDATE b SET value='generation-two'")
		newWAL, err := os.ReadFile(path + "-wal")
		if err != nil || len(newWAL) < 32 {
			t.Fatal("missing reset WAL generation", err)
		}
		phase.WALGenerationChanged = !bytes.Equal(oldWAL[16:24], newWAL[16:24])
		tx, err = db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("UPDATE a SET value=?", strings.Repeat("uncommitted", 3000)); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown fixture mode")
	}
	defer tx.Rollback()
	ready, err := json.Marshal(phase)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(ready))
	// Parent closes stdin only after the no-open assertions and positive control.
	var release [1]byte
	if _, err = os.Stdin.Read(release[:]); err != nil && err != io.EOF {
		t.Fatal(err)
	}
}
