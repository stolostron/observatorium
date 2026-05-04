package alertmanager

import (
	"net/http"
	"net/url"
	"sync"
)

type Endpoint struct {
	URL    string `yaml:"url"`
	client *http.Client
}

type EndpointLoader struct {
	mu        sync.RWMutex
	path      string
	endpoints []Endpoint
}

func NewEndpointLoader(urls []*url.URL) *EndpointLoader {
	caPool := loadCACertPool()
	resolved := make([]Endpoint, len(urls))
	for i, u := range urls {
		resolved[i] = Endpoint{
			URL:    u.String(),
			client: newHTTPClient(caPool),
		}
	}
	return &EndpointLoader{
		endpoints: resolved,
	}
}

// reload rebuilds HTTP clients for the current endpoint URLs while preserving order.
// Call this when the mounted service CA bundle may have rotated (Kubernetes Secret/config
// reload) or when you want to drop idle pooled connections to upstream Alertmanager.
// URLs themselves come from CLI/static setup at process start; reload does not change that list.
func (l *EndpointLoader) reload() error {
	caPool := loadCACertPool()

	l.mu.RLock()
	prev := make([]Endpoint, len(l.endpoints))
	copy(prev, l.endpoints)
	l.mu.RUnlock()

	resolved := make([]Endpoint, len(prev))
	for i, ep := range prev {
		if ep.client != nil {
			ep.client.CloseIdleConnections()
		}
		resolved[i] = Endpoint{
			URL:    ep.URL,
			client: newHTTPClient(caPool),
		}
	}

	l.mu.Lock()
	l.endpoints = resolved
	l.mu.Unlock()

	return nil
}

// GetEndpoints returns the current snapshot of endpoints.
func (l *EndpointLoader) GetEndpoints() []Endpoint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Endpoint, len(l.endpoints))
	copy(out, l.endpoints)
	return out
}
