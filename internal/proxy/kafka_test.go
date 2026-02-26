package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
)

func TestIsKafka(t *testing.T) {
	tests := []struct {
		name   string
		header []byte
		want   bool
	}{
		{
			name:   "valid metadata request",
			header: makeKafkaHeader(26, 3, 12), // frameLen=26, apiKey=Metadata(3), version=12
			want:   true,
		},
		{
			name:   "valid produce request",
			header: makeKafkaHeader(100, 0, 9), // apiKey=Produce(0)
			want:   true,
		},
		{
			name:   "valid fetch request",
			header: makeKafkaHeader(50, 1, 13), // apiKey=Fetch(1)
			want:   true,
		},
		{
			name:   "max valid api key",
			header: makeKafkaHeader(20, 74, 0),
			want:   true,
		},
		{
			name:   "api key too high",
			header: makeKafkaHeader(20, 75, 0),
			want:   false,
		},
		{
			name:   "frame length too small",
			header: makeKafkaHeader(7, 3, 0), // less than 8
			want:   false,
		},
		{
			name:   "frame length exactly 8",
			header: makeKafkaHeader(8, 3, 0),
			want:   true,
		},
		{
			name:   "too short header",
			header: []byte{0, 0, 0, 10, 0, 3},
			want:   false,
		},
		{
			name:   "empty header",
			header: []byte{},
			want:   false,
		},
		{
			name:   "HTTP GET request",
			header: []byte("GET /ind"),
			want:   false,
		},
		{
			name:   "TLS ClientHello",
			header: []byte{0x16, 0x03, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isKafka(tt.header); got != tt.want {
				t.Errorf("isKafka() = %v, want %v", got, tt.want)
			}
		})
	}
}

func makeKafkaHeader(frameLen uint32, apiKey, apiVersion int16) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:4], frameLen)
	binary.BigEndian.PutUint16(buf[4:6], uint16(apiKey))
	binary.BigEndian.PutUint16(buf[6:8], uint16(apiVersion))
	return buf
}

func TestRewriteMetadataResponseV0(t *testing.T) {
	// Build a v0 Metadata response with one broker:
	// correlation_id(4) + broker_count(4) + [node_id(4) + host(2+N) + port(4)]
	var payload bytes.Buffer
	binary.Write(&payload, binary.BigEndian, int32(42))  // correlation_id
	binary.Write(&payload, binary.BigEndian, int32(1))   // broker count
	binary.Write(&payload, binary.BigEndian, int32(0))   // node_id
	binary.Write(&payload, binary.BigEndian, int16(9))   // host length
	payload.WriteString("localhost")                      // host
	binary.Write(&payload, binary.BigEndian, int32(9092)) // port
	// trailing topic data (just a count of 0)
	binary.Write(&payload, binary.BigEndian, int32(0))

	rewritten, err := rewriteMetadataResponse(payload.Bytes(), 0, "127.0.0.5", 9092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)

	corrID, _ := r.int32()
	if corrID != 42 {
		t.Fatalf("correlation_id = %d, want 42", corrID)
	}

	brokerCount, _ := r.int32()
	if brokerCount != 1 {
		t.Fatalf("broker count = %d, want 1", brokerCount)
	}

	nodeID, _ := r.int32()
	if nodeID != 0 {
		t.Fatalf("node_id = %d, want 0", nodeID)
	}

	host, _ := r.string()
	if host != "127.0.0.5" {
		t.Fatalf("host = %q, want %q", host, "127.0.0.5")
	}

	port, _ := r.int32()
	if port != 9092 {
		t.Fatalf("port = %d, want 9092", port)
	}
}

