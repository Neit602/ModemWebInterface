package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ATPort is a minimal modem AT port client: one command at a time, response read until OK/ERROR.
// It sends nothing on its own when the port opens: every setting stays as the stock software left it.
type ATPort struct {
	path    string
	mu      sync.Mutex
	c       *lineConn
	opening <-chan openResult // an open(2) still in progress; never start a second one
}

var errPortClosed = errors.New("the AT port closed")
var errModemBusy = errors.New("the modem is busy with another action")

func NewATPort(path string) *ATPort { return &ATPort{path: path} }

func (a *ATPort) ensure() error {
	if a.c != nil {
		return nil
	}
	if a.opening == nil {
		a.opening = startOpen(a.path)
	}
	c, err := openWithin(a.opening, 3*time.Second)
	if errors.Is(err, errOpenPending) {
		return fmt.Errorf("%s: %w", a.path, err)
	}
	a.opening = nil
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", a.path, err)
	}
	a.c = c
	return nil
}

func (a *ATPort) reset() {
	if a.c != nil {
		_ = a.c.Close()
	}
	a.c = nil
}

// drain drops what arrived since the last command: unsolicited lines and late replies.
func (a *ATPort) drain() error {
	for {
		_, err := a.c.readLine(time.Now())
		switch {
		case err == nil:
		case errors.Is(err, errTimeout):
			return nil
		default:
			a.reset()
			return errPortClosed
		}
	}
}

// cmd sends one command; call it only while holding a.mu.
func (a *ATPort) cmd(cmd string, timeout time.Duration) ([]string, error) {
	if err := a.drain(); err != nil {
		return nil, err
	}
	if err := a.c.write([]byte(cmd+"\r"), 2*time.Second); err != nil {
		a.reset()
		return nil, fmt.Errorf("%s → %v", cmd, err)
	}
	var out []string
	deadline := time.Now().Add(timeout)
	for {
		line, err := a.c.readLine(deadline)
		if errors.Is(err, errTimeout) {
			return out, fmt.Errorf("%s → no response within %s", cmd, timeout)
		}
		if err != nil {
			a.reset()
			return out, errPortClosed
		}
		switch {
		case line == cmd:
			continue // echo: the modem keeps echo on and we do not change that
		case line == "OK":
			return out, nil
		case line == "ERROR", line == "NO CARRIER",
			strings.HasPrefix(line, "+CME ERROR"), strings.HasPrefix(line, "+CMS ERROR"):
			return out, fmt.Errorf("%s → %s", cmd, line)
		}
		out = append(out, line)
	}
}

func (a *ATPort) with(try bool, fn func() error) error {
	if try {
		if !a.mu.TryLock() {
			return errModemBusy
		}
	} else {
		a.mu.Lock()
	}
	defer a.mu.Unlock()
	if err := a.ensure(); err != nil {
		return err
	}
	return fn()
}

