package alertmanager

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

const alertmanagerCAPath = "/etc/observatorium/alertmanager/service-ca.crt"

func loadCACertPool() *x509.CertPool {
	caCert, err := os.ReadFile(alertmanagerCAPath)
	if err != nil {
		stdlog.Printf("CA certificate not available at %s, using system roots: %v", alertmanagerCAPath, err)
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		stdlog.Printf("failed to parse CA certificate from %s, using system roots", alertmanagerCAPath)
		return nil
	}
	stdlog.Printf("loaded CA certificate from %s", alertmanagerCAPath)
	return pool
}

func newHTTPClient(caPool *x509.CertPool) *http.Client {
	transport := &http.Transport{
		DisableKeepAlives: true,
		IdleConnTimeout:   30 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	if caPool != nil {
		transport.TLSClientConfig = &tls.Config{
			ServerName: "alertmanager.open-cluster-management-observability.svc",
			RootCAs:    caPool,
		}
	}
	return &http.Client{Transport: transport}
}

func fanoutAlert(loader *EndpointLoader, logger log.Logger, registry *prometheus.Registry) http.Handler {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "http_alert_fanout_requests_total",
		Help:        "Counter of alert HTTP requests.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-write"},
	}, []string{"method"})

	fanoutRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "alert_fanout_requests_total",
		Help:        "Counter of fanout requests.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-write"},
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

				resp, err := ep.client.Do(req)
				if err != nil {
					fanoutRequests.With(prometheus.Labels{"code": "<error>", "name": ep.Name}).Inc()
					level.Error(rlogger).Log("msg", "failed to send request", "err", err, "url", ep.URL)
					return
				}
				defer resp.Body.Close()
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
		fmt.Printf("msg: fanout complete, total: %d, succeeded: %d, failed: %d\n", total, succeeded, failed)
		level.Info(rlogger).Log("msg", "fanout complete", "total", total, "succeeded", succeeded, "failed", failed)
		w.WriteHeader(http.StatusOK)

	})
}
