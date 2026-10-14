package store_test

import (
	"testing"

	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/store/storetest"
)

// TestMemoryContract runs the store contract on the in-memory store.
func TestMemoryContract(t *testing.T) {
	storetest.Run(t, func(_ *testing.T, history, maxPackets int) store.Store {
		return store.NewMemory(history, maxPackets)
	})
}
