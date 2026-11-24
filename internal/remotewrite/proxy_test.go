package remotewrite

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kit/kit/log"
	promconfig "github.com/prometheus/common/config"
)

func TestRemoteWriteSkipsEndpointOnBadClientConfig(t *testing.T) {
	logger := log.NewNopLogger()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	endpoints := []Endpoint{
		{
			Name: "bad-endpoint",
			URL:  backend.URL,
			ClientConfig: &promconfig.HTTPClientConfig{
				TLSConfig: promconfig.TLSConfig{
					CAFile: "/nonexistent/ca.crt",
				},
			},
		},
		{
			Name: "good-endpoint",
			URL:  backend.URL,
		},
	}

	InitChannels(logger, len(endpoints))

	handler := remoteWrite(nil, endpoints, logger)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/receive", strings.NewReader("test-body"))
	rec := httptest.NewRecorder()

	// Must not panic — previously this caused a nil pointer dereference
	// when NewClientFromConfig failed and client.Do(req) was called on nil.
	handler.ServeHTTP(rec, req)
}
