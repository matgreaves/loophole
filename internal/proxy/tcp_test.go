package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func startEchoServer(t *testing.T) (port uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

func TestTCPProxyForwards(t *testing.T) {
	echoPort := startEchoServer(t)

	p := NewTCP("127.0.0.1", 0, echoPort)

	// Use port 0 to get an ephemeral port; we need to find the actual port after Start.
	// Instead, let's pick a specific high port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	p = NewTCP("127.0.0.1", listenPort, echoPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	// Connect through the proxy
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	msg := "hello loophole"
	conn.Write([]byte(msg))
	conn.(*net.TCPConn).CloseWrite()

	buf, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", string(buf), msg)
	}
}

func TestTCPProxyStop(t *testing.T) {
	echoPort := startEchoServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	p := NewTCP("127.0.0.1", listenPort, echoPort)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}

	p.Stop()

	// Listener should be closed
	_, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected connection to fail after stop")
	}
}

func TestTCPProxyPortInUse(t *testing.T) {
	// Occupy a port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	p := NewTCP("127.0.0.1", port, 9999)

	ctx := context.Background()
	if err := p.Start(ctx); err == nil {
		p.Stop()
		t.Fatal("expected error when port in use")
	}
}
