package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// Kafka wire protocol constants.
const (
	kafkaAPIKeyMetadata = 3
	kafkaMaxFrameSize   = 256 * 1024 * 1024 // 256 MB
	kafkaMaxAPIKey      = 74
)

// isKafka checks whether the first 8 bytes look like a Kafka request frame.
// A Kafka request starts with: frame_length(4) + api_key(2) + api_version(2).
func isKafka(header []byte) bool {
	if len(header) < 8 {
		return false
	}
	frameLen := binary.BigEndian.Uint32(header[0:4])
	apiKey := int16(binary.BigEndian.Uint16(header[4:6]))
	if frameLen < 8 || frameLen > kafkaMaxFrameSize {
		return false
	}
	return apiKey >= 0 && apiKey <= kafkaMaxAPIKey
}

// apiInfo tracks the API key and version for a correlated request/response pair.
type apiInfo struct {
	apiKey     int16
	apiVersion int16
}

// correlationTracker maps correlation IDs to their request API key and version.
type correlationTracker struct {
	mu sync.Mutex
	m  map[int32]apiInfo
}

func newCorrelationTracker() *correlationTracker {
	return &correlationTracker{m: make(map[int32]apiInfo)}
}

func (t *correlationTracker) track(correlationID int32, key, version int16) {
	t.mu.Lock()
	t.m[correlationID] = apiInfo{apiKey: key, apiVersion: version}
	t.mu.Unlock()
}

func (t *correlationTracker) lookup(correlationID int32) (apiInfo, bool) {
	t.mu.Lock()
	info, ok := t.m[correlationID]
	if ok {
		delete(t.m, correlationID)
	}
	t.mu.Unlock()
	return info, ok
}

// relayKafkaRequests reads Kafka request frames from src, records
// (correlation_id → api_key, api_version) in the tracker, and forwards
// the complete frame unchanged to dst.
func relayKafkaRequests(src io.Reader, dst io.Writer, tracker *correlationTracker) {
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(src, hdr); err != nil {
			return
		}
		frameLen := binary.BigEndian.Uint32(hdr)
		if frameLen > kafkaMaxFrameSize {
			return
		}

		payload := make([]byte, frameLen)
		if _, err := io.ReadFull(src, payload); err != nil {
			return
		}

		// Parse request header: api_key(2) + api_version(2) + correlation_id(4).
		if len(payload) >= 8 {
			apiKey := int16(binary.BigEndian.Uint16(payload[0:2]))
			apiVersion := int16(binary.BigEndian.Uint16(payload[2:4]))
			correlationID := int32(binary.BigEndian.Uint32(payload[4:8]))
			tracker.track(correlationID, apiKey, apiVersion)
		}

		if _, err := dst.Write(hdr); err != nil {
			return
		}
		if _, err := dst.Write(payload); err != nil {
			return
		}
	}
}

// relayKafkaResponses reads Kafka response frames from src, checks the
// correlation tracker to identify Metadata responses, rewrites broker
// addresses in those responses, and forwards everything to dst.
func relayKafkaResponses(src io.Reader, dst io.Writer, tracker *correlationTracker, proxyHost string, proxyPort int32) {
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(src, hdr); err != nil {
			return
		}
		frameLen := binary.BigEndian.Uint32(hdr)
		if frameLen > kafkaMaxFrameSize {
			return
		}

		payload := make([]byte, frameLen)
		if _, err := io.ReadFull(src, payload); err != nil {
			return
		}

		if len(payload) < 4 {
			dst.Write(hdr)
			dst.Write(payload)
			continue
		}

		correlationID := int32(binary.BigEndian.Uint32(payload[0:4]))
		info, ok := tracker.lookup(correlationID)

		if !ok || info.apiKey != kafkaAPIKeyMetadata {
			if _, err := dst.Write(hdr); err != nil {
				return
			}
			if _, err := dst.Write(payload); err != nil {
				return
			}
			continue
		}

		// Rewrite Metadata response.
		rewritten, err := rewriteMetadataResponse(payload, info.apiVersion, proxyHost, proxyPort)
		if err != nil {
			slog.Debug("kafka metadata rewrite failed, forwarding original", "err", err)
			dst.Write(hdr)
			dst.Write(payload)
			continue
		}

		newHdr := make([]byte, 4)
		binary.BigEndian.PutUint32(newHdr, uint32(len(rewritten)))
		if _, err := dst.Write(newHdr); err != nil {
			return
		}
		if _, err := dst.Write(rewritten); err != nil {
			return
		}
	}
}

