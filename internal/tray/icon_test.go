package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestIconICO(t *testing.T) {
	ico := iconICO(statusWaiting, 3, rgba{1, 1, 1, 1})
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || int(binary.LittleEndian.Uint16(ico[4:])) != len(icoSizes) {
		t.Fatalf("bad ICO header % x", ico[:6])
	}
	for i, s := range icoSizes {
		e := ico[6+16*i:]
		size, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		img, err := png.Decode(bytes.NewReader(ico[off : off+size]))
		if err != nil || img.Bounds().Dx() != s || int(e[0]) != s {
			t.Fatalf("entry %d: size %d, err %v", i, s, err)
		}
	}
	// The badge of a waiting status is red in the lower-right corner. The upper-left corner is clear.
	img := iconImage(statusWaiting, 0, 32, rgba{1, 1, 1, 1})
	if c := img.NRGBAAt(23, 30); c.R < 200 || c.G > 100 || c.A != 255 {
		t.Fatalf("badge color %v", c)
	}
	if c := img.NRGBAAt(0, 0); c.A != 0 {
		t.Fatalf("corner %v", c)
	}
}
