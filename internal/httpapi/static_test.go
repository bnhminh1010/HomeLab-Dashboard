package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestStaticHandlerServesGzipAndETag(t *testing.T) {
	handler, err := NewStaticHandler(fstest.MapFS{
		"index.html":   {Data: []byte("<!doctype html><title>dashboard</title>")},
		"js/app.js":    {Data: []byte("console.log('local')")},
		"lib/chart.js": {Data: []byte("window.Chart = function Chart() {}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://dashboard.test/js/app.js", nil)
	r.Header.Set("Accept-Encoding", "br, gzip")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d encoding=%q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("ETag") == "" {
		t.Fatal("security header or ETag missing")
	}
	vendorRequest := httptest.NewRequest(http.MethodGet, "http://dashboard.test/lib/chart.js", nil)
	vendorResponse := httptest.NewRecorder()
	handler.ServeHTTP(vendorResponse, vendorRequest)
	if cache := vendorResponse.Header().Get("Cache-Control"); cache != "public, max-age=86400, must-revalidate" {
		t.Fatalf("vendor cache policy = %q", cache)
	}

	conditional := httptest.NewRequest(http.MethodGet, "http://dashboard.test/js/app.js", nil)
	conditional.Header.Set("If-None-Match", w.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified {
		t.Fatalf("conditional status=%d", conditionalResponse.Code)
	}
}

func TestStaticHandlerDoesNotFallbackUnknownPaths(t *testing.T) {
	handler, err := NewStaticHandler(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://dashboard.test/missing", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
}
