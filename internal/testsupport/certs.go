// Package testsupport はテストだけが使う道具を持つ: 証明書の生成、ローカルの HTTP / HTTPS サーバー、固定の時計。
//
// 証明書はテストの中で作る。期限のある証明書をファイルとしてリポジトリに置かない
// （いつか期限が切れてテストが壊れる。docs/test-perspectives.md §1）。
package testsupport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// CA はテスト用の認証局。Root は信頼の起点、Intermediate はサーバー証明書の発行者。
type CA struct {
	Root         *x509.Certificate
	Intermediate *x509.Certificate
	rootKey      *ecdsa.PrivateKey
	intKey       *ecdsa.PrivateKey
}

// LeafOptions はサーバー証明書の作り方。ゼロ値の期間は「now の前後 1 年」。
type LeafOptions struct {
	DNSNames []string
	IPs      []net.IP // テストのサーバーは 127.0.0.1 なので、名前で検証しない接続に使う
	// ExtKeyUsage は証明書の用途。空ならサーバー認証。鎖が規則に合わない場合（用途の不一致）を作るために変える
	ExtKeyUsage []x509.ExtKeyUsage
	NotBefore   time.Time
	NotAfter    time.Time
}

// Leaf はサーバー証明書と、それを送るときの鎖。
type Leaf struct {
	Cert *x509.Certificate
	// TLS は中間証明書を含む鎖を送る tls.Certificate。
	TLS tls.Certificate
	// TLSWithoutIntermediate は中間証明書を送らない tls.Certificate（チェーンの不備を作るため）。
	TLSWithoutIntermediate tls.Certificate
}

// CAOptions は中間の認証局の作り方。ゼロ値なら now の前後 10 年で、CA として使える。
type CAOptions struct {
	RootNotBefore         time.Time
	RootNotAfter          time.Time
	IntermediateNotBefore time.Time
	IntermediateNotAfter  time.Time
	IntermediateNotCA     bool // CA:FALSE の中間（鎖が規則に合わない場合を作る）
	// IntermediateUnhandledCritical は、crypto/x509 が扱えない重要な（critical）拡張を中間に付ける
	IntermediateUnhandledCritical bool
	// IntermediateExtKeyUsage は中間の用途の制限（空なら制限なし）
	IntermediateExtKeyUsage []x509.ExtKeyUsage
}

// NewCA はルートと中間の認証局を作る。now はテストの時計で、有効期間はその前後 10 年。
func NewCA(t testing.TB, now time.Time) *CA {
	t.Helper()
	return NewCAWith(t, now, CAOptions{})
}

// NewCAWith は中間の認証局の作り方を変えた NewCA。
func NewCAWith(t testing.TB, now time.Time, o CAOptions) *CA {
	t.Helper()
	rnb, rna := o.RootNotBefore, o.RootNotAfter
	if rnb.IsZero() {
		rnb = now.AddDate(-10, 0, 0)
	}
	if rna.IsZero() {
		rna = now.AddDate(10, 0, 0)
	}
	inb, ina := o.IntermediateNotBefore, o.IntermediateNotAfter
	if inb.IsZero() {
		inb = now.AddDate(-10, 0, 0)
	}
	if ina.IsZero() {
		ina = now.AddDate(10, 0, 0)
	}
	rootKey := newKey(t)
	root := sign(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "tenken test root"},
		NotBefore:             rnb,
		NotAfter:              rna,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, nil, &rootKey.PublicKey, rootKey)
	var extra []pkix.Extension
	if o.IntermediateUnhandledCritical {
		extra = []pkix.Extension{{Id: []int{1, 3, 6, 1, 4, 1, 99999, 1}, Critical: true, Value: []byte{0x05, 0x00}}}
	}
	intKey := newKey(t)
	inter := sign(t, &x509.Certificate{
		ExtraExtensions:       extra,
		ExtKeyUsage:           o.IntermediateExtKeyUsage,
		Subject:               pkix.Name{CommonName: "tenken test intermediate"},
		NotBefore:             inb,
		NotAfter:              ina,
		IsCA:                  !o.IntermediateNotCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, root, &intKey.PublicKey, rootKey)
	return &CA{Root: root, Intermediate: inter, rootKey: rootKey, intKey: intKey}
}

