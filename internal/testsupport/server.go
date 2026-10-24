package testsupport

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NewHTTPServer は 127.0.0.1 で平文の HTTP サーバーを立てる。テストの終わりに閉じる。
// ⚠ テストが話してよい相手は、テストが自分で立てたサーバーだけ（docs/test-perspectives.md §1）。
func NewHTTPServer(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// NewTLSServer は 127.0.0.1 で、与えた証明書を出す HTTPS サーバーを立てる。TLS の版は Go の既定に任せる。
// 証明書の名前（SAN）は呼び出し側が決める。127.0.0.1 に接続して ServerName を指定すれば、名前の一致・不一致を試せる。
func NewTLSServer(t testing.TB, h http.Handler, cert tls.Certificate) *httptest.Server {
	t.Helper()
	return newTLSServer(t, h, &tls.Config{Certificates: []tls.Certificate{cert}})
}

// NewTLSServerVersion は NewTLSServer と同じで、TLS の版を 1 つに固定する（例: tls.VersionTLS12）。
func NewTLSServerVersion(t testing.TB, h http.Handler, cert tls.Certificate, version uint16) *httptest.Server {
	t.Helper()
	return newTLSServer(t, h, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: version, MaxVersion: version})
}

func newTLSServer(t testing.TB, h http.Handler, cfg *tls.Config) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.TLS = cfg
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
