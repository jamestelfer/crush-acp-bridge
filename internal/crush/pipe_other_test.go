//go:build !windows

package crush_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

func TestNamedPipeAddressesNeedWindows(t *testing.T) {
	err := crush.New("npipe:////./pipe/crush-S-1-5-21.sock").Ping(t.Context())

	require.ErrorContains(t, err, "named pipe")
	require.ErrorContains(t, err, "Windows")
}
