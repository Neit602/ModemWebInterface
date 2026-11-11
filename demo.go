package main

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Demo generates realistic LTE router data so the UI can be built without the device.
type Demo struct {
	mu               sync.Mutex
	rnd              *rand.Rand
	rsrp, rsrq, sinr float64
	batt             float64
	charging         bool
	rx, tx           uint64
	burst            int
	burstRate        float64
	searchingUntil   time.Time
	sessionStart     time.Time
	last             time.Time
}

func NewDemo() *Demo {
	return &Demo{
		rnd:  rand.New(rand.NewSource(time.Now().UnixNano())),
		rsrp: -97, rsrq: -11, sinr: 7,
		batt: 78, charging: true,
		rx: 3_412_000_000, tx: 412_000_000,
		sessionStart: time.Now().Add(-47 * time.Minute),
	}
}

func (d *Demo) walk(v, mean, pull, noise, lo, hi float64) float64 {
	v += (mean-v)*pull + d.rnd.NormFloat64()*noise
	return math.Max(lo, math.Min(hi, v))
}

func (d *Demo) Fill(st *Status, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dt := 2.0
	if !d.last.IsZero() {
		dt = now.Sub(d.last).Seconds()
	}
	d.last = now

	searching := now.Before(d.searchingUntil)
	// Band, EARFCN and PCI come from Cells(), the same way the real panel gets them.
	st.Network = Network{Operator: "Kyivstar", Tech: "LTE", TechName: "LTE", CellID: "174092810", TAC: "8010", MCC: "255", MNC: "03", Reg: "home"}
	if searching {
		st.Network = Network{Reg: "searching"}
	} else {
		d.rsrp = d.walk(d.rsrp, -97, 0.08, 1.4, -125, -70)
		d.rsrq = d.walk(d.rsrq, -11+(d.rsrp+97)*0.08, 0.15, 0.6, -20, -3)
		d.sinr = d.walk(d.sinr, 7+(d.rsrp+97)*0.35, 0.15, 1.1, -5, 28)
		st.Signal = Signal{RSRP: fp(round1(d.rsrp)), RSRQ: fp(round1(d.rsrq)), SINR: fp(round1(d.sinr)), RSSI: fp(round1(d.rsrp + 24))}
	}

	// Traffic: background plus periodic bursts (video, downloads).
	var rxRate, txRate float64
	if !searching {
		if d.burst > 0 {
			d.burst--
		} else if d.rnd.Float64() < 0.12 {
			d.burst = 3 + d.rnd.Intn(12)
			d.burstRate = 0.6e6 + d.rnd.Float64()*2.8e6
		}
		rxRate = 8e3 + d.rnd.Float64()*30e3
		if d.burst > 0 {
			rxRate += d.burstRate * (0.7 + d.rnd.Float64()*0.6)
		}
		txRate = 3e3 + d.rnd.Float64()*12e3 + rxRate*0.04
	}
	d.rx += uint64(rxRate * dt)
	d.tx += uint64(txRate * dt)
	st.WAN = WAN{Iface: "rmnet_data0", IP: "10.87.142.19", Up: !searching, State: "connected", SessionS: int64(now.Sub(d.sessionStart).Seconds()),
		RxBytes: d.rx, TxBytes: d.tx, RxRate: rxRate, TxRate: txRate}
	if searching {
		st.WAN.IP, st.WAN.State, st.WAN.SessionS = "", "connecting", 0
	}

	if d.charging {
		d.batt += 0.02 * dt
		if d.batt >= 100 {
			d.batt, d.charging = 100, false
		}
	} else {
		d.batt -= 0.01 * dt
		if d.batt <= 20 {
			d.charging = true
		}
	}
	status := "discharging"
	if d.charging {
		status = "charging"
	} else if d.batt >= 100 {
		status = "full"
	}
	st.Battery = Battery{Present: true, Percent: ip(int(d.batt)), Status: status, ExtPower: bp(d.charging), TempC: fp(31.5), VoltageV: fp(3.92)}
	if d.charging {
		st.Battery.CurrentMA, st.Battery.PowerW, st.Battery.Phase = fp(812), fp(3.18), "fast charging"
	} else {
		left := int64(d.batt / 100 * 4400 / 610 * 3600)
		st.Battery.CurrentMA, st.Battery.PowerW, st.Battery.Phase = fp(-610), fp(-2.39), "on battery"
		st.Battery.TimeLeftS, st.Battery.TimeLeftEst = &left, true
	}

	st.Clients = []Client{
		{IP: "192.168.1.2", MAC: "a4:83:e7:1c:22:9f", Name: "laptop", Online: true},
		{IP: "192.168.1.5", MAC: "3c:06:30:8d:41:aa", Name: "Pixel-7", Online: true},
		{IP: "192.168.1.9", MAC: "f0:2f:74:55:10:03", Name: "kitchen-tv", Online: false},
	}
}

