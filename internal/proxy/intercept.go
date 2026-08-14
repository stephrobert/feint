package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"crypto/tls"
)

// Interceptor is the certificate half of DNS/TLS interception (#76), minted from
// the standard library alone and nothing else.
//
// It is what lets a real client be redirected to this proxy by *name* rather
// than by configuration. A client whose endpoint is hardcoded, or one handed a
// republished address in a response body (Exoscale's `GET /v2/zone`, which is
// what defeated the plain proxy in #92), does not read SCW_API_URL or an
// endpoint file for that call: it resolves the name and connects. Two knobs,
// both measured in docs/limits.md, make it land here instead:
//
//   - the name resolves to loopback, done in a network namespace the client owns
//     — a container's own /etc/hosts, never the operator's — so it is disposable
//     and leaves no trace on the machine;
//   - the client trusts the leaf this mints, done with SSL_CERT_FILE pointed at
//     [Interceptor.WriteCA]'s output, which every Go client (scw, exo, and the
//     Terraform plugin, inheriting the parent's environment) honours.
//
// The safety argument of the #76 section rests entirely on that scoping. A
// process that trusts this CA *and* resolves a real cloud hostname to loopback
// is exactly how a real `terraform apply` silently hits a local emulator, so the
// redirect must be as narrow as the certificate. This type helps keep it narrow:
// the CA is minted to a caller-supplied path (a namespace's temporary file), the
// leaf is short-lived and not itself a CA (it cannot be repurposed as a trust
// anchor), and neither half is ever installed into the operator's system store.
type Interceptor struct {
	tlsConfig *tls.Config
	caPEM     []byte
	pool      *x509.CertPool
	hosts     []string
}

// leafValidity bounds how long a minted leaf is accepted.
//
// A recording session is minutes; a day is generous and still short enough that
// a certificate forgotten inside a disposed namespace cannot quietly become a
// durable trust anchor. This is the bound TestAMintedLeafIsShortLivedAndNotACA
// pins: raise it to a year or mark the leaf IsCA and that test fails.
const leafValidity = 24 * time.Hour

// MintInterceptor builds a fresh CA and one leaf covering hosts.
//
// Plain names (`api-ch-gva-2.exoscale.com`) and single-level wildcards
// (`*.s3.fr-par.scw.cloud`) are both accepted, which is the exact pair the #76
// measurement found the Scaleway S3 case needs.
//
// It refuses an empty host set, and an empty name within it: a certificate
// covering nothing redirects nothing, and returning one would hand back a proxy
// that answers TLS for no name the caller asked about — the silent no-op this
// tool exists to avoid. TestMintInterceptorRefusesNoHost fails without the guard.
func MintInterceptor(hosts ...string) (*Interceptor, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no host to intercept: a certificate covering nothing redirects nothing")
	}
	for _, h := range hosts {
		if h == "" {
			return nil, fmt.Errorf("an empty host name cannot be intercepted")
		}
	}

	notBefore := time.Now().Add(-time.Minute)
	notAfter := time.Now().Add(leafValidity)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the CA key: %w", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "feint interception CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("sign the CA: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("parse the CA back: %w", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the leaf key: %w", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string(nil), hosts...),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("sign the leaf: %w", err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("the minted CA does not parse as a trust root")
	}

	return &Interceptor{
		tlsConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			Certificates: []tls.Certificate{{
				Certificate: [][]byte{leafDER, caDER},
				PrivateKey:  leafKey,
				Leaf:        leafTmpl,
			}},
		},
		caPEM: caPEM,
		pool:  pool,
		hosts: append([]string(nil), hosts...),
	}, nil
}

// serial draws a random 128-bit certificate serial. Two certificates minted in
// the same second must differ, or a client that has seen one rejects the other
// as a duplicate; a counter would collide across two proxy processes, a random
// draw does not.
func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		// rand.Reader failing is a broken machine, not a condition to paper over
		// with a fixed serial that would then collide.
		panic("feint: crypto/rand is unavailable: " + err.Error())
	}
	return n
}

// ServerTLSConfig is the configuration the proxy's listener serves. It presents
// the minted leaf for every intercepted name.
func (i *Interceptor) ServerTLSConfig() *tls.Config { return i.tlsConfig.Clone() }

// CAPEM is the CA in PEM, the bytes [Interceptor.WriteCA] writes. A caller that
// keeps its own trust pool (a client in this process, a test) appends these.
func (i *Interceptor) CAPEM() []byte { return append([]byte(nil), i.caPEM...) }

// CAPool trusts the minted CA and nothing else. It is what a client in the same
// process (a test, or a client library the operator embeds) sets as its root, in
// place of the SSL_CERT_FILE a separate process reads.
func (i *Interceptor) CAPool() *x509.CertPool { return i.pool }

// Hosts are the names the leaf covers.
func (i *Interceptor) Hosts() []string { return append([]string(nil), i.hosts...) }

// WriteCA writes the CA in PEM to path, for a client to trust through
// SSL_CERT_FILE.
//
// 0644 rather than 0600: SSL_CERT_FILE is read by whatever the operator runs,
// commonly a client in a container under a different uid than the writer, and a
// public trust root carries no secret — the private key never leaves this
// process. The path is caller-supplied precisely so it can be a namespace's
// temporary file that vanishes with the namespace, which is the scoping the
// safety argument depends on.
func (i *Interceptor) WriteCA(path string) error {
	if err := os.WriteFile(path, i.caPEM, 0o644); err != nil { //nolint:gosec // a public CA, no secret; must be readable by the client process
		return fmt.Errorf("write the interception CA to %s: %w", path, err)
	}
	return nil
}