func (a *ATPort) Raw(cmd string, timeout time.Duration) (out []string, err error) {
	e := a.with(false, func() error {
		out, err = a.cmd(cmd, timeout)
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, err
}

// Radio polls network and signal info over AT. It is the fallback and the supplement to modem2_cli:
// on the 8800L AT+CESQ returns ERROR and AT+CSQ says 99,99 even with good signal, so signal values
// come from Qualcomm's $QCSQ (3G) instead. The LTE cells (and the serving EARFCN) come from Cells().
// Only read-only queries are sent.
func (a *ATPort) Radio() (n Network, s Signal, warn []string) {
	n.Reg = "unknown"
	err := a.with(true, func() error {
		if l, err := a.cmd("AT+COPS?", 5*time.Second); err != nil {
			warn = append(warn, err.Error())
		} else if v, ok := findLine(l, "+COPS:"); ok {
			f := splitFields(v)
			if len(f) >= 3 {
				n.Operator = f[2]
			}
			if len(f) >= 4 {
				n.Tech = actToTech(atoi(f[3]))
			}
		}

		stat := -1
		for _, q := range []struct{ cmd, prefix string }{{"AT+CEREG?", "+CEREG:"}, {"AT+CGREG?", "+CGREG:"}, {"AT+CREG?", "+CREG:"}} {
			l, err := a.cmd(q.cmd, 5*time.Second)
			if err != nil {
				continue
			}
			v, ok := findLine(l, q.prefix)
			if !ok {
				continue
			}
			f := splitFields(v)
			if len(f) < 2 {
				continue
			}
			stat = atoi(f[1])
			if tac, ci := regAreaCell(f); tac != "" {
				n.TAC, n.CellID = tac, ci
			}
			if stat == 1 || stat == 5 {
				break
			}
		}
		n.Reg = regToStr(stat)

		if n.Tech == "3G" {
			if l, err := a.cmd("AT$QCSQ", 5*time.Second); err == nil {
				if v, ok := findLine(l, "$QCSQ"); ok {
					s.RSCP, s.EcIo, s.RSSI = parseQCSQ3G(v)
				}
			}
		}
		return nil
	})
	if err != nil {
		warn = append(warn, err.Error())
	}
	return
}

// parseQCSQ3G reads "$QCSQ :-57,-7,0,0,-50" on WCDMA: RSCP, Ec/Io, two unused fields, RSSI.
// On the real modem RSCP = RSSI + Ec/Io held for every sample, which confirms the field order.
func parseQCSQ3G(v string) (rscp, ecio, rssi *float64) {
	f := splitFields(strings.TrimLeft(v, " :"))
	if len(f) < 5 {
		return nil, nil, nil
	}
	num := func(s string) *float64 {
		x, err := strconv.ParseFloat(s, 64)
		if err != nil || x == 0 || x < -150 || x > 0 {
			return nil
		}
		return fp(x)
	}
	return num(f[0]), num(f[1]), num(f[4])
}

// Cells reads the LTE cells the modem measures: $QCRSRP? (PCI, EARFCN, RSRP) and $QCRSRQ? (RSRQ),
// plus the TAC from AT+CEREG? (present once AT+CEREG=2 is set; on this modem that setting is global).
// All are passive queries: they never interrupt the connection. Skipped while an action holds the port.
func (a *ATPort) Cells() (rsrp, rsrq []qcCell, tac string, err error) {
	err = a.with(true, func() error {
		l, err := a.cmd("AT$QCRSRP?", 5*time.Second)
		if err != nil {
			return err
		}
		if v, ok := findLine(l, "$QCRSRP"); ok {
			rsrp = parseQCCells(v, "rsrp")
		}
		if l, err := a.cmd("AT$QCRSRQ?", 5*time.Second); err == nil {
			if v, ok := findLine(l, "$QCRSRQ"); ok {
				rsrq = parseQCCells(v, "rsrq")
			}
		}
		if l, err := a.cmd("AT+CEREG?", 5*time.Second); err == nil {
			if v, ok := findLine(l, "+CEREG:"); ok {
				tac, _ = regAreaCell(splitFields(v))
			}
		}
		return nil
	})
	return
}

// Reregister does what the manual network search in the stock menu does, only faster:
// detach from the network and register again.
func (a *ATPort) Reregister(p func(string)) (string, error) {
	var msg string
	err := a.with(false, func() error {
		p("Detaching from the network…")
		if _, err := a.cmd("AT+COPS=2", 30*time.Second); err != nil {
			return err
		}
		time.Sleep(3 * time.Second)
		p("Registering again, this can take up to a minute…")
		if _, err := a.cmd("AT+COPS=0", 120*time.Second); err != nil {
			return err
		}
		msg = "Re-registration complete"
		return nil
	})
	return msg, err
}

var copsTuple = regexp.MustCompile(`\((\d),"([^"]*)","([^"]*)","(\d+)"(?:,(\d+))?\)`)

// Scan lists the networks the modem can see (AT+COPS=?), then returns to automatic selection.
// The modem leaves the network while it searches, so the internet drops for 1-3 minutes.
func (a *ATPort) Scan(p func(string)) (string, []ScanNetwork, error) {
	var msg string
	var nets []ScanNetwork
	err := a.with(false, func() error {
		p("Searching for available networks, this can take up to 3 minutes…")
		l, err := a.cmd("AT+COPS=?", 200*time.Second)
		if err != nil {
			return err
		}
		nets = parseCopsList(strings.Join(l, " "))
		var found []string
		for _, n := range nets {
			note := ""
			if n.Status == "current" || n.Status == "forbidden" {
				note = ", " + n.Status
			}
			tech := n.Tech
			if tech == "" {
				tech = "?"
			}
			found = append(found, fmt.Sprintf("%s (%s%s)", n.Name, tech, note))
		}
		p("Switching back to automatic network selection…")
		if _, err := a.cmd("AT+COPS=0", 120*time.Second); err != nil {
			return err
		}
		if len(found) == 0 {
			msg = "Search finished, the modem reported no networks"
		} else {
			msg = "Found: " + strings.Join(found, ", ")
		}
		return nil
	})
	return msg, nets, err
}

// parseCopsList reads the AT+COPS=? answer: (stat,"long","short","numeric"[,AcT]) tuples.
func parseCopsList(s string) []ScanNetwork {
	var out []ScanNetwork
	for _, m := range copsTuple.FindAllStringSubmatch(s, -1) {
		n := ScanNetwork{Name: m[2], MCCMNC: m[4], Tech: actToTech(atoi(m[5]))}
		if n.Name == "" {
			n.Name = m[3]
		}
		if n.Name == "" {
			n.Name = m[4]
		}
		switch m[1] {
		case "1":
			n.Status = "available"
		case "2":
			n.Status = "current"
		case "3":
			n.Status = "forbidden"
		default:
			n.Status = "unknown"
		}
		out = append(out, n)
	}
	return out
}

// EnableTAC turns on the extended +CEREG answer (AT+CEREG=2), which carries the LTE TAC and cell ID.
// This is the one setting the panel changes on its own (CLAUDE.md, rule 3 exception, approved 2026-11-02).
// On the 8800L it is global, not per port, and only changes how +CEREG answers look.
func (a *ATPort) EnableTAC() error {
	return a.with(true, func() error {
		_, err := a.cmd("AT+CEREG=2", 5*time.Second)
		return err
	})
}

// regAreaCell takes the area code and cell ID from a +CEREG?/+CGREG? answer in the extended form
// (n=2) and returns both in decimal. The 8800L adds a field: `2,1,"C95","88","88C571F",7` is
// n, stat, TAC, an extra code (probably RAC), cell ID, AcT; the 3GPP form is `2,1,"C95","88C571F",7`.
func regAreaCell(f []string) (area, cell string) {
	if len(f) < 4 {
		return "", ""
	}
	ci := f[3]
	if len(f) >= 6 {
		ci = f[4]
	}
	dec := func(h string) string {
		v, err := strconv.ParseUint(strings.TrimSpace(h), 16, 32)
		if err != nil || h == "" {
			return ""
		}
		return strconv.FormatUint(v, 10)
	}
	return dec(f[2]), dec(ci)
}

func findLine(lines []string, prefix string) (string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], prefix) {
			return strings.TrimSpace(lines[i][len(prefix):]), true
		}
	}
	return "", false
}

// splitFields splits `a,"b,c",d` into fields, respecting quotes.
func splitFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
		case r == ',' && !inQ:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(out, strings.TrimSpace(cur.String()))
}

func atoi(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return v
}

func actToTech(act int) string {
	switch act {
	case 0, 1, 3:
		return "2G"
	case 2, 4, 5, 6:
		return "3G"
	case 7, 9, 10:
		return "LTE"
	case 11, 12, 13:
		return "5G"
	}
	return ""
}

func regToStr(stat int) string {
	switch stat {
	case 0:
		return "none"
	case 1:
		return "home"
	case 2:
		return "searching"
	case 3:
		return "denied"
	case 5:
		return "roaming"
	}
	return "unknown"
}
