package alertmanager

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeBackend struct {
	server       *httptest.Server
	requestCount atomic.Int64
	lastBody     []byte
	lastPath     string
	mu           sync.Mutex
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	fb := &fakeBackend{}
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

func (fb *fakeBackend) url() string {
	return fb.server.URL
}

func (fb *fakeBackend) count() int64 {
	return fb.requestCount.Load()
}

func (fb *fakeBackend) body() []byte {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	out := make([]byte, len(fb.lastBody))
	copy(out, fb.lastBody)
	return out
}

func (fb *fakeBackend) path() string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.lastPath
}

func TestFanoutAlert_SendsToAllEndpoints(t *testing.T) {
	backends := make([]*fakeBackend, 3)
	endpoints := make([]Endpoint, 3)
	for i := range backends {
		backends[i] = newFakeBackend(t)
		endpoints[i] = Endpoint{URL: backends[i].url(), client: backends[i].server.Client()}
	}

	loader := &EndpointLoader{
		endpoints: endpoints,
	}

	handler := fanoutAlert(loader, log.NewNopLogger(), prometheus.NewRegistry())

	alertBody := []byte(`[{"labels":{"alertname":"TestAlert"}}]`)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(alertBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		allReceived := true
		for _, b := range backends {
			if b.count() < 1 {
				allReceived = false
				break
			}
		}
		if allReceived {
			break
		}
		select {
		case <-deadline:
			for i, b := range backends {
				if b.count() == 0 {
					t.Errorf("backend %d never received a request", i)
				}
			}
			t.FailNow()
		case <-tick.C:
		}
	}

	for i, b := range backends {
		if got := b.body(); !bytes.Equal(got, alertBody) {
			t.Errorf("backend %d: body = %q, want %q", i, got, alertBody)
		}
		if got := b.path(); got != "/api/v2/alerts" {
			t.Errorf("backend %d: path = %q, want %q", i, got, "/api/v2/alerts")
		}
	}
}

func TestFanoutAlert_AppendsRequestPath(t *testing.T) {
	backend := newFakeBackend(t)
	loader := &EndpointLoader{
		endpoints: []Endpoint{
			{URL: backend.url(), client: backend.server.Client()},
		},
	}

	handler := fanoutAlert(loader, log.NewNopLogger(), prometheus.NewRegistry())

	for _, path := range []string{"/api/v1/alerts", "/api/v2/alerts"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`[]`)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		waitForBackends(t, []*fakeBackend{backend}, backend.count())

		if got := backend.path(); got != path {
			t.Errorf("request to %s: backend received path %q, want %q", path, got, path)
		}
	}
}

func TestFanoutAlert_EmptyEndpoints(t *testing.T) {
	loader := &EndpointLoader{}

	handler := fanoutAlert(loader, log.NewNopLogger(), prometheus.NewRegistry())

	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader([]byte(`[]`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty endpoints: got status %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestFanoutAlert_ReloadEndpoints(t *testing.T) {
	oldBackends := make([]*fakeBackend, 2)
	oldEndpoints := make([]Endpoint, 2)
	for i := range oldBackends {
		oldBackends[i] = newFakeBackend(t)
		oldEndpoints[i] = Endpoint{URL: oldBackends[i].url(), client: oldBackends[i].server.Client()}
	}

	loader := &EndpointLoader{
		endpoints: oldEndpoints,
	}

	handler := fanoutAlert(loader, log.NewNopLogger(), prometheus.NewRegistry())

	firstBody := []byte(`[{"labels":{"alertname":"BeforeReload"}}]`)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(firstBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	waitForBackends(t, oldBackends, 1)

	newBackends := make([]*fakeBackend, 3)
	newEndpoints := make([]Endpoint, 3)
	for i := range newBackends {
		newBackends[i] = newFakeBackend(t)
		newEndpoints[i] = Endpoint{URL: newBackends[i].url(), client: newBackends[i].server.Client()}
	}

	loader.mu.Lock()
	loader.endpoints = newEndpoints
	loader.mu.Unlock()

	secondBody := []byte(`[{"labels":{"alertname":"AfterReload"}}]`)
	req = httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(secondBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	waitForBackends(t, newBackends, 1)

	for i, b := range newBackends {
		if got := b.body(); !bytes.Equal(got, secondBody) {
			t.Errorf("new backend %d: body = %q, want %q", i, got, secondBody)
		}
	}

	for i, b := range oldBackends {
		if b.count() != 1 {
			t.Errorf("old backend %d: received %d requests after reload, want 0 additional (1 total)",
				i, b.count())
		}
	}
}

func waitForBackends(t *testing.T, backends []*fakeBackend, wantCount int64) {
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
