package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func newPersistentReaderTest(t *testing.T, maxBytes int64) (context.Context, *openAIWSClientReader, *coderws.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ready := make(chan context.Context, 1)
	exit := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := coderws.Accept(w, req, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(maxBytes)
		readerCtx, stop := StartOpenAIWSClientReader(ctx, conn, maxBytes)
		defer stop()
		ready <- readerCtx
		<-exit
	}))
	t.Cleanup(func() { close(exit); server.Close() })
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.CloseNow() })
	select {
	case readerCtx := <-ready:
		return readerCtx, openAIWSReaderFromContext(readerCtx), client
	case <-ctx.Done():
		t.Fatal("reader did not start")
		return nil, nil, nil
	}
}

func TestPersistentReaderOrderAndIndependentDisconnect(t *testing.T) {
	ctx, reader, client := newPersistentReaderTest(t, 1024)
	for _, message := range []string{"first", "second", "third"} {
		require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(message)))
	}
	for _, want := range []string{"first", "second", "third"} {
		_, payload, err := ReadOpenAIWSClientMessage(ctx, reader.conn, time.Second, coderws.StatusNormalClosure, "idle")
		require.NoError(t, err)
		require.Equal(t, want, string(payload))
	}
	// No consumer is reading now (as while blocked in admission). Control
	// frames must still work, and closing must not cancel the drain context.
	client.CloseRead(ctx) // The client also needs a reader to receive the pong.
	require.NoError(t, client.Ping(ctx))
	require.NoError(t, client.CloseNow())
	select {
	case <-reader.peer.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnect hidden by admission")
	}
	require.NoError(t, ctx.Err(), "peer disconnect must not cancel upstream usage drain")
}

func TestPersistentReaderBoundedBuffer(t *testing.T) {
	for _, maxBytes := range []int64{8, 1024} {
		t.Run(strconv.FormatInt(maxBytes, 10), func(t *testing.T) {
			ctx, reader, client := newPersistentReaderTest(t, maxBytes)
			for i := 0; i < 6; i++ {
				if err := client.Write(ctx, coderws.MessageText, []byte("four")); err != nil {
					break
				}
			}
			select {
			case <-reader.peer.Done():
			case <-time.After(time.Second):
				t.Fatal("buffer did not reject overflow")
			}
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, context.Cause(reader.peer), &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		})
	}
}

func TestPersistentReaderPreservesControlClose(t *testing.T) {
	ctx, reader, client := newPersistentReaderTest(t, 1024)
	controlCtx, cancel := context.WithCancelCause(ctx)
	result := make(chan error, 1)
	go func() {
		_, _, err := ReadOpenAIWSClientMessage(controlCtx, reader.conn, time.Second, coderws.StatusNormalClosure, "idle")
		result <- err
	}()
	cancel(ErrOpenAIWSIngressLeaseLost)
	_, _, err := client.Read(ctx)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Contains(t, closeErr.Reason, "capacity lease lost")
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrOpenAIWSIngressLeaseLost)
	case <-time.After(time.Second):
		t.Fatal("reader not joined")
	}
}
