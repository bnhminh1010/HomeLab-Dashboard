package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

type staticAsset struct {
	contentType string
	raw         []byte
	gzip        []byte
	etag        string
}

// StaticHandler pre-compresses embedded assets once at startup. This keeps the
// scratch runtime image small without spending CPU on every browser request.
type StaticHandler struct {
	assets map[string]staticAsset
}

func NewStaticHandler(source fs.FS) (*StaticHandler, error) {
	handler := &StaticHandler{assets: make(map[string]staticAsset)}
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		var compressed bytes.Buffer
		zw, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
		if err != nil {
			return err
		}
		if _, err := zw.Write(raw); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = http.DetectContentType(raw)
		}
		handler.assets["/"+name] = staticAsset{
			contentType: contentType,
			raw:         raw,
			gzip:        compressed.Bytes(),
			etag:        `"` + hex.EncodeToString(digest[:12]) + `"`,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index static assets: %w", err)
	}
	if _, ok := handler.assets["/index.html"]; !ok {
		return nil, fmt.Errorf("index static assets: index.html is missing")
	}
	return handler, nil
}

func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if requestPath == "/" {
		requestPath = "/index.html"
	}
	asset, ok := h.assets[requestPath]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("ETag", asset.etag)
	w.Header().Set("Vary", "Accept-Encoding")
	if requestPath == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(requestPath, "/lib/") {
		// Vendor filenames are stable across dashboard releases. Require
		// revalidation so an upgraded embedded bundle cannot remain pinned in a
		// browser cache for a year under the same URL.
		w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
	}
	if r.Header.Get("If-None-Match") == asset.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := asset.raw
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		body = asset.gzip
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func acceptsGzip(value string) bool {
	for _, encoding := range strings.Split(value, ",") {
		if strings.TrimSpace(strings.SplitN(encoding, ";", 2)[0]) == "gzip" {
			return true
		}
	}
	return false
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self' ws: wss:; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
