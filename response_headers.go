package main

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

var hopByHopResponseHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// copyProxiedResponseHeaders copies safe ClickHouse HTTP response headers to the client.
func copyProxiedResponseHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	for k, vals := range src {
		kl := strings.ToLower(k)
		if _, skip := hopByHopResponseHeaders[kl]; skip {
			continue
		}
		if kl == "content-length" || kl == "content-encoding" {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copyHTTPHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	dst := make(http.Header, len(h))
	for k, vals := range h {
		valsCopy := make([]string, len(vals))
		copy(valsCopy, vals)
		dst[k] = valsCopy
	}
	return dst
}

func writeProxiedQueryResponse(c echo.Context, status int, body string, headers http.Header) error {
	copyProxiedResponseHeaders(c.Response().Header(), headers)
	ct := c.Response().Header().Get("Content-Type")
	if ct == "" {
		return c.String(status, body)
	}
	return c.Blob(status, ct, []byte(body))
}
