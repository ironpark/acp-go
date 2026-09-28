package acpconn

import (
	"context"
	"encoding/json/jsontext"
	"net"
	"testing"
	"time"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// readingConn reports each read it starts.
type readingConn struct {
	net.Conn
	reading chan struct{}
}

func (c readingConn) Read(p []byte) (int, error) {
	select {
	case c.reading <- struct{}{}:
	default:
	}
	return c.Conn.Read(p)
}

// TestRunStopsABlockedRead: a stdio transport's read ignores cancellation, so
// stopping the connection must close the transport to end it. The peer here
// never writes or closes, like an agent that is still thinking.
func TestRunStopsABlockedRead(t *testing.T) {
	for _, stop := range []string{"Close", "ctx"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			local, remote := net.Pipe()
			defer remote.Close()
			reading := make(chan struct{}, 1)
			tr := jsonrpc.NewStdioTransport(readingConn{local, reading}, local)
			conn := jsonrpc.New(func(context.Context, string, jsontext.Value) (any, error) { return nil, nil },
				func(context.Context, string, jsontext.Value) error { return nil }, tr)
			wait := Run(ctx, conn, tr)
			<-reading // the read loop is past its last cancellation check

			stopped := make(chan error, 1)
			go func() {
				if stop == "Close" {
					_ = conn.Close()
				} else {
					cancel()
				}
				stopped <- wait()
			}()
			select {
			case err := <-stopped:
				if stop == "Close" && err != nil {
					t.Errorf("wait after Close = %v, want nil", err)
				}
				if stop == "ctx" && err == nil {
					t.Error("wait after ctx cancel = nil, want an error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("wait blocked on a read the connection could not cancel")
			}
		})
	}
}
