package fetch

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/testsupport"
)

// client は既定の設定に o を重ねた Client を作り、テストの終わりに閉じる。時計は常に固定の時計。
// ⚠ ここのテストは 127.0.0.1 の自前のサーバーに当てるので、要求は PrivateAllowed で送る（確かめる口は private_test.go が見る）。
func client(t *testing.T, o Options) *Client {
	t.Helper()
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Second
	}
	if o.MaxBody == 0 {
		o.MaxBody = 1 << 20
	}
	if o.Now == nil {
		o.Now = testsupport.Clock(testsupport.Now)
	}
	if o.Proxy == nil {
		o.Proxy = func(*url.URL) (*url.URL, error) { return nil, nil } // ⚠ 手元の HTTP_PROXY に左右されない
	}
	c := New(o)
	t.Cleanup(c.Close)
	return c
}

func reason(t *testing.T, err error) Reason {
	t.Helper()
	var ue *UnreachableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnreachableError", err)
	}
	return ue.Reason
}

func TestGetReturnsStatusHeadersAndBody(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Content-Type-Options", "nosniff")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))
	got, err := client(t, Options{}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != http.StatusTeapot || string(got.Body) != "hello" || got.Truncated {
		t.Fatalf("got %d %q truncated=%v", got.Status, got.Body, got.Truncated)
	}
	if v := got.Header["X-Content-Type-Options"]; len(v) != 1 || v[0] != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %v", v)
	}
	if v := got.Header["Set-Cookie"]; len(v) != 2 {
		t.Fatalf("a header sent twice must keep both values, got %v", v)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			http.Redirect(w, r, "/b", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte("b"))
	}))
	got, err := client(t, Options{}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL + "/a"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != http.StatusMovedPermanently || got.Location() != "/b" {
		t.Fatalf("got %d Location=%q, want 301 to /b without following", got.Status, got.Location())
	}
}

func TestBodyIsReadUpToMaxBody(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	got, err := client(t, Options{MaxBody: 10}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Body) != 10 || !got.Truncated {
		t.Fatalf("body %d bytes truncated=%v, want 10 and true", len(got.Body), got.Truncated)
	}
	exact, _ := client(t, Options{MaxBody: 100}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if len(exact.Body) != 100 || exact.Truncated {
		t.Fatalf("a body exactly at the limit: %d bytes truncated=%v", len(exact.Body), exact.Truncated)
	}
}

// 上限は切り詰めのためではなく、読み続けないため: 終わらない本文でも、上限まで読んだら返る。
func TestAnEndlessBodyStopsAtMaxBody(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 1024))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	got, err := client(t, Options{MaxBody: 4096, Timeout: 2 * time.Second}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil {
		t.Fatalf("an endless body must stop at MaxBody, not run into the timeout: %v", err)
	}
	if len(got.Body) != 4096 || !got.Truncated {
		t.Fatalf("body %d bytes truncated=%v", len(got.Body), got.Truncated)
	}
}

func TestHeadIsSentWithTheConfiguredUserAgent(t *testing.T) {
	var method, ua string
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		method, ua = r.Method, r.UserAgent()
	}))
	if _, err := client(t, Options{UserAgent: "tenken/0.0.0-dev (+https://example.test)"}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "HEAD", URL: s.URL}); err != nil {
		t.Fatal(err)
	}
	if method != "HEAD" || ua != "tenken/0.0.0-dev (+https://example.test)" {
		t.Fatalf("method=%s ua=%q", method, ua)
	}
}

func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestUnreachableReasons(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	untrusted := testsupport.NewTLSServer(t, http.NotFoundHandler(), testsupport.SelfSigned(t, testsupport.Now, "127.0.0.1").TLS)
	plain := testsupport.NewHTTPServer(t, http.NotFoundHandler())
	slow := testsupport.NewHTTPServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	dnsFails := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "nowhere.test", IsNotFound: true}}
	}
	cases := []struct {
		name string
		c    *Client
		url  string
		want Reason
	}{
		{"dns", client(t, Options{DialContext: dnsFails}), "http://nowhere.test/", ReasonDNS},
		{"connection refused", client(t, Options{}), "http://" + closedAddr(t) + "/", ReasonConnect},
		{"timeout", client(t, Options{Timeout: 200 * time.Millisecond}), slow.URL, ReasonTimeout},
		{"untrusted certificate", client(t, Options{RootCAs: ca.Pool()}), untrusted.URL, ReasonTLS},
		{"https to a plain HTTP server", client(t, Options{}), strings.Replace(plain.URL, "http://", "https://", 1), ReasonTLS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: c.url})
			if got := reason(t, err); got != c.want {
				t.Fatalf("reason = %s, want %s (err: %v)", got, c.want, err)
			}
		})
	}
}

