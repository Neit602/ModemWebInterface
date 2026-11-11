package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Wi-Fi details come from Broadcom's wl tool (the 8800L has a BCM43596 full-MAC chip; iw shows nothing).
// wlan0 is the primary network on 5 GHz, wlan1 the guest network on 2.4 GHz by default; the band is taken
// from the chanspec, not assumed.

// wlAllowed lists the wl subcommands the panel runs. All are reads except "scan", which runs only when the
// user presses "Scan for networks" (the access point leaves its channel for a moment).
var wlAllowed = map[string]bool{
	"status": true, "chanspec": true, "noise": true, "chanim_stats": true, "assoclist": true,
	"sta_info": true, "ver": true, "revinfo": true, "scan": true, "scanresults": true,
}

var wifiIfaces = []string{"wlan0", "wlan1"}

func runWL(dir, iface string, args ...string) (string, error) {
	if len(args) == 0 || !wlAllowed[args[0]] {
		return "", fmt.Errorf("wl %v is not on the allowed list", args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "wl"), append([]string{"-i", iface}, args...)...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("wl %s: no reply", args[0])
	}
	if err != nil {
		return string(out), fmt.Errorf("wl %s: %v", args[0], err)
	}
	return string(out), nil
}

func hasWL(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "wl"))
	return err == nil && fi.Mode()&0o111 != 0
}

// Chanspec is a decoded Broadcom chanspec (d11ac format): band, width, centre and primary channel.
type Chanspec struct {
	Band    string `json:"band"` // "2.4 GHz" or "5 GHz"
	Primary int    `json:"channel"`
	Center  int    `json:"center"`
	Width   int    `json:"width"` // MHz
}

func decodeChanspec(v uint16) (Chanspec, bool) {
	c := Chanspec{Center: int(v & 0xff)}
	switch v & 0x3800 {
	case 0x1000:
		c.Width = 20
	case 0x1800:
		c.Width = 40
	case 0x2000:
		c.Width = 80
	case 0x2800:
		c.Width = 160
	default:
		return c, false
	}
	switch v & 0xc000 {
	case 0x0000:
		c.Band = "2.4 GHz"
	case 0xc000:
		c.Band = "5 GHz"
	default:
		return c, false
	}
	sb := int(v>>8) & 7
	c.Primary = c.Center
	if c.Width > 20 {
		c.Primary = c.Center - (c.Width/10 - 2) + 4*sb
	}
	return c, c.Center > 0
}

var hexSpec = regexp.MustCompile(`\(0x([0-9a-fA-F]{4})\)`)

// parseChanspec reads "161/80 (0xe39b)" or a scan line "Chanspec: 5GHz channel 155 80MHz (0xe09b)".
func parseChanspec(s string) (Chanspec, bool) {
	m := hexSpec.FindStringSubmatch(s)
	if m == nil {
		return Chanspec{}, false
	}
	v, err := strconv.ParseUint(m[1], 16, 16)
	if err != nil {
		return Chanspec{}, false
	}
	return decodeChanspec(uint16(v))
}

// freqRange is the part of the spectrum a channel covers, in MHz.
func (c Chanspec) freqRange() (lo, hi int) {
	f := 5000 + 5*c.Center
	if c.Band == "2.4 GHz" {
		f = 2407 + 5*c.Center
		if c.Center == 14 {
			f = 2484
		}
	}
	return f - c.Width/2, f + c.Width/2
}

func (c Chanspec) FreqMHz() int {
	if c.Band == "2.4 GHz" {
		return 2407 + 5*c.Primary
	}
	return 5000 + 5*c.Primary
}

func overlaps(a, b Chanspec) bool {
	if a.Band != b.Band {
		return false
	}
	alo, ahi := a.freqRange()
	blo, bhi := b.freqRange()
	return alo < bhi && blo < ahi
}

var wlSSID = regexp.MustCompile(`^SSID: "(.*)"`)

// parseWLStatus takes the SSID and the noise floor from `wl status`.
func parseWLStatus(out string) (ssid string, noise *int) {
	for _, line := range strings.Split(out, "\n") {
		if m := wlSSID.FindStringSubmatch(line); m != nil {
			ssid = m[1]
		}
		if i := strings.Index(line, "noise: "); i >= 0 {
			f := strings.Fields(line[i+7:])
			if len(f) > 0 {
				if v, err := strconv.Atoi(f[0]); err == nil && v != 0 {
					noise = &v
				}
			}
		}
	}
	return
}

