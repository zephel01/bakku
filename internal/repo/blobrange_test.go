package repo

import (
	"math"
	"testing"
)

func TestValidBlobRange(t *testing.T) {
	cases := []struct {
		name             string
		off, length, pln int64
		want             bool
	}{
		{"ok unknown packlen", 0, 10, -1, true},
		{"ok within pack", 4, 6, 10, true},
		{"negative offset", -1, 10, -1, false},
		{"zero length", 0, 0, -1, false},
		{"negative length", 0, -5, -1, false},
		{"overflow sum", math.MaxInt64 - 3, 10, -1, false},
		{"exceeds pack", 8, 5, 10, false},
		{"exact pack end", 5, 5, 10, true},
	}
	for _, c := range cases {
		if got := validBlobRange(c.off, c.length, c.pln); got != c.want {
			t.Errorf("%s: validBlobRange(%d,%d,%d)=%v want %v", c.name, c.off, c.length, c.pln, got, c.want)
		}
	}
}
