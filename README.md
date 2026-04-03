# chart_bot

kabutan.jp の株価チャートをスクリーンショットして Discord に送信する Bot。

## 実行

```bash
docker run --rm -e DISCORD_TOKEN="<your-token>" ghcr.io/ryskn/chart_bot:latest
```

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
