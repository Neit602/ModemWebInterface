package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lteBands is the downlink part of the E-UTRA channel table (3GPP TS 36.101, table 5.7.3-1):
// F_DL = lowMHz + 0.1 * (EARFCN - first). Name is the everyday frequency name of the band.
var lteBands = []struct {
	band        int
	first, last int
	lowMHz      float64
	name        string
}{
	{1, 0, 599, 2110, "2100 MHz"}, {2, 600, 1199, 1930, "1900 MHz"}, {3, 1200, 1949, 1805, "1800 MHz"},
	{4, 1950, 2399, 2110, "AWS 1700/2100 MHz"}, {5, 2400, 2649, 869, "850 MHz"}, {7, 2750, 3449, 2620, "2600 MHz"},
	{8, 3450, 3799, 925, "900 MHz"}, {12, 5010, 5179, 729, "700 MHz"}, {13, 5180, 5279, 746, "700 MHz"},
	{14, 5280, 5379, 758, "700 MHz"}, {17, 5730, 5849, 734, "700 MHz"}, {20, 6150, 6449, 791, "800 MHz"},
	{25, 8040, 8689, 1930, "1900 MHz"}, {26, 8690, 9039, 859, "850 MHz"}, {28, 9210, 9659, 758, "700 MHz"},
	{38, 37750, 38249, 2570, "2600 MHz TDD"}, {40, 38650, 39649, 2300, "2300 MHz TDD"},
	{41, 39650, 41589, 2496, "2500 MHz TDD"}, {46, 46790, 54539, 5150, "5 GHz LAA"},
	{48, 55240, 56739, 3550, "3500 MHz CBRS"}, {66, 66436, 67335, 2110, "AWS 1700/2100 MHz"},
	{71, 68586, 68935, 617, "600 MHz"},
}

// lteChannel returns the band ("B3"), its everyday name ("1800 MHz") and the downlink frequency in MHz.
func lteChannel(earfcn int) (band, name string, mhz float64, ok bool) {
	for _, b := range lteBands {
		if earfcn >= b.first && earfcn <= b.last {
			mhz = math.Round((b.lowMHz+0.1*float64(earfcn-b.first))*10) / 10
			return "B" + strconv.Itoa(b.band), b.name, mhz, true
		}
	}
	return "", "", 0, false
}

func lteBand(earfcn int) string {
	b, _, _, _ := lteChannel(earfcn)
	return b
}

// qcCell is one entry of $QCRSRP? / $QCRSRQ?: PCI, EARFCN and the measured value.
type qcCell struct {
	pci, earfcn int
	val         *float64
	estimated   bool
}

// parseQCCells reads ` 080,1700,"-069.50",361,1700,"-078.00"` (the part after "$QCRSRP:").
// kind is "rsrp" or "rsrq". On 3G this firmware prints the values ten times too small
// ("-012.70" for a cell that reads "-127.10" on LTE); such impossible values are scaled back
// and marked as estimated. An empty list is valid: no LTE cells measured right now.
func parseQCCells(v, kind string) []qcCell {
	f := splitFields(strings.TrimLeft(v, " :"))
	var out []qcCell
	for i := 0; i+2 < len(f); i += 3 {
		pci, earfcn := atoi(f[i]), atoi(f[i+1])
		if pci < 0 || earfcn < 0 {
			continue
		}
		c := qcCell{pci: pci, earfcn: earfcn}
		if x, err := strconv.ParseFloat(f[i+2], 64); err == nil {
			c.val, c.estimated = plausibleLTE(x, kind)
		}
		out = append(out, c)
	}
	return out
}

// plausibleLTE keeps RSRP within -140..-44 dBm and RSRQ within -30..-3 dB. A value that is too high
// to be real but becomes plausible times ten is the 3G print format and is returned scaled.
func plausibleLTE(x float64, kind string) (*float64, bool) {
	lo, hi := -140.0, -44.0
	if kind == "rsrq" {
		lo, hi = -30.0, -3.0
	}
	switch {
	case x >= lo && x <= hi:
		return fp(x), false
	case x > hi && x < 0 && x*10 >= lo && x*10 <= hi:
		return fp(math.Round(x*100) / 10), true
	}
	return nil, false
}

// Cell is one LTE cell the modem measures, for the Cells view.
type Cell struct {
	EARFCN    int      `json:"earfcn"`
	PCI       int      `json:"pci"`
	Band      string   `json:"band"`
	BandName  string   `json:"band_name"`
	FreqMHz   *float64 `json:"freq_mhz"`
	RSRP      *float64 `json:"rsrp"`
	RSRQ      *float64 `json:"rsrq"`
	Estimated bool     `json:"estimated"` // values were printed in the 3G format and scaled back
	Serving   bool     `json:"serving"`
	SeenAgoS  int      `json:"seen_ago_s"` // seconds since the modem last reported it
	seen      time.Time
}