func TestSkipVerifyReadsAnUntrustedOrigin(t *testing.T) {
	s := testsupport.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
		testsupport.SelfSigned(t, testsupport.Now, "127.0.0.1").TLS)
	got, err := client(t, Options{}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL, SkipVerify: true})
	if err != nil || string(got.Body) != "ok" {
		t.Fatalf("got %q, %v", got.Body, err)
	}
}

func TestTrustedCAVerifiesOnTheGivenClock(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: testsupport.Now.AddDate(0, 0, -1), NotAfter: testsupport.Now.AddDate(0, 0, 1)})
	s := testsupport.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), leaf.TLS)
	got, err := client(t, Options{RootCAs: ca.Pool(), Now: testsupport.Clock(testsupport.Now)}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil || string(got.Body) != "ok" {
		t.Fatalf("a certificate from a trusted CA, valid on the given clock: got %q, %v", got.Body, err)
	}
	// ⚠ 同じ証明書が、時計を 2 日進めると期限切れで tls になる: 検証は実際の時計ではなく渡した時計で行われている
	_, err = client(t, Options{RootCAs: ca.Pool(), Now: testsupport.Clock(testsupport.Now.AddDate(0, 0, 2))}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if got := reason(t, err); got != ReasonTLS {
		t.Fatalf("2 days later: reason = %s, want tls", got)
	}
}

func TestChainReturnsWhatTheServerSentEvenIfExpired(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{DNSNames: []string{"example.test"},
		NotBefore: testsupport.Now.AddDate(0, -3, 0), NotAfter: testsupport.Now.AddDate(0, 0, -1)})
	s := testsupport.NewTLSServerVersion(t, http.NotFoundHandler(), leaf.TLS, tls.VersionTLS12)
	ch, err := client(t, Options{}).Chain(context.Background(), s.URL, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Certificates) != 2 || !ch.Certificates[0].NotAfter.Equal(leaf.Cert.NotAfter) {
		t.Fatalf("got %d certificates, want leaf + intermediate with the expired leaf readable", len(ch.Certificates))
	}
}

func TestChainUnreachableReasons(t *testing.T) {
	plain := testsupport.NewHTTPServer(t, http.NotFoundHandler())
	if _, err := client(t, Options{}).Chain(context.Background(), "https://"+closedAddr(t)+"/", netip.Addr{}); reason(t, err) != ReasonConnect {
		t.Fatalf("closed port: %v", err)
	}
	if _, err := client(t, Options{}).Chain(context.Background(), strings.Replace(plain.URL, "http://", "https://", 1), netip.Addr{}); reason(t, err) != ReasonTLS {
		t.Fatalf("plain server: %v", err)
	}
}

func TestCallerCancellationIsNotUnreachable(t *testing.T) {
	started := make(chan struct{})
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := client(t, Options{}).Do(ctx, Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("err = %v, want ErrCanceled: our own cancellation is not the site being unreachable", err)
	}
	var ue *UnreachableError
	if errors.As(err, &ue) {
		t.Fatalf("cancellation reported as unreachable (%s)", ue.Reason)
	}
}

func TestAReceivedStatusSurvivesACutOffBody(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("short"))
		// ハンドラが返ると、約束した 1000 バイトに届かないまま接続が閉じる
	}))
	got, err := client(t, Options{}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil {
		t.Fatalf("a 404 that arrived must be returned, not turned into an error: %v", err)
	}
	if got.Status != http.StatusNotFound || !got.Incomplete || string(got.Body) != "short" {
		t.Fatalf("got status %d incomplete=%v body %q", got.Status, got.Incomplete, got.Body)
	}
}

func TestCloseLeavesNoGoroutines(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	before := runtime.NumGoroutine()
	c := New(Options{Timeout: 2 * time.Second, MaxBody: 1024, Now: testsupport.Clock(testsupport.Now),
		Proxy: func(*url.URL) (*url.URL, error) { return nil, nil }})
	for i := 0; i < 3; i++ {
		if _, err := c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL}); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("%d goroutines before, %d after Close: the client left connections open", before, n)
	}
}

