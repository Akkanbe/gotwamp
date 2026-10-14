package stats

import (
	"math"
	"testing"
	"time"
)

func TestMeasure(t *testing.T) {
	base := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	at := func(us int) time.Time { return base.Add(time.Duration(us) * time.Microsecond) }
	clock := ClockStatus{Synced: true, Error: 3 * time.Microsecond}
	r := PacketRecord{
		T1: at(0), T2: at(4000), T3: at(4500), T4: at(10500),
		SenderClock: clock, ReflectorClock: clock,
		SenderTTL: 60, ReplyTTL: 250,
	}

	got := measure(r, 64)
	want := Metrics{
		RTT:            10 * time.Millisecond,
		RTTRaw:         10500 * time.Microsecond,
		Forward:        4 * time.Millisecond,
		Reverse:        6 * time.Millisecond,
		ReflectorDelay: 500 * time.Microsecond,
		ClockOffset:    -1 * time.Millisecond,
		MaxClockError:  6 * time.Microsecond,
		DelayUsable:    true,
		OWDValid:       true,
		ForwardHops:    4,
		ReverseHops:    5,
	}
	if got != want {
		t.Errorf("measure:\n got %+v\nwant %+v", got, want)
	}

	tests := []struct {
		name  string
		edit  func(*PacketRecord)
		ttl   uint8
		check func(Metrics) bool
	}{
		{"default send TTL", func(*PacketRecord) {}, 0, func(m Metrics) bool { return m.ForwardHops == 195 }},
		{"TTL above send TTL", func(r *PacketRecord) { r.SenderTTL = 70 }, 64, func(m Metrics) bool { return m.ForwardHops == -1 }},
		{"reply TTL unknown", func(r *PacketRecord) { r.ReplyTTL = 0 }, 64, func(m Metrics) bool { return m.ReverseHops == -1 }},
		{"unknown error", func(r *PacketRecord) { r.ReflectorClock.ErrorUnknown = true }, 64, func(m Metrics) bool {
			return !m.OWDValid && m.DelayUsable && m.MaxClockError == 3*time.Microsecond
		}},
		{"sender unknown error", func(r *PacketRecord) { r.SenderClock.ErrorUnknown = true }, 64, func(m Metrics) bool {
			return !m.OWDValid && m.MaxClockError == 3*time.Microsecond
		}},
		{"sender unsynced", func(r *PacketRecord) { r.SenderClock.Synced = false }, 64, func(m Metrics) bool {
			return !m.OWDValid && m.Forward == 4*time.Millisecond
		}},
		{"negative forward", func(r *PacketRecord) { r.T2 = at(-100) }, 64, func(m Metrics) bool {
			return !m.OWDValid && m.Forward == -100*time.Microsecond
		}},
		{"sender unsupported format", func(r *PacketRecord) { r.SenderClock.UnsupportedFormat = true }, 64, func(m Metrics) bool {
			return !m.DelayUsable && !m.OWDValid && m.RTT == 0 && m.ForwardHops == 4 && m.MaxClockError == 6*time.Microsecond
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := r
			tt.edit(&r)
			if m := measure(r, tt.ttl); !tt.check(m) {
				t.Errorf("measure = %+v", m)
			}
		})
	}
}

func TestMaxClockErrorSaturates(t *testing.T) {
	huge := ClockStatus{Error: time.Duration(math.MaxInt64)}
	if m := measure(PacketRecord{SenderClock: huge, ReflectorClock: huge}, 0); m.MaxClockError != math.MaxInt64 {
		t.Errorf("MaxClockError = %v", m.MaxClockError)
	}
}
