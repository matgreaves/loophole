package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

// Proxy defines the interface for port proxies.
type Proxy interface {
	Start(ctx context.Context) error
	Stop()
	ListenAddr() string
	ListenPort() uint16
	TargetPort() uint16
}

// TCPProxy forwards TCP connections from listenAddr:listenPort to 127.0.0.1:targetPort.
type TCPProxy struct {
	listenAddr string
	listenPort uint16
	targetPort uint16

	listener net.Listener
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewTCP(listenAddr string, listenPort, targetPort uint16) *TCPProxy {
	return &TCPProxy{
		listenAddr: listenAddr,
		listenPort: listenPort,
		targetPort: targetPort,
	}
}

func (p *TCPProxy) ListenAddr() string { return p.listenAddr }
func (p *TCPProxy) ListenPort() uint16 { return p.listenPort }
func (p *TCPProxy) TargetPort() uint16 { return p.targetPort }

// Start begins listening and forwarding connections.
func (p *TCPProxy) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", p.listenAddr, p.listenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy listen %s: %w", addr, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	p.listener = ln
	p.cancel = cancel

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.acceptLoop(ctx)
	}()

	slog.Info("proxy started", "listen", addr, "target", fmt.Sprintf("127.0.0.1:%d", p.targetPort))
	return nil
}

// Stop closes the listener and waits for active connections to finish.
func (p *TCPProxy) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.listener != nil {
		p.listener.Close()
	}
	p.wg.Wait()
}

func (p *TCPProxy) acceptLoop(ctx context.Context) {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Error("proxy accept error", "err", err)
				return
			}
		}

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handleConn(ctx, conn)
		}()
	}
}

func (p *TCPProxy) handleConn(ctx context.Context, src net.Conn) {
	defer src.Close()

	target := fmt.Sprintf("127.0.0.1:%d", p.targetPort)
	dst, err := net.Dial("tcp", target)
	if err != nil {
		slog.Error("proxy dial failed", "target", target, "err", err)
		return
	}
	defer dst.Close()

	// Sniff the first 8 bytes to detect Kafka wire protocol.
	header := make([]byte, 8)
	n, err := io.ReadFull(src, header)
	if err != nil {
		// Connection closed or too short — forward whatever we got and bail.
		if n > 0 {
			dst.Write(header[:n])
		}
		return
	}

	if isKafka(header) {
		slog.Info("kafka protocol detected", "listen", fmt.Sprintf("%s:%d", p.listenAddr, p.listenPort))
		p.handleKafkaConn(ctx, src, dst, header)
		return
	}

	// Not Kafka — plain bidirectional copy with sniffed bytes prepended.
	srcReader := io.MultiReader(bytes.NewReader(header), src)

	done := make(chan struct{})

	go func() {
		io.Copy(dst, srcReader)
		dst.(*net.TCPConn).CloseWrite()
		done <- struct{}{}
	}()

	go func() {
		io.Copy(src, dst)
		src.(*net.TCPConn).CloseWrite()
		done <- struct{}{}
	}()

	// Wait for both directions or context cancellation.
	select {
	case <-done:
		<-done
	case <-ctx.Done():
	}
}

func (p *TCPProxy) handleKafkaConn(ctx context.Context, src, dst net.Conn, header []byte) {
	tracker := newCorrelationTracker()
	proxyHost := p.listenAddr
	proxyPort := int32(p.listenPort)

	// Prepend the sniffed header bytes to the client reader.
	srcReader := io.MultiReader(bytes.NewReader(header), src)

	done := make(chan struct{})

	go func() {
		relayKafkaRequests(srcReader, dst, tracker)
		dst.(*net.TCPConn).CloseWrite()
		done <- struct{}{}
	}()

	go func() {
		relayKafkaResponses(dst, src, tracker, proxyHost, proxyPort)
		src.(*net.TCPConn).CloseWrite()
		done <- struct{}{}
	}()

	select {
	case <-done:
		<-done
	case <-ctx.Done():
	}
}
