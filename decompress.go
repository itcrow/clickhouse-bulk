package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/ClickHouse/ch-go/compress"
	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

var ErrRequestTooLarge = errors.New("request body too large")

func needsDecompression(contentEncoding, params string) bool {
	enc := normalizeContentEncoding(contentEncoding)
	if enc != "" && enc != "identity" {
		return true
	}
	return queryParamEnabled(params, "decompress")
}

func decompressRequestBody(body []byte, contentEncoding, params string, maxBytes int64) ([]byte, string, error) {
	qs := params
	var err error

	body, err = decompressHTTPEncoding(body, contentEncoding, maxBytes)
	if err != nil {
		return nil, qs, err
	}

	if queryParamEnabled(params, "decompress") {
		body, err = decompressNativeBlocks(body, maxBytes)
		if err != nil {
			return nil, qs, err
		}
		qs = stripQueryParam(qs, "decompress")
	}

	if err := checkRequestSize(len(body), maxBytes); err != nil {
		return nil, qs, err
	}
	return body, qs, nil
}

func normalizeContentEncoding(contentEncoding string) string {
	enc := strings.TrimSpace(strings.ToLower(contentEncoding))
	if enc == "" {
		return ""
	}
	if i := strings.IndexAny(enc, ",;"); i >= 0 {
		enc = strings.TrimSpace(enc[:i])
	}
	return enc
}

func queryParamEnabled(params, key string) bool {
	keyLower := strings.ToLower(key)
	for _, p := range strings.Split(params, "&") {
		if p == "" {
			continue
		}
		name := p
		val := "1"
		if i := strings.Index(p, "="); i >= 0 {
			name = p[:i]
			val = p[i+1:]
		}
		if !strings.EqualFold(name, keyLower) {
			continue
		}
		v, err := queryUnescape(val)
		if err != nil {
			v = val
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func stripQueryParam(params, key string) string {
	if params == "" {
		return params
	}
	parts := strings.Split(params, "&")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		name := p
		if i := strings.Index(p, "="); i >= 0 {
			name = p[:i]
		}
		if strings.EqualFold(name, key) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "&")
}

func decompressHTTPEncoding(body []byte, contentEncoding string, maxBytes int64) ([]byte, error) {
	enc := normalizeContentEncoding(contentEncoding)
	if enc == "" || enc == "identity" {
		return body, nil
	}

	src := bytes.NewReader(body)
	var r io.Reader
	var closer io.Closer

	switch enc {
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		r, closer = gz, gz
	case "deflate":
		r = flate.NewReader(src)
		closer = r.(io.Closer)
	case "zstd":
		dec, err := zstd.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		defer dec.Close()
		r = dec
	case "lz4":
		r = lz4.NewReader(src)
	case "snappy", "x-snappy-framed":
		r = snappy.NewReader(src)
	case "br":
		r = brotli.NewReader(src)
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", enc)
	}

	out, err := readAllLimited(r, maxBytes)
	if closer != nil {
		_ = closer.Close()
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func decompressNativeBlocks(body []byte, maxBytes int64) ([]byte, error) {
	r := compress.NewReader(bytes.NewReader(body))
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if maxBytes > 0 && int64(out.Len()+n) > maxBytes {
				return nil, ErrRequestTooLarge
			}
			out.Write(buf[:n])
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if n == 0 && out.Len() > 0 && isNativeBlockEOF(err) {
			break
		}
		return nil, fmt.Errorf("native blocks: %w", err)
	}
	return out.Bytes(), nil
}

func isNativeBlockEOF(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "header: EOF") || strings.Contains(msg, "header: unexpected EOF")
}

func readAllLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(r)
	}
	lr := io.LimitReader(r, maxBytes+1)
	out, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxBytes {
		return nil, ErrRequestTooLarge
	}
	return out, nil
}

func checkRequestSize(n int, maxBytes int64) error {
	if maxBytes > 0 && int64(n) > maxBytes {
		return ErrRequestTooLarge
	}
	return nil
}

func readRequestBody(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(r)
	}
	// Allow compressed bodies to be larger than the decompressed limit.
	compressedLimit := maxBytes * 4
	if compressedLimit < maxBytes {
		compressedLimit = maxBytes
	}
	lr := io.LimitReader(r, compressedLimit+1)
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > compressedLimit {
		return nil, ErrRequestTooLarge
	}
	return body, nil
}

func queryUnescape(s string) (string, error) {
	return url.QueryUnescape(s)
}
