// Package component holds component tests that run gotwamp parts against each
// other across network namespaces joined by a veth pair, with tc netem
// injecting delay, loss, duplication and reordering (PLAN §7).
//
// The tests need root (CAP_NET_ADMIN and CAP_SYS_ADMIN), ip(8) and tc(8), so
// they are behind the "integration" build tag and skip themselves otherwise:
//
//	make test-component    # go test -tags integration -exec sudo ./test/component/...
//
// Tests that need neither root nor namespaces, such as the TWAMP Full run on
// the loopback interfaces, carry no build tag and run with go test ./....
package component
