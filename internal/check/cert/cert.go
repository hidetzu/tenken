// Package cert は SSL 証明書のチェック（docs/cli-reference.md §2.1 の cert、§2.2 の cert.* のルール）。
// 受け取った証明書の鎖から指摘を返すだけで、取りに行かない・表示しない・終了しない（docs/design.md §2）。
package cert

import (
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"time"

	"github.com/hidetzu/tenken/internal/finding"
)

// Threshold は残りのしきい値。有効期間に対する割合か、日数（docs/cli-reference.md §3.2 の --cert-warn / --cert-fail）。
type Threshold struct {
	percent float64
	days    int
	byDays  bool
}

// Percent は有効期間に対する割合のしきい値（25 なら 25%）。
func Percent(p float64) Threshold { return Threshold{percent: p} }

// Days は日数のしきい値。
func Days(d int) Threshold { return Threshold{days: d, byDays: true} }

// Less は、単位が同じなら t が other より小さいか（same = true）。単位が違えば same = false:
// 大小は証明書の有効期間で決まるので、ここでは言えない（docs/cli-reference.md §3.2）。
func (t Threshold) Less(other Threshold) (less, same bool) {
	if t.byDays != other.byDays {
		return false, false
	}
	if t.byDays {
		return t.days < other.days, true
	}
	return t.percent < other.percent, true
}

// below は、残りがしきい値を「下回った」か。⚠ ちょうどは下回っていない（docs/cli-reference.md §2.2 "below the fail threshold"）。
func (t Threshold) below(remaining, lifetime time.Duration) bool {
	if t.byDays {
		return remaining < time.Duration(t.days)*24*time.Hour
	}
	return float64(remaining) < float64(lifetime)*t.percent/100
}

// Options はチェックの設定。
type Options struct {
	Roots *x509.CertPool   // 信頼する CA（OS の信頼ストアに --ca-file を加えたもの）。nil なら OS の信頼ストア
	Now   func() time.Time // 判定の時刻（docs/design.md §2: 時計は引数）
	Warn  Threshold        // --cert-warn（既定 25%）
	Fail  Threshold        // --cert-fail（既定 10%）
}

// ErrNoCertificate は、鎖が空だったこと（呼び出し側の誤り。相手は TLS で答えている）。
var ErrNoCertificate = errors.New("cert: no certificate in the chain")

