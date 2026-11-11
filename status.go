package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type Network struct {
	Operator string   `json:"operator"`
	Tech     string   `json:"tech"`      // LTE, 3G, 2G or empty
	TechName string   `json:"tech_name"` // what the modem calls it: HSPA+, LTE...
	Band     string   `json:"band"`
	EARFCN   *int     `json:"earfcn"`
	PCI      *int     `json:"pci"`
	FreqMHz  *float64 `json:"freq_mhz"` // downlink, from the EARFCN
	CellID   string   `json:"cell_id"`
	TAC      string   `json:"tac"` // TAC on LTE, LAC on 3G
	MCC      string   `json:"mcc"`
	MNC      string   `json:"mnc"`
	Reg      string   `json:"reg"` // home, roaming, searching, denied, none, unknown
}

type Signal struct {
	RSRP *float64 `json:"rsrp"`
	RSRQ *float64 `json:"rsrq"`
	SINR *float64 `json:"sinr"`
	RSSI *float64 `json:"rssi"`
	RSCP *float64 `json:"rscp"`
	EcIo *float64 `json:"ecio"`
}

type WAN struct {
	Details  *WANDetails `json:"details"` // APN, gateway, mask, DNS; nil = not read yet
	TZMin    *int        `json:"tz_min"`  // network time zone, minutes east of UTC
	Iface    string      `json:"iface"`
	IP       string      `json:"ip"`
	Up       bool        `json:"up"`
	State    string      `json:"state"`     // connected, connecting, disconnected; empty when unknown
	SessionS int64       `json:"session_s"` // how long the data session has been up
	RxBytes  uint64      `json:"rx_bytes"`
	TxBytes  uint64      `json:"tx_bytes"`
	RxRate   float64     `json:"rx_rate"` // bytes per second
	TxRate   float64     `json:"tx_rate"`
}

type Battery struct {
	Present     bool     `json:"present"`
	Percent     *int     `json:"percent"`
	Status      string   `json:"status"` // charging, discharging, full, unknown
	ExtPower    *bool    `json:"ext_power"`
	TempC       *float64 `json:"temp_c"`
	VoltageV    *float64 `json:"voltage_v"`
	CurrentMA   *float64 `json:"current_ma"` // positive = charging, negative = draining
	PowerW      *float64 `json:"power_w"`    // same sign as the current
	Phase       string   `json:"phase"`      // fast charging, topping up, full, on battery...
	Health      string   `json:"health,omitempty"`
	TimeLeftS   *int64   `json:"time_left_s"` // on battery only
	TimeLeftEst bool     `json:"time_left_est,omitempty"`
	PowerOn     string   `json:"power_on"` // why the router last started
}

type System struct {
	UptimeS    float64 `json:"uptime_s"`
	Load1      float64 `json:"load1"`
	MemUsedPct float64 `json:"mem_used_pct"`
}

type Client struct {
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	// Filled on the 8800L: how the device is connected and, on Wi-Fi, its signal and link.
	Via        string       `json:"via,omitempty"`     // "wifi" or "usb"
	Network    string       `json:"network,omitempty"` // Primary or Guest
	Band       string       `json:"band,omitempty"`    // 2.4 GHz or 5 GHz
	Signal     *int         `json:"signal,omitempty"`  // dBm, as the router hears the device
	TxRate     *float64     `json:"tx_rate,omitempty"` // Mbps, router to device
	RxRate     *float64     `json:"rx_rate,omitempty"` // Mbps, device to router
	ConnectedS int64        `json:"connected_s,omitempty"`
	Usage      *deviceUsage `json:"usage,omitempty"`
}

type Action struct {
	Name     string `json:"name"`
	State    string `json:"state"` // running, done, error
	Message  string `json:"message"`
	Started  int64  `json:"started"`
	Finished int64  `json:"finished"`
}