func (d *Demo) Reregister(p func(string)) (string, error) {
	p("Detaching from the network…")
	time.Sleep(2 * time.Second)
	d.mu.Lock()
	d.searchingUntil = time.Now().Add(5 * time.Second)
	d.mu.Unlock()
	p("Registering again, this can take up to a minute…")
	time.Sleep(5 * time.Second)
	return "Re-registration complete", nil
}

func (d *Demo) Scan(p func(string)) (string, []ScanNetwork, error) {
	p("Searching for available networks, this can take up to 3 minutes…")
	d.mu.Lock()
	d.searchingUntil = time.Now().Add(8 * time.Second)
	d.mu.Unlock()
	time.Sleep(8 * time.Second)
	p("Switching back to automatic network selection…")
	time.Sleep(2 * time.Second)
	nets := parseCopsList(demoCops)
	return "Found: Kyivstar (LTE, current), Vodafone UA (LTE), lifecell (LTE, forbidden), Vodafone UA (3G)", nets, nil
}

const demoCops = `+COPS: (2,"Kyivstar","KS","25503",7),(1,"Vodafone UA","MTS UA","25501",7),(3,"lifecell","life:)","25506",7),(1,"Vodafone UA","MTS UA","25501",2),,(0,1,2,3,4),(0,1,2)`

// Cells makes $QCRSRP?/$QCRSRQ?-like readings: the serving B3 cell follows the demo RSRP,
// neighbours come and go so the one-minute memory of the cell list can be seen.
func (d *Demo) Cells(tech string) (rsrp, rsrq []qcCell) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if tech == "" {
		return nil, nil
	}
	sec := time.Now().Unix()
	add := func(pci, earfcn int, p, q float64) {
		rsrp = append(rsrp, qcCell{pci: pci, earfcn: earfcn, val: fp(round1(p))})
		rsrq = append(rsrq, qcCell{pci: pci, earfcn: earfcn, val: fp(round1(q))})
	}
	add(212, 1300, d.rsrp, d.rsrq)
	add(318, 1300, d.rsrp-7+d.rnd.NormFloat64(), d.rsrq-2.5)
	add(91, 6300, d.rsrp-4+d.rnd.NormFloat64(), d.rsrq-1)
	if d.rnd.Float64() < 0.6 {
		add(77, 3050, d.rsrp-16+d.rnd.NormFloat64(), -17.5)
	}
	if (sec/150)%2 == 0 { // shows for 2.5 minutes, then is gone for 2.5 minutes
		add(402, 1300, d.rsrp-12+d.rnd.NormFloat64(), -15)
	}
	return rsrp, rsrq
}

// SeedEvents fills the log with a plausible last hour and returns the state it ends in.
func (d *Demo) SeedEvents(l *EventLog, now time.Time) (connState, bool) {
	ago := func(m float64) time.Time { return now.Add(-time.Duration(m * float64(time.Minute))) }
	l.Add("start", "neutral", "Monitoring started: LTE B3, cell 174092810, data session connected", ago(58))
	l.Add("cell", "neutral", "Serving cell changed: 174092810 → 174092811 (PCI 318, B3)", ago(44))
	l.Add("cell", "neutral", "Serving cell changed: 174092811 → 174092810 (PCI 212, B3)", ago(43.5))
	l.Add("network", "neutral", "Network type changed: LTE B3 → 3G", ago(31))
	l.Add("data", "warning", "Data session: connecting", ago(31))
	l.Add("data", "good", "Data session: connected, IP address 10.87.140.7", ago(30.8))
	l.Add("network", "neutral", "Network type changed: 3G → LTE B3", ago(29))
	l.Add("registration", "warning", "Registration: home network → searching for a network", ago(12))
	l.Add("data", "warning", "Data session: connecting", ago(12))
	l.Add("registration", "good", "Registration: searching for a network → home network", ago(11.5))
	l.Add("data", "good", "Data session: connected, IP address 10.87.142.19", ago(11.4))
	return connState{tech: "LTE", band: "B3", cell: "174092810", reg: "home", data: "connected", ip: "10.87.142.19"}, true
}

