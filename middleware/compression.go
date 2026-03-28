package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// CompressionConfig configures response compression.
type CompressionConfig struct {
	// Enabled enables response compression.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// MinSize is the minimum response body size (bytes) to trigger compression.
	MinSize int `json:"minSize" yaml:"min_size"`

	// Level is the gzip compression level (1-9, or -1 for default).
	Level int `json:"level" yaml:"level"`

	// ContentTypes lists MIME types eligible for compression.
	// Empty means all compressible types.
	ContentTypes []string `json:"contentTypes,omitempty" yaml:"content_types"`
}

// CompressionMiddleware wraps an http.Handler with gzip compression.
type CompressionMiddleware struct {
	config CompressionConfig
	pool   sync.Pool
}

// NewCompressionMiddleware creates a new compression middleware.
func NewCompressionMiddleware(config CompressionConfig) *CompressionMiddleware {
	if config.MinSize <= 0 {
		config.MinSize = 1024 // 1KB default
	}

	level := config.Level
	if level == 0 {
		level = gzip.DefaultCompression
	}

	return &CompressionMiddleware{
		config: config,
		pool: sync.Pool{
			New: func() any {
				w, _ := gzip.NewWriterLevel(io.Discard, level)
				return w
			},
		},
	}
}

// Wrap returns a handler that compresses responses.
func (cm *CompressionMiddleware) Wrap(next http.Handler) http.Handler {
	if !cm.config.Enabled {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}

		gz := cm.pool.Get().(*gzip.Writer)
		defer cm.pool.Put(gz)

		cw := &compressWriter{
			ResponseWriter: w,
			gz:             gz,
			config:         cm.config,
		}
		defer cw.Close()

		next.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	config      CompressionConfig
	buf         []byte
	wroteHeader bool
	compressed  bool
}

func (cw *compressWriter) Write(b []byte) (int, error) {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}

	if cw.compressed {
		return cw.gz.Write(b)
	}

	// Buffer small responses
	cw.buf = append(cw.buf, b...)

	if len(cw.buf) >= cw.config.MinSize {
		cw.startCompression()
		return len(b), nil
	}

	return len(b), nil
}

func (cw *compressWriter) WriteHeader(code int) {
	if cw.wroteHeader {
		return
	}

	cw.wroteHeader = true

	ct := cw.ResponseWriter.Header().Get("Content-Type")
	if !cw.shouldCompress(ct) {
		cw.ResponseWriter.WriteHeader(code)
		return
	}

	// Defer actual header write — we don't know if we'll compress yet
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *compressWriter) startCompression() {
	cw.compressed = true
	cw.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	cw.ResponseWriter.Header().Del("Content-Length")
	cw.gz.Reset(cw.ResponseWriter)

	if len(cw.buf) > 0 {
		cw.gz.Write(cw.buf) //nolint:errcheck
		cw.buf = nil
	}
}

func (cw *compressWriter) Close() {
	if cw.compressed {
		cw.gz.Close() //nolint:errcheck
	} else if len(cw.buf) > 0 {
		cw.ResponseWriter.Write(cw.buf) //nolint:errcheck
	}
}

func (cw *compressWriter) shouldCompress(ct string) bool {
	if len(cw.config.ContentTypes) == 0 {
		return isCompressibleType(ct)
	}

	for _, allowed := range cw.config.ContentTypes {
		if strings.HasPrefix(ct, allowed) {
			return true
		}
	}

	return false
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

func isCompressibleType(ct string) bool {
	compressible := []string{
		"text/", "application/json", "application/xml",
		"application/javascript", "application/xhtml",
		"image/svg", "application/yaml",
	}

	for _, prefix := range compressible {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}

	return false
}
