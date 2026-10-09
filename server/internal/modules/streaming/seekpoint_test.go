package streaming

import "testing"

func TestParseKeyframeProbe(t *testing.T) {
	cases := []struct {
		out  string
		want float64
		ok   bool
	}{
		{"30.000000\n0.000000\n", 30, true},
		// start_time 5 s: the packet time is absolute
		{"35.000000\n5.000000\n", 30, true},
		// MPEG-TS style: trailing comma and an empty line
		{"32.566667,\n\n1.483333\n", 31.084, true},
		{"12.5\nN/A\n", 12.5, true},
		// Only the format line: no packet after the seek
		{"0.000000\n", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := parseKeyframeProbe(c.out)
		if (err == nil) != c.ok {
			t.Fatalf("%q: err = %v", c.out, err)
		}
		if c.ok && got != c.want {
			t.Fatalf("%q: got %v, want %v", c.out, got, c.want)
		}
	}
}
