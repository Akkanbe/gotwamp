//declscope:namespace sqlite

package sqlite

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// fill sets every exported field of v to a value other than zero.
func fill(v reflect.Value, n int64) {
	switch v.Type() {
	case reflect.TypeFor[time.Time]():
		v.Set(reflect.ValueOf(time.Unix(1_790_985_600+n, n*7919).UTC()))
		return
	case reflect.TypeFor[netip.AddrPort]():
		v.Set(reflect.ValueOf(netip.MustParseAddrPort("[2001:db8::1]:50000")))
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), n+int64(i))
			}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(n % 100)
		if v.Kind() == reflect.Int64 {
			v.SetInt(-n*1_000_003 - 1) // negative and large: durations
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(n%200) + 1) //nolint:gosec // G115: small
		if v.Kind() == reflect.Uint64 {
			v.SetUint(1<<63 + uint64(n)) //nolint:gosec // G115: above 2^53: digits must survive
		}
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(n) / 3)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		v.SetString("stage-" + strings.Repeat("x", int(n%5)))
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i), n+int64(i))
		}
	}
}

// TestCompactRoundTrip: the compressed payload decodes to exactly the value
// encoded: every field set, none set, and a few set.
func TestCompactRoundTrip(t *testing.T) {
	var full sender.Summary
	fill(reflect.ValueOf(&full).Elem(), 3)
	partial := sender.Summary{
		Summary: stats.Summary{Sent: 10, Received: 9, LossRatio: 0.1, RTT: stats.Distribution{Count: 9, Max: time.Second}},
		SID:     wire.SID{15: 1}, EndedAt: time.Unix(5, 6).UTC(),
	}
	c := newCompactPayload()
	for name, v := range map[string]any{
		"full": &full, "zero": &sender.Summary{}, "partial": &partial,
		"error": &store.RunError{Stage: "request", Message: "accept-session accept=3", Accept: 3}, "zero error": &store.RunError{},
	} {
		data, err := c.encode(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if data[0] != payloadDeflate {
			t.Errorf("%s: format %#x", name, data[0])
		}
		b, err := payloadJSON(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := reflect.New(reflect.TypeOf(v).Elem())
		if err := json.Unmarshal(b, got.Interface()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, _ := json.Marshal(v)
		back, _ := json.Marshal(got.Interface())
		if !bytes.Equal(want, back) {
			t.Errorf("%s:\n got  %s\n want %s", name, back, want)
		}
	}
}

// realistic is a summary as a run of 100 packets leaves it: every delay
// statistic set, with values that differ.
func realistic(i int) *sender.Summary {
	d := func(base int) stats.Distribution {
		ns := time.Duration(base + i*37)
		return stats.Distribution{
			Count: 100, Samples: 100, Min: ns, Max: 9 * ns, Mean: 2*ns + 11, Median: 2*ns - 7, StdDev: ns + 3,
			P90: 4*ns + 1, P95: 5*ns + 9, P99: 7*ns + 2, P999: 8*ns + 5,
		}
	}
	s := &sender.Summary{
		Summary: stats.Summary{
			Sent: 100, Received: 99, Lost: 1, LossRatio: 0.01, LossSplitValid: true, LostForward: 1,
			RTT: d(14857), Forward: d(3191), Reverse: d(11665), ReflectorDelay: d(37053),
			Jitter: stats.Jitter{
				IPDVRTT: d(7321), IPDVForward: d(2976), IPDVReverse: d(5248), PDV: d(8694),
				RFC3550: time.Duration(7135 + i), P95P50: time.Duration(10957 + i),
			},
			ForwardHops: stats.Hops{Count: 99, Min: 4, Max: 4, Mode: 4}, ReverseHops: stats.Hops{Count: 99, Min: 4, Max: 5, Mode: 4},
			OWDValid: true, SenderSynced: true, ReflectorSynced: true, MaxClockError: 210 * time.Microsecond,
		},
		StartedAt: time.Unix(1_790_985_600+int64(i)*10, 770137438).UTC(), EndedAt: time.Unix(1_790_985_610+int64(i)*10, 870342795).UTC(),
		Local: netip.MustParseAddrPort("192.0.2.1:50123"), Remote: netip.MustParseAddrPort("192.0.2.10:862"),
		Timestamping: 1, TxTimestamps: true,
	}
	s.SID[0] = byte(i)
	return s
}

// TestPayloadSize: a realistic summary takes a few hundred octets, against
// over 2 KiB as JSON.
func TestPayloadSize(t *testing.T) {
	c := newCompactPayload()
	total, raw := 0, 0
	for i := range 100 {
		data, err := c.encode(realistic(i))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(realistic(i))
		total, raw = total+len(data), raw+len(b)
	}
	t.Logf("payload %d octets on average, JSON %d", total/100, raw/100)
	if total/100 > 512 {
		t.Errorf("payload %d octets on average, want at most 512", total/100)
	}
}

func TestPayloadFormats(t *testing.T) {
	if b, err := payloadJSON([]byte(`{"Sent":1}`)); err != nil || string(b) != `{"Sent":1}` {
		t.Errorf("JSON of version 1: %q, %v", b, err)
	}
	for _, bad := range [][]byte{nil, {0x7f, 1}, {payloadDeflate, 0xff, 0xff}} {
		if _, err := payloadJSON(bad); err == nil {
			t.Errorf("payload %x accepted", bad)
		}
	}
	if _, err := compactJSON(&store.Event{}); err == nil {
		t.Error("compact form of an unknown type")
	}
	if _, err := newCompactPayload().encode(struct{}{}); err == nil {
		t.Error("encoded an unknown type")
	}
}
