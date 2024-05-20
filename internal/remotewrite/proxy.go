package remotewrite

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	promconfig "github.com/prometheus/common/config"
)

const (
	thanosEndpointName = "thanos-receiver"
)

type Endpoint struct {
	Name         string                       `yaml:"name"`
	URL          string                       `yaml:"url"`
	ClientConfig *promconfig.HTTPClientConfig `yaml:"http_client_config,omitempty"`
}

var (
	requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "http_proxy_requests_total",
		Help:        "Counter of proxy HTTP requests.",
		ConstLabels: prometheus.Labels{"proxy": "metricsv1-write"},
	}, []string{"method"})

	remotewriteRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "remote_write_requests_total",
		Help:        "Counter of remote write requests.",
		ConstLabels: prometheus.Labels{"proxy": "metricsv1-remotewrite"},
	}, []string{"code", "name"})
)

func (rd *RequestDuplicator) remoteWrite(write *url.URL, endpoints []Endpoint, logger log.Logger, logManager *logManager) http.Handler {
	var clientMap = map[string]*http.Client{}
	clientMap = make(map[string]*http.Client)
	defaultHTTPClient := defaultClient()
	writePath := write.Path
	writeHost := write.Host
	if write.Scheme == "" {
		write.Scheme = "http"
	}
	writeScheme := write.Scheme

	for _, ep := range endpoints {
		var client = defaultHTTPClient
		if ep.ClientConfig != nil {
			epClient, err := promconfig.NewClientFromConfig(*ep.ClientConfig, ep.Name,
				promconfig.WithDialContextFunc((&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext))
			if err == nil {
				client = epClient
			}
		}
		clientMap[ep.Name] = client
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.With(prometheus.Labels{"method": r.Method}).Inc()
		rlogger := log.With(logger, "request", middleware.GetReqID(r.Context()))

		body, err := io.ReadAll(r.Body)
		if err != nil {
			level.Error(rlogger).Log("msg", "failed to read request body", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		headers := r.Header.Clone()

		rwReq, err := rebuildProxyRequest(r, body, writePath, writeHost, writeScheme)
		if err != nil {
			level.Error(rlogger).Log("msg", "failed to rebuild the request", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		go func() {
			for _, endpoint := range endpoints {
				go func() {
					req, err := mirrorRequestFromBody(body, headers, endpoint.URL)
					if err != nil {
						level.Error(rlogger).Log("msg", "failed to build the remote write request", "url", endpoint.URL, "err", err)
						return
					}
					client := getClientForEndpoint(endpoint.Name, clientMap)
					_ = rd.doRemoteWriteRequest(client, req, endpoint.Name, logger)
				}()

			}
		}()

		// handle the main remote write endpoint request synchronously
		if write != nil {
			statusCode := rd.doRemoteWriteRequest(defaultHTTPClient, rwReq, thanosEndpointName, logger)
			w.WriteHeader(statusCode)
		}
	})
}

type RequestDuplicator struct {
	logManager *logManager
}

func (rd *RequestDuplicator) Proxy(write *url.URL, endpoints []Endpoint, logger log.Logger, r *prometheus.Registry) http.Handler {

	r.MustRegister(requests)
	r.MustRegister(remotewriteRequests)

	if endpoints == nil {
		endpoints = []Endpoint{}
	}

	if rd.logManager == nil {
		rd.logManager = newLogManager(logger, endpoints, nil)
	}

	return rd.remoteWrite(write, endpoints, logger, rd.logManager)
}

func rebuildProxyRequest(r *http.Request, body []byte, reqPath, host, scheme string) (*http.Request, error) {
	remotewriteUrl := url.URL{}
	remotewriteUrl.Path = path.Join(reqPath, r.URL.Path)
	remotewriteUrl.Host = host
	remotewriteUrl.Scheme = scheme

	req, err := http.NewRequest(r.Method, remotewriteUrl.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err

	}
	req.Header = r.Header.Clone()
	req.WithContext(r.Context())
	return req, nil
}

// mirrorRequestFromBody build a remote write request for the upstream remote write endpoint
// we enforce a 5s timeout here to avoid having unbounded goroutines due to slow backends
func mirrorRequestFromBody(body []byte, headers http.Header, endpoint string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	req = req.WithContext(ctx)
	return req, nil
}

func defaultClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			IdleConnTimeout:   30 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
}

func getClientForEndpoint(name string, fromPool map[string]*http.Client) *http.Client {
	c, ok := fromPool[name]
	if !ok {
		return defaultClient()
	}
	return c
}

func (rd *RequestDuplicator) doRemoteWriteRequest(
	client *http.Client,
	req *http.Request,
	epName string,
	logger log.Logger,
) int {
	resp, err := client.Do(req)
	if err != nil {
		remotewriteRequests.With(prometheus.Labels{"code": "<error>", "name": epName}).Inc()
		rd.logManager.log(epName, "failed to send request to the server", "msg", "failed to send request to the server", "err", err)
		return http.StatusInternalServerError
	}

	remotewriteRequests.With(prometheus.Labels{"code": strconv.Itoa(resp.StatusCode), "name": epName}).Inc()
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		responseBody, err := io.ReadAll(resp.Body)
		keyVals := []interface{}{
			"msg", "failed to forward metrics",
			"endpoint", epName,
			"response code", resp.Status,
			"response", string(responseBody),
			"url", req.URL.String(),
		}

		if err != nil {
			keyVals = append(keyVals, "err", err)
		}
		rd.logManager.log(epName, "failed to forward metrics "+resp.Status, keyVals...)
		return resp.StatusCode
	}
	level.Debug(logger).Log("msg", "Successfully forwarded metrics", "url", req.URL.String())
	return resp.StatusCode
}
