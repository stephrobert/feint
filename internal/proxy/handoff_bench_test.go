package proxy

import (
	"bytes"
	"fmt"
	"testing"
)

// These benchmarks answer one question: what does scanning every response body
// for absolute URLs cost on the request path? The claim "negligible on a
// terraform apply" was a guess when handoff.go landed, and this repository does
// not ship guesses. The measured numbers live in the comment on
// [hostsElsewhere]; re-run with
//
//	go test ./internal/proxy/ -bench BenchmarkHostsElsewhere -benchmem
//
// and update that comment if the implementation changes.
//
// Three sizes, because the input is bounded: a few kilobytes is what one API
// answer weighs (the shape below is a Scaleway server object), 64 KiB is a large
// list page, and 1 MiB is the hard cap — DefaultMaxBody truncates anything
// bigger before the scan ever sees it. Each size runs twice: the common case
// where no body names another host, and the finding case where every element
// carries an endpoint elsewhere, which costs more because each hit is parsed by
// url.Parse.

// benchBody builds a JSON list of about `size` bytes out of realistic server
// objects. With elsewhere set, each object carries the in-band endpoint that
// made this scan exist (Exoscale's api-endpoint in GET /v2/zone).
func benchBody(size int, elsewhere bool) []byte {
	endpoint := ""
	if elsewhere {
		endpoint = `"api-endpoint":"https://api-ch-gva-2.exoscale.com",`
	}
	var b bytes.Buffer
	b.WriteString(`{"servers":[`)
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b,
			`{"id":"11111111-1111-1111-1111-%012d","name":"conformance-%d",`+
				`"state":"running","commercial_type":"DEV1-S",%s`+
				`"public_ips":[{"address":"51.15.0.%d"}],"tags":["a","b"],"zone":"fr-par-1"}`,
			i, i, endpoint, i%254+1)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func BenchmarkHostsElsewhere(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10, 1 << 20} {
		for _, elsewhere := range []bool{false, true} {
			label := "clean"
			if elsewhere {
				label = "handoffs"
			}
			body := benchBody(size, elsewhere)
			b.Run(fmt.Sprintf("%dKiB/%s", size>>10, label), func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for range b.N {
					hostsElsewhere("127.0.0.1:4701", body)
				}
			})
		}
	}
}
