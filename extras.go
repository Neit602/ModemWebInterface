package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// extras holds the slower values added in 0.7.0. They are read on their own schedule (every 10 s to 10 min),
// and the more expensive groups only while a page that shows them is open ("demand"): every page polls its
// API, and a group stays active for 30 s after the last request.
type extras struct {
	mu     sync.Mutex
	demand map[string]time.Time
	last   map[string]time.Time

	powerOn  string
	versions Versions
	identity *Identity
	infoOK   bool

	smsUnread *int
	lifetime  *Lifetime
	tzMin     *int
	wan       *WANDetails
	wanIP     string

	temps    []Temperature
	flash    *Flash
	firewall *Firewall
	fwErr    string
	sim      *SIM

	bands      map[string]string // wlan0 -> "5 GHz"
	radios     []WiFiRadio
	wifiCl     map[string]wifiClient
	wifiAt     time.Time // when wifiCl was last read successfully
	usage      usageSnapshot
	usageErr   string
	scan       *WiFiScan
	modem      *ModemConfig
	modemAt    time.Time
	bsrNextLTE *int

	drain batteryDrain
}

const demandWindow = 30 * time.Second

func newExtras() *extras {
	return &extras{demand: map[string]time.Time{}, last: map[string]time.Time{}, bands: map[string]string{}}
}

func (c *Collector) want(group string) {
	c.x.mu.Lock()
	c.x.demand[group] = time.Now()
	c.x.mu.Unlock()
}

func (x *extras) active(group string, now time.Time) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.demand[group]
	return ok && now.Sub(t) < demandWindow
}

// due says whether a read of key is due and, if so, marks it as done now (failed reads also wait).
func (x *extras) due(key string, every time.Duration, now time.Time) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if t, ok := x.last[key]; ok && now.Sub(t) < every {
		return false
	}
	x.last[key] = now
	return true
}

func (c *Collector) extrasLoop() {
	c.extrasRound(time.Now())
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for now := range t.C {
		c.extrasRound(now)
	}
}

func (c *Collector) extrasRound(now time.Time) {
	x, v := c.x, c.vendor
	if x.due("temps", 15*time.Second, now) {
		ts := readTemps("/sys/class/thermal")
		x.mu.Lock()
		x.temps = ts
		x.mu.Unlock()
	}
	if v != nil {
		x.mu.Lock()
		needPower, needInfo := x.powerOn == "", !x.infoOK
		x.mu.Unlock()
		if needPower && x.due("power", time.Minute, now) {
			if out, err := v.run("dsm_cli", "get_state", "", 5*time.Second); err == nil && checkRC("dsm_cli", out) == nil {
				if cause := parseDsmState(out)["power_on_cause"]; cause != "" {
					x.mu.Lock()
					x.powerOn = powerOnCauseText(cause)
					x.mu.Unlock()
				}
			}
		}
		if needInfo && x.due("info", time.Minute, now) {
			c.readInfo()
		}
		if x.due("sms", time.Minute, now) {
			if out, err := v.run("msgbus_cli", "MsgBusGet", "sms.list.unread\n", 5*time.Second); err == nil {
				if n, ok := parseMsgBusInt(out); ok {
					x.mu.Lock()
					x.smsUnread = ip(n)
					x.mu.Unlock()
				}
			}
		}
		if x.due("life", 10*time.Minute, now) {
			if out, err := v.run("dmdb_cli", "get_data_usage_statistics", "\n\n\n-1\n", 5*time.Second); err == nil {
				if l, ok := parseUsageStats(out); ok {
					x.mu.Lock()
					x.lifetime = &l
					x.mu.Unlock()
				}
			}
		}
		if x.due("tz", 10*time.Minute, now) {
			if out, err := v.run("modem2_cli", "get_network_time", "", 5*time.Second); err == nil {
				if tz, _, ok := parseNetworkTime(out); ok {
					x.mu.Lock()
					x.tzMin = tz
					x.mu.Unlock()
				} else { // no network time yet: try again in a minute
					x.mu.Lock()
					x.last["tz"] = now.Add(-9 * time.Minute)
					x.mu.Unlock()
				}
			}
		}
		c.readWANDetails(now)
	}

	if c.x.active("ui", now) || c.x.active("wifi", now) {
		c.readWiFi(now, c.x.active("wifi", now))
	}
	if c.x.active("ui", now) && x.due("usage", time.Minute, now) {
		c.readDeviceUsage()
	}
	if c.x.active("system", now) {
		if x.due("flash", time.Minute, now) {
			f := readFlash("/sys/class/ubi", "/")
			x.mu.Lock()
			x.flash = f
			x.mu.Unlock()
		}
		if x.due("firewall", 30*time.Second, now) {
			c.readFirewall()
		}
		if v != nil && x.due("sim", time.Minute, now) {
			if out, err := v.run("modem2_cli", "sim_get_status", "", 5*time.Second); err == nil {
				if s, ok := parseSIMStatus(out); ok {
					x.mu.Lock()
					x.sim = &s
					x.mu.Unlock()
				}
			}
		}
	}
	if v != nil && c.x.active("cells", now) {
		if x.due("bands", 10*time.Minute, now) {
			c.readBands(now)
		}
		if c.Status().Network.Tech == "3G" && x.due("bsr", 5*time.Minute, now) {
			if out, err := v.run("modem2_cli", "bsr_get_timers", "", 5*time.Second); err == nil {
				if n, ok := parseBSR(out); ok {
					x.mu.Lock()
					x.bsrNextLTE = ip(n)
					x.mu.Unlock()
				}
			}
		}
	}
}

