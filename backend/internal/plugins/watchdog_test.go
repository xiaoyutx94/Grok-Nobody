package plugins

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFirstInt(t *testing.T) {
	m := map[string]any{"solved": float64(42), "busy": "7", "active": float64(3)}
	require.Equal(t, int64(42), firstInt(m, "solved", "total_solved"))
	require.Equal(t, int64(7), firstInt(m, "browser_busy", "busy"))
	require.Equal(t, int64(3), firstInt(m, "active"))
	require.Equal(t, int64(0), firstInt(m, "missing_key"))
}

func TestProbeEngineHealthEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","active":4,"workers_busy":4,"solved":123,"total_solved":456}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	snap := probeEngineHealth(port)
	require.True(t, snap.Seen)
	require.Equal(t, int64(123), snap.Solved, "solved 应优先于 total_solved")
	require.Equal(t, 4, snap.Active)
	require.Equal(t, 4, snap.Busy, "workers_busy 应被识别")
}

func TestProbeEngineHealthUnreachable(t *testing.T) {
	// 无人监听的端口：Seen=false，不触发重启
	snap := probeEngineHealth(1)
	require.False(t, snap.Seen)
}