func TestRewriteMetadataResponseV1(t *testing.T) {
	// v1 adds throttle_time_ms and rack (nullable string).
	var payload bytes.Buffer
	binary.Write(&payload, binary.BigEndian, int32(7))    // correlation_id
	binary.Write(&payload, binary.BigEndian, int32(0))    // throttle_time_ms
	binary.Write(&payload, binary.BigEndian, int32(1))    // broker count
	binary.Write(&payload, binary.BigEndian, int32(1))    // node_id
	binary.Write(&payload, binary.BigEndian, int16(15))   // host length
	payload.WriteString("kafka-broker-01")                // host
	binary.Write(&payload, binary.BigEndian, int32(9093)) // port
	binary.Write(&payload, binary.BigEndian, int16(-1))   // rack (null)
	// trailing data
	binary.Write(&payload, binary.BigEndian, int32(0)) // topic count

	rewritten, err := rewriteMetadataResponse(payload.Bytes(), 1, "127.0.0.10", 19092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)

	corrID, _ := r.int32()
	if corrID != 7 {
		t.Fatalf("correlation_id = %d, want 7", corrID)
	}

	throttle, _ := r.int32()
	if throttle != 0 {
		t.Fatalf("throttle = %d, want 0", throttle)
	}

	brokerCount, _ := r.int32()
	if brokerCount != 1 {
		t.Fatalf("broker count = %d, want 1", brokerCount)
	}

	nodeID, _ := r.int32()
	if nodeID != 1 {
		t.Fatalf("node_id = %d, want 1", nodeID)
	}

	host, _ := r.string()
	if host != "127.0.0.10" {
		t.Fatalf("host = %q, want %q", host, "127.0.0.10")
	}

	port, _ := r.int32()
	if port != 19092 {
		t.Fatalf("port = %d, want 19092", port)
	}

	rack, _ := r.nullableString()
	if rack != nil {
		t.Fatalf("rack = %v, want nil", rack)
	}
}

func TestRewriteMetadataResponseMultipleBrokers(t *testing.T) {
	var payload bytes.Buffer
	binary.Write(&payload, binary.BigEndian, int32(99))   // correlation_id
	binary.Write(&payload, binary.BigEndian, int32(0))    // throttle_time_ms
	binary.Write(&payload, binary.BigEndian, int32(3))    // broker count
	for i := int32(0); i < 3; i++ {
		binary.Write(&payload, binary.BigEndian, i)           // node_id
		binary.Write(&payload, binary.BigEndian, int16(6))    // host length
		payload.WriteString("broker")                         // host
		binary.Write(&payload, binary.BigEndian, int32(9092)) // port
		binary.Write(&payload, binary.BigEndian, int16(-1))   // rack (null)
	}
	binary.Write(&payload, binary.BigEndian, int32(0)) // topic count

	rewritten, err := rewriteMetadataResponse(payload.Bytes(), 1, "127.0.0.2", 29092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)
	r.int32() // correlation_id
	r.int32() // throttle
	brokerCount, _ := r.int32()
	if brokerCount != 3 {
		t.Fatalf("broker count = %d, want 3", brokerCount)
	}

	for i := 0; i < 3; i++ {
		r.int32() // node_id
		host, _ := r.string()
		if host != "127.0.0.2" {
			t.Fatalf("broker %d host = %q, want %q", i, host, "127.0.0.2")
		}
		port, _ := r.int32()
		if port != 29092 {
			t.Fatalf("broker %d port = %d, want 29092", i, port)
		}
		r.nullableString() // rack
	}
}

func TestCorrelationTracker(t *testing.T) {
	tracker := newCorrelationTracker()

	tracker.track(1, 3, 12)
	tracker.track(2, 0, 9)

	info, ok := tracker.lookup(1)
	if !ok {
		t.Fatal("expected to find correlation 1")
	}
	if info.apiKey != 3 || info.apiVersion != 12 {
		t.Fatalf("got apiKey=%d version=%d, want 3/12", info.apiKey, info.apiVersion)
	}

	// Second lookup should miss (consumed).
	_, ok = tracker.lookup(1)
	if ok {
		t.Fatal("expected correlation 1 to be consumed")
	}

	// Correlation 2 should still be there.
	info, ok = tracker.lookup(2)
	if !ok {
		t.Fatal("expected to find correlation 2")
	}
	if info.apiKey != 0 {
		t.Fatalf("got apiKey=%d, want 0", info.apiKey)
	}
}

