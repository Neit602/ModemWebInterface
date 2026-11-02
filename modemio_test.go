package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// openPTY returns the master side and the slave path of a new pseudo-terminal,
// which stands in for a modem port in tests.
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skipf("unlockpt: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Skipf("ptsname: %v", e)
	}
	t.Cleanup(func() { m.Close() })
	return m, fmt.Sprintf("/dev/pts/%d", n)
}

// fakeModem answers each command line with the reply from replies (echo first, like the 8800L);
// commands not in the map get no answer at all. It reports every command it receives on got.
func fakeModem(m *os.File, replies map[string]string, got chan<- string) {
	r := bufio.NewReader(m)
	for {
		line, err := r.ReadString('\r')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		if got != nil {
			got <- cmd
		}
		if rep, ok := replies[cmd]; ok {
			fmt.Fprintf(m, "%s\r\n%s", cmd, rep)
		}
	}
}

func TestQuickATSilentPortTimesOut(t *testing.T) {
	_, slave := openPTY(t) // nobody answers on the master side
	start := time.Now()
	_, err := quickAT(slave, "ATI", 300*time.Millisecond)
	if err == nil || err.Error() != "no answer" {
		t.Fatalf("want 'no answer', got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("silent port took %s, it must return right after the timeout", d)
	}
}

func TestQuickATReply(t *testing.T) {
	m, slave := openPTY(t)
	go fakeModem(m, map[string]string{"AT+CSQ": "+CSQ: 99,99\r\n\r\nOK\r\n"}, nil)
	got, err := quickAT(slave, "AT+CSQ", 2*time.Second)
	if err != nil || strings.Join(got, "|") != "+CSQ: 99,99|OK" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestATPortSendsNothingOnOpen(t *testing.T) {
	m, slave := openPTY(t)
	got := make(chan string, 16)
	go fakeModem(m, map[string]string{
		"AT$QCSQ": "$QCSQ :-57,-7,0,0,-50\r\n\r\nOK\r\n",
		"AT+X":    "ERROR\r\n",
	}, got)
	a := NewATPort(slave)

	out, err := a.Raw("AT$QCSQ", 2*time.Second)
	if err != nil || len(out) != 1 || out[0] != "$QCSQ :-57,-7,0,0,-50" {
		t.Fatalf("got %q, %v", out, err)
	}
	if c := <-got; c != "AT$QCSQ" {
		t.Fatalf("the first thing sent must be the command itself, got %q", c)
	}
	if _, err := a.Raw("AT+X", 2*time.Second); err == nil || !strings.Contains(err.Error(), "ERROR") {
		t.Fatalf("want ERROR, got %v", err)
	}
	// A command the modem ignores times out, and the port stays usable afterwards.
	if _, err := a.Raw("AT+SILENT", 200*time.Millisecond); err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("want timeout, got %v", err)
	}
	if out, err := a.Raw("AT$QCSQ", 2*time.Second); err != nil || len(out) != 1 {
		t.Fatalf("after a timeout: %q, %v", out, err)
	}
	a.mu.Lock()
	a.reset()
	a.mu.Unlock()
}

func TestQCSQ3G(t *testing.T) {
	rscp, ecio, rssi := parseQCSQ3G(" :-57,-7,0,0,-50")
	if rscp == nil || *rscp != -57 || ecio == nil || *ecio != -7 || rssi == nil || *rssi != -50 {
		t.Fatalf("got %v %v %v", rscp, ecio, rssi)
	}
}