func (d *Demo) RestartData(p func(string)) (string, error) {
	p("Stopping the data session…")
	time.Sleep(time.Second)
	d.mu.Lock()
	d.searchingUntil = time.Now().Add(4 * time.Second)
	d.sessionStart = time.Now().Add(4 * time.Second)
	d.mu.Unlock()
	p("Starting the data session again…")
	time.Sleep(2 * time.Second)
	p("Waiting for the network to give an IP address…")
	time.Sleep(2 * time.Second)
	return "Data session restarted, IP address 10.87.142.19", nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// demoExtras fills the slower 0.7.0 values with plausible made-up data.
func demoExtras(x *extras) {
	x.powerOn = "Power button"
	x.smsUnread = ip(3)
	x.lifetime = &Lifetime{Down: 399_690_510_667, Up: 24_699_257_709}
	x.tzMin = ip(120)
	x.wan = &WANDetails{APN: "internet", Gateway: "10.87.142.20", Mask: "255.255.255.252", DNS: []string{"193.41.60.1", "193.41.60.2"}}
	x.wanIP = "10.87.142.19"
	x.temps = []Temperature{{"Chip sensor 0", 37.0}, {"Chip sensor 1", 37.0}, {"Chip sensor 2", 36.3}, {"Chip sensor 3", 35.2}, {"Chip sensor 4", 37.7}, {"Crystal (XO)", 33}}
	x.flash = &Flash{MaxEraseCount: 144, BadBlocks: 2, ReservedBad: 38, TotalBlocks: 1484, FreeMB: 217.4, SizeMB: 330.2}
	x.firewall = &Firewall{Policy: "DROP", PolicyDropped: 3, WANRejected: 41, WANClosed: true}
	x.sim = &SIM{State: "ready", PINRetries: ip(3), PUKRetries: ip(10), StateDetail: "SIM_READY"}
	x.versions = Versions{Model: "MIFI8800L", Maker: "Inseego", MiFiOS: "2.100.29", Modem: "SDx20ALP-1.22.11 (2020-04-13)", PRI: "151",
		Hardware: "4", Kernel: "3.18.71", WiFiChip: "BCM43596", WiFiDrv: "9.86.13"}
	x.identity = &Identity{IMEI: "990016870000000", IMSI: "255030000000000", ICCID: "8938030000000000000"}
	x.infoOK = true
	x.radios = demoRadios()
	x.bands = map[string]string{"wlan0": "5 GHz", "wlan1": "2.4 GHz"}
	x.wifiCl = map[string]wifiClient{
		"a4:83:e7:1c:22:9f": {iface: "wlan0", band: "5 GHz", staInfo: staInfo{rssi: ip(-52), txRate: fp(390), rxRate: fp(351), connectedS: 7260}},
		"3c:06:30:8d:41:aa": {iface: "wlan1", band: "2.4 GHz", staInfo: staInfo{rssi: ip(-74), txRate: fp(26), rxRate: fp(13), connectedS: 1835}},
	}
	x.usage = usageSnapshot{session: 533, byMAC: map[string]deviceUsage{
		"a4:83:e7:1c:22:9f": {SessDown: 812_400_000, SessUp: 41_200_000, TotalDown: 96_300_000_000, TotalUp: 5_100_000_000},
		"3c:06:30:8d:41:aa": {SessDown: 64_100_000, SessUp: 7_900_000, TotalDown: 12_800_000_000, TotalUp: 1_400_000_000},
		"f0:2f:74:55:10:03": {TotalDown: 210_000_000_000, TotalUp: 3_200_000_000},
	}}
	pref := map[int]bool{}
	for b := 1; b <= 43; b++ {
		pref[b] = true
	}
	mc := buildBands(pref, []int{1, 2, 3, 4, 5, 7, 8, 13, 14, 20, 28})
	x.modem, x.modemAt = &mc, time.Now()
	x.bsrNextLTE = ip(300)
}

func demoRadios() []WiFiRadio {
	p := Chanspec{Band: "5 GHz", Primary: 161, Center: 155, Width: 80}
	g := Chanspec{Band: "2.4 GHz", Primary: 6, Center: 6, Width: 20}
	return []WiFiRadio{
		{Iface: "wlan0", Network: "Primary", SSID: "Verizon-MiFi8800L-1A2B", Up: true, Chanspec: p, FreqMHz: p.FreqMHz(), Noise: ip(-89), BusyPct: ip(4), OthersPct: ip(1), Clients: 1},
		{Iface: "wlan1", Network: "Guest", SSID: "Verizon-MiFi8800L-Guest-1A2B", Up: true, Chanspec: g, FreqMHz: g.FreqMHz(), Noise: ip(-92), BusyPct: ip(23), OthersPct: ip(15), Clients: 1},
	}
}

func demoNeighbours() []Neighbour {
	n := func(ssid string, rssi int, band string, primary, center, width int) Neighbour {
		return Neighbour{SSID: ssid, RSSI: rssi, Secure: true, Chanspec: Chanspec{Band: band, Primary: primary, Center: center, Width: width}, bssid: ssid}
	}
	return []Neighbour{
		n("HomeNet-5G", -73, "5 GHz", 149, 155, 80),
		n("Kyivstar-WiFi", -81, "5 GHz", 36, 42, 80),
		n("OLAX_MT80", -33, "2.4 GHz", 11, 9, 40),
		n("TP-Link_7F21", -75, "2.4 GHz", 3, 3, 20),
		n("", -88, "2.4 GHz", 1, 1, 20),
		n("ASUS", -84, "2.4 GHz", 6, 6, 20),
	}
}