func TestRelayKafkaRequests(t *testing.T) {
	// Build a single Kafka request frame: Metadata request (apiKey=3, version=12, correlationID=42).
	var frame bytes.Buffer
	reqPayload := make([]byte, 12)
	binary.BigEndian.PutUint16(reqPayload[0:2], 3)  // api_key
	binary.BigEndian.PutUint16(reqPayload[2:4], 12)  // api_version
	binary.BigEndian.PutUint32(reqPayload[4:8], 42)  // correlation_id
	binary.BigEndian.PutUint16(reqPayload[8:10], 0)  // client_id (null = length -1... use 0 length)
	binary.BigEndian.PutUint16(reqPayload[10:12], 0) // padding

	frameLen := make([]byte, 4)
	binary.BigEndian.PutUint32(frameLen, uint32(len(reqPayload)))
	frame.Write(frameLen)
	frame.Write(reqPayload)

	tracker := newCorrelationTracker()
	var dst bytes.Buffer

	relayKafkaRequests(&frame, &dst, tracker)

	// Verify the tracker recorded the correlation.
	info, ok := tracker.lookup(42)
	if !ok {
		t.Fatal("expected tracker to have correlation 42")
	}
	if info.apiKey != 3 || info.apiVersion != 12 {
		t.Fatalf("got apiKey=%d version=%d, want 3/12", info.apiKey, info.apiVersion)
	}

	// Verify the frame was forwarded verbatim.
	expected := append(frameLen, reqPayload...)
	if !bytes.Equal(dst.Bytes(), expected) {
		t.Fatal("forwarded frame does not match original")
	}
}

func TestRelayKafkaResponsesRewritesMetadata(t *testing.T) {
	tracker := newCorrelationTracker()
	tracker.track(42, kafkaAPIKeyMetadata, 0) // v0 metadata

	// Build a v0 Metadata response frame.
	var respPayload bytes.Buffer
	binary.Write(&respPayload, binary.BigEndian, int32(42))   // correlation_id
	binary.Write(&respPayload, binary.BigEndian, int32(1))    // broker count
	binary.Write(&respPayload, binary.BigEndian, int32(0))    // node_id
	binary.Write(&respPayload, binary.BigEndian, int16(9))    // host len
	respPayload.WriteString("localhost")                      // host
	binary.Write(&respPayload, binary.BigEndian, int32(9092)) // port
	binary.Write(&respPayload, binary.BigEndian, int32(0))    // topic count

	var frame bytes.Buffer
	frameLen := make([]byte, 4)
	binary.BigEndian.PutUint32(frameLen, uint32(respPayload.Len()))
	frame.Write(frameLen)
	frame.Write(respPayload.Bytes())

	var dst bytes.Buffer
	relayKafkaResponses(&frame, &dst, tracker, "127.0.0.5", 19092)

	// Parse the rewritten frame.
	result := dst.Bytes()
	if len(result) < 4 {
		t.Fatal("response too short")
	}
	newFrameLen := binary.BigEndian.Uint32(result[0:4])
	payload := result[4 : 4+newFrameLen]

	r := newKafkaReader(payload)
	corrID, _ := r.int32()
	if corrID != 42 {
		t.Fatalf("correlation_id = %d, want 42", corrID)
	}

	brokerCount, _ := r.int32()
	if brokerCount != 1 {
		t.Fatalf("broker count = %d, want 1", brokerCount)
	}

	r.int32() // node_id
	host, _ := r.string()
	if host != "127.0.0.5" {
		t.Fatalf("host = %q, want %q", host, "127.0.0.5")
	}
	port, _ := r.int32()
	if port != 19092 {
		t.Fatalf("port = %d, want 19092", port)
	}
}

func TestRelayKafkaResponsesPassthroughNonMetadata(t *testing.T) {
	tracker := newCorrelationTracker()
	tracker.track(10, 0, 9) // Produce request, not metadata

	var respPayload bytes.Buffer
	binary.Write(&respPayload, binary.BigEndian, int32(10)) // correlation_id
	respPayload.Write([]byte("somedata"))                   // opaque body

	var frame bytes.Buffer
	frameLen := make([]byte, 4)
	binary.BigEndian.PutUint32(frameLen, uint32(respPayload.Len()))
	frame.Write(frameLen)
	frame.Write(respPayload.Bytes())

	original := make([]byte, frame.Len())
	copy(original, frame.Bytes())

	var dst bytes.Buffer
	relayKafkaResponses(&frame, &dst, tracker, "127.0.0.5", 9092)

	// Should be forwarded unchanged.
	if !bytes.Equal(dst.Bytes(), original) {
		t.Fatalf("non-metadata response should be forwarded unchanged\ngot:  %x\nwant: %x", dst.Bytes(), original)
	}
}

