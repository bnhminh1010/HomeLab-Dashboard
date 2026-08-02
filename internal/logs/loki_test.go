package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLokiQueryBuildsBoundedSelector(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		gotQuery = request.URL.Query().Get("query")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"result":[{"stream":{"node":"local","service_name":"dashboard"},"values":[["1700000000000000000","{\"level\":\"ERROR\",\"message\":\"failed\"}"]]}]}}`))
	}))
	defer server.Close()
	client, err := NewLoki(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	to := time.Unix(1700000000, 0).UTC()
	result, err := client.Query(context.Background(), Query{NodeID: LocalNodeID, From: to.Add(-time.Hour), To: to, Service: "dashboard", Container: "dashboard", Level: "error", Text: "failed", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Labels["service_name"] != "dashboard" {
		t.Fatalf("result = %+v", result)
	}
	for _, expected := range []string{`job="podman"`, `node="local"`, `service_name="dashboard"`, `container_name="dashboard"`, `(?i)^error$`, `|= "failed"`} {
		if !strings.Contains(gotQuery, expected) {
			t.Fatalf("query %q does not contain %q", gotQuery, expected)
		}
	}
}

func TestLokiQueryRejectsUnboundedInput(t *testing.T) {
	client, err := NewLoki("http://127.0.0.1:3100", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, query := range []Query{
		{NodeID: "remote", From: now.Add(-time.Hour), To: now},
		{NodeID: LocalNodeID, From: now.Add(-MaxRange - time.Second), To: now},
		{NodeID: LocalNodeID, From: now.Add(-time.Hour), To: now, Text: "bad\ninput"},
		{NodeID: LocalNodeID, From: now.Add(-time.Hour), To: now, Limit: MaxLimit + 1},
	} {
		if _, err := client.Query(context.Background(), query); err == nil {
			t.Fatalf("expected invalid query: %+v", query)
		}
	}
}
