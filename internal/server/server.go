package server

import (
	"context"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/observatorium/observatorium/internal/proxy"
	"github.com/observatorium/observatorium/prober"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	logger log.Logger
	prober *prober.Prober
	srv    *http.Server

	opts options
}

func New(logger log.Logger, reg *prometheus.Registry, opts ...Option) Server {
	options := options{
		gracePeriod: 5 * time.Second,
		profile:     false,
	}

	for _, o := range opts {
		o.apply(&options)
	}

	ins := newInstrumentationMiddleware(reg)
	p := prober.New(logger)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	registerProber(r, p)
	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		promhttp.InstrumentMetricHandler(reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{})).ServeHTTP(w, r)
	})

	queryEndpoint := "/api/v1/metrics/query"
	r.Get(queryEndpoint,
		ins.newHandler("query", proxy.New(logger, queryEndpoint, options.metricsQueryEndpoint, options.proxyOptions...)))

	queryRangeEndpoint := "/api/v1/metrics/query_range"
	r.Get(queryEndpoint,
		ins.newHandler("query_range", proxy.New(logger, queryRangeEndpoint, options.metricsQueryEndpoint, options.proxyOptions...)))

	receivePath := "/api/v1/metrics/receive"
	r.Post(receivePath,
		ins.newHandler("receive", proxy.New(logger, receivePath, options.metricsReceiveEndpoint, options.proxyOptions...)))

	if options.profile {
		registerProfiler(r)
	}

	p.Healthy()

	return Server{
		logger: logger,
		prober: p,
		srv:    &http.Server{Addr: options.listen, Handler: r},
		opts:   options,
	}
}

func (s *Server) ListenAndServe() error {
	level.Info(s.logger).Log("msg", "starting the HTTP server", "address", s.opts.listen)
	s.prober.Ready()

	return s.srv.ListenAndServe()
}

func (s *Server) Shutdown(err error) {
	s.prober.NotReady(err)

	if err == http.ErrServerClosed {
		level.Warn(s.logger).Log("msg", "internal server closed unexpectedly")
		return
	}

	ctx, c := context.WithTimeout(context.Background(), s.opts.gracePeriod)
	defer c()

	level.Info(s.logger).Log("msg", "shutting down internal server")

	if err := s.srv.Shutdown(ctx); err != nil {
		level.Error(s.logger).Log("msg", "shutting down failed", "err", err)
	}
}

func registerProfiler(r *chi.Mux) {
	r.Get("/debug/pprof/", pprof.Index)
	r.Get("/debug/pprof/cmdline", pprof.Cmdline)
	r.Get("/debug/pprof/profile", pprof.Profile)
	r.Get("/debug/pprof/symbol", pprof.Symbol)
	r.Get("/debug/pprof/trace", pprof.Trace)
}

func registerProber(r *chi.Mux, p *prober.Prober) {
	r.Get("/-/healthy", p.HealthyHandler())
	r.Get("/-/ready", p.ReadyHandler())
}