func TestNewRefusesOptionsThatWouldMisbehave(t *testing.T) {
	ok := Options{Timeout: time.Second, MaxBody: 1, Now: testsupport.Clock(testsupport.Now)}
	for name, mutate := range map[string]func(*Options){
		"zero timeout":     func(o *Options) { o.Timeout = 0 },
		"zero max body":    func(o *Options) { o.MaxBody = 0 },
		"max int64 body":   func(o *Options) { o.MaxBody = 1<<63 - 1 },
		"no clock":         func(o *Options) { o.Now = nil },
		"negative timeout": func(o *Options) { o.Timeout = -time.Second },
	} {
		o := ok
		mutate(&o)
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: New did not refuse", name)
				}
			}()
			New(o)
		}()
	}
}

// connectProxy は CONNECT だけを受け付けるローカルのプロキシ。トンネルを開いた先と Proxy-Authorization を記録する。
// テストの終わりに、受け付けを止め、開いている接続を閉じ、goroutine の終わりを待つ。
func connectProxy(t *testing.T) (addr string, seen func() []string) {
	t.Helper()
	return connectProxyWith(t, proxyOpts{})
}

// proxyOpts は connectProxyWith の振る舞い。TLS があれば https:// のプロキシになり、Refuse なら CONNECT を 403 で断る。
type proxyOpts struct {
	TLS    *tls.Config
	Refuse bool
}

func connectProxyWith(t *testing.T, o proxyOpts) (addr string, seen func() []string) {
	t.Helper()
	var l net.Listener
	var err error
	if o.TLS != nil {
		l, err = tls.Listen("tcp", "127.0.0.1:0", o.TLS)
	} else {
		l, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var log []string
	var conns []net.Conn
	var wg sync.WaitGroup
	track := func(c net.Conn) { mu.Lock(); conns = append(conns, c); mu.Unlock() }
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			track(conn)
			wg.Add(1)
			go func(conn net.Conn) {
				defer wg.Done()
				defer conn.Close()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				if req.Method != http.MethodConnect {
					// http:// の相手への要求（絶対形式の GET）も記録して答える。⚠ 黙って切ると、届いたことが見えない
					mu.Lock()
					log = append(log, req.Method+" "+req.URL.String()+" auth="+req.Header.Get("Proxy-Authorization"))
					mu.Unlock()
					_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\nproxied"))
					return
				}
				mu.Lock()
				log = append(log, req.Host+" auth="+req.Header.Get("Proxy-Authorization"))
				mu.Unlock()
				if o.Refuse {
					_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
					return
				}
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				track(up)
				defer up.Close()
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(up, conn); done <- struct{}{} }()
				go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
				<-done
			}(conn)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return l.Addr().String(), func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), log...) }
}

func TestChainAndDoGoThroughTheSameProxyWithCredentials(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}})
	s := testsupport.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), leaf.TLS)
	target := strings.TrimPrefix(s.URL, "https://")
	proxyAddr, seen := connectProxy(t)
	c := client(t, Options{RootCAs: ca.Pool(), Proxy: func(*url.URL) (*url.URL, error) {
		return &url.URL{Scheme: "http", User: url.UserPassword("u", "p"), Host: proxyAddr}, nil
	}})
	ch, err := c.Chain(context.Background(), s.URL, netip.Addr{})
	if err != nil || len(ch.Certificates) != 2 {
		t.Fatalf("Chain through the proxy: %d certificates, %v", len(ch.Certificates), err)
	}
	got, err := c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil || string(got.Body) != "ok" {
		t.Fatalf("Do through the proxy: %q, %v", got.Body, err)
	}
	want := target + " auth=Basic dTpw" // base64("u:p")
	if log := seen(); len(log) != 2 || log[0] != want || log[1] != want {
		t.Fatalf("the proxy saw %v, want two CONNECTs to %s with credentials", log, target)
	}
}

// 握手のあとに失敗しても（HTTP を話さない相手）、握手で受け取った鎖は返す。
func TestChainReturnsTheCertificatesEvenIfHTTPFailsAfterTheHandshake(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{leaf.TLS}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := l.Accept()
		if err != nil {
			return
		}
		_ = conn.(*tls.Conn).Handshake()
		conn.Close() // 握手だけして、HTTP は話さずに切る
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	ch, err := client(t, Options{}).Chain(context.Background(), "https://"+l.Addr().String()+"/", netip.Addr{})
	if err != nil || len(ch.Certificates) != 2 {
		t.Fatalf("got %d certificates, %v: the handshake succeeded, so the chain must be returned", len(ch.Certificates), err)
	}
}

