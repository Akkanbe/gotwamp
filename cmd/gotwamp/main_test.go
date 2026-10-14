package main

import (
	"os"
	"testing"
)

// TestMainExit runs main with the linked values and gets its exit status.
func TestMainExit(t *testing.T) {
	defer func(args []string, v, c string) { os.Args, version, commit, exit = args, v, c, os.Exit }(os.Args, version, commit)
	os.Args, version, commit = []string{"gotwamp", "version"}, "v1.2.3", "abc1234"
	code := -1
	exit = func(c int) { code = c }
	main()
	if code != 0 {
		t.Errorf("exit %d", code)
	}
}
