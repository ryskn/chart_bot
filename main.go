package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/chromedp/chromedp"
)

var display = ":99"

func init() {
	if d := os.Getenv("XVFB_DISPLAY"); d != "" {
		display = d
	}
}

var ashiMap = map[string]struct {
	selector string
	label    string
}{
	"daily":   {"#kc_ashi_1", "日足"},
	"weekly":  {"#kc_ashi_2", "週足"},
	"monthly": {"#kc_ashi_3", "月足"},
	"yearly":  {"#kc_ashi_6", "年足"},
	"5min":    {"#kc_ashi_5", "5分足"},
	"1min":    {"#kc_ashi_4", "1分足"},
}

// Chrome常駐用のブラウザコンテキスト
var browserCtx context.Context

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN が設定されていません")
	}

	// Xvfb起動
	xvfb := exec.Command("Xvfb", display, "-screen", "0", "1920x1080x24")
	if err := xvfb.Start(); err != nil {
		log.Fatalf("Xvfb起動失敗: %v", err)
	}
	defer func() {
		xvfb.Process.Kill()
		xvfb.Wait()
	}()
	// Xvfbのソケットが作成されるまで待機
	sockPath := fmt.Sprintf("/tmp/.X11-unix/X%s", strings.TrimPrefix(display, ":"))
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Setenv("DISPLAY", display)

	// Chrome常駐起動（Xvfb上で通常モード）
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"),
		chromedp.WindowSize(1920, 1080),
	)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	if proxy := os.Getenv("PROXY_SERVER"); proxy != "" {
		opts = append(opts, chromedp.ProxyServer(proxy))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	var browserCancel context.CancelFunc
	browserCtx, browserCancel = chromedp.NewContext(allocCtx)
	defer browserCancel()

	if err := chromedp.Run(browserCtx, chromedp.Navigate("about:blank")); err != nil {
		log.Fatalf("Chrome起動失敗: %v", err)
	}
	log.Println("Brave常駐起動完了")

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Discord セッション作成失敗: %v", err)
	}

	dg.AddHandler(onMessage)
	dg.Identify.Intents = discordgo.IntentsGuildMessages

	if err := dg.Open(); err != nil {
		log.Fatalf("Discord 接続失敗: %v", err)
	}
	defer dg.Close()

	log.Println("Bot起動完了。 !chart <ティッカー> [足種] でチャートを取得できます")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
	log.Println("シャットダウン")
}

func onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	if !strings.HasPrefix(m.Content, "!chart") {
		return
	}

	args := strings.Fields(m.Content)
	if len(args) < 2 {
		s.ChannelMessageSend(m.ChannelID, "使い方: `!chart <ティッカー> [daily|weekly|monthly|yearly|5min|1min]`")
		return
	}

	ticker := args[1]
	ashi := "daily"
	if len(args) >= 3 {
		ashi = args[2]
	}

	info, ok := ashiMap[ashi]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "不明な足種: `"+ashi+"` (選択肢: daily, weekly, monthly, yearly, 5min, 1min)")
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("📊 %s (%s) のチャートを取得中...", ticker, info.label))

	imgBuf, err := captureChart(ticker, info.selector)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "エラー: "+err.Error())
		return
	}

	s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("**%s** - %s", ticker, info.label),
		Files: []*discordgo.File{
			{
				Name:   fmt.Sprintf("%s_%s.png", ticker, ashi),
				Reader: bytes.NewReader(imgBuf),
			},
		},
	})
}

func captureChart(ticker, ashiSelector string) ([]byte, error) {
	url := fmt.Sprintf("https://kabutan.jp/stock/chart?code=%s", ticker)

	ctx, cancel := chromedp.NewContext(browserCtx)
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var exists bool
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelector('#kc_area') !== null`, &exists, chromedp.WithPollingInterval(200*time.Millisecond)),
	)
	if err != nil || !exists {
		return nil, fmt.Errorf("ティッカー「%s」は存在しません", ticker)
	}

	var rect map[string]float64
	var buf []byte
	err = chromedp.Run(ctx,
		chromedp.Click(ashiSelector, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`(() => {
			const stock = document.querySelector('#stockinfo');
			const chart = document.querySelector('#kc_area');
			if (!stock || !chart) return null;
			// チャート下端までスクロール
			chart.scrollIntoView(false);
			const sy = window.scrollY;
			const s = stock.getBoundingClientRect();
			const c = chart.getBoundingClientRect();
			return {x: s.x, y: s.y + sy, width: Math.max(s.width, c.width), height: c.bottom + sy - (s.y + sy)};
		})()`, &rect),
		chromedp.FullScreenshot(&buf, 100),
	)
	if err != nil {
		return nil, fmt.Errorf("スクリーンショット取得失敗: %w", err)
	}

	if rect == nil {
		return nil, fmt.Errorf("チャート要素が見つかりませんでした")
	}

	img, err := png.Decode(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("画像デコード失敗: %w", err)
	}

	x := int(math.Round(rect["x"]))
	y := int(math.Round(rect["y"]))
	w := int(math.Round(rect["width"]))
	h := int(math.Round(rect["height"]))

	cropped, ok := img.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	if !ok {
		return nil, fmt.Errorf("SubImage非対応")
	}
	chartImg := cropped.SubImage(image.Rect(x, y, x+w, y+h))

	var out bytes.Buffer
	if err := png.Encode(&out, chartImg); err != nil {
		return nil, fmt.Errorf("PNG書き込み失敗: %w", err)
	}

	return out.Bytes(), nil
}