const cellKeep = 60 * time.Second

// CellTracker remembers recently seen cells so a neighbour that drops out of one report
// does not flicker in the list; a cell is dropped a minute after it was last reported.
type CellTracker struct {
	mu    sync.Mutex
	cells map[string]*Cell
}

func NewCellTracker() *CellTracker { return &CellTracker{cells: map[string]*Cell{}} }

// Update merges one $QCRSRP?/$QCRSRQ? pair. On LTE the first $QCRSRP? entry is the serving cell
// (verified: its RSRP matches modem2_cli get_state); on 3G every listed cell is a neighbour.
// It returns the serving cell, or nil.
func (t *CellTracker) Update(tech string, rsrp, rsrq []qcCell, now time.Time) *Cell {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.cells {
		c.Serving = false
	}
	key := func(earfcn, pci int) string { return fmt.Sprintf("%d/%d", earfcn, pci) }
	get := func(q qcCell) *Cell {
		k := key(q.earfcn, q.pci)
		c := t.cells[k]
		if c == nil {
			c = &Cell{EARFCN: q.earfcn, PCI: q.pci}
			if b, name, mhz, ok := lteChannel(q.earfcn); ok {
				c.Band, c.BandName, c.FreqMHz = b, name, fp(mhz)
			}
			t.cells[k] = c
		}
		c.seen = now
		return c
	}
	var serving *Cell
	for i, q := range rsrp {
		c := get(q)
		c.RSRP, c.Estimated = q.val, q.estimated
		if i == 0 && tech == "LTE" {
			c.Serving = true
			serving = c
		}
	}
	for _, q := range rsrq {
		c := get(q)
		c.RSRQ = q.val
		c.Estimated = c.Estimated || q.estimated
	}
	for k, c := range t.cells {
		if now.Sub(c.seen) > cellKeep {
			delete(t.cells, k)
		}
	}
	if serving == nil {
		return nil
	}
	s := *serving
	return &s
}

// Snapshot lists the cells: serving first, then the strongest.
func (t *CellTracker) Snapshot(now time.Time) []Cell {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Cell, 0, len(t.cells))
	for _, c := range t.cells {
		if now.Sub(c.seen) > cellKeep {
			continue
		}
		x := *c
		x.SeenAgoS = int(now.Sub(c.seen).Seconds())
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Serving != b.Serving {
			return a.Serving
		}
		ra, rb := -999.0, -999.0
		if a.RSRP != nil {
			ra = *a.RSRP
		}
		if b.RSRP != nil {
			rb = *b.RSRP
		}
		if ra != rb {
			return ra > rb
		}
		return a.EARFCN*1000+a.PCI < b.EARFCN*1000+b.PCI
	})
	return out
}

// ServingCell is what the Cells view shows about the cell the modem uses.
type ServingCell struct {
	Tech      string   `json:"tech"`
	TechName  string   `json:"tech_name"`
	Operator  string   `json:"operator"`
	MCC       string   `json:"mcc"`
	MNC       string   `json:"mnc"`
	Band      string   `json:"band"`
	BandName  string   `json:"band_name"`
	FreqMHz   *float64 `json:"freq_mhz"`
	EARFCN    *int     `json:"earfcn"`
	PCI       *int     `json:"pci"`
	CellID    string   `json:"cell_id"`
	ENB       *int     `json:"enb"`    // LTE: eNodeB ID = cell ID >> 8
	Sector    *int     `json:"sector"` // LTE: cell ID & 0xFF
	RNC       *int     `json:"rnc"`    // 3G: RNC ID = cell ID >> 16
	CID       *int     `json:"cid"`    // 3G: cell ID & 0xFFFF
	AreaCode  string   `json:"area_code"`
	AreaLabel string   `json:"area_label"` // TAC or LAC
}

func servingCell(n Network) ServingCell {
	s := ServingCell{Tech: n.Tech, TechName: n.TechName, Operator: n.Operator, MCC: n.MCC, MNC: n.MNC,
		Band: n.Band, FreqMHz: n.FreqMHz, EARFCN: n.EARFCN, PCI: n.PCI, CellID: n.CellID}
	if n.EARFCN != nil {
		_, s.BandName, _, _ = lteChannel(*n.EARFCN)
	}
	s.AreaLabel = "LAC"
	if n.Tech == "LTE" {
		s.AreaLabel = "TAC"
	}
	if n.TAC != "" && n.TAC != "0" {
		s.AreaCode = n.TAC
	}
	// modem2_cli prints the cell ID in decimal; AT+CEREG would give it in hex.
	id, err := strconv.ParseInt(n.CellID, 10, 64)
	if err != nil || id <= 0 {
		return s
	}
	switch n.Tech {
	case "LTE": // 28-bit E-UTRAN cell identity: 20-bit eNB ID + 8-bit cell (sector)
		s.ENB, s.Sector = ip(int(id>>8)), ip(int(id&0xFF))
	case "3G": // 28-bit UTRAN cell ID: 12-bit RNC ID + 16-bit cell ID
		s.RNC, s.CID = ip(int(id>>16)), ip(int(id&0xFFFF))
	}
	return s
}