// readInfo reads versions and the identity once (they do not change while the router runs).
func (c *Collector) readInfo() {
	x := c.x
	out, err := c.vendor.run("modem2_cli", "get_info", "", 5*time.Second)
	if err != nil || checkRC("modem2_cli", out) != nil {
		return
	}
	ver, id, ok := parseGetInfo(out)
	if !ok {
		return
	}
	ver.MiFiOS, ver.Kernel = readMiFiOS(), kernelVersion()
	if hasWL(c.cfg.VendorDir) {
		if o, err := runWL(c.cfg.VendorDir, "wlan0", "ver"); err == nil {
			ver.WiFiDrv = parseWLVer(o)
		}
		if o, err := runWL(c.cfg.VendorDir, "wlan0", "revinfo"); err == nil {
			ver.WiFiChip = parseRevinfo(o)
		}
	}
	x.mu.Lock()
	x.versions, x.identity, x.infoOK = ver, &id, true
	x.mu.Unlock()
}

// readWANDetails reads the data session's addresses when the session (IP) changes, and every 10 minutes.
func (c *Collector) readWANDetails(now time.Time) {
	x := c.x
	w := c.Status().WAN
	x.mu.Lock()
	changed := w.IP != x.wanIP
	x.mu.Unlock()
	if !x.due("wan", 10*time.Minute, now) && !changed {
		return
	}
	x.mu.Lock()
	x.last["wan"] = now
	x.mu.Unlock()
	if w.IP == "" {
		x.mu.Lock()
		x.wan, x.wanIP = nil, ""
		x.mu.Unlock()
		return
	}
	out, err := c.vendor.run("modem2_cli", "call_get_status", "1\n0\n", 5*time.Second)
	if err != nil {
		return
	}
	d, ok := parseCallStatus(out)
	if !ok {
		return
	}
	if c.at != nil {
		var lines []string
		if e := c.at.with(true, func() error {
			var err error
			lines, err = c.at.cmd("AT+CGCONTRDP", 5*time.Second)
			return err
		}); e == nil {
			apn, dns := parseCGCONTRDP(lines)
			d.APN = apn
			if len(d.DNS) == 0 {
				d.DNS = dns
			}
		}
	}
	x.mu.Lock()
	x.wan, x.wanIP = &d, w.IP
	x.mu.Unlock()
}

// readWiFi reads the clients every 10 s; the networks every 20 s while the Wi-Fi page is open, otherwise
// every 5 minutes (only their band is needed then).
func (c *Collector) readWiFi(now time.Time, page bool) {
	x, dir := c.x, c.cfg.VendorDir
	if !hasWL(dir) {
		return
	}
	every := 5 * time.Minute
	if page {
		every = 30 * time.Second
	}
	if len(x.radiosCopy()) == 0 || x.due("radios", every, now) {
		var rs []WiFiRadio
		bands := map[string]string{}
		for _, ifc := range wifiIfaces {
			r := readRadio(dir, ifc)
			rs = append(rs, r)
			if r.Up {
				bands[ifc] = r.Band
			}
		}
		x.mu.Lock()
		x.radios, x.bands = rs, bands
		x.mu.Unlock()
	}
	if x.due("wificlients", 10*time.Second, now) {
		x.mu.Lock()
		bands := x.bands
		x.mu.Unlock()
		if cl, ok := readWiFiClients(dir, bands); ok {
			x.mu.Lock()
			x.wifiCl, x.wifiAt = cl, now
			x.mu.Unlock()
		}
	}
}

func (x *extras) radiosCopy() []WiFiRadio {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]WiFiRadio(nil), x.radios...)
}