// parseChanim reads `wl chanim_stats`: the share of time the channel was idle and taken by other networks.
func parseChanim(out string) (busy, others *int) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var head []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == "chanspec" {
			head = f
			continue
		}
		if head == nil || len(f) != len(head) {
			continue
		}
		col := func(name string) (int, bool) {
			for i, h := range head {
				if h == name {
					v, err := strconv.Atoi(f[i])
					return v, err == nil
				}
			}
			return 0, false
		}
		if idle, ok := col("idle"); ok && idle >= 0 && idle <= 100 {
			busy = ip(100 - idle)
		}
		if o, ok := col("obss"); ok {
			n, _ := col("nocat")
			others = ip(min(100, o+n))
		}
	}
	return
}

func parseAssoclist(out string) []string {
	var macs []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "assoclist" {
			macs = append(macs, strings.ToLower(f[1]))
		}
	}
	return macs
}

// staInfo is what `wl sta_info <mac>` tells about one client.
type staInfo struct {
	rssi       *int
	txRate     *float64 // Mbps, last frame sent to the client
	rxRate     *float64 // Mbps, last frame received from it
	connectedS int64
	idleS      int64
	txBytes    int64
	rxBytes    int64
}

var (
	staNum  = regexp.MustCompile(`-?\d+`)
	staRate = regexp.MustCompile(`(\d+) kbps`)
)

func parseStaInfo(out string) (s staInfo, ok bool) {
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(raw)
		k, v, found := strings.Cut(l, ":")
		first := func() int64 {
			m := staNum.FindString(v)
			n, _ := strconv.ParseInt(m, 10, 64)
			return n
		}
		switch {
		case strings.HasPrefix(l, "in network "):
			f := strings.Fields(l)
			if len(f) >= 3 {
				s.connectedS, _ = strconv.ParseInt(f[2], 10, 64)
				ok = true
			}
		case strings.HasPrefix(l, "idle "):
			f := strings.Fields(l)
			if len(f) >= 2 {
				s.idleS, _ = strconv.ParseInt(f[1], 10, 64)
			}
		case !found:
		case k == "tx total bytes":
			s.txBytes = first()
		case k == "rx data bytes":
			s.rxBytes = first()
		case k == "rate of last tx pkt":
			if m := staRate.FindStringSubmatch(v); m != nil {
				r, _ := strconv.ParseFloat(m[1], 64)
				s.txRate = fp(r / 1000)
			}
		case k == "rate of last rx pkt":
			if m := staRate.FindStringSubmatch(v); m != nil {
				r, _ := strconv.ParseFloat(m[1], 64)
				s.rxRate = fp(r / 1000)
			}
		case k == "per antenna average rssi of rx data frames" || (k == "per antenna rssi of last rx data frame" && s.rssi == nil):
			for _, f := range strings.Fields(v) {
				if n, err := strconv.Atoi(f); err == nil && n < 0 {
					s.rssi = &n
					break
				}
			}
		}
	}
	return s, ok
}

// WiFiRadio is one of the router's Wi-Fi networks.
type WiFiRadio struct {
	Iface   string `json:"iface"`
	Network string `json:"network"` // Primary or Guest
	SSID    string `json:"ssid"`
	Up      bool   `json:"up"`
	Chanspec
	FreqMHz   int  `json:"freq_mhz"`
	Noise     *int `json:"noise"`
	BusyPct   *int `json:"busy_pct"`   // share of time the air was busy
	OthersPct *int `json:"others_pct"` // of which other networks and interference
	Clients   int  `json:"clients"`
}

func networkName(iface string) string {
	if iface == "wlan0" {
		return "Primary"
	}
	return "Guest"
}

func readRadio(dir, iface string) WiFiRadio {
	r := WiFiRadio{Iface: iface, Network: networkName(iface)}
	out, err := runWL(dir, iface, "chanspec")
	if err != nil {
		return r
	}
	if cs, ok := parseChanspec(out); ok {
		r.Chanspec, r.Up = cs, true
		r.FreqMHz = cs.FreqMHz()
	}
	if out, err := runWL(dir, iface, "status"); err == nil {
		r.SSID, r.Noise = parseWLStatus(out)
	}
	if out, err := runWL(dir, iface, "chanim_stats"); err == nil {
		r.BusyPct, r.OthersPct = parseChanim(out)
	}
	if out, err := runWL(dir, iface, "assoclist"); err == nil {
		r.Clients = len(parseAssoclist(out))
	}
	return r
}

