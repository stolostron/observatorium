package alertmanager

import (
	"crypto/tls"
	"crypto/x509"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-kit/kit/log"
	"github.com/observatorium/observatorium/internal/fanout"
	"github.com/prometheus/client_golang/prometheus"
)

const alertmanagerCAPath = "/etc/observatorium/alertmanager/service-ca.crt"
const alertmanagerServerName = "alertmanager.open-cluster-management-observability.svc"

type handlerConfiguration struct {
	logger           log.Logger
	registry         *prometheus.Registry
	caPath           string
	serverName       string
	writeMiddlewares []func(http.Handler) http.Handler
}

type HandlerOption func(h *handlerConfiguration)

func Logger(logger log.Logger) HandlerOption {
	return func(h *handlerConfiguration) {
		h.logger = logger
	}
}

func Registry(r *prometheus.Registry) HandlerOption {
	return func(h *handlerConfiguration) {
		h.registry = r
	}
}

func WriteMiddleware(m func(http.Handler) http.Handler) HandlerOption {
	return func(h *handlerConfiguration) {
		h.writeMiddlewares = append(h.writeMiddlewares, m)
	}
}

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

func newHTTPClient(caPool *x509.CertPool, serverName string) *http.Client {
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
			ServerName: serverName,
			RootCAs:    caPool,
		}
	}
	return &http.Client{Transport: transport}
}

func NewHandler(loader *fanout.EndpointLoader, opts ...HandlerOption) http.Handler {
	c := &handlerConfiguration{
		logger:   log.NewNopLogger(),
		registry: prometheus.NewRegistry(),
	}
	for _, o := range opts {
		o(c)
	}
	r := chi.NewRouter()

	// create client here
	caPool := loadCACertPool()
	client := newHTTPClient(caPool, alertmanagerServerName)

	alertFanout := fanout.FanoutRequestToEndpoints(loader, c.logger, *client, fanout.NewFanoutMetrics(c.registry))
	r.Group(func(r chi.Router) {
		r.Use(c.writeMiddlewares...)
		r.Post("/api/v2/alerts", alertFanout.ServeHTTP)
	})
	return r
}
