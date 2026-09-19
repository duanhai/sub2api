package probes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Run from backend: go test -v ../docs/probes/ws_queue_lifecycle_test.go
// This is a feasibility probe, not a production queue implementation. It checks
// whether the request context used by ResponsesWebSocket detects a disconnected
// peer while admission blocks without a reader.
func TestWebSocketQueueCannotRelyOnHTTPRequestContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	peerClosed := make(chan struct{})
	type observation struct {
		readErr    error
		requestErr error
	}
	result := make(chan observation, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			result <- observation{readErr: err, requestErr: err}
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(ctx); err != nil {
			result <- observation{readErr: err, requestErr: err}
			return
		}
		close(ready)
		select {
		case <-peerClosed:
		case <-ctx.Done():
			return
		}
		// The peer has closed. Reading the socket observes it, but the HTTP
		// request context still does not become a WebSocket lifetime context.
		_, _, err = conn.Read(ctx)
		result <- observation{readErr: err, requestErr: r.Context().Err()}
	}))
	defer server.Close()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	if err := client.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("server did not receive the first request")
	}
	if err := client.CloseNow(); err != nil {
		t.Fatal(err)
	}
	close(peerClosed)
	select {
	case got := <-result:
		if got.readErr == nil {
			t.Fatal("expected socket read to detect the disconnected client")
		}
		if got.requestErr != nil {
			t.Fatalf("request context unexpectedly canceled: %v", got.requestErr)
		}
		t.Log("confirmed: socket read detects disconnect, HTTP request context remains active")
	case <-ctx.Done():
		t.Fatal("probe timed out")
	}
}
