// Package perfsonar holds the interoperability tests between gotwamp and
// perfSONAR's twampd and twping (PLAN §8, cases I-01 to I-04).
//
// The tests need Docker and the perfSONAR testpoint container started by
// compose.yaml, so they are behind the "interop" build tag:
//
//	make interop
package perfsonar
