package alertmanager_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/observatorium/observatorium/internal/api/alertmanager"
)

type fakeAlertBackend struct {
	server       *httptest.Server
	requestCount atomic.Int64
	lastBody     []byte
	lastPath     string
	mu           sync.Mutex
}

func newFakeAlertBackend(t *testing.T) *fakeAlertBackend {
	t.Helper()
	fb := &fakeAlertBackend{}
	fb.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("fake backend: reading body: %v", err)
		}
		defer r.Body.Close()
		fb.mu.Lock()
		fb.lastBody = body
		fb.lastPath = r.URL.Path
		fb.mu.Unlock()
		fb.requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fb.server.Close)
	return fb
}

func (fb *fakeAlertBackend) url() string {
	return fb.server.URL
}

func (fb *fakeAlertBackend) count() int64 {
	return fb.requestCount.Load()
}

func (fb *fakeAlertBackend) body() []byte {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	out := make([]byte, len(fb.lastBody))
	copy(out, fb.lastBody)
	return out
}

func (fb *fakeAlertBackend) path() string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.lastPath
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u
}

func waitForAlertBackends(t *testing.T, backends []*fakeAlertBackend, wantCount int64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		allReady := true
		for _, b := range backends {
			if b.count() < wantCount {
				allReady = false
				break
			}
		}
		if allReady {
			return
		}
		select {
		case <-deadline:
			for i, b := range backends {
				if b.count() < wantCount {
					t.Errorf("backend %d: got %d requests, want at least %d", i, b.count(), wantCount)
				}
			}
			t.FailNow()
		case <-tick.C:
		}
	}
}

func TestNewHandler_FanoutAlertsSuccess(t *testing.T) {
	backend := newFakeAlertBackend(t)
	endpoints := []*url.URL{mustParseURL(t, backend.url())}

	h := alertmanager.NewHandler(endpoints, "", "")

	payload := []byte(`[{"labels":{"alertname":"Test"}}]`)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	waitForAlertBackends(t, []*fakeAlertBackend{backend}, 1)

	if rec.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := backend.path(); got != "/api/v2/alerts" {
		t.Errorf("backend path = %q, want %q", got, "/api/v2/alerts")
	}
	if got := backend.body(); !bytes.Equal(got, payload) {
		t.Errorf("backend body = %q, want %q", got, payload)
	}
}

func TestNewHandler_FanoutAlertsMultipleEndpoints(t *testing.T) {
	backends := []*fakeAlertBackend{newFakeAlertBackend(t), newFakeAlertBackend(t)}
	endpoints := []*url.URL{
		mustParseURL(t, backends[0].url()),
		mustParseURL(t, backends[1].url()),
	}

	h := alertmanager.NewHandler(endpoints, "", "")

	payload := []byte(`[]`)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	waitForAlertBackends(t, backends, 1)

	if rec.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusOK)
	}
	for i, b := range backends {
		if got := b.path(); got != "/api/v2/alerts" {
			t.Errorf("backend %d path = %q, want %q", i, got, "/api/v2/alerts")
		}
		if got := b.body(); !bytes.Equal(got, payload) {
			t.Errorf("backend %d body = %q, want %q", i, got, payload)
		}
	}
}
