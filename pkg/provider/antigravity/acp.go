package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// rpcConn is a JSON-RPC 2.0 connection over newline-delimited stdio, the ACP
// transport. Both sides send requests: moa calls initialize/session/*, agy
// calls session/request_permission. Notifications (session/update) and
// incoming requests are handed to onMessage in arrival order.
type rpcConn struct {
	w      io.Writer
	wmu    sync.Mutex
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  error

	onMessage func(rpcMessage)
	done      chan struct{}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("%s (code %d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

var errConnClosed = errors.New("antigravity: agy acp exited")

func newRPCConn(r io.Reader, w io.Writer, onMessage func(rpcMessage)) *rpcConn {
	c := &rpcConn{w: w, pending: map[int64]chan rpcResponse{}, onMessage: onMessage, done: make(chan struct{})}
	go c.readLoop(r)
	return c
}

func (c *rpcConn) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			slog.Warn("antigravity: malformed ACP line", "error", err)
			continue
		}
		if m.Method == "" && len(m.ID) > 0 {
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				if m.Error != nil {
					ch <- rpcResponse{err: m.Error}
				} else {
					ch <- rpcResponse{result: m.Result}
				}
			}
			continue
		}
		c.onMessage(m)
	}
	err := sc.Err()
	if err == nil {
		err = errConnClosed
	}
	c.mu.Lock()
	c.closed = err
	for id, ch := range c.pending {
		ch <- rpcResponse{err: err}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

func (c *rpcConn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for its response, or for ctx.
func (c *rpcConn) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	if c.closed != nil {
		c.mu.Unlock()
		return c.closed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case resp := <-ch:
		if resp.err != nil {
			return resp.err
		}
		if result != nil && len(resp.result) > 0 {
			return json.Unmarshal(resp.result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

func (c *rpcConn) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *rpcConn) reply(id json.RawMessage, result any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *rpcConn) replyError(id json.RawMessage, code int, msg string) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}
