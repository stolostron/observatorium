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

const (
	dialTimeout = 30 * time.Second
)

const AlertmanagerAlertsRoute = "/api/v2/alerts"

type handlerConfiguration struct {
	logger           log.Logger
	registry         *prometheus.Registry
	instrument       handlerInstrumenter
	writeMiddlewares []func(http.Handler) http.Handler
}

type handlerInstrumenter interface {
	NewHandler(labels prometheus.Labels, handler http.Handler) http.HandlerFunc
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

type nopInstrumentHandler struct{}

func (n nopInstrumentHandler) NewHandler(_ prometheus.Labels, handler http.Handler) http.HandlerFunc {
	return handler.ServeHTTP
}

func loadCACertPool(caPath string) *x509.CertPool {
	if caPath == "" {
		return nil
	}
	caCert, err := os.ReadFile(caPath)
	if err != nil {
		stdlog.Printf("CA certificate not available at %s, using system roots: %v", caPath, err)
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		stdlog.Printf("failed to parse CA certificate from %s, using system roots", caPath)
		return nil
	}
	return pool
}

func newHTTPClient(caPool *x509.CertPool, serverName string) *http.Client {
	transport := &http.Transport{
		DisableKeepAlives: true,
		IdleConnTimeout:   dialTimeout,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: dialTimeout,
		}).DialContext,
	}

	if caPool != nil && serverName != "" {
		transport.TLSClientConfig = &tls.Config{
			ServerName: serverName,
			RootCAs:    caPool,
		}
	}

	return &http.Client{Transport: transport}
}

func NewHandler(loader *fanout.EndpointLoader, upstreamCAFile, upstreamServerName string, opts ...HandlerOption) http.Handler {
	c := &handlerConfiguration{
		logger:     log.NewNopLogger(),
		registry:   prometheus.NewRegistry(),
		instrument: nopInstrumentHandler{},
	}
	for _, o := range opts {
		o(c)
	}
	r := chi.NewRouter()

	caPool := loadCACertPool(upstreamCAFile)
	client := newHTTPClient(caPool, upstreamServerName)

	alertFanout := fanout.FanoutRequestToEndpoints(loader, c.logger, *client, fanout.NewFanoutMetrics(c.registry))
	r.Group(func(r chi.Router) {
		r.Use(c.writeMiddlewares...)
		r.Post(AlertmanagerAlertsRoute, alertFanout.ServeHTTP)
	})
	return r
}
