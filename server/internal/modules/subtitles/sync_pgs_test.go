package subtitles

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// supSegment is a segment of a sup stream as ffmpeg writes it: "PG", pts and dts (90 kHz), type,
// length, payload.
func supSegment(at float64, typ byte, payload []byte) []byte {
	b := []byte{'P', 'G'}
	b = binary.BigEndian.AppendUint32(b, uint32(at*90000+0.5))
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, typ)
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)))
	return append(b, payload...)
}

// supSet is a display set: a composition of objects objects, then (with colours > 0) a palette of
// that many entries, a window and an object of picture bytes, and its end.
func supSet(at float64, objects, colours, picture int, window bool) []byte {
	pcs := make([]byte, 11+8*objects)
	binary.BigEndian.PutUint16(pcs[0:], 1920)
	binary.BigEndian.PutUint16(pcs[2:], 1080)
	pcs[10] = byte(objects)
	b := supSegment(at, pgsPCS, pcs)
	if window {
		b = append(b, supSegment(at, 0x17, make([]byte, 10))...)
	}
	if colours > 0 {
		b = append(b, supSegment(at, 0x14, make([]byte, 2+5*colours))...)
	}
	if picture > 0 {
		b = append(b, supSegment(at, pgsODS, make([]byte, 11+picture))...)
	}
	return append(b, supSegment(at, pgsEND, nil)...)
}

// A short line drawn with a small palette that directly replaces the one showing: its display
// set (a picture of 400 bytes and 4 colours: 477 bytes) is below the size that tells a picture
// from a palette change, so by sizes alone its start was missed and the fade step of its 4
// colours (50 bytes) read as a clear. By its segments it is a new line, the step changes it.
func TestSupSets(t *testing.T) {
	var sup []byte
	sup = append(sup, supSet(10, 1, 256, 9000, true)...)      // a line
	sup = append(sup, supSet(10.5, 1, 256, 0, false)...)      // fade step of its 256 colours
	sup = append(sup, supSet(12, 1, 4, 400, true)...)         // a short line replacing it
	sup = append(sup, supSet(12.4, 1, 4, 0, false)...)        // fade step of its 4 colours
	sup = append(sup, supSet(14, 0, 0, 0, true)...)           // clear
	sup = append(sup, supSet(16, 1, 4, 400, true)...)         // another short line
	sup = append(sup, supSet(17, 0, 0, 0, true)...)           // clear
	sup = append(sup, supSet(19, 1, 256, 9000, true)[:60]...) // cut off by the output cap
	sets := supSets(sup)
	if len(sets) != 7 {
		t.Fatalf("%d sets: %+v", len(sets), sets)
	}
	if s := sets[2]; s.at != 12 || s.size != 477 || !s.read || !s.object || s.empty {
		t.Fatalf("short line: %+v", s)
	}
	if s := sets[4]; !s.empty || s.object || s.size != 30 {
		t.Fatalf("clear: %+v", s)
	}
	if got := pictureCues(sets, 20); fmt.Sprint(got) != fmt.Sprint([]span{{10, 12}, {12, 14}, {16, 17}}) {
		t.Fatalf("cues: %v", got)
	}
	// the same sets by their sizes (as framecrc gives them)
	var sized []framePacket
	for _, s := range sets {
		sized = append(sized, framePacket{at: s.at, size: s.size})
	}
	if got := pictureCues(sized, 20); fmt.Sprint(got) != fmt.Sprint([]span{{10, 12.4}, {16, 17}}) {
		t.Fatalf("by sizes: %v", got)
	}
}
