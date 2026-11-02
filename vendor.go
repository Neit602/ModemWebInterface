package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Vendor runs the 8800L's own command-line tools in /opt/nvtl/bin. They ask the stock daemons
// (modem2d, ccm2d, dmdbd), so the values match the stock UI and no modem port is needed.
type Vendor struct{ dir string }

// NewVendor returns nil when the tools are not there (any other device, or a desktop).
func NewVendor(dir string) *Vendor {
	if fi, err := os.Stat(filepath.Join(dir, "modem2_cli")); err != nil || fi.Mode()&0o111 == 0 {
		return nil
	}
	return &Vendor{dir: dir}
}

// vendorAllowed is the only set of subcommands the panel may run. The reads are polled; the two
// wwan commands run only from the Restart data session button (CLAUDE.md, safety rule 4).
var vendorAllowed = map[string]bool{
	"modem2_cli get_state":               true,
	"ccm2_cli get_curr_wan_stats":        true,
	"dmdb_cli get_connected_device_list": true,
	"ccm2_cli wwan_disconnect":           true,
	"ccm2_cli wwan_connect":              true,
	// Only from the screen failsafe while devuiappd is paused for "Show on router".
	"watchdog_cli application_send_keep_alive": true,
}

// run starts one tool. Some subcommands ask questions on stdin, so stdin is always given explicitly.
func (v *Vendor) run(tool, sub, stdin string, timeout time.Duration) (string, error) {
	if !vendorAllowed[tool+" "+sub] {
		return "", fmt.Errorf("%s %s is not on the allowed list", tool, sub)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(v.dir, tool), sub)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("%s %s: no reply within %s", tool, sub, timeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v", tool, sub, err)
	}
	return string(out), nil
}

var cliReturned = regexp.MustCompile(`returned (-?\d+) \(([^)]*)\)`)

// cliRC finds "xxx returned 0 (MDM_MAIN: success.)". The tools exit with 0 even when the call fails,
// so this line is the real result. ok is false when the line is missing.
func cliRC(out string) (rc int, msg string, ok bool) {
	m := cliReturned.FindStringSubmatch(out)
	if m == nil {
		return 0, "", false
	}
	return atoi(m[1]), m[2], true
}

func (v *Vendor) Radio() (Network, Signal, error) {
	out, err := v.run("modem2_cli", "get_state", "", 5*time.Second)
	if err != nil {
		return Network{}, Signal{}, err
	}
	return parseModem2State(out)
}

func (v *Vendor) WAN() (wanInfo, error) {
	out, err := v.run("ccm2_cli", "get_curr_wan_stats", "1\n", 5*time.Second) // 1 = WWAN
	if err != nil {
		return wanInfo{}, err
	}
	return parseWANStats(out)
}

func (v *Vendor) Clients() ([]Client, error) {
	out, err := v.run("dmdb_cli", "get_connected_device_list", "", 5*time.Second)
	if err != nil {
		return nil, err
	}
	return parseDmdbClients(out)
}

// RestartData stops and starts the LTE/3G data session the way the stock software does (ccm2d),
// then waits for a new IP address. Both commands ask for an optional call id: an empty line = default.
func (v *Vendor) RestartData(p func(string)) (string, error) {
	p("Stopping the data session…")
	out, err := v.run("ccm2_cli", "wwan_disconnect", "\n", 20*time.Second)
	if err != nil {
		return "", err
	}
	if err := checkRC("ccm2_cli wwan_disconnect", out); err != nil {
		return "", err
	}
	time.Sleep(3 * time.Second)
	p("Starting the data session again…")
	out, err = v.run("ccm2_cli", "wwan_connect", "\n", 20*time.Second)
	if err != nil {
		return "", err
	}
	// Auto-connect in ccm2d may have reconnected already, so a refusal here is not final:
	// the session state below decides.
	connErr := checkRC("ccm2_cli wwan_connect", out)
	p("Waiting for the network to give an IP address…")
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if w, err := v.WAN(); err == nil && w.state == "connected" && w.ip != "" {
			return "Data session restarted, IP address " + w.ip, nil
		}
	}
	if connErr != nil {
		return "", fmt.Errorf("the data session did not come back within a minute (%v)", connErr)
	}
	return "", errors.New("the data session did not come back within a minute")
}