type Status struct {
	TS       int64    `json:"ts"`
	Mode     string   `json:"mode"` // live, demo
	Network  Network  `json:"network"`
	Signal   Signal   `json:"signal"`
	WAN      WAN      `json:"wan"`
	Battery  Battery  `json:"battery"`
	System   System   `json:"system"`
	Clients  []Client `json:"clients"`
	Action   *Action  `json:"action"`
	Warnings []string `json:"warnings"`

	SMSUnread *int      `json:"sms_unread"` // nil = not known
	Lifetime  *Lifetime `json:"lifetime"`   // traffic since the router was first used
	SoCTempC  *float64  `json:"soc_temp_c"` // hottest chip sensor
}

const historyCap = 450 // 15 minutes at a 2 s interval

type History struct {
	T    []int64    `json:"t"`
	RSRP []*float64 `json:"rsrp"`
	RSRQ []*float64 `json:"rsrq"`
	SINR []*float64 `json:"sinr"`
	RSCP []*float64 `json:"rscp"`
	EcIo []*float64 `json:"ecio"`
	Rx   []float64  `json:"rx"`
	Tx   []float64  `json:"tx"`
}

// add appends a sample. The router boots with its clock at 1970 and jumps to the real time once the
// network provides it; when the clock jumps, the stored timestamps are shifted so the chart stays continuous.
func (h *History) add(s *Status, interval time.Duration) {
	step := interval.Milliseconds()
	if n := len(h.T); n > 0 {
		gap := s.TS - h.T[n-1]
		if gap < 0 || (gap > 60_000 && gap > 30*step) {
			shift := s.TS - step - h.T[n-1]
			for i := range h.T {
				h.T[i] += shift
			}
		}
	}
	h.T = append(h.T, s.TS)
	h.RSRP = append(h.RSRP, s.Signal.RSRP)
	h.RSRQ = append(h.RSRQ, s.Signal.RSRQ)
	h.SINR = append(h.SINR, s.Signal.SINR)
	h.RSCP = append(h.RSCP, s.Signal.RSCP)
	h.EcIo = append(h.EcIo, s.Signal.EcIo)
	h.Rx = append(h.Rx, s.WAN.RxRate)
	h.Tx = append(h.Tx, s.WAN.TxRate)
	if n := len(h.T); n > historyCap {
		d := n - historyCap
		h.T, h.RSRP, h.RSRQ, h.SINR, h.RSCP, h.EcIo = h.T[d:], h.RSRP[d:], h.RSRQ[d:], h.SINR[d:], h.RSCP[d:], h.EcIo[d:]
		h.Rx, h.Tx = h.Rx[d:], h.Tx[d:]
	}
}

type netPrev struct {
	iface  string
	rx, tx uint64
	at     time.Time
}

// radioSnap is the latest modem reading; the radio loop writes it, sample() reads it.
type radioSnap struct {
	net     Network
	sig     Signal
	wan     *wanInfo
	clients []Client // from dmdb; nil = not read
	warn    []string
}

type Collector struct {
	cfg    Config
	at     *ATPort
	vendor *Vendor
	demo   *Demo

	mu    sync.RWMutex
	cur   Status
	hist  History
	prev  netPrev
	radio radioSnap
	scan  *ScanResult

	cells  *CellTracker
	events *EventLog
	conn   connState // last known state, for the event log
	connOK bool
	tacOn  bool // AT+CEREG=2 sent

	actMu  sync.Mutex
	action *Action

	x *extras // slower values (0.7.0)
}

var errBusy = errors.New("wait, the previous action is still running")

func NewCollector(cfg Config) *Collector {
	c := &Collector{cfg: cfg, cells: NewCellTracker(), events: &EventLog{}, x: newExtras()}
	if cfg.Demo {
		c.demo = NewDemo()
		c.conn, c.connOK = c.demo.SeedEvents(c.events, time.Now())
		demoExtras(c.x)
	} else {
		c.vendor = NewVendor(cfg.VendorDir)
		if cfg.ATPath != "" {
			c.at = NewATPort(cfg.ATPath)
		}
	}
	return c
}

func (c *Collector) Run() {
	if c.demo == nil {
		c.pollRadio(0)
		go c.radioLoop()
		go c.extrasLoop()
	}
	c.sample()
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for range t.C {
		c.sample()
	}
}

