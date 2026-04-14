package alertmanager

import (
	"net/http"

	"github.com/go-chi/chi"
	"github.com/go-kit/kit/log"
	"github.com/prometheus/client_golang/prometheus"
)

type handlerConfiguration struct {
	logger           log.Logger
	registry         *prometheus.Registry
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

func NewHandler(loader *EndpointLoader, opts ...HandlerOption) http.Handler {
	c := &handlerConfiguration{
		logger:   log.NewNopLogger(),
		registry: prometheus.NewRegistry(),
	}
	for _, o := range opts {
		o(c)
	}
	r := chi.NewRouter()
	fanout := fanoutAlert(loader, c.logger, c.registry)
	r.Group(func(r chi.Router) {
		r.Use(c.writeMiddlewares...)
		r.Post("/api/v2/alerts", fanout.ServeHTTP)
	})
	return r
}
