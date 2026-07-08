package handlers

import "testing"

func TestCRSVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v4.9.0", "v4.25.0", true},   // numeric compare, NOT lexical ("4.9" < "4.25")
		{"v4.25.0", "v4.9.0", false},  // and the reverse
		{"v4.25.0", "v4.25.0", false}, // equal is not less
		{"v4.25.0", "v4.25.1", true},  // patch component
		{"v4.14.0", "v4.25.0", true},  // the real WASM-vs-shield gap
		{"4.25.0", "v4.25.0", false},  // tolerant of a missing "v" prefix on either side
	}
	for _, c := range cases {
		if got := crsVersionLess(c.a, c.b); got != c.want {
			t.Errorf("crsVersionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
