//go:build windows

package crush

import "net"

// verifyUnixPeer accepts any server. On Windows the shared server listens on
// a named pipe, which dialPipe verifies; a unix:// URL is used only when
// named explicitly, and like an HTTP URL it is not verified. Crush's own
// client does the same (Crush 0.102.0, internal/client/dial_windows.go).
func verifyUnixPeer(net.Conn, string) error { return nil }
