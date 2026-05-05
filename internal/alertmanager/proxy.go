package alertmanager

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
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

	// Maybe don't want this (N metrics per error where N is the number of endpoints)
	fanoutRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "alert_fanout_requests_total",
		Help:        "Counter of fanout requests.",
		ConstLabels: prometheus.Labels{"fanout": "fanoutv1-write"},
	}, []string{"code", "url"})

	registry.MustRegister(requests, fanoutRequests)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.With(prometheus.Labels{"method": r.Method}).Inc()

		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		rlogger := log.With(logger, "request", middleware.GetReqID(r.Context()))

		endpoints := loader.GetEndpoints()
		if len(endpoints) == 0 {
			http.Error(w, "alertmanager endpoints not loaded", http.StatusBadRequest)
			return
		}
		var (
			wg         sync.WaitGroup
			successCnt atomic.Int32
		)

		// todo add more error handling
		// Incorporate use of logchannels?
		// What if one request fails?
		// what if 2/3 requests fail?
		// have a primary endpoint to send alerts to?
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

				resp, err := ep.client.Do(req)
				if err != nil {
					level.Error(rlogger).Log("msg", "failed to send request", "err", err, "url", ep.URL)
					fanoutRequests.With(prometheus.Labels{"code": "<error>", "url": ep.URL}).Inc()
					return
				}

				defer resp.Body.Close()
				fanoutRequests.With(prometheus.Labels{"code": strconv.Itoa(resp.StatusCode), "url": ep.URL}).Inc()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					respBody, _ := io.ReadAll(resp.Body)
					level.Error(rlogger).Log("msg", "failed to fanout alert", "code", resp.Status, "response", string(respBody), "url", ep.URL)
				} else {
					successCnt.Add(1)
				}

				//  only write if primary endpoint
				if ep.primary && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
					http.Error(w, "Failed to reach alertmanager", resp.StatusCode)
				} else if ep.primary {
					w.WriteHeader(http.StatusOK)
				}

				if ep.primary {
					stdlog.Printf("Status from primary endpoint %d", resp.StatusCode)
				}
			}()
		}

		wg.Wait()

		total := int32(len(endpoints))
		succeeded := successCnt.Load()

		if total == succeeded {
			stdlog.Println("Alertmanager fanout success")

		} else {
			stdlog.Printf("Alertmanager fanout failure %d / %d succeeded", succeeded, total)
		}
	})
}