type kvPair struct{ v, note string }

var kvLine = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9 ._]*?)\s*:?\[([^\]]*)\]\s*(?:\(([^)]*)\))?\s*$`)

// parseKV reads the "key:[value] (note)" lines the vendor tools print. Keys are lower-cased;
// when a key repeats, the last one wins (ccm2_cli prints a WAN menu before the answer).
func parseKV(out string) map[string]kvPair {
	m := map[string]kvPair{}
	for _, line := range strings.Split(out, "\n") {
		if g := kvLine.FindStringSubmatch(strings.TrimRight(line, "\r")); g != nil {
			m[strings.ToLower(g[1])] = kvPair{strings.TrimSpace(g[2]), strings.TrimSpace(g[3])}
		}
	}
	return m
}

func checkRC(tool, out string) error {
	if rc, msg, ok := cliRC(out); ok && rc != 0 {
		return fmt.Errorf("%s: %s", tool, msg)
	}
	return nil
}

// parseModem2State reads `modem2_cli get_state`. The tool prints 0 for values the current network
// type does not have (RSRP on 3G, Ec/Io on LTE), so 0 means "not available" here.
// On LTE this firmware always prints sinr:[0]; the SINR in dB is in the snr field (12..24 on B3 at home).
func parseModem2State(out string) (n Network, s Signal, err error) {
	if err := checkRC("modem2_cli", out); err != nil {
		return n, s, err
	}
	kv := parseKV(out)
	if _, ok := kv["tech"]; !ok {
		return n, s, errors.New("modem2_cli: unexpected output")
	}
	n.TechName = kv["tech"].note
	if strings.EqualFold(n.TechName, "NONE") {
		n.TechName = ""
	}
	n.Tech = techClass(n.TechName)
	n.Operator = kv["oper name"].v
	n.MCC, n.MNC = splitMCCMNC(kv["oper id"].v)
	n.CellID = kv["cell id"].v
	n.TAC = kv["lac"].v
	switch kv["reg state"].v { // QMI NAS registration states
	case "1":
		n.Reg = "home"
		if r := kv["roam"].v; r != "" && r != "0" {
			n.Reg = "roaming"
		}
	case "0":
		n.Reg = "none"
	case "2":
		n.Reg = "searching"
	case "3":
		n.Reg = "denied"
	default:
		n.Reg = "unknown"
	}

	val := func(k string) *float64 {
		x, err := strconv.ParseFloat(kv[k].v, 64)
		if err != nil || x == 0 {
			return nil
		}
		return fp(x)
	}
	s.RSSI = val("rssi")
	switch n.Tech {
	case "3G":
		if s.RSSI != nil {
			s.EcIo = val("ecio")
		}
		if s.RSSI != nil && s.EcIo != nil {
			s.RSCP = fp(*s.RSSI + *s.EcIo) // RSCP = RSSI + Ec/Io, the same relation $QCSQ shows
		}
	case "LTE":
		s.RSRP, s.RSRQ = val("rsrp"), val("rsrq")
		if s.RSRP != nil { // 0 dB is a real SINR once RSRP is there
			if x, err := strconv.ParseFloat(kv["snr"].v, 64); err == nil {
				s.SINR = fp(x)
			}
		}
	}
	return n, s, nil
}

// techClass turns the modem's technology name (HSPA+, LTE, EDGE...) into LTE, 3G, 2G or "".
func techClass(name string) string {
	u := strings.ToUpper(name)
	switch {
	case u == "":
		return ""
	case strings.Contains(u, "LTE"):
		return "LTE"
	case strings.Contains(u, "5G") || strings.Contains(u, "NR"):
		return "5G"
	case strings.Contains(u, "HSPA") || strings.Contains(u, "HSDPA") || strings.Contains(u, "HSUPA") ||
		strings.Contains(u, "UMTS") || strings.Contains(u, "WCDMA") || strings.Contains(u, "EVDO") ||
		strings.Contains(u, "EHRPD"):
		return "3G"
	case strings.Contains(u, "GSM") || strings.Contains(u, "GPRS") || strings.Contains(u, "EDGE") ||
		strings.Contains(u, "1X") || strings.Contains(u, "CDMA"):
		return "2G"
	}
	return ""
}

type wanInfo struct {
	state    string // connected, connecting, disconnected
	iface    string
	ip       string
	sessionS int64
}

// parseWANStats reads `ccm2_cli get_curr_wan_stats` (answered with "1" = WWAN on stdin).
// Its byte counters equal /proc/net/dev for the same interface, so speed still comes from there.
func parseWANStats(out string) (w wanInfo, err error) {
	if err := checkRC("ccm2_cli", out); err != nil {
		return w, err
	}
	kv := parseKV(out)
	st, ok := kv["v4 connection state"]
	if !ok {
		return w, errors.New("ccm2_cli: unexpected output")
	}
	switch strings.ToLower(st.v) {
	case "connect", "connected":
		w.state = "connected"
	case "connecting", "waiting":
		w.state = "connecting"
	default:
		w.state = "disconnected"
	}
	w.iface = kv["v4 ifc"].v
	if ip := kv["v4 ip addr"].v; ip != "0.0.0.0" {
		w.ip = ip
	}
	w.sessionS, _ = strconv.ParseInt(kv["v4 duration"].v, 10, 64)
	return w, nil
}

var dmdbField = regexp.MustCompile(`^list\[(\d+)\]\.([a-z_.]+):\[(.*)\]\s*$`)

// parseDmdbClients reads `dmdb_cli get_connected_device_list`: the devices on Wi-Fi right now,
// with the name the owner gave them in the stock UI or the hostname they sent.
func parseDmdbClients(out string) ([]Client, error) {
	if err := checkRC("dmdb_cli", out); err != nil {
		return nil, err
	}
	byIdx := map[int]map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		g := dmdbField.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if g == nil {
			continue
		}
		i := atoi(g[1])
		if byIdx[i] == nil {
			byIdx[i] = map[string]string{}
		}
		byIdx[i][g[2]] = g[3]
	}
	idx := make([]int, 0, len(byIdx))
	for i := range byIdx {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out2 []Client
	for _, i := range idx {
		f := byIdx[i]
		if f["mac"] == "" {
			continue
		}
		name := f["settings.name"]
		if name == "" {
			name = f["entry.hostname"]
		}
		out2 = append(out2, Client{IP: f["data.ipv4"], MAC: strings.ToLower(f["mac"]), Name: name, Online: f["data.connected"] == "1"})
	}
	return out2, nil
}

// mergeClients puts what dmdb knows (connected now, names) over the lease/ARP list, which also
// remembers devices that left.
func mergeClients(base, live []Client) []Client {
	pos := map[string]int{}
	for i, c := range base {
		pos[c.MAC] = i
	}
	for _, c := range live {
		i, ok := pos[c.MAC]
		if !ok {
			base = append(base, c)
			continue
		}
		b := &base[i]
		b.Online = b.Online || c.Online
		if c.Name != "" {
			b.Name = c.Name
		}
		if c.IP != "" {
			b.IP = c.IP
		}
	}
	sort.SliceStable(base, func(i, j int) bool {
		if base[i].Online != base[j].Online {
			return base[i].Online
		}
		return base[i].IP < base[j].IP
	})
	return base
}
