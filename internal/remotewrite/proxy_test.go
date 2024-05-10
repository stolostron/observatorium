package remotewrite

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/observatorium/observatorium/internal"
	"github.com/prometheus/client_golang/prometheus"
)

func TestProxy(t *testing.T) {
	logger := internal.NewLogger("debug", "logfmt", "test")

	// remoteWriteMain is the primary remote write endpoint that always returns 403 Forbidden.
	remoteWriteMain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Log("msg", "remote write main")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer remoteWriteMain.Close()

	// remoteWriteMirror is a secondary remote write endpoint that always returns 403 Forbidden.
	remoteWriteMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Log("msg", "remote write mirror")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer remoteWriteMirror.Close()

	reg := prometheus.NewRegistry()

	writeURL, err := url.Parse(remoteWriteMain.URL)
	if err != nil {
		t.Fatal(err)
	}

	endpoints := []Endpoint{
		{
			Name: "mirror",
			URL:  remoteWriteMirror.URL,
		},
	}
	gateway := httptest.NewServer(Proxy(writeURL, endpoints, logger, reg))
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodPost, gateway.URL, bytes.NewBufferString("some metrics here :)"))
	if err != nil {
		t.Fatal(err)
	}

	client := http.DefaultClient

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status code %d, got %d", http.StatusForbidden, res.StatusCode)
	}
}
