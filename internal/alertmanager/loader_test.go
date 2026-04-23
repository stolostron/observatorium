package alertmanager

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func writeEndpointsFile(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "endpoints.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEndpointLoader_InitialLoad(t *testing.T) {
	dir := t.TempDir()
	path := writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
- name: am-2
  url: http://am-2:9093
`)

	loader, err := NewEndpointLoader(path)
	if err != nil {
		t.Fatalf("NewEndpointLoader: %v", err)
	}

	eps := loader.GetEndpoints()
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(eps))
	}
	for i, ep := range eps {
		if ep.URL == "" {
			t.Errorf("endpoint[%d] %q: URL is empty", i, ep.Name)
		}
		if ep.client == nil {
			t.Errorf("endpoint[%d] %q: client is nil", i, ep.Name)
		}
	}
}

func TestEndpointLoader_ReloadReusesClients(t *testing.T) {
	dir := t.TempDir()
	path := writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
- name: am-2
  url: http://am-2:9093
`)

	loader, err := NewEndpointLoader(path)
	if err != nil {
		t.Fatalf("NewEndpointLoader: %v", err)
	}

	before := loader.GetEndpoints()
	clientsBefore := map[string]*http.Client{}
	for _, ep := range before {
		clientsBefore[ep.URL] = ep.client
	}

	writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
- name: am-2
  url: http://am-2:9093
`)

	if err := loader.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	after := loader.GetEndpoints()
	if len(after) != 2 {
		t.Fatalf("got %d endpoints after reload, want 2", len(after))
	}
	for _, ep := range after {
		prev, ok := clientsBefore[ep.URL]
		if !ok {
			t.Errorf("unexpected endpoint URL %q after reload", ep.URL)
			continue
		}
		if ep.client != prev {
			t.Errorf("endpoint %q: client was recreated, want reuse", ep.URL)
		}
	}
}

func TestEndpointLoader_ReloadCreatesNewClients(t *testing.T) {
	dir := t.TempDir()
	path := writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
`)

	loader, err := NewEndpointLoader(path)
	if err != nil {
		t.Fatalf("NewEndpointLoader: %v", err)
	}

	before := loader.GetEndpoints()
	if len(before) != 1 {
		t.Fatalf("got %d endpoints, want 1", len(before))
	}
	oldClient := before[0].client

	writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
- name: am-new
  url: http://am-new:9093
`)

	if err := loader.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	after := loader.GetEndpoints()
	if len(after) != 2 {
		t.Fatalf("got %d endpoints after reload, want 2", len(after))
	}

	for _, ep := range after {
		if ep.URL == "http://am-1:9093" && ep.client != oldClient {
			t.Error("existing endpoint am-1: client was recreated, want reuse")
		}
		if ep.URL == "http://am-new:9093" && ep.client == nil {
			t.Error("new endpoint am-new: client is nil")
		}
	}
}

func TestEndpointLoader_ReloadRemovesStaleEndpoints(t *testing.T) {
	dir := t.TempDir()
	path := writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
- name: am-2
  url: http://am-2:9093
`)

	loader, err := NewEndpointLoader(path)
	if err != nil {
		t.Fatalf("NewEndpointLoader: %v", err)
	}

	writeEndpointsFile(t, dir, `
- name: am-1
  url: http://am-1:9093
`)

	if err := loader.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	after := loader.GetEndpoints()
	if len(after) != 1 {
		t.Fatalf("got %d endpoints after reload, want 1", len(after))
	}
	if after[0].URL != "http://am-1:9093" {
		t.Errorf("remaining endpoint URL = %q, want %q", after[0].URL, "http://am-1:9093")
	}
}

func TestEndpointLoader_EmptyURLLogged(t *testing.T) {
	dir := t.TempDir()
	path := writeEndpointsFile(t, dir, `
- name: am-broken
  url: ""
- name: am-good
  url: http://am-good:9093
`)

	loader, err := NewEndpointLoader(path)
	if err != nil {
		t.Fatalf("NewEndpointLoader: %v", err)
	}

	eps := loader.GetEndpoints()
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(eps))
	}
	if eps[0].URL != "" {
		t.Errorf("endpoint[0] URL = %q, want empty", eps[0].URL)
	}
	if eps[1].URL != "http://am-good:9093" {
		t.Errorf("endpoint[1] URL = %q, want %q", eps[1].URL, "http://am-good:9093")
	}
}
