package cert

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
	"github.com/hidetzu/tenken/internal/testsupport"
)

var now = testsupport.Now

func defaults(roots *x509.CertPool) Options {
	return Options{Roots: roots, Now: testsupport.Clock(now), Warn: Percent(25), Fail: Percent(10)}
}

func rules(fs []finding.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	sort.Strings(out)
	return out
}

func check(t *testing.T, certs []*x509.Certificate, o Options) []finding.Finding {
	t.Helper()
	fs, err := Check("example.test:443", certs, o)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// leafRemaining は、now の時点で残りが remaining、有効期間が lifetime の証明書の鎖を作る。
func leafRemaining(t *testing.T, ca *testsupport.CA, lifetime, remaining time.Duration) []*x509.Certificate {
	t.Helper()
	notAfter := now.Add(remaining)
	l := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}, NotBefore: notAfter.Add(-lifetime), NotAfter: notAfter})
	return []*x509.Certificate{l.Cert, ca.Intermediate}
}

// TEST-PERSPECTIVES §3.3: 90 日・47 日・6 日の証明書で、警告（25%）と失敗（10%）の境界の手前・ちょうど・先。
func TestPercentThresholdBoundaries(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	for _, days := range []int{90, 47, 6} {
		lifetime := time.Duration(days) * 24 * time.Hour
		step := lifetime / 100 // 境界の前後（6 日の証明書では 1 日は大きすぎる）
		for _, c := range []struct {
			name      string
			remaining time.Duration
			want      []string
		}{
			{"just above warn", lifetime/4 + step, nil},
			{"exactly warn", lifetime / 4, nil}, // ⚠ ちょうどは下回っていない
			{"just below warn", lifetime/4 - step, []string{"cert.expiring"}},
			{"just above fail", lifetime/10 + step, []string{"cert.expiring"}},
			{"exactly fail", lifetime / 10, []string{"cert.expiring"}},
			{"just below fail", lifetime/10 - step, []string{"cert.expiring-soon"}},
		} {
			got := rules(check(t, leafRemaining(t, ca, lifetime, c.remaining), defaults(ca.Pool())))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("%d-day certificate, %s: got %v, want %v", days, c.name, got, c.want)
			}
		}
	}
}

func TestDayThresholdsOverridePercent(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	o := defaults(ca.Pool())
	o.Warn = Days(30)
	day := 24 * time.Hour
	for remaining, want := range map[time.Duration]string{31 * day: "", 30 * day: "", 30*day - time.Second: "cert.expiring"} {
		// 200 日の証明書: 失敗の既定（10%）は 20 日なので、30 日の前後では警告だけが動く
		got := strings.Join(rules(check(t, leafRemaining(t, ca, 200*day, remaining), o)), ",")
		if got != want {
			t.Errorf("remaining %v with --cert-warn 30d: got %q, want %q", remaining, got, want)
		}
	}
}

// 単位が違って警告が失敗より小さい（逆転した）ときは expiring-soon だけ（docs/cli-reference.md §2.2、§3.2）。
func TestInvertedThresholdsGiveOnlyExpiringSoon(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	o := defaults(ca.Pool())
	o.Warn, o.Fail = Days(5), Percent(10) // 90 日の証明書なら失敗は 9 日、警告は 5 日
	got := rules(check(t, leafRemaining(t, ca, 90*24*time.Hour, 7*24*time.Hour), o))
	if strings.Join(got, ",") != "cert.expiring-soon" {
		t.Fatalf("got %v, want only cert.expiring-soon", got)
	}
}

func TestExpiredIsReportedWithItsDateAndNothingAboutTrust(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	fs := check(t, leafRemaining(t, ca, 90*24*time.Hour, -10*24*time.Hour), defaults(ca.Pool()))
	if got := rules(fs); strings.Join(got, ",") != "cert.expired" {
		t.Fatalf("got %v, want only cert.expired (a trusted chain stays trusted after it expires)", got)
	}
	d := fs[0].Detail
	if d["days_left"] != -10 || d["not_after"] != now.Add(-10*24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("detail = %v", d)
	}
}

func TestNotYetValid(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	l := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}, NotBefore: now.Add(24 * time.Hour), NotAfter: now.AddDate(0, 3, 0)})
	if got := rules(check(t, []*x509.Certificate{l.Cert, ca.Intermediate}, defaults(ca.Pool()))); strings.Join(got, ",") != "cert.not-yet-valid" {
		t.Fatalf("got %v", got)
	}
}

