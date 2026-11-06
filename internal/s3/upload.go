package s3

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"strconv"
	"strings"
)

var errUpload = errors.New("invalid upload")

// Validation must finish before EOF reaches ObjectStore: a checksum failure
// returned after PutObject would be too late to prevent metadata publication.
type uploadReader struct {
	body     io.Reader
	framing  *bufio.Reader
	crc      hash.Hash32
	sha      hash.Hash
	expected map[string]string
	trailers map[string]bool
	done     bool
}

func newUpload(w http.ResponseWriter, r *http.Request, max int64) (*uploadReader, int64, error) {
	for _, name := range []string{"Content-Type", "Content-Encoding", "X-Amz-Content-Sha256",
		"X-Amz-Decoded-Content-Length", "X-Amz-Trailer", "X-Amz-Sdk-Checksum-Algorithm"} {
		if len(r.Header.Values(name)) > 1 {
			return nil, 0, fmt.Errorf("%w: ambiguous upload header %s", errUpload, name)
		}
	}
	size := r.ContentLength
	encoding := r.Header.Get("Content-Encoding")
	chunked := encoding == "aws-chunked"
	if encoding != "" && !chunked {
		return nil, 0, fmt.Errorf("%w: unsupported content encoding", errUpload)
	}
	if !chunked && r.Header.Get("X-Amz-Decoded-Content-Length") != "" {
		return nil, 0, fmt.Errorf("%w: decoded size requires aws-chunked encoding", errUpload)
	}
	if chunked {
		var err error
		size, err = strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: exact decoded content length is required", errUpload)
		}
	}
	if size < 0 || size > max {
		return nil, 0, fmt.Errorf("%w: decoded upload exceeds the byte bound or has no exact size", errUpload)
	}
	u := &uploadReader{
		body: http.MaxBytesReader(w, r.Body, max*2+(64<<10)),
		crc:  crc32.NewIEEE(), sha: sha256.New(),
		expected: make(map[string]string), trailers: make(map[string]bool),
	}
	for _, name := range []string{"X-Amz-Checksum-Crc32", "X-Amz-Checksum-Sha256"} {
		if values := r.Header.Values(name); len(values) != 0 {
			if len(values) != 1 || !checksumValue(name, values[0]) {
				return nil, 0, fmt.Errorf("%w: invalid checksum header", errUpload)
			}
			u.expected[name] = values[0]
		}
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	switch payload {
	case "", "UNSIGNED-PAYLOAD":
	case "STREAMING-UNSIGNED-PAYLOAD-TRAILER", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
		"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER":
		if !chunked {
			return nil, 0, fmt.Errorf("%w: streaming payload requires aws-chunked encoding", errUpload)
		}
	default:
		digest, err := hex.DecodeString(payload)
		if err != nil || len(digest) != sha256.Size || chunked {
			return nil, 0, fmt.Errorf("%w: invalid payload hash", errUpload)
		}
		value := base64.StdEncoding.EncodeToString(digest)
		if want := u.expected["X-Amz-Checksum-Sha256"]; want != "" && want != value {
			return nil, 0, fmt.Errorf("%w: conflicting SHA256 checksum headers", errUpload)
		}
		u.expected["X-Amz-Checksum-Sha256"] = value
	}
	algorithm := r.Header.Get("X-Amz-Sdk-Checksum-Algorithm")
	if algorithm != "" && algorithm != "CRC32" && algorithm != "SHA256" {
		return nil, 0, fmt.Errorf("%w: only CRC32 and SHA256 checksums are implemented", errUpload)
	}
	if declared := r.Header.Get("X-Amz-Trailer"); declared != "" {
		if !chunked {
			return nil, 0, fmt.Errorf("%w: checksum trailers require aws-chunked encoding", errUpload)
		}
		for _, raw := range strings.Split(declared, ",") {
			name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(raw))
			if (name != "X-Amz-Checksum-Crc32" && name != "X-Amz-Checksum-Sha256") || u.trailers[name] || u.expected[name] != "" {
				return nil, 0, fmt.Errorf("%w: unsupported or ambiguous checksum trailer", errUpload)
			}
			u.trailers[name] = true
		}
	}
	if algorithm != "" {
		name := "X-Amz-Checksum-Crc32"
		if algorithm == "SHA256" {
			name = "X-Amz-Checksum-Sha256"
		}
		if u.expected[name] == "" && !u.trailers[name] {
			return nil, 0, fmt.Errorf("%w: declared checksum algorithm has no checksum", errUpload)
		}
	}
	if chunked {
		u.framing = bufio.NewReader(u.body)
		u.body = httputil.NewChunkedReader(u.framing)
	}
	return u, size, nil
}

func (u *uploadReader) Read(p []byte) (int, error) {
	if u.done {
		return 0, io.EOF
	}
	n, err := u.body.Read(p)
	// The standard hash implementations cannot fail their Write.
	if _, hashErr := u.crc.Write(p[:n]); hashErr != nil {
		return n, hashErr
	}
	if _, hashErr := u.sha.Write(p[:n]); hashErr != nil {
		return n, hashErr
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("%w: decode payload: %w", errUpload, err)
	}
	if errors.Is(err, io.EOF) {
		u.done = true
		if err := u.finish(); err != nil {
			return n, err
		}
	}
	return n, err
}

func (u *uploadReader) finish() error {
	if u.framing != nil {
		tail, err := io.ReadAll(io.LimitReader(u.framing, (8<<10)+1))
		if err != nil {
			return fmt.Errorf("%w: read checksum trailers: %w", errUpload, err)
		}
		if len(tail) > 8<<10 || (string(tail) != "\r\n" && !bytes.HasSuffix(tail, []byte("\r\n\r\n"))) {
			return fmt.Errorf("%w: oversized or incomplete checksum trailers", errUpload)
		}
		reader := bufio.NewReader(bytes.NewReader(tail))
		headers, err := textproto.NewReader(reader).ReadMIMEHeader()
		if err != nil || reader.Buffered() != 0 || len(headers) != len(u.trailers) {
			return fmt.Errorf("%w: invalid checksum trailers", errUpload)
		}
		for name, values := range headers {
			if !u.trailers[name] || len(values) != 1 || !checksumValue(name, values[0]) {
				return fmt.Errorf("%w: undeclared or invalid checksum trailer", errUpload)
			}
			u.expected[name] = values[0]
		}
	}
	for name, want := range u.expected {
		got := u.crc32()
		if name == "X-Amz-Checksum-Sha256" {
			got = base64.StdEncoding.EncodeToString(u.sha.Sum(nil))
		}
		if got != want {
			return fmt.Errorf("%w: checksum mismatch", errUpload)
		}
	}
	return nil
}

func (u *uploadReader) crc32() string {
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], u.crc.Sum32())
	return base64.StdEncoding.EncodeToString(sum[:])
}

func checksumValue(name, value string) bool {
	sum, err := base64.StdEncoding.DecodeString(value)
	size := 4
	if name == "X-Amz-Checksum-Sha256" {
		size = sha256.Size
	}
	return err == nil && len(sum) == size
}