// splitMCCMNC turns "25501" into "255", "01". Three-digit MNCs exist (6 digits in total).
func splitMCCMNC(id string) (mcc, mnc string) {
	if len(id) < 5 || len(id) > 6 {
		return "", ""
	}
	if _, err := strconv.Atoi(id); err != nil {
		return "", ""
	}
	return id[:3], id[3:]
}

// Event is one entry of the connection log.
type Event struct {
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`  // start, network, band, cell, registration, data, ip
	Level string `json:"level"` // good, warning, critical, neutral
	Text  string `json:"text"`
	at    time.Time
}

const eventCap = 300

// EventLog keeps the last few hundred connection events in memory. Times are kept with Go's
// monotonic clock, so they stay right when the router's wall clock jumps from 1970 to the real time.
type EventLog struct {
	mu   sync.Mutex
	list []Event
}

func (l *EventLog) Add(kind, level, text string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, Event{Kind: kind, Level: level, Text: text, at: at})
	if len(l.list) > eventCap {
		l.list = l.list[len(l.list)-eventCap:]
	}
}

// Snapshot returns the events newest first, with timestamps on the current wall clock.
func (l *EventLog) Snapshot(now time.Time) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.list))
	for i, e := range l.list {
		e.TS = now.UnixMilli() - now.Sub(e.at).Milliseconds()
		out[len(l.list)-1-i] = e
	}
	return out
}

// connState is what the event log compares between two modem readings.
type connState struct {
	tech, band, cell, cellNote, reg, data, ip string
}

var regText = map[string]string{"home": "home network", "roaming": "roaming", "searching": "searching for a network",
	"denied": "registration denied", "none": "not registered", "unknown": "unknown"}

func regLevel(reg string) string {
	switch reg {
	case "home", "roaming":
		return "good"
	case "searching", "unknown":
		return "warning"
	}
	return "critical"
}

func dataLevel(state string) string {
	switch state {
	case "connected":
		return "good"
	case "connecting":
		return "warning"
	}
	return "critical"
}

func techText(tech, band string) string {
	switch {
	case tech == "":
		return "no service"
	case band != "":
		return tech + " " + band
	}
	return tech
}

// diffEvents describes what changed between two readings, in plain words.
func diffEvents(old, cur connState, add func(kind, level, text string)) {
	if old.tech != cur.tech {
		add("network", "neutral", fmt.Sprintf("Network type changed: %s → %s", techText(old.tech, old.band), techText(cur.tech, cur.band)))
	} else if old.band != "" && cur.band != "" && old.band != cur.band {
		add("band", "neutral", fmt.Sprintf("Band changed: %s → %s", old.band, cur.band))
	}
	if old.cell != "" && cur.cell != "" && old.cell != cur.cell {
		add("cell", "neutral", fmt.Sprintf("Serving cell changed: %s → %s%s", old.cell, cur.cell, cur.cellNote))
	}
	if old.reg != cur.reg {
		add("registration", regLevel(cur.reg), fmt.Sprintf("Registration: %s → %s", regText[old.reg], regText[cur.reg]))
	}
	if old.data != cur.data {
		t := "Data session: " + cur.data
		if cur.data == "connected" && cur.ip != "" {
			t += ", IP address " + cur.ip
		}
		add("data", dataLevel(cur.data), t)
	} else if old.ip != "" && cur.ip != "" && old.ip != cur.ip {
		add("ip", "neutral", fmt.Sprintf("IP address changed: %s → %s", old.ip, cur.ip))
	}
}

func startText(s connState) string {
	t := "Monitoring started: " + techText(s.tech, s.band)
	if s.cell != "" {
		t += ", cell " + s.cell
	}
	if s.data != "" {
		t += ", data session " + s.data
	}
	return t
}

// ScanNetwork is one row of the network scan table.
type ScanNetwork struct {
	Name   string `json:"name"`
	MCCMNC string `json:"mccmnc"`
	Tech   string `json:"tech"`
	Status string `json:"status"` // current, available, forbidden, unknown
}

type ScanResult struct {
	TS       int64         `json:"ts"`
	Networks []ScanNetwork `json:"networks"`
	at       time.Time
}

// CellsView is the /api/cells answer.
type CellsView struct {
	Serving ServingCell `json:"serving"`
	Cells   []Cell      `json:"cells"`
	Scan    *ScanResult `json:"scan"`
	Source  string      `json:"source"` // why the list may be empty: "", "no-at", "demo"
}