func TestKafkaProxyIntegration(t *testing.T) {
	// Start a fake Kafka broker that responds to a Metadata request.
	broker, err := startFakeKafkaBroker(t, "real-broker", 9092)
	if err != nil {
		t.Fatal(err)
	}

	// Start proxy pointing at the fake broker.
	p := NewTCP("127.0.0.1", 0, broker.port)

	// Find an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	p = NewTCP("127.0.0.1", proxyPort, broker.port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	// Connect through the proxy and send a Metadata request.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Send Metadata request (apiKey=3, version=0, correlationID=1).
	reqPayload := make([]byte, 12)
	binary.BigEndian.PutUint16(reqPayload[0:2], 3) // api_key = Metadata
	binary.BigEndian.PutUint16(reqPayload[2:4], 0) // api_version = 0
	binary.BigEndian.PutUint32(reqPayload[4:8], 1) // correlation_id
	// client_id + topics (minimal)
	binary.BigEndian.PutUint16(reqPayload[8:10], 0)
	binary.BigEndian.PutUint16(reqPayload[10:12], 0)

	frameHdr := make([]byte, 4)
	binary.BigEndian.PutUint32(frameHdr, uint32(len(reqPayload)))
	conn.Write(frameHdr)
	conn.Write(reqPayload)

	// Read the response.
	respHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, respHdr); err != nil {
		t.Fatal(err)
	}
	respLen := binary.BigEndian.Uint32(respHdr)
	respPayload := make([]byte, respLen)
	if _, err := io.ReadFull(conn, respPayload); err != nil {
		t.Fatal(err)
	}

	// Parse response: correlation_id + broker_count + broker(node_id, host, port)
	r := newKafkaReader(respPayload)
	corrID, _ := r.int32()
	if corrID != 1 {
		t.Fatalf("correlation_id = %d, want 1", corrID)
	}
	brokerCount, _ := r.int32()
	if brokerCount != 1 {
		t.Fatalf("broker count = %d, want 1", brokerCount)
	}
	r.int32() // node_id
	host, _ := r.string()
	if host != "127.0.0.1" {
		t.Fatalf("rewritten host = %q, want %q", host, "127.0.0.1")
	}
	port, _ := r.int32()
	if port != int32(proxyPort) {
		t.Fatalf("rewritten port = %d, want %d", port, proxyPort)
	}
}

// fakeBroker is a minimal Kafka broker that responds to Metadata requests
// with a single broker entry.
type fakeBroker struct {
	port uint16
}

func startFakeKafkaBroker(t *testing.T, host string, port int32) (*fakeBroker, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { ln.Close() })

	actualPort := uint16(ln.Addr().(*net.TCPAddr).Port)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleFakeBrokerConn(conn, host, port)
		}
	}()

	return &fakeBroker{port: actualPort}, nil
}

func handleFakeBrokerConn(conn net.Conn, brokerHost string, brokerPort int32) {
	defer conn.Close()

	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		frameLen := binary.BigEndian.Uint32(hdr)
		payload := make([]byte, frameLen)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}

		if len(payload) < 8 {
			continue
		}
		apiKey := int16(binary.BigEndian.Uint16(payload[0:2]))
		correlationID := int32(binary.BigEndian.Uint32(payload[4:8]))

		if apiKey != kafkaAPIKeyMetadata {
			continue
		}

		// Build a v0 Metadata response.
		var resp bytes.Buffer
		binary.Write(&resp, binary.BigEndian, correlationID)        // correlation_id
		binary.Write(&resp, binary.BigEndian, int32(1))             // broker count
		binary.Write(&resp, binary.BigEndian, int32(0))             // node_id
		binary.Write(&resp, binary.BigEndian, int16(len(brokerHost))) // host len
		resp.WriteString(brokerHost)
		binary.Write(&resp, binary.BigEndian, brokerPort) // port
		binary.Write(&resp, binary.BigEndian, int32(0))   // topic count

		respHdr := make([]byte, 4)
		binary.BigEndian.PutUint32(respHdr, uint32(resp.Len()))
		conn.Write(respHdr)
		conn.Write(resp.Bytes())
	}
}
