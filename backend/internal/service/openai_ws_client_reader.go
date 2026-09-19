package service

import (
	"context"
	"sync"

	coderws "github.com/coder/websocket"
)

type openAIWSClientReaderKey struct{}

// openAIWSClientReader owns the only socket reader for one ingress connection.
// It never runs admission or frame policy, which must remain ordered in the
// consumer. In particular, a blocked admission cannot block control frames.
type openAIWSClientReader struct {
	conn     *coderws.Conn
	frames   chan openAIWSClientReadResult
	done     chan struct{}
	peer     context.Context
	cancel   context.CancelCauseFunc
	mu       sync.Mutex
	buffered int64
	maxBytes int64
}

// StartOpenAIWSClientReader must be called once, before reading the first frame.
// The owner must call stop on every exit. The returned context carries the reader
// but is NOT canceled by peer disconnect: active upstream usage must still drain.
func StartOpenAIWSClientReader(ctx context.Context, conn *coderws.Conn, maxBytes int64) (context.Context, func()) {
	peer, cancel := context.WithCancelCause(context.Background())
	r := &openAIWSClientReader{conn: conn, frames: make(chan openAIWSClientReadResult, 4), done: make(chan struct{}), peer: peer, cancel: cancel, maxBytes: maxBytes}
	go r.run()
	return context.WithValue(ctx, openAIWSClientReaderKey{}, r), func() {
		_ = conn.CloseNow()
		<-r.done
	}
}

func openAIWSReaderFromContext(ctx context.Context) *openAIWSClientReader {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(openAIWSClientReaderKey{}).(*openAIWSClientReader)
	return r
}

func (r *openAIWSClientReader) run() {
	defer close(r.done)
	for {
		typ, payload, err := r.conn.Read(context.Background())
		if err != nil {
			r.cancel(err)
			return
		}
		r.mu.Lock()
		fits := r.buffered+int64(len(payload)) <= r.maxBytes
		if fits {
			r.buffered += int64(len(payload))
		}
		r.mu.Unlock()
		if fits {
			select {
			case r.frames <- openAIWSClientReadResult{messageType: typ, payload: payload}:
				continue
			default:
			}
		}
		// Never block a socket reader behind a full application queue. That
		// would hide disconnect/ping frames and recreate the admission deadlock.
		err = NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "websocket pending message buffer full", nil)
		r.cancel(err)
		_ = r.conn.Close(coderws.StatusPolicyViolation, "websocket pending message buffer full")
		_ = r.conn.CloseNow()
		return
	}
}

func (r *openAIWSClientReader) consume(result openAIWSClientReadResult) openAIWSClientReadResult {
	r.mu.Lock()
	r.buffered -= int64(len(result.payload))
	r.mu.Unlock()
	return result
}
