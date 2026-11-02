package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// TestFrame is a pattern that shows whether the RGB666 encoding is right: pure colour bars, grey steps,
// a 1-pixel border and text with Cyrillic. note names the update method being tried.
func TestFrame(note string) *Canvas {
	c := NewCanvas(colWhite)
	bars := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255},
		{0, 255, 255, 255}, {255, 0, 255, 255}, colBlack, colWhite}
	for i, col := range bars {
		c.Fill(image.Rect(i*40, 0, (i+1)*40, 80), col)
	}
	for i := 0; i < 16; i++ { // grey steps, black to white
		v := uint8(i * 17)
		c.Fill(image.Rect(i*20, 80, (i+1)*20, 110), color.RGBA{v, v, v, 255})
	}
	c.TextIn(image.Rect(0, 118, screenW, 150), "mifi-panel test frame", textSizes["l"], "center", colBlack)
	c.TextIn(image.Rect(0, 152, screenW, 176), "Тестовий кадр · RGB666", textSizes["mb"], "center", colAccent)
	c.TextIn(image.Rect(0, 182, screenW, 230), note, textSizes["xl"], "center", levelColors[0])
	for x := 0; x < screenW; x++ {
		c.set(x, 0, colBlack)
		c.set(x, screenH-1, colBlack)
	}
	for y := 0; y < screenH; y++ {
		c.set(0, y, colBlack)
		c.set(screenW-1, y, colBlack)
	}
	return c
}

// The 8800L's LCD is a Sitronix ST7789V2 behind Inseego's nvtl_lcd driver. The panel has its own memory and
// the driver copies the framebuffer to it only when asked: neither write() nor mmap writes show up by
// themselves, and FBIOPAN_DISPLAY returns EINVAL. The stock mifi_display_png (disassembled) writes through
// mmap, then calls ioctl(fd, 0x9999, &rect) with rect = {x, y, width, height} = {0, 0, w, h}.
const (
	fbioGetVScreenInfo = 0x4600
	nvtlLCDUpdate      = 0x9999 // nvtl_lcd: push a rectangle of the framebuffer to the panel
)

type fbDev struct {
	f   *os.File
	mem []byte
}

func openFB(dev string) (*fbDev, error) {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var vi [40]uint32 // struct fb_var_screeninfo: checks that this is a framebuffer at all
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fbioGetVScreenInfo, uintptr(unsafe.Pointer(&vi))); e != 0 {
		f.Close()
		return nil, fmt.Errorf("reading the screen info: %v", e)
	}
	if vi[0] != screenW || vi[1] != screenH {
		f.Close()
		return nil, fmt.Errorf("the screen is %dx%d, expected %dx%d", vi[0], vi[1], screenW, screenH)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, screenW*screenH*3, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("mapping the framebuffer: %w", err)
	}
	return &fbDev{f: f, mem: mem}, nil
}

// update asks nvtl_lcd to copy the whole framebuffer to the panel.
func (d *fbDev) update() error {
	rect := [4]int32{0, 0, screenW, screenH}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), nvtlLCDUpdate, uintptr(unsafe.Pointer(&rect))); e != 0 {
		return e
	}
	return nil
}

func (d *fbDev) Close() {
	_ = syscall.Munmap(d.mem)
	d.f.Close()
}

// runTestFrame saves the current screen, shows the test frame for d (written through mmap, then pushed to
// the panel with the nvtl_lcd update ioctl) and puts the saved screen back the same way.
// devuiappd keeps running; touches still reach it.
func runTestFrame(dev string, d time.Duration) error {
	fb, err := openFB(dev)
	if err != nil {
		return err
	}
	defer fb.Close()
	saved := append([]byte(nil), fb.mem...)

	copy(fb.mem, TestFrame("mmap + update").RGB666())
	if err := fb.update(); err != nil {
		copy(fb.mem, saved)
		return fmt.Errorf("the panel update was refused: %v (screen memory restored)", err)
	}
	fmt.Printf("test frame on the panel for %s\n", d)
	time.Sleep(d)

	copy(fb.mem, saved)
	if err := fb.update(); err != nil {
		return fmt.Errorf("restoring the panel: %v", err)
	}
	fmt.Println("saved screen restored")
	return nil
}
