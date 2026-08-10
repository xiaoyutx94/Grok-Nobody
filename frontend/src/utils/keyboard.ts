/**
 * Desktop shell keyboard helpers.
 *
 * Cut/Copy/Paste are handled by the native Cocoa Edit menu (menu_darwin.go).
 * Do NOT re-implement Cmd+V in JS — native paste + JS insert causes double text.
 */
export function installKeyboardShortcuts() {
  if ((window as any).__umbraforgeKeyboardInstalled) return
  ;(window as any).__umbraforgeKeyboardInstalled = true
}
