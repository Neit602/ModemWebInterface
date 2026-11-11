package main

import (
	"os"
	"strings"
	"time"
)

// The stock device database (dmdbd) keeps every data session and, per session, every device's usage.
// It is read with the lock-free reader in sqlite.go, at most once a minute and only when the file changed.
const dmdbPath = "/opt/nvtl/data/dmdb/dmdb.db"

// deviceUsage is the data one device used, from UsageLogs. In dmdb's naming BytesTX is what the router
// sent to the device (its downloads) and BytesRX what it received from the device (its uploads).
type deviceUsage struct {
	SessDown  int64 `json:"sess_down"` // during the current data session
	SessUp    int64 `json:"sess_up"`
	TotalDown int64 `json:"total_down"` // since the database started
	TotalUp   int64 `json:"total_up"`
}

type usageSnapshot struct {
	byMAC   map[string]deviceUsage
	session int64 // the latest data session id
	mod     time.Time
}

func readUsage(path string) (usageSnapshot, error) {
	snap := usageSnapshot{byMAC: map[string]deviceUsage{}}
	if fi, err := os.Stat(path); err == nil {
		snap.mod = fi.ModTime()
	}
	db, err := readSQLite(path)
	if err != nil {
		return snap, err
	}
	type row struct {
		mac      string
		sess     int64
		down, up int64
	}
	var rows []row
	err = db.rows("UsageLogs", func(r sqliteRow) {
		x := row{mac: strings.ToLower(r.Str("Mac")), sess: r.Int("IDDataSession"), down: r.Int("BytesTX"), up: r.Int("BytesRX")}
		if x.sess > snap.session {
			snap.session = x.sess
		}
		rows = append(rows, x)
	})
	if err != nil {
		return snap, err
	}
	for _, x := range rows {
		u := snap.byMAC[x.mac]
		u.TotalDown += max(0, x.down)
		u.TotalUp += max(0, x.up)
		if x.sess == snap.session {
			u.SessDown += max(0, x.down)
			u.SessUp += max(0, x.up)
		}
		snap.byMAC[x.mac] = u
	}
	return snap, nil
}
