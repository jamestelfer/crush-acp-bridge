package bridge_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
)

func TestMain(m *testing.M) {
	code := m.Run()

	dirty, err := snaps.Clean(m, snaps.CleanOpts{Sort: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "clean snapshots:", err)
		os.Exit(1)
	}
	if dirty {
		fmt.Fprintln(os.Stderr, "obsolete snapshots found; run `just snapshots`")
		os.Exit(1)
	}
	os.Exit(code)
}
