package electrum

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"time"

	"golang.org/x/net/proxy"
)

// ConnectionState : Known connection state values
type ConnectionState string

// Connection state flags
const (
	Ready        ConnectionState = "READY"
	Disconnected ConnectionState = "DISCONNECTED"
	Reconnecting ConnectionState = "RECONNECTING"
	Reconnected  ConnectionState = "RECONNECTED"
	Closed       ConnectionState = "CLOSED"
)

type transport struct {
	conn     net.Conn
	messages chan []byte
	errors   chan error
	done     chan bool
	ready    bool
	opts     *transportOptions
	state    chan ConnectionState
	r        *bufio.Reader
}

type transportOptions struct {
	address   string
	tls       *tls.Config
	timeout   uint64
	reconnect bool
	tor       string
}

// Get network connection
func connect(opts *transportOptions) (net.Conn, error) {
	var err error
	var conn net.Conn
	var dialer proxy.Dialer

	if opts.tor != "" {
		dialer, err = proxy.SOCKS5("tcp", opts.tor, nil, proxy.Direct)
		if err == nil {
			conn, err = dialer.Dial("tcp", opts.address)
		}
	} else if opts.timeout > 0 {
		conn, err = net.DialTimeout("tcp", opts.address, time.Duration(opts.timeout)*time.Second)
	} else {
		conn, err = net.Dial("tcp", opts.address)
	}
	if err != nil {
		return nil, err
	}
	conn.(*net.TCPConn).SetKeepAlive(true)
	conn.(*net.TCPConn).SetKeepAlivePeriod(30 * time.Second)

	if opts.tls != nil {
		return tls.Client(conn, opts.tls), nil
	}
	return conn, nil
}

// Initialize a proper handler for the underlying network connection
func getTransport(opts *transportOptions) (*transport, error) {
	conn, err := connect(opts)
	if err != nil {
		return nil, err
	}

	t := &transport{
		done:     make(chan bool),
		messages: make(chan []byte),
		errors:   make(chan error),
		state:    make(chan ConnectionState),
		opts:     opts,
	}
	t.setup(conn)
	go t.listen()
	return t, nil
}

// Prepare transport instance for usage with a given network connection
func (t *transport) setup(conn net.Conn) {
	t.conn = conn
	t.ready = true
	t.r = bufio.NewReader(t.conn)
}

// Attempt automatic reconnection
func (t *transport) reconnect() {
	t.conn.Close()
	t.ready = false
	t.state <- Reconnecting

	// Future implementations could include support for a max number of retries
	// and dynamically increasing the interval
	rt := time.NewTicker(5 * time.Second)
	go func() {
		defer rt.Stop()
		for range rt.C {
			conn, err := connect(t.opts)
			if err == nil {
				t.setup(conn)
				t.state <- Reconnected
				break
			}
		}
		go t.listen()
	}()
}

// Send raw bytes across the network
func (t *transport) sendMessage(message []byte) error {
	if !t.ready {
		return ErrUnreachableHost
	}
	//fmt.Printf("%+v", string(message))
	_, err := t.conn.Write(message)
	return err
}

// Finish execution and close network connection
func (t *transport) close() {
	close(t.done)
}

// Wait for new messages on the network connection until
// the instance is signaled to stop
func (t *transport) listen() {

	defer t.conn.Close() // buttha

	select { // buttha
	case t.state <- Ready:
	case <-time.After(time.Duration(t.opts.timeout) * time.Second):
		t.conn.Close()
		t.state <- Closed
		return
	}

LOOP:
	for {
		select {
		case <-t.done:
			t.conn.Close()
			t.state <- Closed
			break LOOP
		default:
			line, err := t.r.ReadBytes(delimiter)

			// Detect dropped connections
			if err == io.EOF {
				if t.opts.reconnect {
					// buttha: it keeps trying to connect forever. Too many ESTABLISHED connections: memory leak
					t.state <- Disconnected
					t.reconnect()
					break LOOP
				} else {
					t.conn.Close()
					t.state <- Closed
					break LOOP
				}
			}

			if err != nil {
				/* buttha
				t.errors <- err
				break
				*/
				t.errors <- err
				t.conn.Close()
				t.state <- Closed
				break LOOP
			}
			t.messages <- line
		}
	}
}
