package fanout

import (
	"net/url"
	"sync"
)

type Endpoint struct {
	URL     string `yaml:"url"`
	primary bool
}

type EndpointLoader struct {
	mu        sync.RWMutex
	endpoints []Endpoint
}

func NewEndpointLoader(urls []*url.URL) *EndpointLoader {
	endpoints := make([]Endpoint, len(urls))
	for i, u := range urls {
		endpoints[i] = Endpoint{
			URL:     u.String(),
			primary: i == 0, // naive choose first
		}
	}
	return &EndpointLoader{
		endpoints: endpoints,
	}
}

func (l *EndpointLoader) GetEndpoints() []Endpoint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Endpoint, len(l.endpoints))
	copy(out, l.endpoints)
	return out
}
