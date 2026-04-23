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

	log.Printf("raw alertmanager endpoints config:\n%s", string(data))

	var eps []Endpoint
	if err := yamlv2.Unmarshal(data, &eps); err != nil {
		return fmt.Errorf("parsing %s: %w", l.path, err)
	}

	caPool := loadCACertPool()

	l.mu.RLock()
	existing := make(map[string]resolvedEndpoint, len(l.endpoints))
	for _, ep := range l.endpoints {
		existing[ep.URL] = ep
	}
	l.mu.RUnlock()

	resolved := make([]resolvedEndpoint, len(eps))
	var stale []resolvedEndpoint
	for i, ep := range eps {
		log.Printf("endpoint[%d]: name=%q url=%q", i, ep.Name, ep.URL)
		if prev, ok := existing[ep.URL]; ok {
			log.Printf("endpoint[%d]: reusing existing client for %q", i, ep.URL)
			resolved[i] = resolvedEndpoint{ep, prev.client}
			delete(existing, ep.URL)
			continue
		}
		log.Printf("endpoint[%d]: creating new client for %q", i, ep.URL)
		resolved[i] = resolvedEndpoint{ep, newHTTPClient(caPool)}
	}

	for _, ep := range existing {
		stale = append(stale, ep)
	}

	l.mu.Lock()
	l.endpoints = resolved
	l.mu.Unlock()

	for _, ep := range stale {
		log.Printf("closing idle connections for removed endpoint %q", ep.URL)
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

func (l *EndpointLoader) Run(ctx context.Context, fallbackInterval time.Duration) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	endpointsDir := filepath.Dir(l.path)
	log.Printf("alertmanager endpoint watcher: watching %s for changes to %s", endpointsDir, filepath.Base(l.path))
	if err := watcher.Add(endpointsDir); err != nil {
		return fmt.Errorf("watching %s: %w", endpointsDir, err)
	}

	caDir := filepath.Dir(alertmanagerCAPath)
	log.Printf("alertmanager endpoint watcher: watching %s for CA certificate changes", caDir)
	if err := watcher.Add(caDir); err != nil {
		log.Printf("alertmanager endpoint watcher: unable to watch CA directory %s (will rely on endpoint reloads): %v", caDir, err)
	}

	shouldReload := func(event fsnotify.Event) bool {
		log.Println("alertmanager shouldReload logic")
		if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) == 0 {
			log.Printf("Evaluated to false for event %s", event.Op.String())
			return false
		}
		base := filepath.Base(event.Name)
		matchesPath := base == filepath.Base(l.path)
		matchesCA := base == filepath.Base(alertmanagerCAPath)
		log.Printf("shouldReload: base=%q, l.path=%q, caPath=%q, matchesPath=%v, matchesCA=%v",
			base, filepath.Base(l.path), filepath.Base(alertmanagerCAPath), matchesPath, matchesCA)
		return matchesPath || matchesCA
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("alertmanager endpoint watcher: context cancelled, shutting down")
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				log.Println("alertmanager endpoint watcher: events channel closed, exiting")
				return nil
			}
			log.Printf("alertmanager endpoint watcher: received event %s on %s", event.Op, event.Name)
			if shouldReload(event) {
				log.Printf("alertmanager endpoint watcher: reloading due to %s event on %s", event.Op, event.Name)
				if err := l.reload(); err != nil {
					log.Printf("alertmanager endpoint watcher: error reloading: %v", err)
					continue
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				log.Println("alertmanager endpoint watcher: errors channel closed, exiting")
				return nil
			}
			log.Printf("alertmanager endpoint watcher: fsnotify error: %v", err)
		}
	}
}