// Impostor は、Intermediate と同じ名前で同じルートが署名した、鍵の違う中間証明書（鍵を替える前の古い中間証明書の代わり）。
// Leaf が作るサーバー証明書には署名していない。
func (ca *CA) Impostor(t testing.TB, now time.Time) *x509.Certificate {
	t.Helper()
	key := newKey(t)
	return sign(t, &x509.Certificate{
		Subject:               ca.Intermediate.Subject,
		NotBefore:             now.AddDate(-10, 0, 0),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, ca.Root, &key.PublicKey, ca.rootKey)
}

// CrossSigned は、Intermediate と同じ名前・同じ鍵の中間証明書を、別の認証局 by のルートが署名したもの（相互署名）。
// Leaf が作るサーバー証明書の親の候補に、どちらもなる。
func (ca *CA) CrossSigned(t testing.TB, now time.Time, by *CA) *x509.Certificate {
	t.Helper()
	return sign(t, &x509.Certificate{
		Subject:               ca.Intermediate.Subject,
		NotBefore:             now.AddDate(-10, 0, 0),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, by.Root, &ca.intKey.PublicKey, by.rootKey)
}

// SubCA は、Intermediate の下にもう 1 段の中間を置いた認証局（ルート → 中間 → 中間 → サーバー証明書）。
// 返す CA の Intermediate は下の段で、Root は同じルート。upper は上の段。notCA なら下の段を CA:FALSE にする。
func (ca *CA) SubCA(t testing.TB, now time.Time, notCA bool) (sub *CA, upper *x509.Certificate) {
	t.Helper()
	key := newKey(t)
	inter := sign(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "tenken test sub intermediate"},
		NotBefore:             now.AddDate(-10, 0, 0),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  !notCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, ca.Intermediate, &key.PublicKey, ca.intKey)
	return &CA{Root: ca.Root, Intermediate: inter, rootKey: ca.rootKey, intKey: key}, ca.Intermediate
}

// Pool は Root だけを信頼する証明書プール。
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Root)
	return p
}

// Leaf は中間認証局が発行したサーバー証明書を作る。
func (ca *CA) Leaf(t testing.TB, now time.Time, o LeafOptions) Leaf {
	t.Helper()
	nb, na := o.NotBefore, o.NotAfter
	if nb.IsZero() {
		nb = now.AddDate(-1, 0, 0)
	}
	if na.IsZero() {
		na = now.AddDate(1, 0, 0)
	}
	eku := o.ExtKeyUsage
	if len(eku) == 0 {
		eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	key := newKey(t)
	leaf := sign(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: first(o.DNSNames)},
		DNSNames:    o.DNSNames,
		IPAddresses: o.IPs,
		NotBefore:   nb,
		NotAfter:    na,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: eku,
	}, ca.Intermediate, &key.PublicKey, ca.intKey)
	return Leaf{
		Cert:                   leaf,
		TLS:                    tls.Certificate{Certificate: [][]byte{leaf.Raw, ca.Intermediate.Raw}, PrivateKey: key, Leaf: leaf},
		TLSWithoutIntermediate: tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf},
	}
}

// SelfSigned は自己署名のサーバー証明書を作る。
func SelfSigned(t testing.TB, now time.Time, dnsNames ...string) Leaf {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: first(dnsNames)},
		DNSNames:              dnsNames,
		NotBefore:             now.AddDate(0, 0, -1),
		NotAfter:              now.AddDate(0, 0, 30),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	c := sign(t, tmpl, nil, &key.PublicKey, key)
	tc := tls.Certificate{Certificate: [][]byte{c.Raw}, PrivateKey: key, Leaf: c}
	return Leaf{Cert: c, TLS: tc, TLSWithoutIntermediate: tc}
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

// serialLimit はシリアル番号の上限（RFC 5280: 20 オクテット以内の正の整数）。
// ⚠ 共有のカウンタは使わない: 並行するテストで競合する（go test -race が見つけた）。乱数なら共有の状態が無い。
var serialLimit = new(big.Int).Lsh(big.NewInt(1), 127)

// sign は tmpl を parent で署名する。parent が nil なら自己署名。
func sign(t testing.TB, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		t.Fatalf("serial number: %v", err)
	}
	tmpl.SerialNumber = serial
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return c
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
