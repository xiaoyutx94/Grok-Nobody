//go:build !darwin

package main

// notifyAlreadyRunning 非 darwin 平台无提示。
func notifyAlreadyRunning() {}