// wifiClient is the Wi-Fi side of a connected device.
type wifiClient struct {
	iface string
	band  string
	staInfo
}

// readWiFiClients asks each interface for its clients and each client for its signal and link.
// ok is false when no interface answered, so the list cannot be trusted.
func readWiFiClients(dir string, bands map[string]string) (map[string]wifiClient, bool) {
	out := map[string]wifiClient{}
	ok := false
	for _, iface := range wifiIfaces {
		al, err := runWL(dir, iface, "assoclist")
		if err != nil {
			continue
		}
		ok = true
		for _, mac := range parseAssoclist(al) {
			c := wifiClient{iface: iface, band: bands[iface]}
			if si, err := runWL(dir, iface, "sta_info", mac); err == nil {
				if s, ok := parseStaInfo(si); ok {
					c.staInfo = s
				}
			}
			out[mac] = c
		}
	}
	return out, ok
}

// Neighbour is a Wi-Fi network heard in a scan.
type Neighbour struct {
	SSID    string `json:"ssid"`
	RSSI    int    `json:"rssi"`
	Secure  bool   `json:"secure"`
	Overlap string `json:"overlap"` // "same", "partly" or ""
	Ours    bool   `json:"ours,omitempty"`
	Chanspec
	bssid string
}

// parseScanResults reads `wl scanresults`.
func parseScanResults(out string) []Neighbour {
	var list []Neighbour
	var cur *Neighbour
	flush := func() {
		if cur != nil && cur.Center > 0 {
			list = append(list, *cur)
		}
		cur = nil
	}
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(raw)
		switch {
		case wlSSID.MatchString(l):
			flush()
			cur = &Neighbour{SSID: wlSSID.FindStringSubmatch(l)[1]}
		case cur == nil:
		case strings.HasPrefix(l, "Mode:"):
			if i := strings.Index(l, "RSSI: "); i >= 0 {
				f := strings.Fields(l[i+6:])
				if len(f) > 0 {
					cur.RSSI, _ = strconv.Atoi(f[0])
				}
			}
		case strings.HasPrefix(l, "BSSID:"):
			if f := strings.Fields(l); len(f) >= 2 {
				cur.bssid = strings.ToLower(f[1])
			}
			if strings.Contains(l, "WEP") {
				cur.Secure = true // "WEP" in the capability means privacy on: WPA2 shows it too
			}
		case strings.HasPrefix(l, "RSN:"), strings.HasPrefix(l, "WPA:"):
			cur.Secure = true
		case strings.HasPrefix(l, "Chanspec:"):
			if cs, ok := parseChanspec(l); ok {
				cur.Chanspec = cs
			}
		}
	}
	flush()
	return list
}

// WiFiScan is the result of the on-demand neighbour scan.
type WiFiScan struct {
	TS         int64       `json:"ts"`
	Running    bool        `json:"running"`
	Error      string      `json:"error,omitempty"`
	Neighbours []Neighbour `json:"neighbours"`
	Advice     []string    `json:"advice"`
	at         time.Time
}

var scanChannels = map[string]string{
	"2.4 GHz": "1,2,3,4,5,6,7,8,9,10,11,12,13",
	"5 GHz":   "36,40,44,48,52,56,60,64,100,104,108,112,116,120,124,128,132,136,140,144,149,153,157,161,165",
}

// scanWiFi listens for other networks on both bands, one interface at a time. The access point leaves its
// channel for a moment during the scan, so clients may notice a short pause.
func scanWiFi(dir string, radios []WiFiRadio, progress func(string)) ([]Neighbour, error) {
	var all []Neighbour
	var lastErr error
	for _, r := range radios {
		if !r.Up {
			continue
		}
		progress(fmt.Sprintf("Listening on %s…", r.Band))
		if _, err := runWL(dir, r.Iface, "scan", "-c", scanChannels[r.Band]); err != nil {
			lastErr = err
			continue
		}
		time.Sleep(6 * time.Second)
		out, err := runWL(dir, r.Iface, "scanresults")
		if err != nil {
			lastErr = err
			continue
		}
		for _, n := range parseScanResults(out) {
			if n.Band == r.Band {
				all = append(all, n)
			}
		}
	}
	if len(all) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return dedupe(all), nil
}