// Check は hostport（host:port）の相手が送った鎖 certs を点検して、指摘を返す。
//   - 有効期間について、いちばん重い 1 つだけ: expired / not-yet-valid > expiring-soon > expiring（docs/cli-reference.md §2.2 "Which findings can appear together"）
//   - 信頼について 1 つだけ: self-signed か untrusted
//   - 名前の不一致は、それらと独立に出る
func Check(hostport string, certs []*x509.Certificate, o Options) ([]finding.Finding, error) {
	if len(certs) == 0 {
		return nil, ErrNoCertificate
	}
	// ⚠ net は import しない（接続を開けるパッケージは internal/fetch だけ。docs/design.md §2）。net/url で分ける
	u := &url.URL{Host: hostport}
	host := u.Hostname()
	if host == "" || u.Port() == "" {
		return nil, fmt.Errorf("cert: %q is not host:port", hostport)
	}
	leaf := certs[0]
	now := o.Now().UTC()
	detail := func() map[string]any {
		return map[string]any{
			"not_before":        leaf.NotBefore.UTC().Format(time.RFC3339),
			"not_after":         leaf.NotAfter.UTC().Format(time.RFC3339),
			"days_left":         days(leaf.NotAfter.Sub(now)),
			"lifetime_days":     days(leaf.NotAfter.Sub(leaf.NotBefore)),
			"issuer":            leaf.Issuer.String(),
			"subject_alt_names": altNames(leaf),
		}
	}
	var out []finding.Finding
	add := func(rule, msg string, d map[string]any) {
		out = append(out, finding.New(rule, hostport, msg, d))
	}

	// 有効期間（crypto/x509 と同じく、NotAfter のその瞬間までは有効）
	remaining, lifetime := leaf.NotAfter.Sub(now), leaf.NotAfter.Sub(leaf.NotBefore)
	switch {
	case now.After(leaf.NotAfter):
		add("cert.expired", fmt.Sprintf("%s: the certificate expired on %s", hostport, leaf.NotAfter.UTC().Format(time.DateOnly)), detail())
	case now.Before(leaf.NotBefore):
		add("cert.not-yet-valid", fmt.Sprintf("%s: the certificate is not valid until %s", hostport, leaf.NotBefore.UTC().Format(time.DateOnly)), detail())
	case o.Fail.below(remaining, lifetime):
		add("cert.expiring-soon", expiringMessage(hostport, leaf, remaining, lifetime), detail())
	case o.Warn.below(remaining, lifetime):
		add("cert.expiring", expiringMessage(hostport, leaf, remaining, lifetime), detail())
	}

	// 名前（SAN）。ワイルドカードは 1 段だけ（crypto/x509 の規則）
	if err := leaf.VerifyHostname(host); err != nil {
		add("cert.hostname-mismatch", fmt.Sprintf("%s: the certificate is not valid for %s", hostport, host), detail())
	}

	// 信頼。鎖は今の時刻で判定する（docs/cli-reference.md §2.2）。
	// ⚠ ただし相手の証明書自体が期限切れ・未発効なら、その有効期間の端の時刻で判定する: 期限の指摘はすでに出したので、
	//   ここでは「それ以外に信頼できない理由があるか」だけを答える。中間証明書やルートが今の時点で期限切れ・未発効なら invalid-chain。
	at := clamp(now, leaf)
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: o.Roots, Intermediates: inter, CurrentTime: at}); err != nil {
		st := &search{seen: map[*x509.Certificate]bool{leaf: true}}
		switch {
		case selfSigned(leaf):
			add("cert.self-signed", fmt.Sprintf("%s: the certificate is self-signed", hostport), detail())
		case !reaches(leaf, certs[1:], o.Roots, at, st):
			// 中間証明書の欠落か、未知の認証局か、送られた証明書が使えない。TLS の性質上、区別できない（docs/design.md §1）
			d := detail()
			d["reason"] = "issuer-not-found"
			msg := "no trusted issuer could be confirmed (an intermediate certificate is missing, the issuer is unknown, or a sent certificate could not be used)"
			if st.stopped {
				msg = "no trusted issuer could be confirmed (too many certificates with the issuer's name were sent to check them all)"
			}
			add("cert.untrusted", fmt.Sprintf("%s: %s", hostport, msg), d)
		default:
			// 署名では信頼する CA まで届くが、規則に合わない（CA でない、用途が合わない、期限の外、…）。
			// ⚠ crypto/x509 の文はそのまま人に見せない（CLAUDE.md §4）
			d := detail()
			d["reason"] = "invalid-chain"
			add("cert.untrusted", fmt.Sprintf("%s: the certificates reach a trusted CA but break a rule (a certificate is not a CA, has the wrong usage, or is outside its validity period)", hostport), d)
		}
	}
	finding.Sort(out)
	return out, nil
}

// maxSignatureChecks は、届くかを探すときに確かめる署名の数の上限。
// ⚠ 同じ名前の証明書を何百枚も送られても時間が有限になる。送られた証明書の中で「名前で親になりうる組」の数が
// これを超えれば、どれも確かめずに「確かめられない」= issuer-not-found（docs/cli-reference.md §5.2）。
// ⚠ 組の数は送られた順に依らない（集合の性質）ので、答えも依らない。探しながら数えると、どの枝を先に歩いたかで
// 上限に当たるかが変わる。
// 組を数えるのは n² の名前の比較。n は crypto/tls が 1 回の握手で受け取る証明書の数（メッセージの上限 256 KiB。
// 小さな証明書でも 1000 枚程度）で抑えられていて、fetch 以外から鎖が来ることはない（docs/design.md §2）。
const maxSignatureChecks = 100

// search は、届くかを探す 1 回分の状態。
type search struct {
	seen    map[*x509.Certificate]bool // 同じ証明書を 2 度辿らない（名前の循環、送られた自己署名のルート）
	stopped bool                       // 上限のために確かめなかった
}

// reaches は、上限を見てから reachesTrustedCA を呼ぶ。相手の証明書が信頼する CA の直下なら署名を 1 つも確かめずに
// 届くので、上限より先に見る（屑を混ぜられても、届くものは届く）。
func reaches(leaf *x509.Certificate, rest []*x509.Certificate, roots *x509.CertPool, at time.Time, st *search) bool {
	if verifiesDirectly(leaf, roots, at) {
		return true
	}
	if candidatePairs(leaf, rest) > maxSignatureChecks {
		st.stopped = true
		return false
	}
	return reachesTrustedCA(leaf, rest, roots, at, st)
}

