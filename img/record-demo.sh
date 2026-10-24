#!/usr/bin/env bash
# README のデモ（img/demo.gif）を撮る: 今のソースから tenken を build し、検証サイト（fixture/）を 127.0.0.1 で配信して、
# --no-external で当てるところを vhs で録る。録る tenken の実行は公開のネットワークに出ない。ただし go build はモジュールのキャッシュに
# 無い依存（golang.org/x/net）を取りに行き、vhs は使う Chromium が無ければそれを取りに行く。
# 使い方: img/record-demo.sh          リポジトリのどこからでも。vhs（charmbracelet/vhs）と python3 が要る
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)" # 0700: portfile をほかのプロセスに置かれない（fixture/check.sh と同じ）
server=""
# ⚠ kill の失敗（serve.py がもう終わっている）で後片付けを止めない（set -e が trap の中でも効く。fixture/check.sh と同じ形）
trap 'if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; fi; rm -rf "$work"' EXIT

go build -o "$work/tenken" "$root/cmd/tenken"
python3 "$root/fixture/serve.py" "$work/port" &
server=$!
for _ in $(seq 100); do
  kill -0 "$server" 2>/dev/null || { echo "img/record-demo.sh: fixture/serve.py exited before it started serving" >&2; exit 2; }
  [ -s "$work/port" ] && break
  sleep 0.1
done
[ -s "$work/port" ] || { echo "img/record-demo.sh: fixture/serve.py did not report its port within 10s" >&2; exit 2; }

# sed の置き換え文字列の中の \ & | をエスケープする（mktemp の既定のパスには無いが、TMPDIR によってはありうる）
esc() { printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'; }
sed -e "s|{{PORT}}|$(esc "$(cat "$work/port")")|g" -e "s|{{BIN}}|$(esc "$work")|g" "$root/img/demo.tape" > "$work/demo.tape"
cd "$root"
vhs "$work/demo.tape"
echo "img/record-demo.sh: wrote img/demo.gif ($("$work/tenken" --version))"
