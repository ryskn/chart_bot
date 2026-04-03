# chart_bot

kabutan.jp の株価チャートをスクリーンショットして Discord に送信する Bot。

## 実行

```bash
docker run --rm -e DISCORD_TOKEN="<your-token>" ghcr.io/ryskn/chart_bot:latest
```

### WARP経由で実行

Cloudflare WARP をproxyモードで起動し、SOCKS5プロキシ経由でBraveの通信を高速化できる。

```bash
# ホスト側でWARPをproxyモードにする
warp-cli mode proxy
warp-cli connect

# コンテナをWARP経由で実行
docker run --rm \
  -e DISCORD_TOKEN="<your-token>" \
  -e PROXY_SERVER="socks5://host.docker.internal:40000" \
  ghcr.io/ryskn/chart_bot:latest
```

`PROXY_SERVER` を省略すると直接通信になる。

> Linux の Docker では `host.docker.internal` が解決できない場合がある。その場合は `--add-host=host.docker.internal:host-gateway` を追加する。

## Discord コマンド

```
!chart <ティッカー> [足種]
```

| 足種 | 説明 |
|---|---|
| `daily` | 日足（デフォルト） |
| `weekly` | 週足 |
| `monthly` | 月足 |
| `yearly` | 年足 |
| `5min` | 5分足 |
| `1min` | 1分足 |

### 例

```
!chart 7203              # トヨタ 日足
!chart 7203 weekly       # トヨタ 週足
!chart 9984 monthly      # ソフトバンクG 月足
!chart 6758 yearly       # ソニー 年足
!chart 8306 5min         # 三菱UFJ 5分足
```

存在しないティッカーを指定するとエラーメッセージを返します。

## ビルド

```bash
docker build -t chart_bot .
```

## アーキテクチャ

`linux/amd64`, `linux/arm64` 対応。GitHub Actions で自動ビルド・push。

## 技術構成

- **Brave Browser** — 広告ブロッカー内蔵、Xvfb上で通常モード動作
- **chromedp** — Chrome DevTools Protocol で Brave を制御
- **discordgo** — Discord Bot API
- Chrome常駐 + タブ開閉方式で高速レスポンス
