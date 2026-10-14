package ulid

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	// The example of the ULID specification: 1469918176385 ms.
	ms := time.UnixMilli(1469918176385)
	if id := New(ms, bytes.NewReader(make([]byte, 10))); id != "01ARYZ6S410000000000000000" {
		t.Errorf("New = %q", id)
	}
	if got := New(time.UnixMilli(0), bytes.NewReader(bytes.Repeat([]byte{0xff}, 10))); got != "0000000000ZZZZZZZZZZZZZZZZ" {
		t.Errorf("all-ones randomness = %q", got)
	}
	if got := New(time.UnixMilli(-5), bytes.NewReader(make([]byte, 10))); got != "00000000000000000000000000" {
		t.Errorf("before 1970 = %q", got)
	}
	// IDs sort by time.
	a, b := New(ms, strings.NewReader("abcdefghij")), New(ms.Add(time.Millisecond), strings.NewReader("abcdefghij"))
	if a >= b {
		t.Errorf("%q does not sort before %q", a, b)
	}
	// The default randomness differs between calls.
	if c, d := New(ms, nil), New(ms, nil); c == d {
		t.Errorf("two IDs of one millisecond are equal: %s", c)
	}
}