// 接続を使い回すと握手が起きず、2 回目に鎖が見えない。同じ Client で何度呼んでも鎖を返す。
func TestChainWorksRepeatedlyOnOneClient(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	s := testsupport.NewTLSServer(t, http.NotFoundHandler(), leaf.TLS)
	c := client(t, Options{})
	for i := 0; i < 2; i++ {
		ch, err := c.Chain(context.Background(), s.URL, netip.Addr{})
		if err != nil || len(ch.Certificates) != 2 {
			t.Fatalf("call %d: %d certificates, %v", i+1, len(ch.Certificates), err)
		}
	}
}

func TestCallerDeadlineIsNotUnreachableInAnyPhase(t *testing.T) {
	stallHeaders := testsupport.NewHTTPServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	stallBody := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	for name, u := range map[string]string{"before the headers": stallHeaders.URL, "during the body": stallBody.URL} {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := client(t, Options{Timeout: 5 * time.Second}).Do(ctx, Request{Private: PrivateAllowed, Method: "GET", URL: u})
		cancel()
		if !errors.Is(err, ErrCanceled) {
			t.Errorf("%s: err = %v, want ErrCanceled (the caller's own deadline is not the site's fault)", name, err)
		}
	}
}

func TestOwnTimeoutDuringTheBodyKeepsTheStatus(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	got, err := client(t, Options{Timeout: 200 * time.Millisecond}).Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
	if err != nil || got.Status != http.StatusOK || !got.Incomplete || got.IncompleteReason != ReasonTimeout {
		t.Fatalf("got status %d incomplete=%v reason=%q err=%v", got.Status, got.Incomplete, got.IncompleteReason, err)
	}
}

// cert は TLS で接続するだけ（docs/cli-reference.md §2.1）。Chain は HTTP の要求を 1 つも送らない。
func TestChainSendsNoHTTPRequest(t *testing.T) {
	var requests atomic.Int32
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	s := testsupport.NewTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }), leaf.TLS)
	ch, err := client(t, Options{}).Chain(context.Background(), s.URL, netip.Addr{})
	if err != nil || len(ch.Certificates) != 2 {
		t.Fatalf("got %d certificates, %v", len(ch.Certificates), err)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("the server saw %d HTTP requests, want 0", n)
	}
}

func TestChainNeedsAnHTTPSURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1/", "127.0.0.1:443", "::"} {
		_, err := client(t, Options{}).Chain(context.Background(), u, netip.Addr{})
		var ue *UnreachableError
		if err == nil || errors.As(err, &ue) {
			t.Errorf("%q: err = %v, want a caller error that is not *UnreachableError", u, err)
		}
	}
}

// httpsProxy は CA の発行した証明書を出す https:// の CONNECT プロキシ。
func httpsProxy(t *testing.T, ca *testsupport.CA, refuse bool) (*url.URL, func() []string) {
	t.Helper()
	cert := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}})
	addr, seen := connectProxyWith(t, proxyOpts{TLS: &tls.Config{Certificates: []tls.Certificate{cert.TLS}}, Refuse: refuse})
	return &url.URL{Scheme: "https", Host: addr}, seen
}

func TestChainThroughAnHTTPSProxyReturnsTheSitesChainNotTheProxys(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{DNSNames: []string{"site.test"}})
	s := testsupport.NewTLSServer(t, http.NotFoundHandler(), leaf.TLS)
	proxy, seen := httpsProxy(t, ca, false)
	c := client(t, Options{RootCAs: ca.Pool(), Proxy: func(*url.URL) (*url.URL, error) { return proxy, nil }})
	ch, err := c.Chain(context.Background(), s.URL, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ch.Certificates[0].DNSNames; len(got) != 1 || got[0] != "site.test" {
		t.Fatalf("leaf names %v: Chain returned the proxy's certificate, not the site's", got)
	}
	if len(seen()) != 1 {
		t.Fatalf("the proxy saw %v", seen())
	}
}

