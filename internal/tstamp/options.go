//declscope:namespace conn // option checks are part of socket acquisition

package tstamp

import "fmt"

// Validate checks socket options without acquiring a socket on any platform.
func (o Options) Validate() error {
	_, _, err := resolveOptions(o)
	return err
}

// Defaults of the socket layer, shared by validation on every platform.
const (
	defaultTTL        = 255
	defaultReadBuffer = 1 << 20
)

func resolveOptions(opts Options) (ttl, rbuf int, err error) {
	ttl, rbuf = opts.TTL, opts.ReadBuffer
	if ttl == 0 {
		ttl = defaultTTL
	}
	if rbuf == 0 {
		rbuf = defaultReadBuffer
	}
	switch {
	case ttl < 1 || ttl > 255:
		return 0, 0, fmt.Errorf("tstamp: TTL %d out of range 1-255", opts.TTL)
	case opts.TOS < 0 || opts.TOS > 255:
		return 0, 0, fmt.Errorf("tstamp: TOS %d out of range 0-255", opts.TOS)
	case rbuf < 0:
		return 0, 0, fmt.Errorf("tstamp: negative read buffer %d", opts.ReadBuffer)
	case opts.Mode > ModeHardware:
		return 0, 0, fmt.Errorf("tstamp: unknown mode %v", opts.Mode)
	}
	return ttl, rbuf, nil
}
