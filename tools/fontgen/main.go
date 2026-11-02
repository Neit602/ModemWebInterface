// fontgen turns Terminus BDF fonts into the small bitmap font files the screen renderer embeds.
//
// Usage: go run ./tools/fontgen <out.bin> <font.bdf>
//
// Only the characters the panel needs are kept: ASCII, Latin-1, Ukrainian and Russian Cyrillic
// and a few symbols. Output format (little endian):
//
//	"MPF1", cell width u8, cell height u8, ascent u8, 0, glyph count u16,
//	count x rune u32 (sorted), count x bitmap (ceil(width/8) bytes per row, height rows, MSB = left).
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

var keep = [][2]rune{
	{0x20, 0x7E}, {0xA0, 0xFF}, {0x400, 0x45F}, {0x490, 0x491},
	{0x2010, 0x2027}, {0x2030, 0x203A}, {0x2116, 0x2116}, {0x2190, 0x2195}, {0x2212, 0x2212},
	{0x2248, 0x2248}, {0x2264, 0x2265}, {0x25A0, 0x25A1}, {0x25B2, 0x25BC}, {0x25CF, 0x25CF}, {0xFFFD, 0xFFFD},
}

func wanted(r rune) bool {
	for _, k := range keep {
		if r >= k[0] && r <= k[1] {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: fontgen <out.bin> <font.bdf>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[2])
	if err != nil {
		fail(err)
	}
	defer f.Close()

	var cellW, cellH, ascent int
	glyphs := map[rune][]byte{}
	var cur rune = -1
	var bitmap []byte
	inBitmap := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) == 0 {
			continue
		}
		switch {
		case fs[0] == "FONTBOUNDINGBOX" && len(fs) >= 3:
			cellW, _ = strconv.Atoi(fs[1])
			cellH, _ = strconv.Atoi(fs[2])
		case fs[0] == "FONT_ASCENT" && len(fs) >= 2:
			ascent, _ = strconv.Atoi(fs[1])
		case fs[0] == "ENCODING" && len(fs) >= 2:
			n, _ := strconv.Atoi(fs[1])
			cur = rune(n)
		case fs[0] == "BBX" && len(fs) >= 3:
			if w, _ := strconv.Atoi(fs[1]); w != cellW {
				fail(fmt.Errorf("glyph %U is %d px wide, the font cell is %d: not a fixed-width font", cur, w, cellW))
			}
		case fs[0] == "BITMAP":
			inBitmap, bitmap = true, nil
		case fs[0] == "ENDCHAR":
			inBitmap = false
			if wanted(cur) {
				if len(bitmap) != (cellW+7)/8*cellH {
					fail(fmt.Errorf("glyph %U has %d bytes", cur, len(bitmap)))
				}
				glyphs[cur] = bitmap
			}
		case inBitmap:
			b, err := hex.DecodeString(fs[0])
			if err != nil {
				fail(err)
			}
			bitmap = append(bitmap, b...)
		}
	}
	if err := sc.Err(); err != nil {
		fail(err)
	}

	runes := make([]rune, 0, len(glyphs))
	for r := range glyphs {
		runes = append(runes, r)
	}
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })

	out := []byte("MPF1")
	out = append(out, byte(cellW), byte(cellH), byte(ascent), 0)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(runes)))
	for _, r := range runes {
		out = binary.LittleEndian.AppendUint32(out, uint32(r))
	}
	for _, r := range runes {
		out = append(out, glyphs[r]...)
	}
	if err := os.WriteFile(os.Args[1], out, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("%s: %dx%d, %d glyphs, %d bytes\n", os.Args[1], cellW, cellH, len(runes), len(out))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fontgen:", err)
	os.Exit(1)
}
