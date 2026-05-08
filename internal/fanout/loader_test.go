package fanout

import (
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
		parseUrl(t, "http://alertmanager-0:8080"),
		parseUrl(t, "http://alertmanager-1:8080"),
	}
	loader := NewEndpointLoader(urls)
	eps := loader.GetEndpoints()
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(eps))
	}
	wantURLs := []string{"http://alertmanager-0:8080", "http://alertmanager-1:8080"}
	for i, ep := range eps {
		if ep.URL != wantURLs[i] {
			t.Errorf("endpoint[%d]: URL = %q, want %q", i, ep.URL, wantURLs[i])
		}
		wantPrimary := i == 0
		if ep.primary != wantPrimary {
			t.Errorf("endpoint[%d]: primary = %v, want %v", i, ep.primary, wantPrimary)
		}
	}
}
