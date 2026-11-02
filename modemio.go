package main

import (
	"bytes"
	"errors"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// lineConn is a modem port opened non-blocking. It reads only after poll(2) reports data, so a channel
// that never answers (on the 8800L /dev/smd7 opens but stays silent) can neither block a read nor hang
// close: no goroutine is ever left sleeping inside the driver.
type lineConn struct {
	fd  int
	buf []byte
}

var errTimeout = errors.New("no response")

func openLineConn(path string) (*lineConn, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	makeRaw(uintptr(fd))
	return &lineConn{fd: fd}, nil
}

type openResult struct {
	c   *lineConn
	err error
}

// startOpen opens the port in the background. On the 8800L open(2) itself can sleep for a long time:
// glink channels without a modem side (/dev/smd21) wait for it and ignore O_NONBLOCK.
func startOpen(path string) <-chan openResult {
	ch := make(chan openResult, 1)
	go func() {
		c, err := openLineConn(path)
		ch <- openResult{c, err}
	}()
	return ch
}

var errOpenPending = errors.New("the port has not opened yet (no modem behind this channel?)")

// openWithin waits up to d for a background open. If it is still pending, the caller may wait on
// the same channel later instead of starting another open; giveUp closes the port whenever it does open.
func openWithin(ch <-chan openResult, d time.Duration) (*lineConn, error) {
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(d):
		return nil, errOpenPending
	}
}

func giveUp(ch <-chan openResult) {
	go func() {
		if r := <-ch; r.c != nil {
			r.c.Close()
		}
	}()
}

func (c *lineConn) Close() error { return syscall.Close(c.fd) }

// poll(2) event bits; the same values on every Linux architecture.
const (
	pollIn  = 0x1
	pollOut = 0x4
)

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// wait sleeps in ppoll(2) until the port is ready for events or d passes (errTimeout).
// The kernel updates ts with the time left, so a retry after EINTR keeps the original deadline.
func (c *lineConn) wait(events int16, d time.Duration) (int16, error) {
	if d < 0 {
		d = 0
	}
	ts := syscall.NsecToTimespec(d.Nanoseconds())
	p := pollFd{fd: int32(c.fd), events: events}
	for {
		n, _, e := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&p)), 1, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		switch {
		case e == syscall.EINTR:
			continue
		case e != 0:
			return 0, e
		case n == 0:
			return 0, errTimeout
		}
		return p.revents, nil
	}
}

func (c *lineConn) write(b []byte, d time.Duration) error {
	deadline := time.Now().Add(d)
	for len(b) > 0 {
		n, err := syscall.Write(c.fd, b)
		if n > 0 {
			b = b[n:]
		}
		switch err {
		case nil, syscall.EINTR:
		case syscall.EAGAIN:
			if _, err := c.wait(pollOut, time.Until(deadline)); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// readLine returns the next non-empty line, errTimeout once the deadline passes,
// or errPortClosed when the other side hangs up. A deadline in the past only takes what is already there.
func (c *lineConn) readLine(deadline time.Time) (string, error) {
	var tmp [1024]byte
	for {
		if line, ok := c.nextLine(); ok {
			return line, nil
		}
		rev, err := c.wait(pollIn, time.Until(deadline))
		if err != nil {
			return "", err
		}
		if rev&pollIn == 0 { // POLLHUP or POLLERR with nothing to read
			return "", errPortClosed
		}
		n, err := syscall.Read(c.fd, tmp[:])
		switch {
		case n > 0:
			c.buf = append(c.buf, tmp[:n]...)
			if len(c.buf) > 1<<16 {
				c.buf = c.buf[len(c.buf)-1<<16:]
			}
		case err == syscall.EAGAIN || err == syscall.EINTR:
		case err == nil, err == syscall.EIO:
			return "", errPortClosed
		default:
			return "", err
		}
	}
}

func (c *lineConn) nextLine() (string, bool) {
	for {
		i := bytes.IndexAny(c.buf, "\r\n")
		if i < 0 {
			return "", false
		}
		line := strings.TrimSpace(string(c.buf[:i]))
		c.buf = c.buf[i+1:]
		if line != "" {
			return line, true
		}
	}
}

// makeRaw puts a tty into raw mode; for non-tty devices (e.g. /dev/smd*) it does nothing.
func makeRaw(fd uintptr) {
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&t))); e != 0 {
		return
	}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&t)))
}
