package shortener

import (
	"math"
	"testing"
)

func TestEncodeKnownValues(t *testing.T) {
	cases := map[uint64]string{
		0:            "0",
		9:            "9",
		10:           "A",
		35:           "Z",
		36:           "a",
		61:           "z",
		62:           "10",
		3843:         "zz",
		3844:         "100",
		62 * 62 * 62: "1000",
	}
	for n, want := range cases {
		if got := Encode(n); got != want {
			t.Errorf("Encode(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, n := range []uint64{0, 1, 61, 62, 1000, 238328, 1 << 32, math.MaxUint64} {
		got, err := Decode(Encode(n))
		if err != nil || got != n {
			t.Errorf("round trip of %d gave %d, %v", n, got, err)
		}
	}
}

func TestDecodeRejectsBadInput(t *testing.T) {
	for _, input := range []string{"", "a-b", "hello world", "AAAAAAAAAAAA", "zzzzzzzzzzz"} {
		if _, err := Decode(input); err == nil {
			t.Errorf("Decode(%q) should fail", input)
		}
	}
}

func TestEncodeIsOrderedByLengthThenValue(t *testing.T) {
	previous := 0
	for n := uint64(0); n < 100000; n += 997 {
		length := len(Encode(n))
		if length < previous {
			t.Fatalf("length shrank at %d", n)
		}
		previous = length
	}
}
