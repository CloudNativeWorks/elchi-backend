package syslog

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientUnsupportedProtocol(t *testing.T) {
	c := NewClient(Config{Protocol: "icmp", Host: "127.0.0.1", Port: 1})
	if err := c.Connect(); err == nil {
		t.Fatal("expected error for unsupported protocol")
	}
}

func TestClientTCPSendRoundtrip(t *testing.T) {
	srv, addr := startTCPEcho(t)
	defer srv.Close()

	host, port := splitHostPort(t, addr)
	c := NewClient(Config{Protocol: ProtocolTCP, Host: host, Port: port})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	if err := c.Send([]byte("<134>1 2026-05-10T00:00:00Z host elchi-audit - elchi-audit [audit@elchi action=\"X\"]")); err != nil {
		t.Fatalf("send: %v", err)
	}

	got := srv.waitForMessage(2 * time.Second)
	if !strings.Contains(got, "<134>1") {
		t.Errorf("server did not receive expected message, got=%q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected newline framing for TCP, got=%q", got)
	}
}

func TestClientUDPDoesNotAppendNewline(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	host, port := splitHostPort(t, pc.LocalAddr().String())
	c := NewClient(Config{Protocol: ProtocolUDP, Host: host, Port: port})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	payload := []byte("<134>1 hello")
	if err := c.Send(payload); err != nil {
		t.Fatalf("send: %v", err)
	}

	buf := make([]byte, 1024)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("expected raw payload, got %q", string(buf[:n]))
	}
}

func TestClientLazyDialAfterClose(t *testing.T) {
	srv, addr := startTCPEcho(t)
	defer srv.Close()
	host, port := splitHostPort(t, addr)

	c := NewClient(Config{Protocol: ProtocolTCP, Host: host, Port: port})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Send([]byte("<134>1 first")); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if got := srv.waitForMessage(2 * time.Second); !strings.Contains(got, "first") {
		t.Fatalf("first message not received: got=%q", got)
	}

	// Drop the underlying connection. Next Send must dial again on its own.
	c.Close()

	if err := c.Send([]byte("<134>1 second")); err != nil {
		t.Fatalf("send after close should redial: %v", err)
	}
	if got := srv.waitForMessage(2 * time.Second); !strings.Contains(got, "second") {
		t.Errorf("second message not received: got=%q", got)
	}
}

// --- helpers ---

type tcpEcho struct {
	ln       net.Listener
	mu       sync.Mutex
	messages []string
	signal   chan struct{}
}

func startTCPEcho(t *testing.T) (*tcpEcho, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	e := &tcpEcho{ln: ln, signal: make(chan struct{}, 32)}
	go e.acceptLoop()
	return e, ln.Addr().String()
}

func (e *tcpEcho) acceptLoop() {
	for {
		conn, err := e.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 4096)
			for {
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				n, err := c.Read(buf)
				if n > 0 {
					e.mu.Lock()
					e.messages = append(e.messages, string(buf[:n]))
					e.mu.Unlock()
					select {
					case e.signal <- struct{}{}:
					default:
					}
				}
				if err != nil {
					return
				}
			}
		}(conn)
	}
}

func (e *tcpEcho) waitForMessage(d time.Duration) string {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case <-e.signal:
		case <-time.After(50 * time.Millisecond):
		}
		e.mu.Lock()
		if len(e.messages) > 0 {
			out := e.messages[len(e.messages)-1]
			e.mu.Unlock()
			return out
		}
		e.mu.Unlock()
	}
	return ""
}

func (e *tcpEcho) Close() { _ = e.ln.Close() }

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	return host, port
}
