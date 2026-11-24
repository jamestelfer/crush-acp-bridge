//go:build !windows && !linux && !darwin && !freebsd

package crush

import (
	"fmt"
	"os"
	"syscall"
)

// peerUID returns the owner of the socket file at path. Elsewhere than
// Linux, macOS and FreeBSD the owner stands in for peer credentials: in a
// sticky directory such as /tmp only that user can replace the file.
func peerUID(_ syscall.RawConn, path string) (int, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported file info %T", fi.Sys())
	}
	return int(st.Uid), nil
}