// candidatePairs は、送られた証明書の中で「名前で親になりうる組」の数。
// 探索が確かめる署名の数はこれを超えない（探索も同じ namesAsIssuer で候補を選ぶ）。
func candidatePairs(leaf *x509.Certificate, rest []*x509.Certificate) int {
	n := 0
	for _, c := range append([]*x509.Certificate{leaf}, rest...) {
		for _, p := range rest {
			if p != c && namesAsIssuer(c, p) {
				n++
			}
		}
	}
	return n
}

// namesAsIssuer は、c の発行者の名前が p の主体の名前か（名前だけ。署名は見ない）。
// ⚠ 数えるときと探すときで同じ問いを使う（CLAUDE.md §3: 同じ問いの実装を 2 つ持たない）。
func namesAsIssuer(c, p *x509.Certificate) bool { return string(p.RawSubject) == string(c.RawIssuer) }

// verifiesDirectly は、c が送られた証明書なしに信頼する CA まで検証できるか（3 つの時刻。docs/cli-reference.md §5.2）。
func verifiesDirectly(c *x509.Certificate, roots *x509.CertPool, at time.Time) bool {
	for _, t := range []time.Time{clamp(at, c), c.NotBefore, c.NotAfter} {
		if _, err := c.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: t, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err == nil {
			return true
		}
	}
	return false
}

// reachesTrustedCA は、c から送られた証明書 rest を「署名で」辿って、信頼する CA に届く道があるか（docs/cli-reference.md §5.2）。
// Go の鎖の組み立ては使えない親（CA でない、など）を飛ばし、エラーの種類も最初に当たった理由しか言わないので、
// reason は crypto/x509 のエラーの種類ではなく、ここで決める。
//   - c 自体が信頼する CA で検証できれば届いている。用途は問わない（サーバー証明書の規則）
//   - 時刻は作らない: 見るのは c の「始まり」「終わり」と、at（今。相手の証明書が期限の外ならその端）を c の有効期間に
//     収めた時刻の 3 つだけ（docs/cli-reference.md §5.2）。⚠ 信頼する CA の有効期間は読めない
//     （OS の信頼ストア）ので、始まりで「c を発行したとき CA は有効だった」という普通の鎖を、終わりで鍵を同じにして
//     発行し直したルートを、今で「今はどちらも有効」を拾う。どれでも検証できなければ、届いたことは確かめられない
//     （届かない側に倒す）
//   - 名前が同じでも、c に署名していない証明書は親にしない（鍵を替える前の古い中間証明書など）
//   - 候補はすべて試す: 答えが送られた順に依らない
func reachesTrustedCA(c *x509.Certificate, rest []*x509.Certificate, roots *x509.CertPool, at time.Time, st *search) bool {
	if verifiesDirectly(c, roots, at) {
		return true
	}
	for _, p := range rest {
		if st.seen[p] || !namesAsIssuer(c, p) || p.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) != nil {
			continue
		}
		st.seen[p] = true
		if reachesTrustedCA(p, rest, roots, at, st) {
			return true
		}
	}
	return false
}

// clamp は t を c の有効期間に収める。
func clamp(t time.Time, c *x509.Certificate) time.Time {
	if t.After(c.NotAfter) {
		return c.NotAfter
	}
	if t.Before(c.NotBefore) {
		return c.NotBefore
	}
	return t
}

// selfSigned は、自分の鍵で自分に署名した証明書か（名前は見ない: 自分の鍵で署名が検証できることがすべて）。
// ⚠ CheckSignatureFrom は親が CA であることを求めるので使わない: 自己署名のサーバー証明書の多くは CA:FALSE で、それでも自己署名である。
func selfSigned(c *x509.Certificate) bool {
	return c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}

// days は期間を 24 時間で割って切り捨てる（暦の日付の差ではない。期限切れなら負）。docs/cli-reference.md §5.2
func days(d time.Duration) int { return int(math.Floor(d.Hours() / 24)) }

func expiringMessage(hostport string, leaf *x509.Certificate, remaining, lifetime time.Duration) string {
	pct := int(math.Floor(float64(remaining) / float64(lifetime) * 100))
	return fmt.Sprintf("%s: expires %s (%d days left, %d%% of %d days)", hostport,
		leaf.NotAfter.UTC().Format(time.DateOnly), days(remaining), pct, days(lifetime))
}

// altNames は SAN の名前と IP。⚠ 無くても空の配列（JSON の null にしない。docs/cli-reference.md §5.2 は配列）。
func altNames(c *x509.Certificate) []string {
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}
