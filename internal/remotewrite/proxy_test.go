package remotewrite

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/observatorium/observatorium/internal"
	"github.com/prometheus/client_golang/prometheus"
)

func TestProxy(t *testing.T) {
	logger := internal.NewLogger("debug", "logfmt", "test")
	reg := prometheus.NewRegistry()
	client := http.DefaultClient

	type parsedLog struct {
		Message  string
		Endpoint string
		Code     int
	}

	testCases := []struct {
		name             string
		mainReturnCode   int
		mirrorReturnCode int
		expectLogLength  int
		expectLogs       map[string]parsedLog
	}{
		{
			name:             "test",
			mainReturnCode:   http.StatusForbidden,
			mirrorReturnCode: http.StatusForbidden,
			expectLogLength:  2,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			remoteWriteMain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				logger.Log("msg", "remote write main")
				w.WriteHeader(tc.mainReturnCode)
			}))
			defer remoteWriteMain.Close()

			// remoteWriteMirror is a secondary remote write endpoint that always returns 403 Forbidden.
			remoteWriteMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				logger.Log("msg", "remote write mirror")
				w.WriteHeader(tc.mirrorReturnCode)
			}))
			defer remoteWriteMirror.Close()

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

			var expectKeyVals []logMessage
			var wg sync.WaitGroup
			lm := newLogManager(logger, endpoints, func(logger log.Logger, messages map[string]chan logMessage) {
				for _, v := range messages {
					wg.Add(1)
					messageStream := v
					go func() {
						for {
							select {
							case message := <-messageStream:
								expectKeyVals = append(expectKeyVals, message)
								wg.Done()
							}
						}
					}()
				}
			})
			rd := &RequestDuplicator{
				logManager: lm,
			}
			gateway := httptest.NewServer(rd.Proxy(writeURL, endpoints, logger, reg))
			defer gateway.Close()

			req, err := http.NewRequest(http.MethodPost, gateway.URL, bytes.NewBufferString("some metrics here :)"))
			if err != nil {
				t.Fatal(err)
			}

			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()

			if res.StatusCode != tc.mainReturnCode {
				t.Fatalf("expected status code %d, got %d", tc.mainReturnCode, res.StatusCode)
			}

			wg.Wait()
			if expectKeyVals == nil || len(expectKeyVals) != 2 {
				t.Fatalf("expected 2 log messages, got %d", len(expectKeyVals))
			}
			for _, log := range expectKeyVals {
				fmt.Println(log)
			}
		})
	}
}
