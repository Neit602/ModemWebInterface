package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Modem-side ports that may speak AT on the MiFi 8800L: smd8 answers, smd7 opens but stays silent.
// /dev/at_usb* are left out on purpose: they face the USB host, so writing to them would talk to the computer, not the modem.
// /dev/smdcntl* are QMI/rmnet control channels and must never get AT text.
var probeCandidates = []string{"/dev/at_mdm0", "/dev/smd7", "/dev/smd8", "/dev/smd11", "/dev/smd21", "/dev/smd22"}

// runProbeAT sends only read-only queries (ATI, AT+CSQ) and skips any port another process has open,
// so it cannot interfere with the stock software.
func runProbeAT(paths []string) {
	if len(paths) == 0 {
		paths = probeCandidates
	}
	holders := deviceHolders()
	fmt.Println("mifi-panel", version, "AT port check (read-only: ATI, AT+CSQ)")
	for _, p := range paths {
		fmt.Printf("\n== %s\n", p)
		if strings.HasPrefix(filepath.Base(p), "smdcntl") || strings.HasPrefix(filepath.Base(p), "at_usb") {
			fmt.Println("   skipped: not a modem AT channel")
			continue
		}
		if _, err := os.Stat(p); err != nil {
			fmt.Println("   missing")
			continue
		}
		if h := holders[p]; len(h) > 0 {
			fmt.Printf("   skipped: in use by %s\n", strings.Join(h, ", "))
			continue
		}
		for _, cmd := range []string{"ATI", "AT+CSQ"} {
			lines, err := quickAT(p, cmd, 2*time.Second)
			if errors.Is(err, errOpenPending) {
				fmt.Printf("   %v\n", err)
				break // the open is still hanging in the driver; do not start another one
			}
			if err != nil {
				fmt.Printf("   %s: %v\n", cmd, err)
				continue
			}
			fmt.Printf("   %s: %s\n", cmd, strings.Join(lines, " | "))
		}
	}

	fmt.Println("\n== processes holding modem devices")
	var devs []string
	for d := range holders {
		b := filepath.Base(d)
		if strings.HasPrefix(b, "smd") || strings.HasPrefix(b, "at_") || strings.HasPrefix(b, "ttyHS") ||
			strings.HasPrefix(b, "ttyGS") || b == "diag" || strings.HasPrefix(b, "rmnet") || strings.HasPrefix(b, "wwan") {
			devs = append(devs, d)
		}
	}
	sort.Strings(devs)
	if len(devs) == 0 {
		fmt.Println("   none")
	}
	for _, d := range devs {
		fmt.Printf("   %s: %s\n", d, strings.Join(holders[d], ", "))
	}
}

// deviceHolders maps /dev paths to the processes that have them open, from /proc/*/fd.
func deviceHolders() map[string][]string {
	out := map[string][]string{}
	self := strconv.Itoa(os.Getpid())
	procs, _ := filepath.Glob("/proc/[0-9]*")
	for _, p := range procs {
		pid := filepath.Base(p)
		if pid == self {
			continue
		}
		comm := readTrim(filepath.Join(p, "comm"))
		fds, err := os.ReadDir(filepath.Join(p, "fd"))
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, fd := range fds {
			t, err := os.Readlink(filepath.Join(p, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(t, "/dev/") || seen[t] {
				continue
			}
			seen[t] = true
			out[t] = append(out[t], fmt.Sprintf("%s(%s)", comm, pid))
		}
	}
	return out
}

// quickAT sends one command and collects the reply until OK/ERROR or the timeout.
// The port is non-blocking and read only after poll(2), so a silent port just times out.
func quickAT(path, cmd string, timeout time.Duration) ([]string, error) {
	ch := startOpen(path)
	c, err := openWithin(ch, timeout)
	if errors.Is(err, errOpenPending) {
		giveUp(ch)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := c.write([]byte(cmd+"\r"), timeout); err != nil {
		return nil, err
	}
	var out []string
	deadline := time.Now().Add(timeout)
	for {
		l, err := c.readLine(deadline)
		switch {
		case errors.Is(err, errTimeout):
			if len(out) == 0 {
				return nil, errors.New("no answer")
			}
			return out, nil
		case err != nil:
			return out, err
		case l == cmd:
			continue // echo
		}
		out = append(out, l)
		if l == "OK" || l == "ERROR" || strings.HasPrefix(l, "+CME ERROR") {
			return out, nil
		}
	}
}
