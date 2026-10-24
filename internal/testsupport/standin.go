package testsupport

// プライベートアドレスでない（グローバルな）アドレスの代わり。名前の解決の偽の答えや、確かめた接続先として使う。
// ⚠ どれも、テストの dial（DialContext の差し替え）が 127.0.0.1 の自前のサーバーへ向け直すので、このアドレスへのソケットは開かない
// （docs/test-perspectives.md §1）。差し替えた dial を通らない接続にこのアドレスを渡してはいけない。
// ⚠ 文書用のアドレス（192.0.2.0/24、198.51.100.0/24、203.0.113.0/24、2001:db8::/32）はプライベートアドレスなので使えない
// （fetch の isPrivateAddr。docs/cli-reference.md §5.2 の not_fetched.private）。
// IPv4 は 198.18.0.0/15（Benchmarking、プライベートアドレス）のすぐ外の 198.20.0.0/24、IPv6 は 2000::/3 のうち
// 特殊用途の表の行に入らない 2000::1。
const (
	PublicStandIn      = "198.20.0.7"
	OtherPublicStandIn = "198.20.0.8"
	ThirdPublicStandIn = "198.20.0.9"
	PublicStandInV6    = "2000::1"
)
