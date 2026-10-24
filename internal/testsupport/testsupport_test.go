package testsupport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// peer は検証を外して接続し、相手が送った証明書の鎖を返す。
// tenken が「read the contents of a certificate that failed verification too」ための形そのもの（docs/design.md §1）。
func peer(t *testing.T, addr string) []*x509.Certificate {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // テストのローカルサーバーにだけ向ける
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates
}

func verify(chain []*x509.Certificate, roots *x509.CertPool, name string, now time.Time) error {
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, DNSName: name, CurrentTime: now})
	return err
}

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

func addr(url string) string { return strings.TrimPrefix(url, "https://") }

func TestValidLeafVerifies(t *testing.T) {
	ca := NewCA(t, Now)
	leaf := ca.Leaf(t, Now, LeafOptions{DNSNames: []string{"example.test"}})
	for _, v := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		s := NewTLSServerVersion(t, ok, leaf.TLS, v)
		chain := peer(t, addr(s.URL))
		if len(chain) != 2 {
			t.Fatalf("TLS %x: got %d certificates, want leaf + intermediate", v, len(chain))
		}
		if err := verify(chain, ca.Pool(), "example.test", Now); err != nil {
			t.Fatalf("TLS %x: verify: %v", v, err)
		}
	}
}

func TestExpiredLeafIsReadableAndFailsAsExpired(t *testing.T) {
	ca := NewCA(t, Now)
	leaf := ca.Leaf(t, Now, LeafOptions{DNSNames: []string{"example.test"},
		NotBefore: Now.AddDate(0, -3, 0), NotAfter: Now.AddDate(0, 0, -1)})
	chain := peer(t, addr(NewTLSServer(t, ok, leaf.TLS).URL))
	if got := chain[0].NotAfter; !got.Equal(Now.AddDate(0, 0, -1)) {
		t.Fatalf("NotAfter = %v: the expired certificate must still be readable", got)
	}
	var ce x509.CertificateInvalidError
	if err := verify(chain, ca.Pool(), "example.test", Now); !errors.As(err, &ce) || ce.Reason != x509.Expired {
		t.Fatalf("verify = %v, want CertificateInvalidError{Expired}", err)
	}
}

func TestClockDecidesValidity(t *testing.T) {
	// 同じ証明書が、時計だけで有効にも期限切れにもなる: 残り日数の境界を今日の日付に依存せずに試せる
	ca := NewCA(t, Now)
	leaf := ca.Leaf(t, Now, LeafOptions{DNSNames: []string{"example.test"}, NotAfter: Now.AddDate(0, 0, 10)})
	chain := peer(t, addr(NewTLSServer(t, ok, leaf.TLS).URL))
	if err := verify(chain, ca.Pool(), "example.test", Now); err != nil {
		t.Fatalf("at Now: %v", err)
	}
	var ce x509.CertificateInvalidError
	if err := verify(chain, ca.Pool(), "example.test", Now.AddDate(0, 0, 11)); !errors.As(err, &ce) || ce.Reason != x509.Expired {
		t.Fatalf("11 days later: verify = %v, want CertificateInvalidError{Expired}", err)
	}
}

func TestHostnameMismatch(t *testing.T) {
	ca := NewCA(t, Now)
	leaf := ca.Leaf(t, Now, LeafOptions{DNSNames: []string{"*.example.test"}})
	chain := peer(t, addr(NewTLSServer(t, ok, leaf.TLS).URL))
	if err := verify(chain, ca.Pool(), "a.example.test", Now); err != nil {
		t.Fatalf("a.example.test: %v", err)
	}
	var he x509.HostnameError
	if err := verify(chain, ca.Pool(), "a.b.example.test", Now); !errors.As(err, &he) {
		t.Fatalf("a.b.example.test: verify = %v, want HostnameError", err)
	}
}

func TestMissingIntermediateAndSelfSigned(t *testing.T) {
	ca := NewCA(t, Now)
	leaf := ca.Leaf(t, Now, LeafOptions{DNSNames: []string{"example.test"}})
	chain := peer(t, addr(NewTLSServer(t, ok, leaf.TLSWithoutIntermediate).URL))
	if len(chain) != 1 {
		t.Fatalf("got %d certificates, want the leaf only", len(chain))
	}
	var ue x509.UnknownAuthorityError
	if err := verify(chain, ca.Pool(), "example.test", Now); !errors.As(err, &ue) {
		t.Fatalf("missing intermediate: verify = %v, want UnknownAuthorityError", err)
	}
	self := SelfSigned(t, Now, "example.test")
	chain = peer(t, addr(NewTLSServer(t, ok, self.TLS).URL))
	if err := verify(chain, ca.Pool(), "example.test", Now); !errors.As(err, &ue) {
		t.Fatalf("self-signed: verify = %v, want UnknownAuthorityError", err)
	}
	if chain[0].Issuer.String() != chain[0].Subject.String() {
		t.Fatal("self-signed: issuer and subject differ")
	}
}

// チェックごとの表のテストは並行に走る。道具が並行に呼ばれても壊れないこと（go test -race で見る）。
func TestHelpersAreSafeInParallel(t *testing.T) {
	for i := 0; i < 4; i++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			ca := NewCA(t, Now)
			leaf := ca.Leaf(t, Now, LeafOptions{DNSNames: []string{"example.test"}})
			if leaf.Cert.SerialNumber.Sign() <= 0 {
				t.Fatal("serial number must be positive")
			}
			_ = SelfSigned(t, Now, "example.test")
		})
	}
}

func TestHTTPServerIsLocal(t *testing.T) {
	s := NewHTTPServer(t, ok)
	if !strings.HasPrefix(s.URL, "http://127.0.0.1:") {
		t.Fatalf("URL = %s: test servers must listen on 127.0.0.1", s.URL)
	}
}