func TestChainThroughAnHTTPSProxyThatRefusesIsAnError(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	proxy, _ := httpsProxy(t, ca, true)
	c := client(t, Options{RootCAs: ca.Pool(), Proxy: func(*url.URL) (*url.URL, error) { return proxy, nil }})
	ch, err := c.Chain(context.Background(), "https://site.test:443/", netip.Addr{})
	if err == nil || len(ch.Certificates) != 0 {
		t.Fatalf("got %d certificates, err=%v: a refused CONNECT must not return the proxy's chain", len(ch.Certificates), err)
	}
}

// 検証を外すのは開始 URL のオリジンだけ。プロキシとの TLS は常に検証する。
func TestAnUntrustedHTTPSProxyIsRefusedEvenWhenTheTargetIsNotVerified(t *testing.T) {
	trusted := testsupport.NewCA(t, testsupport.Now)
	other := testsupport.NewCA(t, testsupport.Now)
	leaf := trusted.Leaf(t, testsupport.Now, testsupport.LeafOptions{DNSNames: []string{"site.test"}})
	s := testsupport.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), leaf.TLS)
	proxy, seen := httpsProxy(t, other, false) // ⚠ 信頼していない CA の証明書を出すプロキシ
	c := client(t, Options{RootCAs: trusted.Pool(), Proxy: func(*url.URL) (*url.URL, error) {
		return &url.URL{Scheme: proxy.Scheme, Host: proxy.Host, User: url.UserPassword("u", "p")}, nil
	}})
	if _, err := c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL, SkipVerify: true}); err == nil {
		t.Fatal("Do with SkipVerify went through an untrusted proxy")
	}
	if _, err := c.Chain(context.Background(), s.URL, netip.Addr{}); err == nil {
		t.Fatal("Chain went through an untrusted proxy")
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("credentials or CONNECT reached an untrusted proxy: %v", got)
	}
}

// http:// の相手でも、https:// のプロキシとの TLS は検証する。
func TestAnUntrustedHTTPSProxyIsRefusedForAnHTTPTargetToo(t *testing.T) {
	trusted := testsupport.NewCA(t, testsupport.Now)
	other := testsupport.NewCA(t, testsupport.Now)
	proxy, seen := httpsProxy(t, other, false)
	c := client(t, Options{RootCAs: trusted.Pool(), Proxy: func(*url.URL) (*url.URL, error) { return proxy, nil }})
	if _, err := c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: "http://site.test/", SkipVerify: true}); err == nil {
		t.Fatal("Do went through an untrusted https proxy for an http target")
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("an untrusted proxy received %v", got)
	}
}

// https:// のプロキシを通っても、相手の証明書は検証する（SkipVerify でなければ）。プロキシなしと同じ結果になる。
func TestTheTargetIsVerifiedThroughAnHTTPSProxyAsWithout(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	good := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}})
	expired := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: testsupport.Now.AddDate(0, -2, 0), NotAfter: testsupport.Now.AddDate(0, 0, -1)})
	untrusted := testsupport.SelfSigned(t, testsupport.Now, "127.0.0.1")
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	proxy, _ := httpsProxy(t, ca, false)
	direct := client(t, Options{RootCAs: ca.Pool()})
	viaProxy := client(t, Options{RootCAs: ca.Pool(), Proxy: func(*url.URL) (*url.URL, error) { return proxy, nil }})
	for name, cert := range map[string]tls.Certificate{"valid": good.TLS, "expired": expired.TLS, "untrusted": untrusted.TLS} {
		s := testsupport.NewTLSServer(t, ok, cert)
		_, errDirect := direct.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
		_, errProxy := viaProxy.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL})
		if (errDirect == nil) != (errProxy == nil) {
			t.Errorf("%s: direct err=%v, via proxy err=%v: verification through the proxy must agree with crypto/tls", name, errDirect, errProxy)
		}
		if name == "valid" && errProxy != nil {
			t.Errorf("valid through the proxy: %v", errProxy)
		}
		if name != "valid" && reason(t, errProxy) != ReasonTLS {
			t.Errorf("%s through the proxy: want tls", name)
		}
	}
}

// 壊れた Location を返す応答は「壊れている」のであって「到達不能」ではない（docs/cli-reference.md §6）: ステータスとヘッダがそのまま返る。
func TestABrokenLocationHeaderIsAResponseNotUnreachable(t *testing.T) {
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://[::1")
		w.WriteHeader(301)
	}))
	c := New(Options{Timeout: 2 * time.Second, MaxBody: 1 << 20, Now: testsupport.Clock(testsupport.Now)})
	t.Cleanup(c.Close)
	resp, err := c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: s.URL + "/"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.Status != 301 || resp.Location() != "http://[::1" {
		t.Fatalf("status %d, location %q", resp.Status, resp.Location())
	}
}