func dedupe(list []Neighbour) []Neighbour {
	seen := map[string]bool{}
	var out []Neighbour
	for _, n := range list {
		k := n.bssid
		if k == "" {
			k = n.SSID + "/" + strconv.Itoa(n.Primary)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

// markOverlaps flags networks that share air with ours and suggests a quieter channel per band.
func markOverlaps(list []Neighbour, radios []WiFiRadio) ([]Neighbour, []string) {
	for i := range list {
		for _, r := range radios {
			if !r.Up || r.Band != list[i].Band {
				continue
			}
			if list[i].SSID != "" && list[i].SSID == r.SSID && list[i].Primary == r.Primary {
				list[i].Ours = true
				continue
			}
			switch {
			case list[i].Primary == r.Primary || (list[i].Width > 20 && r.Width > 20 && list[i].Center == r.Center):
				list[i].Overlap = "same"
			case overlaps(list[i].Chanspec, r.Chanspec):
				list[i].Overlap = "partly"
			}
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		if list[a].Band != list[b].Band {
			return list[a].Band > list[b].Band // 5 GHz first, like the router's primary network
		}
		return list[a].RSSI > list[b].RSSI
	})
	var advice []string
	for _, r := range radios {
		if r.Up {
			advice = append(advice, channelAdvice(list, r))
		}
	}
	return list, advice
}

// airLoad sums the strength of the networks that overlap a channel, in milliwatts (stronger counts much more).
func airLoad(list []Neighbour, c Chanspec) (mw float64, n int) {
	for _, x := range list {
		if x.Ours || !overlaps(x.Chanspec, c) {
			continue
		}
		mw += math.Pow(10, float64(x.RSSI)/10)
		n++
	}
	return
}

// channelAdvice compares the current channel with the usual non-overlapping choices of the same width.
func channelAdvice(list []Neighbour, r WiFiRadio) string {
	cur, curN := airLoad(list, r.Chanspec)
	var cands []Chanspec
	if r.Band == "2.4 GHz" {
		for _, ch := range []int{1, 6, 11} {
			cands = append(cands, Chanspec{Band: r.Band, Primary: ch, Center: ch, Width: 20})
		}
	} else {
		// Channels outside the radar (DFS) range, which every 5 GHz device supports.
		blocks := map[int][]int{20: {36, 40, 44, 48, 149, 153, 157, 161, 165}, 40: {38, 46, 151, 159}, 80: {42, 155}}
		w := r.Width
		if blocks[w] == nil {
			w = 20
		}
		for _, c := range blocks[w] {
			p := c
			if w > 20 {
				p = c - (w/10 - 2)
			}
			cands = append(cands, Chanspec{Band: r.Band, Primary: p, Center: c, Width: w})
		}
	}
	best, bestMW, bestN := Chanspec{}, math.Inf(1), 0
	for _, c := range cands {
		if mw, n := airLoad(list, c); mw < bestMW {
			best, bestMW, bestN = c, mw, n
		}
	}
	name := fmt.Sprintf("%s network (%s)", r.Network, r.Band)
	switch {
	case curN == 0:
		return fmt.Sprintf("%s: no other network shares channel %d. Keep it.", name, r.Primary)
	case best.Center == r.Center || bestMW >= cur/2:
		return fmt.Sprintf("%s: %d other network(s) share the air with channel %d, but no listed channel is clearly quieter. Keep it.", name, curN, r.Primary)
	case bestN == 0:
		return fmt.Sprintf("%s: %d other network(s) share the air with channel %d. Channel %d (%d MHz) is free: you can switch to it in the stock settings (my.mifi → Wi-Fi).", name, curN, r.Primary, best.Primary, best.Width)
	default:
		return fmt.Sprintf("%s: channel %d (%d MHz) is quieter than channel %d. You can switch to it in the stock settings (my.mifi → Wi-Fi).", name, best.Primary, best.Width, r.Primary)
	}
}

var errScanBusy = errors.New("a Wi-Fi scan is already running")