// rewriteMetadataResponse parses a Metadata response payload and rewrites
// each broker's host and port to point at the proxy.
func rewriteMetadataResponse(payload []byte, version int16, proxyHost string, proxyPort int32) ([]byte, error) {
	flexible := version >= 9
	r := newKafkaReader(payload)
	w := newKafkaWriter()

	// Response header: correlation_id.
	correlationID, err := r.int32()
	if err != nil {
		return nil, err
	}
	w.writeInt32(correlationID)

	// Flexible versions have a tagged field section in the response header.
	if flexible {
		tagBuf, err := r.tagBuffer()
		if err != nil {
			return nil, err
		}
		w.writeTagBuffer(tagBuf)
	}

	// v1+: throttle_time_ms.
	if version >= 1 {
		throttle, err := r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(throttle)
	}

	// Brokers array.
	var brokerCount int
	if flexible {
		n, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			w.writeUvarint(0)
			w.writeRaw(r.remaining())
			return w.bytes(), nil
		}
		brokerCount = int(n) - 1
		w.writeUvarint(n)
	} else {
		n, err := r.int32()
		if err != nil {
			return nil, err
		}
		brokerCount = int(n)
		w.writeInt32(n)
	}

	for i := 0; i < brokerCount; i++ {
		// node_id
		nodeID, err := r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(nodeID)

		// host — rewrite to proxy host
		if flexible {
			_, err = r.compactString()
		} else {
			_, err = r.string()
		}
		if err != nil {
			return nil, err
		}
		if flexible {
			w.writeCompactString(proxyHost)
		} else {
			w.writeString(proxyHost)
		}

		// port — rewrite to proxy port
		_, err = r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(proxyPort)

		// rack (v1+): nullable string
		if version >= 1 {
			if flexible {
				rack, err := r.compactNullableString()
				if err != nil {
					return nil, err
				}
				w.writeCompactNullableString(rack)
			} else {
				rack, err := r.nullableString()
				if err != nil {
					return nil, err
				}
				w.writeNullableString(rack)
			}
		}

		// Flexible: trailing tag buffer per broker struct.
		if flexible {
			tagBuf, err := r.tagBuffer()
			if err != nil {
				return nil, err
			}
			w.writeTagBuffer(tagBuf)
		}
	}

	// Copy remaining bytes verbatim.
	w.writeRaw(r.remaining())

	return w.bytes(), nil
}

// kafkaReader reads Kafka wire protocol primitives from a byte slice.
type kafkaReader struct {
	buf []byte
	pos int
}

func newKafkaReader(buf []byte) *kafkaReader {
	return &kafkaReader{buf: buf}
}

func (r *kafkaReader) need(n int) error {
	if r.pos+n > len(r.buf) {
		return fmt.Errorf("kafka: short read at offset %d, need %d bytes, have %d", r.pos, n, len(r.buf)-r.pos)
	}
	return nil
}

func (r *kafkaReader) int16() (int16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := int16(binary.BigEndian.Uint16(r.buf[r.pos:]))
	r.pos += 2
	return v, nil
}

func (r *kafkaReader) int32() (int32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := int32(binary.BigEndian.Uint32(r.buf[r.pos:]))
	r.pos += 4
	return v, nil
}

func (r *kafkaReader) string() (string, error) {
	length, err := r.int16()
	if err != nil {
		return "", err
	}
	if length < 0 {
		return "", fmt.Errorf("kafka: unexpected null string")
	}
	n := int(length)
	if err := r.need(n); err != nil {
		return "", err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

func (r *kafkaReader) nullableString() (*string, error) {
	length, err := r.int16()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}
	n := int(length)
	if err := r.need(n); err != nil {
		return nil, err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return &s, nil
}

func (r *kafkaReader) compactString() (string, error) {
	length, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", fmt.Errorf("kafka: unexpected null compact string")
	}
	n := int(length) - 1
	if err := r.need(n); err != nil {
		return "", err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

func (r *kafkaReader) compactNullableString() (*string, error) {
	length, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil
	}
	n := int(length) - 1
	if err := r.need(n); err != nil {
		return nil, err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return &s, nil
}

func (r *kafkaReader) uvarint() (uint64, error) {
	var result uint64
	var shift uint
	for {
		if r.pos >= len(r.buf) {
			return 0, fmt.Errorf("kafka: short read in uvarint")
		}
		b := r.buf[r.pos]
		r.pos++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, fmt.Errorf("kafka: uvarint overflow")
		}
	}
}

func (r *kafkaReader) tagBuffer() ([]byte, error) {
	startPos := r.pos
	numTags, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < numTags; i++ {
		if _, err := r.uvarint(); err != nil {
			return nil, err
		}
		size, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		if err := r.need(int(size)); err != nil {
			return nil, err
		}
		r.pos += int(size)
	}
	return r.buf[startPos:r.pos], nil
}

func (r *kafkaReader) remaining() []byte {
	if r.pos >= len(r.buf) {
		return nil
	}
	return r.buf[r.pos:]
}

// kafkaWriter builds Kafka wire protocol byte sequences.
type kafkaWriter struct {
	buf []byte
}

func newKafkaWriter() *kafkaWriter {
	return &kafkaWriter{}
}

func (w *kafkaWriter) writeInt16(v int16) {
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(v))
}

func (w *kafkaWriter) writeInt32(v int32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v))
}

func (w *kafkaWriter) writeString(s string) {
	w.writeInt16(int16(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *kafkaWriter) writeNullableString(s *string) {
	if s == nil {
		w.writeInt16(-1)
		return
	}
	w.writeString(*s)
}

func (w *kafkaWriter) writeCompactString(s string) {
	w.writeUvarint(uint64(len(s)) + 1)
	w.buf = append(w.buf, s...)
}

func (w *kafkaWriter) writeCompactNullableString(s *string) {
	if s == nil {
		w.writeUvarint(0)
		return
	}
	w.writeCompactString(*s)
}

func (w *kafkaWriter) writeUvarint(v uint64) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	w.buf = append(w.buf, buf[:n]...)
}

func (w *kafkaWriter) writeTagBuffer(raw []byte) {
	w.buf = append(w.buf, raw...)
}

func (w *kafkaWriter) writeRaw(data []byte) {
	w.buf = append(w.buf, data...)
}

func (w *kafkaWriter) bytes() []byte {
	return w.buf
}
