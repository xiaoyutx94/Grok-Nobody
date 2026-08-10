package plugins

import "github.com/umbraforge/desktop/internal/protectionconfig"

// protectedRelease is true only for a build paired with a signed encrypted
// payload. It deliberately disables Docker optimizations that persist or reuse
// plaintext code outside the authenticated bundle boundary.
func protectedRelease() bool {
	return protectionconfig.Enabled()
}
