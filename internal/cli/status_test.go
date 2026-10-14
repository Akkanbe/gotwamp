package cli

import (
	"testing"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

func TestStoreText(t *testing.T) {
	for s, want := range map[*gotwampv1.StoreStatus]string{
		nil:                 "-",
		{Backend: "memory"}: "memory",
		{Backend: "sqlite", Path: "/x.db", ResultsFailed: 2}: "sqlite /x.db (0 results written, 0 dropped, 2 failed)",
	} {
		if got := storeText(s); got != want {
			t.Errorf("storeText(%v) = %q, want %q", s, got, want)
		}
	}
}