func (c *Collector) radioLoop() {
	t := time.NewTicker(c.cfg.Radio)
	defer t.Stop()
	for round := 1; ; round++ {
		<-t.C
		c.pollRadio(round)
	}
}

// pollRadio reads the modem. modem2_cli (the stock modem service) is the main source; the AT port
// adds the LTE cells (serving EARFCN, PCI, neighbours) every round and takes over the rest when the
// vendor tools are missing. Slower-changing things are read every third round.
func (c *Collector) pollRadio(round int) {
	now := time.Now()
	var r radioSnap
	r.net.Reg = "unknown"
	vendorOK, atOK := false, false
	if c.vendor != nil {
		n, s, err := c.vendor.Radio()
		if err != nil {
			r.warn = append(r.warn, "Could not read the signal from the stock modem service: "+err.Error())
		} else {
			r.net, r.sig, vendorOK = n, s, true
		}
	}
	if c.at != nil && !vendorOK {
		n, s, warn := c.at.Radio()
		r.net, r.sig = n, s
		atOK = n.Reg != "unknown"
		for _, w := range warn {
			// "Busy" is expected while an action runs; its progress is shown separately.
			if w != errModemBusy.Error() {
				r.warn = append(r.warn, w)
			}
		}
	}
	if c.vendor == nil && c.at == nil {
		r.warn = append(r.warn, "Signal and network info are not available: the stock modem tools were not found and the modem AT port is not set (-at)")
	}

	if c.at != nil && !c.tacOn {
		c.tacOn = c.at.EnableTAC() == nil // retried next round if the port was busy
	}
	cellsOK := false
	if c.at != nil {
		if rsrp, rsrq, tac, err := c.at.Cells(); err == nil {
			c.applyCells(&r.net, rsrp, rsrq, now)
			if r.net.Tech == "LTE" && tac != "" && (r.net.TAC == "" || r.net.TAC == "0") {
				r.net.TAC = tac // modem2_cli prints lac:[0] on LTE
			}
			cellsOK = true
		}
	}
	if !cellsOK { // port busy (an action runs): keep the channel while the cell stays the same
		c.mu.RLock()
		if old := c.radio.net; old.Tech == r.net.Tech && old.CellID == r.net.CellID {
			r.net.EARFCN, r.net.PCI, r.net.Band, r.net.FreqMHz = old.EARFCN, old.PCI, old.Band, old.FreqMHz
			if r.net.TAC == "" || r.net.TAC == "0" {
				r.net.TAC = old.TAC
			}
		}
		c.mu.RUnlock()
	}

	if c.vendor != nil {
		if w, err := c.vendor.WAN(); err == nil {
			r.wan = &w
		}
		c.mu.RLock()
		r.clients = c.radio.clients
		c.mu.RUnlock()
		// dmdb_cli is one of the dearer tools (it hands over a 570 KB buffer); names change rarely and the
		// Wi-Fi list (wl) says who is connected, so once a minute is enough.
		if round%15 == 0 || r.clients == nil {
			if cl, err := c.vendor.Clients(); err == nil {
				r.clients = cl
			}
		}
	}
	c.trackEvents(r.net, vendorOK || atOK, r.wan, now)

	c.mu.Lock()
	c.radio = r
	c.mu.Unlock()
}

// applyCells feeds one $QCRSRP?/$QCRSRQ? reading to the cell list and copies the serving cell's
// channel into the network info.
func (c *Collector) applyCells(n *Network, rsrp, rsrq []qcCell, now time.Time) {
	if sv := c.cells.Update(n.Tech, rsrp, rsrq, now); sv != nil {
		n.EARFCN, n.PCI, n.Band, n.FreqMHz = ip(sv.EARFCN), ip(sv.PCI), sv.Band, sv.FreqMHz
	}
}

