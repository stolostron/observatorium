package alertmanager

import (
	"net/http"
	"net/url"
	"testing"
)

func parseUrl(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestNewEndpointLoader(t *testing.T) {
	urls := []*url.URL{
		parseUrl(t, "http://alertmanager-0:9093"),
		parseUrl(t, "http://alertmanager-1:9093"),
	}
	loader := NewEndpointLoader(urls)
	eps := loader.GetEndpoints()
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(eps))
	}
	for i, ep := range eps {
		if ep.URL == "" {
			t.Errorf("endpoint[%d]: URL is empty", i)
		}
		if ep.client == nil {
			t.Errorf("endpoint[%d]: client is nil", i)
		}
	}
}

// reload must rebuild HTTP clients (TLS/CA + transports) while preserving URL order and values.
func TestEndpointLoader_ReloadRefreshesHTTPClients(t *testing.T) {
	urls := []*url.URL{
		parseUrl(t, "http://am-a:9093"),
		parseUrl(t, "http://am-b:9093"),
	}
	loader := NewEndpointLoader(urls)

	before := loader.GetEndpoints()
	if len(before) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(before))
	}
	oldClients := make([]*http.Client, len(before))
	for i, ep := range before {
		oldClients[i] = ep.client
	}

	if err := loader.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	after := loader.GetEndpoints()
	if len(after) != 2 {
		t.Fatalf("got %d endpoints after reload, want 2", len(after))
	}
	for i, ep := range after {
		if ep.URL != before[i].URL {
			t.Errorf("endpoint[%d]: URL = %q, want %q", i, ep.URL, before[i].URL)
		}
		if ep.client == nil {
			t.Errorf("endpoint[%d]: client is nil after reload", i)
		}
		if ep.client == oldClients[i] {
			t.Errorf("endpoint[%d]: expected new *http.Client after reload", i)
		}
	}
}

func TestEndpointLoader_Reload_EmptyList(t *testing.T) {
	loader := &EndpointLoader{}
	if err := loader.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loader.GetEndpoints()) != 0 {
		t.Fatalf("want empty endpoints")
	}
}
