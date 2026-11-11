package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// A minimal read-only SQLite reader for the stock device database (dmdb.db).
//
// Go's standard library has no SQLite, and running the sqlite3 tool would lock the file while the stock
// dmdbd writes to it. This reader takes no locks at all: it reads the whole file in one go and parses the
// table b-trees itself. If the stock daemon was writing at that moment (hot journal present, or the file
// changed while it was read), the read is refused and tried again later. Only rollback-journal databases
// are supported (dmdb.db uses journal_mode=delete); WAL databases are refused.

type sqliteDB struct {
	data     []byte
	pageSize int
	usable   int
}

var errDBBusy = errors.New("the database is being written right now")

// readSQLite loads a consistent snapshot of a database file.
func readSQLite(path string) (*sqliteDB, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if fi, err := os.Stat(path + "-journal"); err == nil && fi.Size() > 0 {
			return nil, errDBBusy
		}
		before, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !after.ModTime().Equal(before.ModTime()) || after.Size() != int64(len(data)) {
			continue
		}
		return parseSQLite(data)
	}
	return nil, errDBBusy
}

func parseSQLite(data []byte) (*sqliteDB, error) {
	if len(data) < 100 || !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return nil, errors.New("not an SQLite database")
	}
	if data[18] == 2 || data[19] == 2 {
		return nil, errors.New("WAL databases are not supported")
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 || len(data)%ps != 0 {
		return nil, errors.New("bad page size")
	}
	if enc := binary.BigEndian.Uint32(data[56:60]); enc > 1 {
		return nil, errors.New("only UTF-8 databases are supported")
	}
	return &sqliteDB{data: data, pageSize: ps, usable: ps - int(data[20])}, nil
}

func (db *sqliteDB) page(n uint32) ([]byte, error) {
	if n == 0 || int(n) > len(db.data)/db.pageSize {
		return nil, fmt.Errorf("page %d out of range", n)
	}
	off := int(n-1) * db.pageSize
	return db.data[off : off+db.pageSize], nil
}

