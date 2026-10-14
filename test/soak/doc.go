// Package soak holds the long-running test of the monitoring path (PLAN §6
// M5, SPEC §12.1, §12.2): the scheduler, the sender, the result store and
// both exporters run N sessions at R packets per second against a TWAMP
// Light reflector on loopback, all in one process, while goroutines, heap,
// RSS, file descriptors and CPU are sampled. The test fails when goroutines
// or the live heap keep growing once the result store is full, or when
// anything is left behind after shutdown.
//
// It is behind the "soak" build tag:
//
//	make soak                                    # 20 sessions × 10 pps × 60 s
//	make soak SOAK_SESSIONS=100 SOAK_DURATION=24h
//
// -soak.store sqlite keeps the results in SQLite instead of memory (SOAK_ARGS=-soak.store=sqlite).
package soak
