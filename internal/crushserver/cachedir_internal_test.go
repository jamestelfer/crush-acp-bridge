package crushserver

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCacheDirMirrorsCrush(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{
			name: "CRUSH_CACHE_DIR wins",
			goos: "linux",
			env:  map[string]string{"CRUSH_CACHE_DIR": "/c", "XDG_CACHE_HOME": "/x", "HOME": "/h"},
			want: "/c",
		},
		{
			name: "XDG_CACHE_HOME",
			goos: "windows",
			env:  map[string]string{"XDG_CACHE_HOME": "/x", "LOCALAPPDATA": "/l"},
			want: filepath.Join("/x", "crush"),
		},
		{
			name: "LOCALAPPDATA on Windows",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": "/l", "USERPROFILE": "/u"},
			want: filepath.Join("/l", "crush", "cache"),
		},
		{
			name: "USERPROFILE on Windows",
			goos: "windows",
			env:  map[string]string{"USERPROFILE": "/u"},
			want: filepath.Join("/u", "AppData", "Local", "crush", "cache"),
		},
		{
			name: "HOME elsewhere",
			goos: "darwin",
			env:  map[string]string{"HOME": "/h", "LOCALAPPDATA": "/l"},
			want: filepath.Join("/h", ".cache", "crush"),
		},
		{
			name: "no home",
			goos: "linux",
			env:  map[string]string{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cacheDir(tt.goos, func(k string) string { return tt.env[k] })

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestServerCacheDirUsesCrushsSafeHostName(t *testing.T) {
	got := serverCacheDir("/c", "127.0.0.1:41233")

	assert.Equal(t, filepath.Join("/c", "server-tcp___127.0.0.1_41233"), got)
}

func TestEnvLookupTakesTheLastEntry(t *testing.T) {
	get := envLookup([]string{"A=1", "B=2", "A=3", "C"})

	assert.Equal(t, "3", get("A"))
	assert.Equal(t, "2", get("B"))
	assert.Empty(t, get("C"))
	assert.Empty(t, get("D"))
}
