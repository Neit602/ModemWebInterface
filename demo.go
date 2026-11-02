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
	st.Battery = Battery{Present: true, Percent: ip(int(d.batt)), Status: status, ExtPower: bp(d.charging), TempC: fp(31.5)}

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
