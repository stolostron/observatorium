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

func FanoutRequestToEndpoints(client *http.Client, endpoints []*url.URL, logger log.Logger, m FanoutMetrics) http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests.With(prometheus.Labels{"method": r.Method}).Inc()
		rlogger := log.With(logger, "request", middleware.GetReqID(r.Context()))

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusInternalServerError)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewBuffer(body))

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
			go func(ep *url.URL) {
				defer wg.Done()
				targetURL, err := url.JoinPath(ep.String(), r.URL.Path)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to get url", "err", err, "url", targetURL)
					return
				}
				req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewBuffer(body))
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to create fanout request", "err", err, "url", ep.String())
					return
				}
				req.Header = r.Header.Clone()

				resp, err := client.Do(req)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to send request", "err", err, "url", ep.String())
					m.fanoutRequests.With(prometheus.Labels{"code": "<error>", "url": ep.String()}).Inc()
					return
				}

				defer resp.Body.Close()
				m.fanoutRequests.With(prometheus.Labels{"code": strconv.Itoa(resp.StatusCode), "url": ep.String()}).Inc()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					respBody, _ := io.ReadAll(resp.Body)
					level.Error(rlogger).Log("msg", "fanout target returned error", "code", resp.Status, "response", string(respBody), "url", ep.String())
				} else {
					successCnt.Add(1)
				}

			}(ep)
		}

		wg.Wait()
		// say n is the number of endpoints. If at least 1 of n requests fail, return error response
		// consistent with prometheus fanout implementation that expects to log error when alert request fails to 1+ alertmanager
		// only difference is prometheus fanout logs 1+ times whereas this implementation will only log failed alert once on prometheus
		if int(successCnt.Load()) < len(endpoints) {
			// note that prometheus does not retry failed alert requests, instead relying on resend delay for re-firing alert
			http.Error(w, "Failed to reach all fanout targets", http.StatusBadGateway)
		}
		w.WriteHeader(http.StatusOK)

	})
}
