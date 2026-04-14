package alertmanager

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	yamlv2 "gopkg.in/yaml.v2"
)

type resolvedEndpoint struct {
	Endpoint
	client *http.Client
}

type Endpoint struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// +optional
	//ClientConfig *promconfig.HTTPClientConfig `yaml:"http_client_config,omitempty"`
}

type EndpointLoader struct {
	mu        sync.RWMutex
	path      string
	endpoints []resolvedEndpoint
}

func NewEndpointLoader(path string) (*EndpointLoader, error) {
	l := &EndpointLoader{
		path: path,
	}
	if err := l.reload(); err != nil {
		return nil, fmt.Errorf("initial load of %s: %w", path, err)
	}
	return l, nil
}

func (l *EndpointLoader) reload() error {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}

	var eps []resolvedEndpoint
	if err := yamlv2.Unmarshal(data, &eps); err != nil {
		return fmt.Errorf("parsing %s: %w", l.path, err)
	}

	resolved := make([]resolvedEndpoint, len(eps))
	for i, ep := range eps {
		client, err := newHTTPClient(ep)
		if err != nil {
			return fmt.Errorf("building client for endpoint %q: %w", ep.Name, err)
		}
		resolved[i] = resolvedEndpoint{ep.Endpoint, client}
	}

	l.mu.Lock()
	old := l.endpoints
	l.endpoints = resolved
	l.mu.Unlock()
	for _, ep := range old {
		ep.client.CloseIdleConnections()
	}

	log.Printf("loaded alertmanager endpoints, count=%d", len(eps))
	return nil
}

// GetEndpoints returns the current snapshot of endpoints.
func (l *EndpointLoader) GetEndpoints() []resolvedEndpoint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]resolvedEndpoint, len(l.endpoints))
	copy(out, l.endpoints)
	return out
}

// Just watch for changes in the endpoints.yaml, reload the endpoints the the loader if change
func (l *EndpointLoader) Run(ctx context.Context, fallbackInterval time.Duration) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// TODO lof error here
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	parentDir := filepath.Dir(l.path)
	if err := watcher.Add(parentDir); err != nil {
		// TODO log error here
		return fmt.Errorf("watching %s: %w", parentDir, err)
	}
	// watch for create events on parent directory for symlink

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if filepath.Base(event.Name) == filepath.Base(l.path) {
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) != 0 {

					if err := l.reload(); err != nil {
						log.Printf("Error reloading endpoints: %v\n", err)
						continue
					}

				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Println("error:", err)
		}
	}

}
