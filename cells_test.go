package main

import (
	"strings"
	"testing"
	"time"
)

// Real $QCRSRP?/$QCRSRQ? answers from the 8800L (2026-11-02), the part after the prefix.
const (
	qcRSRPLTE      = ` 080,1700,"-069.50"`                    // LTE B3, serving cell only
	qcRSRQLTE      = ` 080,1700,"-09.50"`                     //
	qcRSRPLTENeigh = ` 080,1700,"-068.70",237,2900,"-127.10"` // serving + a weak B7 neighbour
	qcRSRP3G       = ` 237,2900,"-012.70",165,2900,"-012.50"` // on 3G: values printed ten times too small
	qcRSRQ3G       = ` 237,2900,"-01.70"`                     //
	qcEmpty        = ` `                                      // "$QCRSRP: " with no cells
	copsScanReal   = `+COPS: (1,"VODAFONE","VODAFONE","25501",7),(2,"Vodafone UA","Vodafone UA","25501",2),(3,"life:)","life:)","25506",7),(3,"UA-KYIVSTAR","UA-KYIVSTAR","25503",7),,(0,1,2,3,4),(0,1,2)`
)

func TestLTEChannel(t *testing.T) {
	cases := []struct {
		earfcn int
		band   string
		mhz    float64
	}{
		{1700, "B3", 1855.0}, {2900, "B7", 2635.0}, {300, "B1", 2140.0}, {3600, "B8", 940.0},
		{6300, "B20", 806.0}, {38000, "B38", 2595.0}, {39000, "B40", 2335.0}, {1200, "B3", 1805.0}, {1949, "B3", 1879.9},
	}
	for _, c := range cases {
		b, _, mhz, ok := lteChannel(c.earfcn)
		if !ok || b != c.band || mhz != c.mhz {
			t.Errorf("EARFCN %d: got %s %.1f MHz, want %s %.1f", c.earfcn, b, mhz, c.band, c.mhz)
		}
	}
	if _, _, _, ok := lteChannel(4000); ok {
		t.Error("EARFCN 4000 is in no band")
	}
}

func TestParseQCCells(t *testing.T) {
	got := parseQCCells(qcRSRPLTENeigh, "rsrp")
	if len(got) != 2 || got[0].pci != 80 || got[0].earfcn != 1700 || *got[0].val != -68.7 || got[0].estimated ||
		got[1].pci != 237 || *got[1].val != -127.1 {
		t.Fatalf("LTE: %+v", got)
	}
	g3 := parseQCCells(qcRSRP3G, "rsrp")
	if len(g3) != 2 || *g3[0].val != -127 || !g3[0].estimated || *g3[1].val != -125 {
		t.Fatalf("3G format must be scaled back: %+v %v %v", g3, *g3[0].val, *g3[1].val)
	}
	q3 := parseQCCells(qcRSRQ3G, "rsrq")
	if len(q3) != 1 || *q3[0].val != -17 || !q3[0].estimated {
		t.Fatalf("3G RSRQ: %+v", q3)
	}
	if q := parseQCCells(qcRSRQLTE, "rsrq"); len(q) != 1 || *q[0].val != -9.5 || q[0].estimated {
		t.Fatalf("LTE RSRQ: %+v", q)
	}
	if e := parseQCCells(qcEmpty, "rsrp"); len(e) != 0 {
		t.Fatalf("empty: %+v", e)
	}
}

func TestCellTracker(t *testing.T) {
	tr := NewCellTracker()
	t0 := time.Unix(1000, 0)
	sv := tr.Update("LTE", parseQCCells(qcRSRPLTENeigh, "rsrp"), parseQCCells(qcRSRQLTE, "rsrq"), t0)
	if sv == nil || sv.PCI != 80 || sv.Band != "B3" || *sv.FreqMHz != 1855 || *sv.RSRQ != -9.5 {
		t.Fatalf("serving %+v", sv)
	}
	tr.Update("LTE", parseQCCells(qcRSRPLTE, "rsrp"), nil, t0.Add(30*time.Second))
	s := tr.Snapshot(t0.Add(30 * time.Second))
	if len(s) != 2 || !s[0].Serving || s[1].PCI != 237 || s[1].SeenAgoS != 30 || s[1].Band != "B7" {
		t.Fatalf("after 30 s the neighbour must still be listed: %+v", s)
	}
	tr.Update("LTE", parseQCCells(qcRSRPLTE, "rsrp"), nil, t0.Add(61*time.Second))
	if s := tr.Snapshot(t0.Add(61 * time.Second)); len(s) != 1 {
		t.Fatalf("a cell unseen for over a minute must be dropped: %+v", s)
	}
	// On 3G no cell is the serving one.
	if sv := tr.Update("3G", parseQCCells(qcRSRP3G, "rsrp"), nil, t0.Add(70*time.Second)); sv != nil {
		t.Fatalf("no serving LTE cell on 3G, got %+v", sv)
	}
}