func TestHostnameAndWildcards(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	wild := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"*.example.test"}})
	chain := []*x509.Certificate{wild.Cert, ca.Intermediate}
	for hostport, want := range map[string]string{"a.example.test:443": "", "a.b.example.test:443": "cert.hostname-mismatch", "example.test:443": "cert.hostname-mismatch"} {
		fs, err := Check(hostport, chain, defaults(ca.Pool()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(rules(fs), ","); got != want {
			t.Errorf("%s: got %q, want %q", hostport, got, want)
		}
	}
}

func TestTrust(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	leaf := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	clientOnly := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	self := testsupport.SelfSigned(t, now, "example.test")
	selfPool := x509.NewCertPool()
	selfPool.AddCert(self.Cert)
	cases := []struct {
		name   string
		chain  []*x509.Certificate
		roots  *x509.CertPool
		want   string
		reason any
	}{
		{"full chain", []*x509.Certificate{leaf.Cert, ca.Intermediate}, ca.Pool(), "", nil},
		{"missing intermediate", []*x509.Certificate{leaf.Cert}, ca.Pool(), "cert.untrusted", "issuer-not-found"},
		{"unknown CA", []*x509.Certificate{leaf.Cert, ca.Intermediate}, x509.NewCertPool(), "cert.untrusted", "issuer-not-found"},
		{"self-signed", []*x509.Certificate{self.Cert}, ca.Pool(), "cert.self-signed", nil},
		{"a client-only certificate (usage does not fit)", []*x509.Certificate{clientOnly.Cert, ca.Intermediate}, ca.Pool(), "cert.untrusted", "invalid-chain"},
		{"self-signed but trusted with --ca-file", []*x509.Certificate{self.Cert}, selfPool, "", nil},
	}
	for _, c := range cases {
		o := defaults(c.roots)
		o.Warn, o.Fail = Days(0), Days(0) // 期限の指摘を外して、信頼だけを見る
		fs := check(t, c.chain, o)
		got := strings.Join(rules(fs), ",")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
			continue
		}
		if c.reason != nil && fs[0].Detail["reason"] != c.reason {
			t.Errorf("%s: reason = %v, want %v", c.name, fs[0].Detail["reason"], c.reason)
		}
	}
}

func TestDetailValuesFollowTheSpec(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	nb, na := now.Add(-80*24*time.Hour), now.Add(5*24*time.Hour+23*time.Hour) // 有効期間 85 日 23 時間、残り 5 日 23 時間
	l := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test", "a.example.test"}, IPs: []net.IP{net.ParseIP("192.0.2.1")}, NotBefore: nb, NotAfter: na})
	fs := check(t, []*x509.Certificate{l.Cert, ca.Intermediate}, defaults(ca.Pool()))
	if len(fs) != 1 || fs[0].Rule != "cert.expiring-soon" {
		t.Fatalf("got %v", rules(fs))
	}
	d := fs[0].Detail
	want := map[string]any{
		"not_before":        nb.Format(time.RFC3339),
		"not_after":         na.Format(time.RFC3339),
		"days_left":         5,  // 切り捨て（四捨五入なら 6）
		"lifetime_days":     85, // 切り捨て
		"issuer":            ca.Intermediate.Subject.String(),
		"subject_alt_names": []string{"192.0.2.1", "a.example.test", "example.test"},
	}
	if len(d) != len(want) {
		t.Fatalf("detail has %d keys, want %d (docs/cli-reference.md §5.2): %v", len(d), len(want), d)
	}
	for k, v := range want {
		if got, ok := d[k]; !ok || strings.Join(strs(got), ",") != strings.Join(strs(v), ",") {
			t.Errorf("detail[%s] = %v, want %v", k, got, v)
		}
	}
	if fs[0].Subject != "example.test:443" || len(fs[0].FoundOn) != 0 {
		t.Fatalf("subject %q, found_on %v", fs[0].Subject, fs[0].FoundOn)
	}
}

func strs(v any) []string {
	switch v := v.(type) {
	case []string:
		return v
	default:
		return []string{fmt.Sprint(v)}
	}
}

