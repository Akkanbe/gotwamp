package prom

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package's tests when they leave goroutines behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
