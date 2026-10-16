//go:build linux

package sender_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/reflector"
	"github.com/Akkanbe/gotwamp/sender"
)

func TestFullEndDelayFinalSummary(t *testing.T) {
	for _, mode := range []struct {
		name     string
		features gotwamp.Modes
	}{{"ordinary", 0}, {"isc", gotwamp.ModeIndividualSessionControl}} {
		for _, event := range []string{"cancel", "control_loss"} {
			t.Run(mode.name+"/"+event, func(t *testing.T) {
				parent, stop := context.WithTimeout(t.Context(), 5*time.Second)
				defer stop()
				f, err := reflector.ListenFull(reflector.FullConfig{
					Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
					Modes:     gotwamp.ModeUnauthenticated | mode.features, Timestamping: gotwamp.TimestampUserspace,
				}, reflector.Deps{})
				if err != nil {
					t.Fatal(err)
				}
				served := make(chan error, 1)
				go func() { served <- f.Serve(parent) }()
				defer func() {
					if err := f.Close(); err != nil {
						t.Error(err)
					}
					if err := <-served; err != nil {
						t.Error(err)
					}
				}()
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				cfg := sender.DefaultConfig(netip.AddrPort{})
				cfg.Count, cfg.Interval, cfg.Timeout = 1, time.Millisecond, 500*time.Millisecond
				cfg.Timestamping = gotwamp.TimestampUserspace
				var once sync.Once
				done := make(chan error, 1)
				var summaries []sender.Summary
				summary, err := sender.RunFull(ctx, cfg, sender.FullConfig{Server: f.Addrs()[0], Features: mode.features, EndDelay: time.Second}, sender.WithHooks(sender.Hooks{
					Packet: func(p sender.PacketResult) {
						if p.Result.Kind != gotwamp.KindAccepted {
							return
						}
						once.Do(func() {
							time.AfterFunc(20*time.Millisecond, func() {
								if event == "cancel" {
									cancel()
									done <- nil
								} else {
									done <- f.Close()
								}
							})
						})
					},
					Summary: func(s sender.Summary) { summaries = append(summaries, s) },
				}))
				want := error(context.Canceled)
				if event == "control_loss" {
					want = gotwamp.ErrControlLost
				}
				if !errors.Is(err, want) {
					t.Errorf("error=%v want=%v", err, want)
				}
				if summary.Invalid != (event == "control_loss") || summary.Aborted || summary.Pending != 0 || summary.Received != 1 {
					t.Errorf("summary=%+v", summary)
				}
				if len(summaries) != 1 || !reflect.DeepEqual(summaries[0], summary) {
					t.Errorf("callbacks=%+v returned=%+v", summaries, summary)
				}
				select {
				case eventErr := <-done:
					if eventErr != nil {
						t.Error(eventErr)
					}
				case <-parent.Done():
					t.Fatal("terminal event did not complete")
				}
			})
		}
	}
}
