//go:build !darwin

package main

import "os"

// exitNoAtexit 非 darwin 平台直接 os.Exit（无 CoreSpotlight 问题）。
func exitNoAtexit(code int) {
	os.Exit(code)
}