func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 9 && i < len(b); i++ {
		if i == 8 {
			return v<<8 | uint64(b[i]), 9
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return v, 0
}

// walkTable calls fn for every row of the table b-tree rooted at root, in rowid order.
func (db *sqliteDB) walkTable(root uint32, fn func(rowid int64, payload []byte) error) error {
	return db.walk(root, 0, fn)
}

func (db *sqliteDB) walk(pn uint32, depth int, fn func(int64, []byte) error) error {
	if depth > 20 {
		return errors.New("b-tree too deep (corrupt?)")
	}
	p, err := db.page(pn)
	if err != nil {
		return err
	}
	h := 0
	if pn == 1 {
		h = 100
	}
	if len(p) < h+8 {
		return errors.New("short page")
	}
	typ := p[h]
	n := int(binary.BigEndian.Uint16(p[h+3 : h+5]))
	hdr := 8
	if typ == 0x05 {
		hdr = 12
	}
	ptrs := h + hdr
	if ptrs+2*n > len(p) {
		return errors.New("bad cell count")
	}
	for i := 0; i < n; i++ {
		off := int(binary.BigEndian.Uint16(p[ptrs+2*i:]))
		if off >= len(p) {
			return errors.New("bad cell pointer")
		}
		switch typ {
		case 0x05: // interior table page: left child, rowid
			if off+4 > len(p) {
				return errors.New("bad interior cell")
			}
			if err := db.walk(binary.BigEndian.Uint32(p[off:]), depth+1, fn); err != nil {
				return err
			}
		case 0x0d: // leaf table page: payload size, rowid, payload
			size, k := readVarint(p[off:])
			if k == 0 {
				return errors.New("bad payload size")
			}
			rowid, k2 := readVarint(p[off+k:])
			if k2 == 0 {
				return errors.New("bad rowid")
			}
			payload, err := db.payload(p, off+k+k2, int(size))
			if err != nil {
				return err
			}
			if err := fn(int64(rowid), payload); err != nil {
				return err
			}
		default:
			return fmt.Errorf("page %d: not a table page (type %d)", pn, typ)
		}
	}
	if typ == 0x05 {
		return db.walk(binary.BigEndian.Uint32(p[h+8:]), depth+1, fn)
	}
	return nil
}

// payload returns a cell's payload, following overflow pages when it does not fit on the page.
func (db *sqliteDB) payload(p []byte, off, size int) ([]byte, error) {
	u := db.usable
	x := u - 35
	if size <= x {
		if off+size > len(p) {
			return nil, errors.New("payload past the page end")
		}
		return p[off : off+size], nil
	}
	m := (u-12)*32/255 - 23
	local := m + (size-m)%(u-4)
	if local > x {
		local = m
	}
	if off+local+4 > len(p) {
		return nil, errors.New("payload past the page end")
	}
	out := make([]byte, 0, size)
	out = append(out, p[off:off+local]...)
	next := binary.BigEndian.Uint32(p[off+local:])
	for len(out) < size {
		if next == 0 {
			return nil, errors.New("overflow chain too short")
		}
		op, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = binary.BigEndian.Uint32(op)
		take := min(size-len(out), u-4)
		out = append(out, op[4:4+take]...)
	}
	return out, nil
}

// decodeRecord turns a record into values: nil, int64, float64, string or []byte.
func decodeRecord(b []byte) ([]any, error) {
	hl, k := readVarint(b)
	if k == 0 || int(hl) > len(b) {
		return nil, errors.New("bad record header")
	}
	var types []uint64
	for pos := k; pos < int(hl); {
		t, n := readVarint(b[pos:])
		if n == 0 {
			return nil, errors.New("bad serial type")
		}
		types = append(types, t)
		pos += n
	}
	vals := make([]any, len(types))
	pos := int(hl)
	for i, t := range types {
		var n int
		switch {
		case t == 0, t == 8, t == 9:
			n = 0
		case t <= 4:
			n = int(t)
		case t == 5:
			n = 6
		case t == 6, t == 7:
			n = 8
		case t >= 12:
			n = int(t-12) / 2
		default:
			return nil, fmt.Errorf("unknown serial type %d", t)
		}
		if pos+n > len(b) {
			return nil, errors.New("record past its end")
		}
		v := b[pos : pos+n]
		switch {
		case t == 0:
			vals[i] = nil
		case t == 8:
			vals[i] = int64(0)
		case t == 9:
			vals[i] = int64(1)
		case t <= 6:
			x := int64(int8(v[0])) // sign-extend from the first byte
			for _, c := range v[1:] {
				x = x<<8 | int64(c)
			}
			vals[i] = x
		case t == 7:
			vals[i] = math.Float64frombits(binary.BigEndian.Uint64(v))
		case t%2 == 0:
			vals[i] = append([]byte(nil), v...)
		default:
			vals[i] = string(v)
		}
		pos += n
	}
	return vals, nil
}

// sqliteTable is one table: its root page and column names (from the CREATE TABLE statement).
type sqliteTable struct {
	root  uint32
	cols  []string
	ipk   int // index of the INTEGER PRIMARY KEY column (stored as the rowid), or -1
	index map[string]int
}

func (db *sqliteDB) table(name string) (*sqliteTable, error) {
	var found *sqliteTable
	err := db.walkTable(1, func(_ int64, payload []byte) error {
		v, err := decodeRecord(payload)
		if err != nil || len(v) < 5 {
			return err
		}
		typ, _ := v[0].(string)
		nm, _ := v[1].(string)
		if typ != "table" || !strings.EqualFold(nm, name) {
			return nil
		}
		root, _ := v[3].(int64)
		sql, _ := v[4].(string)
		cols, ipk := parseCreateTable(sql)
		t := &sqliteTable{root: uint32(root), cols: cols, ipk: ipk, index: map[string]int{}}
		for i, c := range cols {
			t.index[strings.ToLower(c)] = i
		}
		found = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("no table %s", name)
	}
	return found, nil
}

// parseCreateTable finds the column names, and which one is the INTEGER PRIMARY KEY, in a CREATE TABLE statement.
func parseCreateTable(sql string) (cols []string, ipk int) {
	ipk = -1
	open := strings.IndexByte(sql, '(')
	end := strings.LastIndexByte(sql, ')')
	if open < 0 || end <= open {
		return nil, -1
	}
	body := sql[open+1 : end]
	var parts []string
	depth, start := 0, 0
	for i, c := range body {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	for _, p := range parts {
		f := strings.Fields(p)
		if len(f) == 0 {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN":
			continue
		}
		name := strings.Trim(f[0], "`\"[]")
		up := strings.ToUpper(p)
		if len(f) > 1 && strings.EqualFold(f[1], "INTEGER") && strings.Contains(up, "PRIMARY KEY") {
			ipk = len(cols)
		}
		cols = append(cols, name)
	}
	return cols, ipk
}

// sqliteRow gives access to one row's values by column name.
type sqliteRow struct {
	t    *sqliteTable
	vals []any
}

func (r sqliteRow) get(col string) any {
	i, ok := r.t.index[strings.ToLower(col)]
	if !ok || i >= len(r.vals) {
		return nil
	}
	return r.vals[i]
}

func (r sqliteRow) Int(col string) int64 {
	switch v := r.get(col).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		return int64(atoi(v))
	}
	return 0
}

func (r sqliteRow) Str(col string) string {
	switch v := r.get(col).(type) {
	case string:
		return v
	case int64:
		return fmt.Sprint(v)
	}
	return ""
}

// rows calls fn for every row of a table.
func (db *sqliteDB) rows(name string, fn func(sqliteRow)) error {
	t, err := db.table(name)
	if err != nil {
		return err
	}
	return db.walkTable(t.root, func(rowid int64, payload []byte) error {
		v, err := decodeRecord(payload)
		if err != nil {
			return err
		}
		if t.ipk >= 0 && t.ipk < len(v) && v[t.ipk] == nil {
			v[t.ipk] = rowid
		}
		fn(sqliteRow{t: t, vals: v})
		return nil
	})
}