// WantBody が false なら、ステータスとヘッダが届いた時点で返り、本文は読まない（巨大なファイルの生存確認で --max-body まで読まない）。
func TestWantBodyFalseDoesNotReadTheBody(t *testing.T) {
	released := make(chan struct{})
	srv := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-released // 本文は、テストが許すまで送らない
		w.Write(make([]byte, 1<<20))
	}))
	defer close(released)
	c := New(Options{Timeout: 5 * time.Second, MaxBody: 10 << 20, UserAgent: "t", Now: testsupport.Clock(testsupport.Now)})
	var seen struct {
		url    string
		status int
		ct     string
	}
	done := make(chan struct{})
	var resp Response
	var err error
	go func() {
		defer close(done)
		resp, err = c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: srv.URL + "/big", WantBody: func(u string, status int, h map[string][]string) bool {
			seen.url, seen.status, seen.ct = u, status, h["Content-Type"][0]
			return false
		}})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Do waited for a body it was told not to read")
	}
	if err != nil || resp.Status != 200 || resp.Body != nil || resp.Truncated || resp.Incomplete {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if seen.url != srv.URL+"/big" || seen.status != 200 || seen.ct != "application/octet-stream" {
		t.Fatalf("WantBody saw %+v", seen)
	}
	// true なら読む（既定と同じ）
	released2 := make(chan struct{})
	close(released2)
	srv2 := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	resp, err = c.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: srv2.URL + "/x", WantBody: func(string, int, map[string][]string) bool { return true }})
	if err != nil || string(resp.Body) != "hello" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
}

func TestIsPrivateHost(t *testing.T) {
	for h, want := range map[string]bool{
		"localhost": true, "LOCALHOST:8080": true, "foo.localhost": true, "127.0.0.1": true, "127.5.6.7:80": true, "::1": true, "[::1]:443": true,
		"10.0.0.1": true, "172.16.0.1": true, "172.31.255.255": true, "192.168.0.1": true, "169.254.1.1": true, "0.0.0.0": true, "[fe80::1]": true, "fd00::1": true, "[::ffff:0.0.0.0]": true, "[::ffff:127.0.0.1]:80": true, "[::ffff:10.1.2.3]": true,
		"[2001:db8::1]:443": true, "100.64.1.1:8080": true, "[ff02::1]": true,
		"example.com": false, "8.8.8.8": false, "172.32.0.1": false, "localhost.example.com": false, "1.1.1.1:8080": false, "[2000::1]:443": false,
	} {
		if got := IsPrivateHost(h); got != want {
			t.Errorf("IsPrivateHost(%q) = %v, want %v", h, got, want)
		}
	}
}