// trackEvents logs what changed since the last reading. Network fields are compared only when the
// reading worked (netOK), the data session only when ccm2d answered, so a failed read is not an event.
func (c *Collector) trackEvents(n Network, netOK bool, w *wanInfo, now time.Time) {
	next := c.conn
	if netOK {
		next.tech, next.band, next.reg = n.Tech, n.Band, n.Reg
		next.cell, next.cellNote = n.CellID, ""
		if n.PCI != nil && n.Band != "" {
			next.cellNote = fmt.Sprintf(" (PCI %d, %s)", *n.PCI, n.Band)
		}
	}
	if w != nil {
		next.data, next.ip = w.state, w.ip
	}
	if !netOK && w == nil {
		return
	}
	if !c.connOK {
		c.events.Add("start", "neutral", startText(next), now)
		c.conn, c.connOK = next, true
		return
	}
	diffEvents(c.conn, next, func(kind, level, text string) { c.events.Add(kind, level, text, now) })
	if c.demo == nil && lostService(c.conn, next) {
		go c.explainLoss(now)
	}
	c.conn = next
}

// CellsView is the data for the Cells page.
func (c *Collector) CellsView() CellsView {
	now := time.Now()
	c.mu.RLock()
	n := c.cur.Network
	var scan *ScanResult
	if c.scan != nil {
		s := *c.scan
		s.TS = now.UnixMilli() - now.Sub(s.at).Milliseconds()
		scan = &s
	}
	c.mu.RUnlock()
	v := CellsView{Serving: servingCell(n), Cells: c.cells.Snapshot(now), Scan: scan, Modem: c.modemView(n.Tech)}
	switch {
	case c.demo != nil:
		v.Source = "demo"
	case c.at == nil:
		v.Source = "no-at"
	}
	return v
}

// ScreenData is the snapshot the screen renderer draws from.
func (c *Collector) ScreenData() ScreenData {
	c.want("ui") // the screen may show the weakest Wi-Fi client
	return ScreenData{St: c.Status(), Hist: c.HistorySnapshot(), AimBest: map[string]float64{}}
}

func (c *Collector) Events() []Event { return c.events.Snapshot(time.Now()) }

func (c *Collector) sample() {
	now := time.Now()
	st := Status{TS: now.UnixMilli(), Mode: "live", Clients: []Client{}, Warnings: []string{}}

	if c.demo != nil {
		st.Mode = "demo"
		c.demo.Fill(&st, now)
		rsrp, rsrq := c.demo.Cells(st.Network.Tech)
		c.applyCells(&st.Network, rsrp, rsrq, now)
		c.trackEvents(st.Network, true, &wanInfo{state: st.WAN.State, ip: st.WAN.IP}, now)
	} else {
		c.mu.RLock()
		r := c.radio
		c.mu.RUnlock()
		st.Network, st.Signal = r.net, r.sig
		st.Warnings = append(st.Warnings, r.warn...)
		st.WAN = c.readWAN(now, r.wan)
		st.Battery = readBattery()
		fillPower(&st.Battery, readPowerDetails("/sys/class/power_supply"), &c.x.drain, now)
		st.Clients = readClients(c.cfg.Leases, st.WAN.Iface)
		if r.clients != nil {
			st.Clients = mergeClients(st.Clients, r.clients)
		}
	}
	st.System = readSystem()
	st.Action = c.currentAction()
	c.fillExtras(&st, now)

	c.mu.Lock()
	c.cur = st
	c.hist.add(&st, c.cfg.Interval)
	c.mu.Unlock()
}

// readWAN takes the data session state from ccm2d when it is known (vi), the counters from /proc/net/dev.
func (c *Collector) readWAN(now time.Time, vi *wanInfo) WAN {
	devs := readNetDev()
	name := c.cfg.WAN
	if name == "" && vi != nil {
		name = vi.iface
	}
	if name == "" {
		name = pickWAN(devs)
	}
	w := WAN{Iface: name}
	if vi != nil {
		w.State, w.SessionS = vi.state, vi.sessionS
	}
	d, ok := devs[name]
	if name == "" || !ok {
		return w
	}
	w.RxBytes, w.TxBytes = d.rx, d.tx
	w.Up, w.IP = ifaceState(name)
	if vi != nil {
		// ccm2d knows the session; the kernel may not carry the IPv4 address on rmnet_data0 at all
		// (seen after a reboot: no inet on the interface, ccm2 reports the IP).
		w.Up = vi.state == "connected"
		if vi.ip != "" {
			w.IP = vi.ip
		}
	}
	if c.prev.iface == name && !c.prev.at.IsZero() && d.rx >= c.prev.rx && d.tx >= c.prev.tx {
		dt := now.Sub(c.prev.at).Seconds()
		if dt > 0 {
			w.RxRate = float64(d.rx-c.prev.rx) / dt
			w.TxRate = float64(d.tx-c.prev.tx) / dt
		}
	}
	c.prev = netPrev{iface: name, rx: d.rx, tx: d.tx, at: now}
	return w
}

