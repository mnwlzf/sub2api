package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// recordingIdleCloserTransport 记录 CloseIdleConnections 的调用次数。
// http.Client.CloseIdleConnections 会把它转发给实现了该方法的 Transport。
type recordingIdleCloserTransport struct {
	closeCalls atomic.Int64
}

func (r *recordingIdleCloserTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("recordingIdleCloserTransport: unused")
}

func (r *recordingIdleCloserTransport) CloseIdleConnections() {
	r.closeCalls.Add(1)
}

func (r *recordingIdleCloserTransport) calls() int64 {
	return r.closeCalls.Load()
}

func TestCloseUpstreamIdleConnections_OnlyMatchingProxyKey(t *testing.T) {
	svc := NewHTTPUpstream(&config.Config{}).(*httpUpstreamService)

	sharedByProxy := &recordingIdleCloserTransport{}
	accountScopedSameProxy := &recordingIdleCloserTransport{}
	otherProxy := &recordingIdleCloserTransport{}
	direct := &recordingIdleCloserTransport{}

	svc.mu.Lock()
	svc.clients["proxy:socks5h://warp-test:1080|proto:openai_h2"] = &upstreamClientEntry{
		client:   &http.Client{Transport: sharedByProxy},
		proxyKey: "socks5h://warp-test:1080",
	}
	svc.clients["account:31568|proxy:socks5h://warp-test:1080"] = &upstreamClientEntry{
		client:   &http.Client{Transport: accountScopedSameProxy},
		proxyKey: "socks5h://warp-test:1080",
	}
	svc.clients["proxy:socks5h://other:1080"] = &upstreamClientEntry{
		client:   &http.Client{Transport: otherProxy},
		proxyKey: "socks5h://other:1080",
	}
	svc.clients["proxy:direct"] = &upstreamClientEntry{
		client:   &http.Client{Transport: direct},
		proxyKey: directProxyKey,
	}
	svc.mu.Unlock()

	svc.CloseUpstreamIdleConnections("socks5h://warp-test:1080", 31568)

	require.Equal(t, int64(1), sharedByProxy.calls())
	require.Equal(t, int64(1), accountScopedSameProxy.calls())
	require.Zero(t, otherProxy.calls(), "clients bound to another proxy must not be touched")
	require.Zero(t, direct.calls(), "direct clients must not be touched")

	svc.mu.RLock()
	entries := len(svc.clients)
	svc.mu.RUnlock()
	require.Equal(t, 4, entries, "idle-conn cleanup must not evict cache entries or interrupt in-flight requests")

	// 幂等：重复调用只是再关一次空闲连接，不 panic。
	svc.CloseUpstreamIdleConnections("socks5h://warp-test:1080", 31568)
	require.Equal(t, int64(2), sharedByProxy.calls())
}

func TestCloseUpstreamIdleConnections_InvalidProxyURLIsNoop(t *testing.T) {
	svc := NewHTTPUpstream(&config.Config{}).(*httpUpstreamService)
	transport := &recordingIdleCloserTransport{}
	svc.mu.Lock()
	svc.clients["proxy:socks5h://warp-test:1080"] = &upstreamClientEntry{
		client:   &http.Client{Transport: transport},
		proxyKey: "socks5h://warp-test:1080",
	}
	svc.mu.Unlock()

	require.NotPanics(t, func() {
		svc.CloseUpstreamIdleConnections("://not a url", 1)
	})
	require.Zero(t, transport.calls())
}

func TestCloseUpstreamIdleConnections_NilServiceIsNoop(t *testing.T) {
	var svc *httpUpstreamService
	require.NotPanics(t, func() { svc.CloseUpstreamIdleConnections("socks5h://warp-test:1080", 1) })
}

// ---------------------------------------------------------------------------
// 轮换服务 HTTP 客户端
// ---------------------------------------------------------------------------

func warpRotateTestConfig(serviceURL string) *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.OpenCodeWarpRotate.ServiceURL = serviceURL
	cfg.Gateway.OpenCodeWarpRotate.Token = "test-token"
	cfg.Gateway.OpenCodeWarpRotate.TimeoutSeconds = 5
	return cfg
}

func TestNewOpenCodeWarpRotator_DisabledWithoutServiceURL(t *testing.T) {
	require.Nil(t, NewOpenCodeWarpRotator(nil))
	require.Nil(t, NewOpenCodeWarpRotator(&config.Config{}))
	cfg := &config.Config{}
	cfg.Gateway.OpenCodeWarpRotate.Enabled = true
	cfg.Gateway.OpenCodeWarpRotate.ServiceURL = "   "
	require.Nil(t, NewOpenCodeWarpRotator(cfg))
}

func TestOpenCodeWarpRotator_EnsureFreshExitSuccess(t *testing.T) {
	var (
		mu       sync.Mutex
		gotPath  string
		gotAuth  string
		gotBody  map[string]any
		gotMeth  string
		requests int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMeth = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"action":"rotated","usable":true,"exit_ip":"2a09:abc::1","attempts":2,"elapsed_seconds":1.2}`))
	}))
	defer server.Close()

	rotator := NewOpenCodeWarpRotator(warpRotateTestConfig(server.URL + "/"))
	require.NotNil(t, rotator)

	exitIP, err := rotator.EnsureFreshExit(context.Background(), 17, "socks5h://warp-test:1080")

	require.NoError(t, err)
	require.Equal(t, "2a09:abc::1", exitIP)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, requests)
	require.Equal(t, "/ensure-fresh", gotPath)
	require.Equal(t, http.MethodPost, gotMeth)
	require.Equal(t, "Bearer test-token", gotAuth)
	require.Equal(t, float64(17), gotBody["proxy_id"])
}

func TestOpenCodeWarpRotator_EnsureFreshExitUnusableIsError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "503 no usable exit",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"ok":false,"usable":false,"error":"no_usable_exit","exit_ip":"2a09:abc::1","attempts":6,"retry_after":49711}`,
		},
		{
			name:       "200 but not ok",
			statusCode: http.StatusOK,
			body:       `{"ok":false,"usable":false,"error":"probe_failed"}`,
		},
		{
			name:       "200 but not usable",
			statusCode: http.StatusOK,
			body:       `{"ok":true,"usable":false,"action":"rotated"}`,
		},
		{
			name:       "malformed json",
			statusCode: http.StatusOK,
			body:       `not-json`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			rotator := NewOpenCodeWarpRotator(warpRotateTestConfig(server.URL))
			_, err := rotator.EnsureFreshExit(context.Background(), 17, "")

			require.Error(t, err)
		})
	}
}

func TestOpenCodeWarpRotator_EnsureFreshExitRejectsInvalidProxyID(t *testing.T) {
	rotator := NewOpenCodeWarpRotator(warpRotateTestConfig("http://warp-rotate.invalid"))
	_, err := rotator.EnsureFreshExit(context.Background(), 0, "")
	require.Error(t, err)
}

func TestOpenCodeWarpRotator_EnsureFreshExitHonorsContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true,"usable":true,"exit_ip":"2a09:abc::1"}`))
	}))
	defer server.Close()

	rotator := NewOpenCodeWarpRotator(warpRotateTestConfig(server.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := rotator.EnsureFreshExit(ctx, 17, "")

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
