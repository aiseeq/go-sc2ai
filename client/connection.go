package client

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/chippydip/go-sc2ai/api"
	"github.com/gogo/protobuf/proto"
	"github.com/gorilla/websocket"
)

type connection struct {
	Status api.Status

	urlStr  string
	timeout time.Duration

	conn     *websocket.Conn
	requests chan request
}

type request struct {
	*api.Request
	callback chan response
}

type response struct {
	*api.Response
	error
}

// MaxMessageSize is the largest protobuf message that can be sent without getting disconnected.
// The gorilla/websocket implementation fragments messages above it's write buffer size and the
// SC2 game doesn't seem to be able to deal with these messages. There is not a check in place
// to prevent large messages from being sent and warnings will be printed if a message size
// exceeds half of this limit. The default is now 2MB (up from 4kb) but can be overrided by
// modifying this value before connecting to SC2.
var MaxMessageSize = 2 * 1024 * 1024

// Connect ...
func (c *connection) Connect(address string, port int, timeout time.Duration) error {
	c.Status = api.Status_unknown

	// Save the connection info in case we need to re-connect
	c.urlStr = fmt.Sprintf("ws://%v:%v/sc2api", address, port)

	dialer := websocket.Dialer{WriteBufferSize: MaxMessageSize}
	conn, _, err := dialer.Dial(c.urlStr, nil)
	if err != nil {
		return err
	}
	c.conn = conn

	c.requests = make(chan request)

	c.conn.SetCloseHandler(func(code int, text string) error {
		//control.Error(ClientError_ConnectionClosed)
		close(c.requests)
		return nil
	})

	// Worker
	go func() {
		defer recoverPanic()

		for r := range c.requests {
			// Send
			data, err := proto.Marshal(r.Request)
			if len(data) > MaxMessageSize {
				err = fmt.Errorf("message too large: %v (max %v)", len(data), MaxMessageSize)
				fmt.Fprintln(os.Stderr, err)
				r.callback <- response{nil, err}
				continue
			} else if len(data) > MaxMessageSize/2 {
				fmt.Fprintln(os.Stderr, "warning, large message size:", len(data))
			}
			if err != nil {
				r.callback <- response{nil, err}
				continue
			}
			err = c.conn.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				r.callback <- response{nil, err}
				continue
			}

			// Receive
			_, data, err = c.conn.ReadMessage()
			if err != nil {
				r.callback <- response{nil, err}
				continue
			}

			resp := &api.Response{}
			err = proto.Unmarshal(data, resp)
			if err != nil {
				r.callback <- response{nil, err}
				continue
			}

			r.callback <- response{resp, c.onResponse(resp)}
		}
	}()

	_, err = c.ping(api.RequestPing{})()
	return err
}

func recoverPanic() {
	if p := recover(); p != nil {
		ReportPanic(p)
	}
}

// ReportPanic ...
func ReportPanic(p interface{}) {
	fmt.Fprintln(os.Stderr, p)

	// Nicer format than what debug.PrintStack() gives us
	var pc [32]uintptr
	n := runtime.Callers(3, pc[:]) // skip the defer, this func, and runtime.Callers
	for _, pc := range pc[:n] {
		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}
		file, line := fn.FileLine(pc)
		fmt.Fprintf(os.Stderr, "%v:%v in %v\n", file, line, fn.Name())
	}
}

func (c *connection) onResponse(r *api.Response) error {
	if r.Status != api.Status_nil {
		c.Status = r.Status
	}
	// for _, e := range r.Error {
	// 	// TODO: error callback
	// }
	if len(r.Error) > 0 {
		return fmt.Errorf("%v", r.Error)
	}
	return nil
}

func (c *connection) request(req *api.Request) func() (*api.Response, error) {
	out := make(chan response, 1)
loop:
	for {
		select {
		case c.requests <- request{req, out}:
			break loop
		case <-time.After(time.Second):
			fmt.Printf("waiting to send request %t\n", req)
		}
	}
	return func() (*api.Response, error) {
		for {
			select {
			case r := <-out:
				return r.Response, r.error
			case <-time.After(10 * time.Second):
				fmt.Printf("waiting for response %t\n", req)
			}
		}
	}
}
