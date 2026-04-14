package alertmanager

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

func newHTTPClient(ep resolvedEndpoint) (*http.Client, error) {
	if ep.client == nil {
		return &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
				IdleConnTimeout:   30 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		}, nil
	}
	return ep.client, nil
}

func fanoutAlert(loader *EndpointLoader, logger log.Logger, registry *prometheus.Registry) http.Handler {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "http_alert_requests_total",
		Help:        "Counter of alert HTTP requests.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-write"},
	}, []string{"method"})

	fanoutRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "alert_fanout_requests_total",
		Help:        "Counter of fanout requests.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-remotewrite"},
	}, []string{"code", "name"})

	registry.MustRegister(requests, fanoutRequests)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.With(prometheus.Labels{"method": r.Method}).Inc()

		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		rlogger := log.With(logger, "request", middleware.GetReqID(r.Context()))

		endpoints := loader.GetEndpoints()
		level.Info(rlogger).Log("msg", "fanout alert called", "method", r.Method, "path", r.URL.Path, "endpoint_count", len(endpoints), "body_size", len(body))
		if len(endpoints) == 0 {
			level.Warn(rlogger).Log("msg", "no alertmanager endpoints loaded, returning 400")
			http.Error(w, "alertmanager endpoints not loaded", http.StatusBadRequest)
			return
		}
		var (
			wg         sync.WaitGroup
			successCnt atomic.Int32
		)
		for _, ep := range endpoints {
			wg.Add(1)
			go func() {
				defer wg.Done()
				level.Info(rlogger).Log("msg", "dispatching alert to endpoint", "endpoint", ep.Name, "url", ep.URL)
				client, err := newHTTPClient(ep)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to create HTTP client", "err", err, "endpoint", ep.Name)
					return
				}
				targetUrl, err := url.JoinPath(ep.URL, r.URL.Path)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to get url", "err", err, "url", targetUrl)
					return
				}
				req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetUrl, bytes.NewBuffer(body))
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to create forward request", "err", err, "url", ep.URL)
					return
				}
				req.Header = r.Header.Clone()

				resp, err := client.Do(req)
				if err != nil {
					fanoutRequests.With(prometheus.Labels{"code": "<error>", "name": ep.Name}).Inc()
					level.Error(rlogger).Log("msg", "failed to send request", "err", err, "url", ep.URL)
					return
				}
				defer resp.Body.Close()
				// what granularity do we need for these?
				fanoutRequests.With(prometheus.Labels{"code": strconv.Itoa(resp.StatusCode), "name": ep.Name}).Inc()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					respBody, _ := io.ReadAll(resp.Body)
					level.Error(rlogger).Log("msg", "failed to forward alert", "code", resp.Status, "response", string(respBody), "url", ep.URL)
				} else {
					successCnt.Add(1)
					level.Debug(rlogger).Log("msg", "alert forwarded successfully", "url", ep.URL)
				}
			}()
		}

		wg.Wait()

		total := int32(len(endpoints))
		succeeded := successCnt.Load()
		failed := total - succeeded

		if succeeded == 0 {
			level.Error(rlogger).Log("msg", "fanout complete, all endpoints failed", "total", total)
			http.Error(w, "all alertmanager endpoints failed", http.StatusBadGateway)
			return
		}
		level.Info(rlogger).Log("msg", "fanout complete", "total", total, "succeeded", succeeded, "failed", failed)
		w.WriteHeader(http.StatusOK)

	})
}
