package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testdata/raw/dmdb-masked.db is a copy of the router's dmdb.db with MACs, names, WAN addresses and the
// ICCID masked (then VACUUMed, so no old values remain in free pages).
const dmdbFixture = "testdata/raw/dmdb-masked.db"

func TestSQLiteReader(t *testing.T) {
	db, err := readSQLite(dmdbFixture)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, name := range []string{"UsageLogs", "DataSessions", "KnownDevices", "SiteAccess"} {
		if err := db.rows(name, func(sqliteRow) { counts[name]++ }); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	want := map[string]int{"UsageLogs": 2497, "DataSessions": 533, "KnownDevices": 24, "SiteAccess": 0}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s: %d rows, want %d", k, counts[k], v)
		}
	}

	got := map[int64]sqliteRow{}
	_ = db.rows("UsageLogs", func(r sqliteRow) {
		if id := r.Int("ID"); id == 1 || id == 1000 || id == 2496 {
			got[id] = r
		}
	})
	check := []struct {
		id     int64
		mac    string
		sess   int64
		start  string
		tx, rx int64
	}{
		{1, "02:00:00:00:00:01", 2, "2023-01-17 12:06:56", 1393784, 628273},
		{1000, "02:00:00:00:00:02", 203, "2024-09-12 15:26:31", 2689450, 12817540},
		{2496, "02:00:00:00:00:0b", 533, "2026-11-05 13:17:04", 112084, 13195},
	}
	for _, c := range check {
		r, ok := got[c.id]
		if !ok {
			t.Errorf("row %d missing", c.id)
			continue
		}
		if r.Str("Mac") != c.mac || r.Int("IDDataSession") != c.sess || r.Str("StartDate") != c.start ||
			r.Int("BytesTX") != c.tx || r.Int("BytesRX") != c.rx {
			t.Errorf("row %d = %v", c.id, r.vals)
		}
	}

	var last sqliteRow
	_ = db.rows("DataSessions", func(r sqliteRow) { last = r })
	if last.Int("ID") != 533 || last.Int("Bearer") != 2 || last.Str("StartDate") != "2026-11-05 13:16:19" || last.Str("EndDate") != "2026-11-05 15:15:59" {
		t.Errorf("last session = %v", last.vals)
	}
}

func TestSQLiteRefusesBusyOrBroken(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile(dmdbFixture)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x.db")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+"-journal", []byte("hot"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSQLite(p); err != errDBBusy {
		t.Errorf("hot journal: err = %v, want errDBBusy", err)
	}
	if _, err := parseSQLite([]byte("not a database at all, just text that is long enough to have a header......................................")); err == nil {
		t.Error("garbage accepted")
	}
	wal := append([]byte(nil), b[:4096]...)
	wal[18], wal[19] = 2, 2
	if _, err := parseSQLite(wal); err == nil {
		t.Error("WAL database accepted")
	}
}

func TestParseCreateTable(t *testing.T) {
	cols, ipk := parseCreateTable(`CREATE TABLE UsageLogs(
	ID INTEGER PRIMARY KEY ON CONFLICT ROLLBACK AUTOINCREMENT,
	Mac VARCHAR(20) NOT NULL ON CONFLICT ROLLBACK,
	BytesTX     BIGINT,
	FOREIGN KEY(IDDataSession) REFERENCES DataSessions ( ID ))`)
	if ipk != 0 || len(cols) != 3 || cols[1] != "Mac" || cols[2] != "BytesTX" {
		t.Errorf("cols %v ipk %d", cols, ipk)
	}
}