func TestANoSANCertificateHasAnEmptyArrayNotNull(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	l := ca.Leaf(t, now, testsupport.LeafOptions{NotAfter: now.Add(24 * time.Hour)}) // SAN 無し（名前の不一致も出る）
	fs := check(t, []*x509.Certificate{l.Cert, ca.Intermediate}, defaults(ca.Pool()))
	b, err := json.Marshal(fs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"subject_alt_names":[]`) {
		t.Fatalf("%s: subject_alt_names must be an array", b)
	}
}

// 鎖は今の時刻で判定する。相手の証明書の日付の並びによらない。
func TestTheChainIsJudgedNow(t *testing.T) {
	day := 24 * time.Hour
	expiredInter := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotBefore: now.AddDate(-2, 0, 0), IntermediateNotAfter: now.Add(-day)})
	futureInter := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotBefore: now.Add(day), IntermediateNotAfter: now.AddDate(2, 0, 0)})
	shortInter := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotBefore: now.AddDate(-2, 0, 0), IntermediateNotAfter: now.Add(10 * day)})
	cases := []struct {
		name   string
		ca     *testsupport.CA
		nb, na time.Time
		want   string
	}{
		{"intermediate expired yesterday, leaf midpoint after it", expiredInter, now.Add(-60 * day), now.Add(300 * day), "cert.untrusted"},
		{"intermediate expired yesterday, leaf midpoint before it", expiredInter, now.Add(-300 * day), now.Add(60 * day), "cert.untrusted"},
		{"intermediate not valid until tomorrow", futureInter, now.Add(-30 * day), now.Add(60 * day), "cert.untrusted"},
		{"leaf outlives an issuer that is valid now", shortInter, now.Add(-30 * day), now.Add(300 * day), ""},
	}
	for _, c := range cases {
		l := c.ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}, NotBefore: c.nb, NotAfter: c.na})
		o := defaults(c.ca.Pool())
		o.Warn, o.Fail = Days(0), Days(0)
		fs := check(t, []*x509.Certificate{l.Cert, c.ca.Intermediate}, o)
		if got := strings.Join(rules(fs), ","); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
			continue
		}
		if c.want != "" {
			if fs[0].Detail["reason"] != "invalid-chain" {
				t.Errorf("%s: reason = %v, want invalid-chain", c.name, fs[0].Detail["reason"])
			}
			if strings.Contains(fs[0].Message, "x509:") || strings.Contains(fs[0].Message, "current time") {
				t.Errorf("%s: message leaks crypto/x509 text: %s", c.name, fs[0].Message)
			}
		}
	}
}

func TestANonCAIntermediateThatWasSentIsInvalidChain(t *testing.T) {
	notCA := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotCA: true})
	l := notCA.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	o := defaults(notCA.Pool())
	o.Warn, o.Fail = Days(0), Days(0)
	sent := check(t, []*x509.Certificate{l.Cert, notCA.Intermediate}, o)
	if len(sent) != 1 || sent[0].Detail["reason"] != "invalid-chain" {
		t.Fatalf("sent: %v %v", rules(sent), sent)
	}
	missing := check(t, []*x509.Certificate{l.Cert}, o)
	if len(missing) != 1 || missing[0].Detail["reason"] != "issuer-not-found" {
		t.Fatalf("not sent: %v", rules(missing))
	}
}

// reason は「署名で信頼する CA に届くか」で分ける（docs/cli-reference.md §5.2）。
func TestTheReasonDependsOnReachingATrustedCAByTheSignatures(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	unknown := testsupport.NewCA(t, now) // 同じ名前の中間証明書を持つ、信頼しない CA
	notCA := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotCA: true})
	broken := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateUnhandledCritical: true})
	leaf := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	notCALeaf := notCA.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	brokenLeaf := broken.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	// サーバー認証に使えない（クライアント用の）証明書: Go は鎖を組まずに断るので、署名で届くかを見る
	clientOnly := testsupport.LeafOptions{DNSNames: []string{"example.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	client := ca.Leaf(t, now, clientOnly)
	cross := ca.CrossSigned(t, now, unknown) // 同じ名前・同じ鍵で、信頼しない CA が署名
	sub, upper := ca.SubCA(t, now, false)
	subClient := sub.Leaf(t, now, clientOnly)
	limited := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	limitedLeaf := limited.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	cases := []struct {
		name   string
		chain  []*x509.Certificate
		roots  *x509.CertPool
		reason string
	}{
		{"a sent intermediate under an unknown CA", []*x509.Certificate{leaf.Cert, ca.Intermediate}, unknown.Pool(), "issuer-not-found"},
		{"a same-name intermediate that did not sign the leaf", []*x509.Certificate{leaf.Cert, ca.Impostor(t, now)}, ca.Pool(), "issuer-not-found"},
		{"two same-name intermediates, the usable one first", []*x509.Certificate{notCALeaf.Cert, notCA.Intermediate, unknown.Intermediate}, notCA.Pool(), "invalid-chain"},
		{"two same-name intermediates, the usable one last", []*x509.Certificate{notCALeaf.Cert, unknown.Intermediate, notCA.Intermediate}, notCA.Pool(), "invalid-chain"},
		{"an intermediate under an unknown CA that is also broken", []*x509.Certificate{brokenLeaf.Cert, broken.Intermediate}, unknown.Pool(), "issuer-not-found"},
		{"a sent root that is not trusted", []*x509.Certificate{leaf.Cert, ca.Intermediate, ca.Root}, unknown.Pool(), "issuer-not-found"},
		{"a sent root that is trusted, CA:FALSE intermediate", []*x509.Certificate{notCALeaf.Cert, notCA.Intermediate, notCA.Root}, notCA.Pool(), "invalid-chain"},
		{"two intermediates with the wrong usage, reached in two steps", []*x509.Certificate{subClient.Cert, sub.Intermediate, upper}, ca.Pool(), "invalid-chain"},
		{"cross-signed, the trusted one first", []*x509.Certificate{client.Cert, ca.Intermediate, cross}, ca.Pool(), "invalid-chain"},
		{"cross-signed, the trusted one last", []*x509.Certificate{client.Cert, cross, ca.Intermediate}, ca.Pool(), "invalid-chain"},
		{"an intermediate limited to client use", []*x509.Certificate{limitedLeaf.Cert, limited.Intermediate}, limited.Pool(), "invalid-chain"},
	}
	for _, c := range cases {
		o := defaults(c.roots)
		o.Warn, o.Fail = Days(0), Days(0)
		fs := check(t, c.chain, o)
		if len(fs) != 1 || fs[0].Rule != "cert.untrusted" || fs[0].Detail["reason"] != c.reason {
			var got any
			if len(fs) == 1 {
				got = fs[0].Detail["reason"]
			}
			t.Errorf("%s: got %v reason %v, want cert.untrusted reason %s", c.name, rules(fs), got, c.reason)
		}
	}
}

// 署名で辿るのは送られた証明書の中だけで、同じものを 2 度辿らない（敵対的な入力でも止まる。verification.md § Priority 3）。
func TestTheSearchStopsOnLoopsAndWalksSeveralSteps(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	leaf := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	// 自己署名のルートを何枚も送り、信頼する CA は無い
	chain := []*x509.Certificate{leaf.Cert, ca.Root, ca.Intermediate, ca.Root}
	done := make(chan bool, 1)
	go func() {
		done <- reachesTrustedCA(leaf.Cert, chain[1:], x509.NewCertPool(), now, &search{seen: map[*x509.Certificate]bool{leaf.Cert: true}})
	}()
	select {
	case got := <-done:
		if got {
			t.Fatal("reached a CA that is not trusted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the search did not stop")
	}
	// 2 段: サーバー証明書 → 中間 → 送られたルート（それ自体が信頼する CA）
	if reachesTrustedCA(leaf.Cert, []*x509.Certificate{ca.Root, ca.Intermediate}, x509.NewCertPool(), now, &search{seen: map[*x509.Certificate]bool{leaf.Cert: true}}) {
		t.Fatal("an empty pool trusts nothing")
	}
	if !reachesTrustedCA(leaf.Cert, []*x509.Certificate{ca.Root, ca.Intermediate}, ca.Pool(), now, &search{seen: map[*x509.Certificate]bool{leaf.Cert: true}}) {
		t.Fatal("did not reach the trusted root through the intermediate")
	}
}

// 届いたかどうかに、作った時刻を使わない。
func TestReachIsNotJudgedAtAMadeUpInstant(t *testing.T) {
	// ルートは 1 年前から。中間はそれより前（逆算した日付）から始まり、CA でない。今はどちらも有効
	for _, interStart := range []time.Time{now.AddDate(-2, 0, 0), now.AddDate(-20, 0, 0)} {
		ca := testsupport.NewCAWith(t, now, testsupport.CAOptions{RootNotBefore: now.AddDate(-1, 0, 0), IntermediateNotBefore: interStart, IntermediateNotCA: true})
		l := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
		o := defaults(ca.Pool())
		o.Warn, o.Fail = Days(0), Days(0)
		fs := check(t, []*x509.Certificate{l.Cert, ca.Intermediate}, o)
		if len(fs) != 1 || fs[0].Detail["reason"] != "invalid-chain" {
			t.Errorf("intermediate from %s: got %v (reason %v), want invalid-chain", interStart.Format(time.DateOnly), rules(fs), fs[0].Detail["reason"])
		}
	}
	// ルートが今の時点で期限の外: 3 つの時刻（始まり・終わり・今）のどれかで届けば invalid-chain、どれでも届かなければ
	// 確かめられない（docs/cli-reference.md §5.2 に書いた限界）
	y := func(n int) time.Time { return now.AddDate(n, 0, 0) }
	d := func(n int) time.Time { return now.AddDate(0, 0, n) }
	for _, c := range []struct {
		name                  string
		rootNB, rootNA, intNB time.Time
		intNA                 time.Time
		reason                string
	}{
		{"root expired now, valid when the intermediate was issued", y(-10), d(-1), y(-10), y(10), "invalid-chain"},
		{"root expired now, intermediate issued one day after it began", y(-1), d(-1), y(-1).AddDate(0, 0, 1), y(10), "invalid-chain"},
		{"root not yet valid now, valid at the intermediate's end", d(1), y(10), y(-10), y(10), "invalid-chain"},
		{"root issued after the intermediate and expired now, intermediate still valid", y(-1), d(-1), y(-2), y(10), "issuer-not-found"},
		{"root valid only now, not at the intermediate's start or end", y(-1), y(1), y(-2), y(10), "invalid-chain"},
		{"root valid only around the intermediate's midpoint, expired now", y(-3), y(-1), y(-10), y(6), "issuer-not-found"},
	} {
		// 中間は CA でない: 今の鎖の検証が通らないようにして、reason を決める道を必ず通す
		ca := testsupport.NewCAWith(t, now, testsupport.CAOptions{RootNotBefore: c.rootNB, RootNotAfter: c.rootNA, IntermediateNotBefore: c.intNB, IntermediateNotAfter: c.intNA, IntermediateNotCA: true})
		l := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
		o := defaults(ca.Pool())
		o.Warn, o.Fail = Days(0), Days(0)
		fs := check(t, []*x509.Certificate{l.Cert, ca.Intermediate}, o)
		if len(fs) != 1 || fs[0].Rule != "cert.untrusted" || fs[0].Detail["reason"] != c.reason {
			t.Errorf("%s: got %v, want cert.untrusted reason %s", c.name, rules(fs), c.reason)
		}
	}
}

// 同じ名前の証明書を何百枚も送られても、署名の確認は上限までで止まり、答えは「確かめられない」。
// 上限を超える数の候補があれば、使える証明書がどこにあっても同じ答え（docs/cli-reference.md §5.2 "It does not depend on the order the certificates were sent in"）。
func TestHundredsOfSameNameCertificatesAreBoundedAndNotConfirmed(t *testing.T) {
	// 本物の中間証明書は CA でない: crypto/x509 の検証は通らず、reason を決めるために署名で辿ることになる
	ca := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotCA: true})
	leaf := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	var impostors []*x509.Certificate
	for i := 0; i < 2*maxSignatureChecks; i++ {
		impostors = append(impostors, ca.Impostor(t, now)) // 名前は同じ、署名していない
	}
	o := defaults(ca.Pool())
	o.Warn, o.Fail = Days(0), Days(0)
	// chain は leaf + 本物を位置 pos に置いた n 枚の候補
	chain := func(n, pos int) []*x509.Certificate {
		out := []*x509.Certificate{leaf.Cert}
		out = append(out, impostors[:pos]...)
		out = append(out, ca.Intermediate)
		return append(out, impostors[pos:n-1]...)
	}
	for _, c := range []struct {
		name   string
		n, pos int
		reason string
	}{
		{"twice the limit, the real one first", 2 * maxSignatureChecks, 0, "issuer-not-found"},
		{"twice the limit, the real one last", 2 * maxSignatureChecks, 2*maxSignatureChecks - 1, "issuer-not-found"},
		{"one over the limit, the real one first", maxSignatureChecks + 1, 0, "issuer-not-found"},
		{"exactly the limit, the real one last", maxSignatureChecks, maxSignatureChecks - 1, "invalid-chain"},
	} {
		start := time.Now()
		fs := check(t, chain(c.n, c.pos), o)
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("%s: took %v", c.name, d)
		}
		if len(fs) != 1 || fs[0].Rule != "cert.untrusted" || fs[0].Detail["reason"] != c.reason {
			t.Errorf("%s: got %v, want cert.untrusted reason %s", c.name, rules(fs), c.reason)
			continue
		}
		if c.reason == "issuer-not-found" && !strings.Contains(fs[0].Message, "too many certificates") {
			t.Errorf("%s: message does not say the search stopped: %s", c.name, fs[0].Message)
		}
	}
	if maxSignatureChecks != 100 {
		t.Fatalf("maxSignatureChecks = %d; the cases above were written for 100", maxSignatureChecks)
	}
}

// 上限は「名前で親になりうる組」の数で決めるので、どの枝を先に歩くかに依らない。
// 信頼するルート R → 中間 I → CA でない中間 S の下の証明書に、S と同じ名前・同じ鍵で未知の CA が署名した Sx と、
// Sx の発行者と同じ名前の屑 N 枚を混ぜる。S は I を経ないと届かないので、歩く順で署名の確認を使い切る作りでは
// Sx を先に歩いたときだけ S に届かなくなる。順を入れ替えても答えが同じであることを確かめる。
func TestTheBoundDoesNotDependOnWhichBranchIsWalkedFirst(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	sub, upper := ca.SubCA(t, now, true)
	unknown := testsupport.NewCA(t, now)
	leaf := sub.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	s, sx := sub.Intermediate, sub.CrossSigned(t, now, unknown)
	o := defaults(ca.Pool())
	o.Warn, o.Fail = Days(0), Days(0)
	for _, c := range []struct {
		n    int
		want string
	}{{3, "invalid-chain"}, {98, "issuer-not-found"}} {
		var junk []*x509.Certificate
		for i := 0; i < c.n; i++ {
			junk = append(junk, testsupport.SelfSigned(t, now, unknown.Root.Subject.CommonName).Cert) // Sx の発行者と同じ名前、何にも署名していない
		}
		for _, order := range [][]*x509.Certificate{{s, sx}, {sx, s}} {
			chain := append([]*x509.Certificate{leaf.Cert}, order...)
			chain = append(chain, upper)
			chain = append(chain, junk...)
			fs := check(t, chain, o)
			if len(fs) != 1 || fs[0].Detail["reason"] != c.want {
				t.Errorf("N=%d, S first %v: got %v, want %s", c.n, order[0] == s, rules(fs), c.want)
			}
		}
	}
}

// 自己発行の屑（自分の名前が発行者の名前と同じで、本物の中間とも同じ名前）は互いに組になり、本物の中間とも組になるので、
// 本物の中間 1 枚に K 枚混ぜると組は K² + K + 1（K=9 で 91、K=10 で 111。candidatePairs で測る）。
// 上限 100 には K=10 で当たり、K=9 では当たらない（docs/test-perspectives.md §3.3 の数はここで測る）。
func TestSelfIssuedJunkReachesTheBoundAtTen(t *testing.T) {
	notCA := testsupport.NewCAWith(t, now, testsupport.CAOptions{IntermediateNotCA: true})
	leaf := notCA.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}})
	o := defaults(notCA.Pool())
	o.Warn, o.Fail = Days(0), Days(0)
	for k, want := range map[int]string{9: "invalid-chain", 10: "issuer-not-found"} {
		chain := []*x509.Certificate{leaf.Cert, notCA.Intermediate}
		for i := 0; i < k; i++ {
			chain = append(chain, testsupport.SelfSigned(t, now, notCA.Intermediate.Subject.CommonName).Cert)
		}
		if got := candidatePairs(leaf.Cert, chain[1:]); got != k*k+k+1 {
			t.Errorf("K=%d: %d pairs, the comment says %d", k, got, k*k+k+1)
		}
		if fs := check(t, chain, o); len(fs) != 1 || fs[0].Detail["reason"] != want {
			t.Errorf("K=%d: got %v, want %s", k, rules(fs), want)
		}
	}
}

// 相手の証明書が信頼する CA の直下なら、署名を確かめずに届く。屑を何枚混ぜても上限の前に答えが出る。
func TestALeafDirectlyUnderATrustedCAReachesBeforeTheBound(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	inter := x509.NewCertPool() // 中間を信頼する CA にする: 相手の証明書はその直下
	inter.AddCert(ca.Intermediate)
	leaf := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"example.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}) // 用途が合わず、鎖の検証は落ちる
	chain := []*x509.Certificate{leaf.Cert}
	for i := 0; i < 50; i++ {
		chain = append(chain, testsupport.SelfSigned(t, now, ca.Intermediate.Subject.CommonName).Cert)
	}
	o := defaults(inter)
	o.Warn, o.Fail = Days(0), Days(0)
	if fs := check(t, chain, o); len(fs) != 1 || fs[0].Detail["reason"] != "invalid-chain" {
		t.Fatalf("got %v, want invalid-chain (reached without any signature check)", rules(fs))
	}
}

func TestASelfSignedLeafWithExtraCertificatesIsStillSelfSigned(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	self := testsupport.SelfSigned(t, now, "example.test")
	o := defaults(ca.Pool())
	o.Warn, o.Fail = Days(0), Days(0)
	if got := strings.Join(rules(check(t, []*x509.Certificate{self.Cert, ca.Intermediate}, o)), ","); got != "cert.self-signed" {
		t.Fatalf("got %q", got)
	}
	// 上の CA に署名された証明書は自己署名ではない
	if selfSigned(ca.Intermediate) {
		t.Fatal("an intermediate is not self-signed")
	}
}

func TestFindingsComeSorted(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	// 期限の警告（warn）を先に作り、名前の不一致（fail）をあとに作る: 並べ直さなければ warn が先に来る
	l := ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"other.test"}, NotBefore: now.AddDate(0, -3, 0), NotAfter: now.AddDate(0, 0, 15)})
	fs := check(t, []*x509.Certificate{l.Cert}, defaults(ca.Pool()))
	var got []string
	for _, f := range fs {
		got = append(got, f.Rule)
	}
	if strings.Join(got, ",") != "cert.hostname-mismatch,cert.untrusted,cert.expiring" {
		t.Fatalf("order %v (fail findings by rule ID)", got)
	}
}

// 残り日数は期間から求めるので、now のタイムゾーンで変わらない（構造上の確認。cli-reference.md §5.2 の "divided by 24 hours, rounded down" は TestDetailValuesFollowTheSpec が見る）。
func TestDaysLeftDoesNotDependOnTheTimeZone(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	chain := leafRemaining(t, ca, 90*24*time.Hour, 5*24*time.Hour+time.Hour)
	tokyo := time.FixedZone("JST", 9*60*60)
	utc := check(t, chain, defaults(ca.Pool()))
	o := defaults(ca.Pool())
	o.Now = testsupport.Clock(now.In(tokyo))
	jst := check(t, chain, o)
	if utc[0].Detail["days_left"] != 5 || jst[0].Detail["days_left"] != 5 || utc[0].Message != jst[0].Message {
		t.Fatalf("UTC %v / JST %v", utc[0].Detail, jst[0].Detail)
	}
}

func TestCallerMistakesAreErrors(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	if _, err := Check("example.test:443", nil, defaults(ca.Pool())); !errors.Is(err, ErrNoCertificate) {
		t.Errorf("empty chain: %v", err)
	}
	if _, err := Check("example.test", []*x509.Certificate{ca.Root}, defaults(ca.Pool())); err == nil {
		t.Error("no port: want an error")
	}
}

// fetch.Chain から cert.Check まで: 期限切れのサイトでも、鎖を読んで期限切れと報告する（docs/design.md §1）。
func TestEndToEndExpiredSiteThroughFetch(t *testing.T) {
	ca := testsupport.NewCA(t, now)
	expired := ca.Leaf(t, now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.AddDate(0, -3, 0), NotAfter: now.AddDate(0, 0, -1)})
	s := testsupport.NewTLSServer(t, http.NotFoundHandler(), expired.TLS)
	c := fetch.New(fetch.Options{Timeout: 2 * time.Second, MaxBody: 1 << 20, Now: testsupport.Clock(now), RootCAs: ca.Pool()})
	t.Cleanup(c.Close)
	ch, err := c.Chain(context.Background(), s.URL, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := Check(strings.TrimPrefix(s.URL, "https://"), ch.Certificates, defaults(ca.Pool()))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rules(fs), ","); got != "cert.expired" {
		t.Fatalf("got %q, want cert.expired", got)
	}
}