func TestServingCell(t *testing.T) {
	lte := servingCell(Network{Tech: "LTE", CellID: "143415071", TAC: "0", EARFCN: ip(1700), MCC: "255", MNC: "01"})
	if lte.ENB == nil || *lte.ENB != 560215 || *lte.Sector != 31 || lte.AreaLabel != "TAC" || lte.AreaCode != "" || lte.BandName != "1800 MHz" {
		t.Fatalf("LTE %+v", lte)
	}
	g3 := servingCell(Network{Tech: "3G", CellID: "86690833", TAC: "3221"})
	if g3.RNC == nil || *g3.RNC != 1322 || *g3.CID != 52241 || g3.AreaLabel != "LAC" || g3.AreaCode != "3221" || g3.ENB != nil {
		t.Fatalf("3G %+v", g3)
	}
	if mcc, mnc := splitMCCMNC("25501"); mcc != "255" || mnc != "01" {
		t.Fatal(mcc, mnc)
	}
	if mcc, mnc := splitMCCMNC("310410"); mcc != "310" || mnc != "410" {
		t.Fatal(mcc, mnc)
	}
	if mcc, _ := splitMCCMNC(""); mcc != "" {
		t.Fatal(mcc)
	}
}

func TestEventsDiff(t *testing.T) {
	var got []string
	add := func(kind, level, text string) { got = append(got, kind+"/"+level+": "+text) }
	old := connState{tech: "3G", reg: "home", cell: "86690833", data: "connected", ip: "10.62.20.52"}
	cur := connState{tech: "LTE", band: "B3", reg: "home", cell: "143415071", cellNote: " (PCI 80, B3)", data: "connected", ip: "10.58.8.156"}
	diffEvents(old, cur, add)
	want := []string{
		"network/neutral: Network type changed: 3G → LTE B3",
		"cell/neutral: Serving cell changed: 86690833 → 143415071 (PCI 80, B3)",
		"ip/neutral: IP address changed: 10.62.20.52 → 10.58.8.156",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	got = nil
	diffEvents(cur, connState{tech: "LTE", band: "B3", reg: "searching", cell: "143415071", data: "connecting"}, add)
	if len(got) != 2 || !strings.HasPrefix(got[0], "registration/warning") || !strings.HasPrefix(got[1], "data/warning") {
		t.Fatalf("got %q", got)
	}
}

func TestEventLog(t *testing.T) {
	var l EventLog
	t0 := time.Now()
	for i := 0; i < eventCap+20; i++ {
		l.Add("ip", "neutral", "e", t0.Add(time.Duration(i)*time.Second))
	}
	s := l.Snapshot(t0.Add(time.Hour))
	if len(s) != eventCap {
		t.Fatalf("kept %d events, want %d", len(s), eventCap)
	}
	if s[0].TS <= s[1].TS {
		t.Fatal("newest first")
	}
}

// Reconstructed from a real scan on 2026-11-02 (the panel showed "VODAFONE (LTE), Vodafone UA (3G, current),
// life:) (LTE, forbidden), UA-KYIVSTAR (LTE, forbidden)"); the raw line itself was not saved.
func TestParseCopsList(t *testing.T) {
	n := parseCopsList(copsScanReal)
	if len(n) != 4 || n[0].Name != "VODAFONE" || n[0].Tech != "LTE" || n[0].Status != "available" ||
		n[1].Status != "current" || n[1].Tech != "3G" || n[2].Status != "forbidden" || n[3].MCCMNC != "25503" {
		t.Fatalf("%+v", n)
	}
}

// Real +CEREG? answers from the 8800L before and after AT+CEREG=2 (2026-11-02, LTE B3).
func TestRegAreaCell(t *testing.T) {
	tac, ci := regAreaCell(splitFields(` 2,1,"C95","88","88C571F",7`))
	if tac != "3221" || ci != "143415071" {
		t.Fatalf("8800L form: tac %q ci %q", tac, ci)
	}
	tac, ci = regAreaCell(splitFields(` 2,1,"1F4A","0A3F2C11",7`))
	if tac != "8010" || ci != "171912209" {
		t.Fatalf("3GPP form: tac %q ci %q", tac, ci)
	}
	if tac, _ := regAreaCell(splitFields(` 0,1`)); tac != "" {
		t.Fatalf("n=0 has no TAC, got %q", tac)
	}
}
