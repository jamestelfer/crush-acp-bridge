//go:build !windows

package crush

import "testing"

// PretendOtherUser makes the client expect a server run by a uid other than
// the current one, as if the test's own sockets belonged to another user.
func PretendOtherUser(t *testing.T) {
	t.Helper()
	real := geteuid
	geteuid = func() int { return real() + 1 }
	t.Cleanup(func() { geteuid = real })
}
