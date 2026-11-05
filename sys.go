package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type devCounters struct{ rx, tx uint64 }

func readNetDev() map[string]devCounters {
	out := map[string]devCounters{}
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		fields := strings.Fields(line[i+1:])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		out[name] = devCounters{rx, tx}
	}
	return out
}

var wanPrefixes = []string{"rmnet_data", "rmnet", "wwan", "ccmni", "qmimux"}

// pickWAN picks the LTE interface: one with an IPv4 address first, otherwise the busiest one.
func pickWAN(devs map[string]devCounters) string {
	var cands []string
	for name := range devs {
		for _, p := range wanPrefixes {
			if strings.HasPrefix(name, p) {
				cands = append(cands, name)
				break
			}
		}
	}
	sort.Strings(cands)
	best, bestRx := "", uint64(0)
	for _, name := range cands {
		if up, ipAddr := ifaceState(name); up && ipAddr != "" {
			return name
		}
		if d := devs[name]; best == "" || d.rx > bestRx {
			best, bestRx = name, d.rx
		}
	}
	return best
}

func ifaceState(name string) (up bool, ipv4 string) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return false, ""
	}
	up = ifi.Flags&net.FlagUp != 0
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return up, n.IP.String()
		}
	}
	return up, ""
}

func readBattery() Battery { return readBatteryFrom("/sys/class/power_supply") }

// batteryTemp reads the battery temperature in tenths of a degree. The 8800L fuel gauge
// (bq27520-battery) exposes it as temp_ambient; other drivers use temp.
func batteryTemp(dir string) *float64 {
	for _, name := range []string{"temp_ambient", "temp"} {
		if v, err := strconv.Atoi(readTrim(filepath.Join(dir, name))); err == nil {
			return fp(float64(v) / 10)
		}
	}
	return nil
}

func readBatteryFrom(base string) Battery {
	var b Battery
	entries, err := os.ReadDir(base)
	if err != nil {
		b.Status = "unknown"
		return b
	}
	var ext *bool
	for _, e := range entries {
		p := filepath.Join(base, e.Name())
		typ := readTrim(filepath.Join(p, "type"))
		switch {
		case typ == "Battery":
			if pr := readTrim(filepath.Join(p, "present")); pr == "0" {
				continue
			}
			// The charger IC (smb1351-charger on the 8800L) also reports type=Battery but has no
			// capacity; only the fuel gauge (bq27520-battery) gives the real level and status.
			v, err := strconv.Atoi(readTrim(filepath.Join(p, "capacity")))
			if err != nil || b.Percent != nil {
				continue
			}
			b.Present = true
			b.Percent = ip(v)
			switch strings.ToLower(readTrim(filepath.Join(p, "status"))) {
			case "charging":
				b.Status = "charging"
			case "discharging", "not charging":
				b.Status = "discharging"
			case "full":
				b.Status = "full"
			default:
				b.Status = "unknown"
			}
			b.TempC = batteryTemp(p)
		case strings.HasPrefix(typ, "USB") || typ == "Mains" || typ == "Wireless":
			online := readTrim(filepath.Join(p, "online")) == "1"
			if ext == nil {
				ext = bp(online)
			} else if online {
				ext = bp(true)
			}
		}
	}
	b.ExtPower = ext
	if b.Status == "" {
		b.Status = "unknown"
	}
	return b
}

func readSystem() System {
	var s System
	if f := strings.Fields(readTrim("/proc/uptime")); len(f) > 0 {
		s.UptimeS, _ = strconv.ParseFloat(f[0], 64)
	}
	if f := strings.Fields(readTrim("/proc/loadavg")); len(f) > 0 {
		s.Load1, _ = strconv.ParseFloat(f[0], 64)
	}
	var total, avail float64
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 2 {
				continue
			}
			v, _ := strconv.ParseFloat(fs[1], 64)
			switch fs[0] {
			case "MemTotal:":
				total = v
			case "MemAvailable:":
				avail = v
			}
		}
		f.Close()
	}
	if total > 0 {
		s.MemUsedPct = (total - avail) / total * 100
	}
	return s
}

var leaseCandidates = []string{
	"/opt/nvtl/tmp/router2/dhcpd.leases", // MiFi 8800L: ISC dhcpd run by router2d
	"/var/lib/dhcp/dhcpd.leases",
	"/var/lib/misc/dnsmasq.leases",
	"/tmp/dnsmasq.leases",
	"/var/run/dnsmasq.leases",
}

// readLeaseFile understands both ISC dhcpd and dnsmasq lease files.
func readLeaseFile(path string) ([]Client, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	s := string(b)
	if strings.Contains(s, "lease ") && strings.Contains(s, "{") {
		return parseISCLeases(s), true
	}
	return parseDnsmasqLeases(s), true
}

// parseISCLeases reads ISC dhcpd blocks. dhcpd appends a new block on every renewal,
// so the last block for a MAC address wins.
func parseISCLeases(s string) []Client {
	byMAC := map[string]Client{}
	var order []string
	var cur *Client
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "lease ") && strings.HasSuffix(line, "{"):
			if f := strings.Fields(line); len(f) >= 2 {
				cur = &Client{IP: f[1]}
			}
		case cur == nil:
		case strings.HasPrefix(line, "hardware ethernet "):
			cur.MAC = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(line, "hardware ethernet "), ";"))
		case strings.HasPrefix(line, "client-hostname "):
			cur.Name = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(line, "client-hostname "), ";"), `"`)
		case line == "}":
			if cur.MAC != "" {
				if _, ok := byMAC[cur.MAC]; !ok {
					order = append(order, cur.MAC)
				}
				byMAC[cur.MAC] = *cur
			}
			cur = nil
		}
	}
	out := make([]Client, 0, len(order))
	for _, m := range order {
		out = append(out, byMAC[m])
	}
	return out
}

// parseDnsmasqLeases reads "expiry mac ip hostname clientid" lines.
func parseDnsmasqLeases(s string) []Client {
	var out []Client
	for _, line := range strings.Split(s, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 4 {
			continue
		}
		name := fs[3]
		if name == "*" {
			name = ""
		}
		out = append(out, Client{IP: fs[2], MAC: strings.ToLower(fs[1]), Name: name})
	}
	return out
}

// readClients merges DHCP leases with the ARP table: who has connected and who is online now.
func readClients(leasesPath, wanIface string) []Client {
	online := map[string]string{} // mac -> ip
	if f, err := os.Open("/proc/net/arp"); err == nil {
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 6 || fs[2] != "0x2" || fs[5] == wanIface {
				continue
			}
			online[strings.ToLower(fs[3])] = fs[0]
		}
		f.Close()
	}

	paths := leaseCandidates
	if leasesPath != "" {
		paths = []string{leasesPath}
	}
	clients := []Client{}
	seen := map[string]bool{}
	for _, p := range paths {
		leases, ok := readLeaseFile(p)
		if !ok {
			continue
		}
		for _, c := range leases {
			if seen[c.MAC] {
				continue
			}
			seen[c.MAC] = true
			if addr, on := online[c.MAC]; on {
				c.Online, c.IP = true, addr
			}
			clients = append(clients, c)
		}
		break
	}
	for mac, addr := range online {
		if !seen[mac] {
			clients = append(clients, Client{IP: addr, MAC: mac, Online: true})
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Online != clients[j].Online {
			return clients[i].Online
		}
		return clients[i].IP < clients[j].IP
	})
	return clients
}