// readDeviceUsage re-reads dmdb.db when it changed since the last read.
func (c *Collector) readDeviceUsage() {
	x := c.x
	fi, err := os.Stat(dmdbPath)
	if err != nil {
		return
	}
	x.mu.Lock()
	same := fi.ModTime().Equal(x.usage.mod) && x.usage.byMAC != nil
	x.mu.Unlock()
	if same {
		return
	}
	snap, err := readUsage(dmdbPath)
	x.mu.Lock()
	defer x.mu.Unlock()
	if err != nil {
		if errors.Is(err, errDBBusy) {
			x.last["usage"] = time.Now().Add(-50 * time.Second) // try again in about 10 s
		}
		x.usageErr = err.Error()
		return
	}
	x.usage, x.usageErr = snap, ""
}

func (c *Collector) readFirewall() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "iptables", "-L", "INPUT", "-v", "-n", "-x").CombinedOutput()
	x := c.x
	x.mu.Lock()
	defer x.mu.Unlock()
	if err != nil {
		x.firewall, x.fwErr = nil, "could not read the firewall: "+strings.TrimSpace(string(out))
		return
	}
	wan := c.Status().WAN.Iface
	f := parseIptablesInput(string(out), wan)
	x.firewall, x.fwErr = &f, ""
}

// readBands reads the LTE band preference for the bands that matter here (11 calls) and the applied list.
func (c *Collector) readBands(now time.Time) {
	v := c.vendor
	pref := map[int]bool{}
	for _, b := range append(append([]int{}, hwLTEBands...), 1, 8) {
		if b > 43 {
			continue // lte_band_get_enabled cannot read bands above 43
		}
		out, err := v.run("modem2_cli", "lte_band_get_enabled", strconv.Itoa(b)+"\n", 5*time.Second)
		if err != nil {
			continue
		}
		if on, ok := parseBandEnabled(out); ok {
			pref[b] = on
		}
	}
	var applied []int
	if out, err := v.run("modem2_cli", "get_diag_info", "", 5*time.Second); err == nil {
		applied, _ = parseDiagBands(out)
	}
	if len(pref) == 0 && applied == nil {
		return
	}
	mc := buildBands(pref, applied)
	c.x.mu.Lock()
	c.x.modem, c.x.modemAt = &mc, now
	c.x.mu.Unlock()
}

// explainLoss adds the network's reject cause and the modem's last failure report to the event log
// after the registration or the data session was lost.
func (c *Collector) explainLoss(at time.Time) {
	if c.vendor != nil {
		if out, err := c.vendor.run("modem2_cli", "get_reject_cause_code", "", 5*time.Second); err == nil {
			if code, ok := parseRejectCause(out); ok && code != 0 {
				c.events.Add("reject", "warning", fmt.Sprintf("Network reject cause %d: %s", code, rejectText(code)), at)
			}
		}
	}
	if c.at == nil {
		return
	}
	for try := 0; try < 5; try++ {
		var lines []string
		err := c.at.with(true, func() error {
			var err error
			lines, err = c.at.cmd("AT+CEER", 5*time.Second)
			return err
		})
		if errors.Is(err, errModemBusy) {
			time.Sleep(2 * time.Second)
			continue
		}
		if t := parseCEER(lines); err == nil && t != "" {
			c.events.Add("reject", "neutral", "Modem's last failure report (AT+CEER): "+t, at)
		}
		return
	}
}

// fillExtras copies the slower values into a status sample and merges the Wi-Fi details into the clients.
func (c *Collector) fillExtras(st *Status, now time.Time) {
	x := c.x
	x.mu.Lock()
	defer x.mu.Unlock()
	st.SMSUnread, st.Lifetime = x.smsUnread, x.lifetime
	st.SoCTempC = socTemp(x.temps)
	st.WAN.Details, st.WAN.TZMin = x.wan, x.tzMin
	st.Battery.PowerOn = x.powerOn
	wifiFresh := x.wifiCl != nil && time.Since(x.wifiAt) < 30*time.Second
	for i := range st.Clients {
		cl := &st.Clients[i]
		w, ok := x.wifiCl[cl.MAC]
		if !ok && wifiFresh && cl.Online && cl.Via != "usb" {
			cl.Online = false // only a stale ARP entry: the device is on neither Wi-Fi network
		}
		if ok {
			cl.Online, cl.Via = true, "wifi"
			cl.Band, cl.Signal, cl.TxRate, cl.RxRate, cl.ConnectedS = w.band, w.rssi, w.txRate, w.rxRate, w.connectedS
			if cl.Network == "" {
				cl.Network = networkName(w.iface)
			}
		}
		if u, ok := x.usage.byMAC[cl.MAC]; ok && (u.TotalDown > 0 || u.TotalUp > 0) {
			u := u
			cl.Usage = &u
		}
	}
}

// WiFiView is the /api/wifi answer.
type WiFiView struct {
	Radios  []WiFiRadio `json:"radios"`
	Clients []Client    `json:"clients"`
	Scan    *WiFiScan   `json:"scan"`
	Source  string      `json:"source"` // "", "demo" or "no-wl"
}

