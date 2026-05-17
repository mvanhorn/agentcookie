package chrome

import (
	"database/sql"
	"fmt"
)

// SqliteRowCount returns the row count of a table at dbPath. Used by the
// sink to verify writes immediately after commit, separately from any
// later state changes Chrome may apply on relaunch.
func SqliteRowCount(dbPath, table string) (int, error) {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(fmt.Sprintf("select count(*) from %s", table)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}
