//go:build protected

package main

import "testing"

func TestProtectedBuildRejectsRawRootOverride(t *testing.T) {
	if _, err := resolveProjectRoot(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("protected build accepted -root and bypassed the signed encrypted plugin bundle")
	}
}