func (c *Collector) WiFiView() WiFiView {
	c.want("wifi")
	c.want("ui")
	v := WiFiView{Radios: c.x.radiosCopy(), Clients: []Client{}}
	for _, cl := range c.Status().Clients {
		if cl.Online {
			v.Clients = append(v.Clients, cl)
		}
	}
	c.x.mu.Lock()
	if c.x.scan != nil {
		s := *c.x.scan
		s.TS = time.Now().UnixMilli() - time.Since(s.at).Milliseconds()
		v.Scan = &s
	}
	c.x.mu.Unlock()
	switch {
	case c.demo != nil:
		v.Source = "demo"
	case !hasWL(c.cfg.VendorDir):
		v.Source = "no-wl"
	}
	if v.Radios == nil {
		v.Radios = []WiFiRadio{}
	}
	return v
}

// StartWiFiScan runs the neighbour scan in the background.
func (c *Collector) StartWiFiScan() error {
	x := c.x
	x.mu.Lock()
	if x.scan != nil && x.scan.Running {
		x.mu.Unlock()
		return errScanBusy
	}
	prev := x.scan
	x.scan = &WiFiScan{Running: true, at: time.Now()}
	if prev != nil {
		x.scan.Neighbours, x.scan.Advice = prev.Neighbours, prev.Advice
	}
	x.mu.Unlock()
	go func() {
		var list []Neighbour
		var radios []WiFiRadio
		var err error
		if c.demo != nil {
			time.Sleep(4 * time.Second)
			radios, list = demoRadios(), demoNeighbours()
		} else if !hasWL(c.cfg.VendorDir) {
			err = errors.New("the Wi-Fi tool (wl) was not found")
		} else {
			for _, ifc := range wifiIfaces {
				radios = append(radios, readRadio(c.cfg.VendorDir, ifc))
			}
			list, err = scanWiFi(c.cfg.VendorDir, radios, func(string) {})
		}
		res := &WiFiScan{at: time.Now(), Neighbours: []Neighbour{}}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Neighbours, res.Advice = markOverlaps(list, radios)
		}
		x.mu.Lock()
		x.scan = res
		x.mu.Unlock()
	}()
	return nil
}

// SystemView is the /api/system answer.
type SystemView struct {
	Temps     []Temperature `json:"temps"`
	SoCTempC  *float64      `json:"soc_temp_c"`
	BattTempC *float64      `json:"battery_temp_c"`
	Flash     *Flash        `json:"flash"`
	Firewall  *Firewall     `json:"firewall"`
	FWError   string        `json:"firewall_error,omitempty"`
	UptimeS   float64       `json:"uptime_s"`
	Load      [3]float64    `json:"load"`
	MemUsed   float64       `json:"mem_used_pct"`
	Versions  Versions      `json:"versions"`
	Panel     string        `json:"panel"`
	SIM       *SIM          `json:"sim"`
	HasID     bool          `json:"has_identity"`
	Source    string        `json:"source"`
}

func (c *Collector) SystemView() SystemView {
	c.want("system")
	st := c.Status()
	v := SystemView{UptimeS: st.System.UptimeS, MemUsed: st.System.MemUsedPct, Panel: version, BattTempC: st.Battery.TempC}
	if f := strings.Fields(readTrim("/proc/loadavg")); len(f) >= 3 {
		for i := 0; i < 3; i++ {
			v.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	} else {
		v.Load[0] = st.System.Load1
	}
	x := c.x
	x.mu.Lock()
	v.Temps, v.Flash, v.Firewall, v.FWError, v.SIM = x.temps, x.flash, x.firewall, x.fwErr, x.sim
	v.Versions, v.HasID = x.versions, x.identity != nil
	x.mu.Unlock()
	v.SoCTempC = socTemp(v.Temps)
	if v.Temps == nil {
		v.Temps = []Temperature{}
	}
	if c.demo != nil {
		v.Source = "demo"
	}
	sort.SliceStable(v.Temps, func(i, j int) bool { return v.Temps[i].Name < v.Temps[j].Name })
	return v
}

// Identity returns IMEI, IMSI and ICCID; only the Show button on the System page asks for them.
func (c *Collector) Identity() (Identity, bool) {
	c.x.mu.Lock()
	defer c.x.mu.Unlock()
	if c.x.identity == nil {
		return Identity{}, false
	}
	return *c.x.identity, true
}

// modemView is the Modem section of the Cells page.
func (c *Collector) modemView(tech string) *ModemConfig {
	c.want("cells")
	x := c.x
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.modem == nil {
		return nil
	}
	m := *x.modem
	m.ReadAgoS = int64(time.Since(x.modemAt).Seconds())
	if tech == "3G" {
		m.NextLTEScS = x.bsrNextLTE
	}
	return &m
}