// docs/test-perspectives.md §2.2: プライベートアドレスは、IANA の特殊用途アドレスの表で Globally Reachable が False の範囲
// （その中の True の範囲を除く）と、マルチキャスト、限定ブロードキャスト。範囲ごとに、内側の最初と最後のアドレスと、すぐ外の隣を見る。
// ⚠ 値は表（fetch.go の notGlobal / globalInsideNotGlobal）から作らず、ここに直に書く: 表から作れば、表の誤りに表が同意するだけ。
// IPv4 の行は、IPv4-mapped IPv6（[::ffff:a.b.c.d]）でも同じ答えになる（表の ::ffff:0:0/96 は False だが、接続するのは中の IPv4 アドレス）。
// 出どころ: IANA IPv4 / IPv6 Special-Purpose Address Registry（どちらも Last Updated 2025-10-09。2026-10-24 に読んだ）。
func TestPrivateAddressesAreTheNonGloballyReachableRangesMulticastAndBroadcast(t *testing.T) {
	private := []string{
		"0.0.0.0", "0.255.255.255", // 0.0.0.0/8
		"10.0.0.0", "10.255.255.255", // 10.0.0.0/8
		"100.64.0.0", "100.127.255.255", // 100.64.0.0/10
		"127.0.0.0", "127.255.255.255", // 127.0.0.0/8
		"169.254.0.0", "169.254.255.255", // 169.254.0.0/16
		"172.16.0.0", "172.31.255.255", // 172.16.0.0/12
		"192.0.0.0", "192.0.0.8", "192.0.0.11", "192.0.0.170", "192.0.0.255", // 192.0.0.0/24（.9 と .10 の隣も）
		"192.0.2.0", "192.0.2.255", // 192.0.2.0/24
		"192.88.99.2",                    // 192.88.99.2/32（6a44-relay anycast）
		"192.168.0.0", "192.168.255.255", // 192.168.0.0/16
		"198.18.0.0", "198.19.255.255", // 198.18.0.0/15
		"198.51.100.0", "198.51.100.255", // 198.51.100.0/24
		"203.0.113.0", "203.0.113.255", // 203.0.113.0/24
		"224.0.0.0", "239.255.255.255", // 224.0.0.0/4（マルチキャスト）
		"240.0.0.0", "255.255.255.254", // 240.0.0.0/4
		"255.255.255.255", // 限定ブロードキャスト
		"::", "::1",       // ::/128、::1/128
		"64:ff9b:1::", "64:ff9b:1:ffff:ffff:ffff:ffff:ffff", // 64:ff9b:1::/48
		"100::", "100::ffff:ffff:ffff:ffff", // 100::/64
		"100:0:0:1::", "100:0:0:1:ffff:ffff:ffff:ffff", // 100:0:0:1::/64
		"2001::", "2001::1", "2001:0:ffff:ffff:ffff:ffff:ffff:ffff", // 2001::/32（TEREDO。表では N/A。tenken は 2001::/23 に従う）
		"2001:1::", "2001:1::4", "2001:2::1", "2001:2:ffff:ffff:ffff:ffff:ffff:ffff", "2001:4::", // 2001::/23（2001:1::1-3 の隣、Benchmarking、2001:3::/32 の隣）
		"2001:4:111:ffff:ffff:ffff:ffff:ffff", "2001:4:113::", "2001:10::1", "2001:1f:ffff:ffff:ffff:ffff:ffff:ffff", "2001:40::", "2001:1ff:ffff:ffff:ffff:ffff:ffff:ffff", // 2001::/23（2001:4:112::/48 の隣、ORCHID（表では値が無い）、2001:20::/28 と 2001:30::/28 の隣、最後）
		"2001:db8::", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", // 2001:db8::/32
		"3fff::", "3fff:fff:ffff:ffff:ffff:ffff:ffff:ffff", // 3fff::/20
		"5f00::", "5f00:ffff:ffff:ffff:ffff:ffff:ffff:ffff", // 5f00::/16
		"fc00::", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", // fc00::/7
		"fe80::", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fe80::1%eth0", // fe80::/10（ゾーン付きも）
		"ff00::", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", // ff00::/8（マルチキャスト）
	}
	global := []string{
		"1.0.0.0", "9.255.255.255", "11.0.0.0", "100.63.255.255", "100.128.0.0", "126.255.255.255", "128.0.0.0",
		"169.253.255.255", "169.255.0.0", "172.15.255.255", "172.32.0.0", "191.255.255.255", "192.0.1.0",
		"192.0.0.9", "192.0.0.10", // 192.0.0.0/24 の中の True（PCP Anycast、TURN Anycast）
		"192.0.1.255", "192.0.3.0", "192.167.255.255", "192.169.0.0", "198.17.255.255", "198.20.0.0",
		"198.51.99.255", "198.51.101.0", "203.0.112.255", "203.0.114.0", "223.255.255.255",
		"192.88.99.0", "192.88.99.1", "192.88.99.3", "192.88.99.255", // 192.88.99.0/24（Deprecated 6to4 Relay Anycast。表では値が無い）。.2 の隣
		"192.31.196.1", "192.52.193.1", "192.175.48.1", // False の範囲の外の True（AS112-v4、AMT、AS112）
		"::2", "64:ff9b::", "64:ff9b::ffff:ffff", "64:ff9b:0:ffff:ffff:ffff:ffff:ffff", "64:ff9b:2::", // 64:ff9b::/96 は True
		"ff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "100:0:0:2::", "2000:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "2001:200::",
		"2001:1::1", "2001:1::2", "2001:1::3", // 2001::/23 の中の True（PCP、TURN、DNS-SD SRP の Anycast）
		"2001:3::", "2001:3:ffff:ffff:ffff:ffff:ffff:ffff", "2001:4:112::", "2001:4:112:ffff:ffff:ffff:ffff:ffff", // AMT、AS112-v6
		"2001:20::", "2001:2f:ffff:ffff:ffff:ffff:ffff:ffff", "2001:30::", "2001:3f:ffff:ffff:ffff:ffff:ffff:ffff", // ORCHIDv2、DETs
		"2001:db7:ffff:ffff:ffff:ffff:ffff:ffff", "2001:db9::", "2002::1", // 2002::/16（6to4。表では N/A）
		"2620:4f:8000::1", "3ffe:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "3fff:1000::", "5eff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "5f01::",
		"fbff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fe00::", "fe7f:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fec0::", "feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888", // 明らかにグローバル
		testsupport.PublicStandIn, testsupport.OtherPublicStandIn, testsupport.ThirdPublicStandIn, testsupport.PublicStandInV6, // テストの代わりのアドレス
	}
	check := func(s string, want bool) {
		t.Helper()
		ip := netip.MustParseAddr(s)
		if got := isPrivateAddr(ip); got != want {
			t.Errorf("isPrivateAddr(%s) = %v, want %v", s, got, want)
		}
		host := s
		if ip.Is6() {
			host = "[" + s + "]:443"
		}
		if ip.Zone() == "" { // URL のホストにゾーンは書けない（RFC 6874 の %25 は IsPrivateHost が読まない）
			if got := IsPrivateHost(host); got != want {
				t.Errorf("IsPrivateHost(%q) = %v, want %v", host, got, want)
			}
		}
		if ip.Is4() {
			mapped := netip.AddrFrom16(ip.As16())
			if got := isPrivateAddr(mapped); got != want {
				t.Errorf("isPrivateAddr(%s) = %v, want %v (IPv4-mapped)", mapped, got, want)
			}
			if got := IsPrivateHost("[" + mapped.String() + "]"); got != want {
				t.Errorf("IsPrivateHost([%s]) = %v, want %v (IPv4-mapped)", mapped, got, want)
			}
		}
	}
	for _, s := range private {
		check(s, true)
	}
	for _, s := range global {
		check(s, false)
	}
}

