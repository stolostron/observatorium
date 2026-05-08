package fanout

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/middleware"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

type FanoutMetrics struct {
	requests       *prometheus.CounterVec
	fanoutRequests *prometheus.CounterVec
}

func NewFanoutMetrics(reg prometheus.Registerer) FanoutMetrics {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "http_fanout_requests_total",
		Help:        "Counter of HTTP requests to fanout.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-write"},
	}, []string{"method"})
	fanoutRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "fanout_requests_total",
		Help:        "Counter HTTP requests from fanout.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-write"},
	}, []string{"code", "url"})
	reg.MustRegister(requests, fanoutRequests)
	return FanoutMetrics{requests: requests, fanoutRequests: fanoutRequests}
}

func FanoutRequestToEndpoints(loader *EndpointLoader, logger log.Logger, client http.Client, m FanoutMetrics) http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests.With(prometheus.Labels{"method": r.Method}).Inc()
		rlogger := log.With(logger, "request", middleware.GetReqID(r.Context()))

		body, err := io.ReadAll(r.Body)
		if err != nil {
			level.Error(rlogger).Log("msg", "failed to read request body")
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		endpoints := loader.GetEndpoints()
		if len(endpoints) == 0 {
			http.Error(w, "fanout endpoints not loaded", http.StatusBadRequest)
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
				targetUrl, err := url.JoinPath(ep.URL, r.URL.Path)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to get url", "err", err, "url", targetUrl)
					return
				}
				req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetUrl, bytes.NewBuffer(body))
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to create fanout request", "err", err, "url", ep.URL)
					return
				}
				req.Header = r.Header.Clone()

				resp, err := client.Do(req)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to send request", "err", err, "url", ep.URL)
					m.fanoutRequests.With(prometheus.Labels{"code": "<error>", "url": ep.URL}).Inc()
					return
				}

				defer resp.Body.Close()
				m.fanoutRequests.With(prometheus.Labels{"code": strconv.Itoa(resp.StatusCode), "url": ep.URL}).Inc()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					respBody, _ := io.ReadAll(resp.Body)
					level.Error(rlogger).Log("msg", "fanout target returned error", "code", resp.Status, "response", string(respBody), "url", ep.URL)
				} else {
					successCnt.Add(1)
				}

				//  only respond with primary endpoint. Others will not return response
				if ep.primary && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
					http.Error(w, "Failed to reach fanout target endpoints", resp.StatusCode)
				} else if ep.primary {
					w.WriteHeader(http.StatusOK)
				}

			}()
		}

		wg.Wait()

	})
}