func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cur
}

func (c *Collector) HistorySnapshot() History {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return History{
		T:    append([]int64{}, c.hist.T...),
		RSRP: append([]*float64{}, c.hist.RSRP...),
		RSRQ: append([]*float64{}, c.hist.RSRQ...),
		SINR: append([]*float64{}, c.hist.SINR...),
		RSCP: append([]*float64{}, c.hist.RSCP...),
		EcIo: append([]*float64{}, c.hist.EcIo...),
		Rx:   append([]float64{}, c.hist.Rx...),
		Tx:   append([]float64{}, c.hist.Tx...),
	}
}

func (c *Collector) currentAction() *Action {
	c.actMu.Lock()
	defer c.actMu.Unlock()
	if c.action == nil {
		return nil
	}
	a := *c.action
	return &a
}

// StartAction runs a modem action in the background; progress shows up in /api/status.
func (c *Collector) StartAction(name string) error {
	var run func(progress func(string)) (string, error)
	switch name {
	case "reregister":
		run = func(p func(string)) (string, error) {
			if c.demo != nil {
				return c.demo.Reregister(p)
			}
			if c.at == nil {
				return "", errors.New("the AT port is not configured")
			}
			return c.at.Reregister(p)
		}
	case "scan":
		run = func(p func(string)) (string, error) {
			var msg string
			var nets []ScanNetwork
			var err error
			switch {
			case c.demo != nil:
				msg, nets, err = c.demo.Scan(p)
			case c.at == nil:
				return "", errors.New("the AT port is not configured")
			default:
				msg, nets, err = c.at.Scan(p)
			}
			if err == nil {
				c.mu.Lock()
				c.scan = &ScanResult{Networks: nets, at: time.Now()}
				c.mu.Unlock()
			}
			return msg, err
		}
	case "restart_data":
		run = func(p func(string)) (string, error) {
			if c.demo != nil {
				return c.demo.RestartData(p)
			}
			if c.vendor == nil {
				return "", errors.New("the stock connection tools (ccm2_cli) were not found")
			}
			return c.vendor.RestartData(p)
		}
	default:
		return fmt.Errorf("unknown action %q", name)
	}

	c.actMu.Lock()
	if c.action != nil && c.action.State == "running" {
		c.actMu.Unlock()
		return errBusy
	}
	a := &Action{Name: name, State: "running", Message: "Starting…", Started: time.Now().UnixMilli()}
	c.action = a
	c.actMu.Unlock()

	go func() {
		progress := func(msg string) {
			c.actMu.Lock()
			a.Message = msg
			c.actMu.Unlock()
		}
		msg, err := run(progress)
		c.actMu.Lock()
		a.Finished = time.Now().UnixMilli()
		if err != nil {
			a.State, a.Message = "error", err.Error()
		} else {
			a.State, a.Message = "done", msg
		}
		c.actMu.Unlock()
	}()
	return nil
}

func (c *Collector) RawAT(cmd string) ([]string, error) {
	cmd, ok := trimAT(cmd)
	if !ok {
		return nil, errors.New("the command must start with AT")
	}
	if c.demo != nil {
		return []string{"(demo) " + cmd, "OK"}, nil
	}
	if c.at == nil {
		return nil, errors.New("the AT port is not configured")
	}
	return c.at.Raw(cmd, 30*time.Second)
}

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }
func bp(v bool) *bool       { return &v }