// docs/test-perspectives.md §3.1: 応答は届いたが HTTP として読めない → malformed。何も送らずに閉じるのは connect（最初のバイトが来ていない）。
func TestMalformedResponseIsNotConnect(t *testing.T) {
	serve := func(reply string, closeOnly bool) string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					buf := make([]byte, 4096)
					c.Read(buf) // 要求を少し読んでから答える
					if !closeOnly {
						c.Write([]byte(reply))
					}
					if strings.HasSuffix(reply, "X-Stall: 1\r\n") {
						time.Sleep(3 * time.Second) // ヘッダの途中で黙る: 閉じないので「切れた」ではなく時間切れ
					}
				}(c)
			}
		}()
		return "http://" + l.Addr().String() + "/"
	}
	c := New(Options{Timeout: 2 * time.Second, MaxBody: 1 << 20, Now: testsupport.Clock(testsupport.Now)})
	for name, tc := range map[string]struct {
		reply     string
		closeOnly bool
		want      Reason
	}{
		"broken status line":            {"HTTP/1.1 ZZZ OK\r\nContent-Length: 0\r\n\r\n", false, ReasonMalformed},
		"not http at all":               {"garbage\r\n\r\n", false, ReasonMalformed},
		"broken header":                 {"HTTP/1.1 200 OK\r\nRetry-After: 5\x01x\r\n\r\n", false, ReasonMalformed},
		"headers cut off":               {"HTTP/1.1 200 OK\r\nContent-Type: text/", false, ReasonMalformed},
		"closed without a byte":         {"", true, ReasonConnect},
		"stalls after a partial header": {"HTTP/1.1 200 OK\r\nX-Stall: 1\r\n", false, ReasonTimeout}, // 最初のバイトは来たが、時間切れは時間切れ
	} {
		cl := c
		if tc.want == ReasonTimeout {
			cl = New(Options{Timeout: 300 * time.Millisecond, MaxBody: 1 << 20, Now: testsupport.Clock(testsupport.Now)})
		}
		_, err := cl.Do(context.Background(), Request{Private: PrivateAllowed, Method: "GET", URL: serve(tc.reply, tc.closeOnly)})
		var u *UnreachableError
		if !errors.As(err, &u) || u.Reason != tc.want {
			t.Errorf("%s: err %v, want reason %s", name, err, tc.want)
		}
	}
}
