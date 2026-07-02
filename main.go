package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/bwmarrin/discordgo"
	"github.com/chromedp/cdproto/page"
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

	loadStocks()
	initCaptureSem()

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
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(sockPath); err != nil {
		xvfb.Process.Kill()
		xvfb.Wait()
		log.Fatalf("Xvfbソケット待機タイムアウト: display=%s sock=%s", display, sockPath)
	}
	os.Setenv("DISPLAY", display)

	// VNCサーバー起動（環境変数VNC_ENABLEDが設定されている場合）
	if os.Getenv("VNC_ENABLED") != "" {
		vnc := exec.Command("x11vnc", "-display", display, "-forever", "-nopw", "-shared", "-rfbport", "5900")
		if err := vnc.Start(); err != nil {
			log.Printf("VNCサーバー起動失敗（無視して続行）: %v", err)
		} else {
			defer func() {
				vnc.Process.Kill()
				vnc.Wait()
			}()
			log.Println("VNCサーバー起動完了 :5900")
		}
	}

	// Brave常駐起動（Xvfb上で通常モード）
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
		browserCancel()
		allocCancel()
		xvfb.Process.Kill()
		xvfb.Wait()
		log.Fatalf("Brave起動失敗: %v", err)
	}
	log.Println("Brave常駐起動完了")

	// 初回スクショのコールドスタート（約30秒）を起動時に吸収しておく。
	// これをやらないと最初のユーザーリクエストだけ極端に遅くなる。
	warmUp()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Discord セッション作成失敗: %v", err)
	}

	dg.AddHandler(onMessage)
	dg.AddHandler(onInteraction)
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent

	if err := dg.Open(); err != nil {
		log.Fatalf("Discord 接続失敗: %v", err)
	}
	defer dg.Close()

	// スラッシュコマンド /chart を登録。GUILD_IDがあればそのguildに即時登録、
	// 無ければグローバル登録（Discordへの反映に最大1時間かかる）。
	guildID := os.Getenv("GUILD_ID")
	if _, err := dg.ApplicationCommandCreate(dg.State.User.ID, guildID, chartCommand); err != nil {
		log.Printf("スラッシュコマンド登録失敗: %v", err)
	} else if guildID != "" {
		log.Printf("スラッシュコマンド /chart 登録完了 (guild=%s, 即時反映)", guildID)
	} else {
		log.Println("スラッシュコマンド /chart 登録完了 (グローバル, 反映に最大1時間)")
	}

	log.Println("Bot起動完了。 /chart または !chart <ティッカー> [足種] でチャートを取得できます")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
	log.Println("シャットダウン")
}

var chartCommand = &discordgo.ApplicationCommand{
	Name:        "chart",
	Description: "kabutan.jpの株価チャートを取得",
	Options: []*discordgo.ApplicationCommandOption{
		{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "ticker",
			Description:  "省略可。指定するなら証券コード or 銘柄名（例: 7203 / トヨタ）",
			Required:     false,
			Autocomplete: true,
		},
		{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "ashi",
			Description: "足種（省略時は日足）",
			Required:    false,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{Name: "日足", Value: "daily"},
				{Name: "週足", Value: "weekly"},
				{Name: "月足", Value: "monthly"},
				{Name: "年足", Value: "yearly"},
				{Name: "5分足", Value: "5min"},
				{Name: "1分足", Value: "1min"},
			},
		},
	},
}

//go:embed data/stocks.tsv
var stocksTSV string

type stock struct {
	code   string
	name   string
	search string // マッチ用に正規化した銘柄名
}

var stocks []stock

// loadStocks は埋め込んだ銘柄マスタ(TSV: code<TAB>name)をメモリに読み込む。
func loadStocks() {
	for _, line := range strings.Split(stocksTSV, "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		name := line[tab+1:]
		stocks = append(stocks, stock{code: line[:tab], name: name, search: fold(name)})
	}
	log.Printf("銘柄マスタ読み込み: %d件", len(stocks))
}

// fold は全角英数字→半角・小文字化して検索用に正規化する。
func fold(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９':
			r = r - '０' + '0'
		case r >= 'Ａ' && r <= 'Ｚ':
			r = r - 'Ａ' + 'A'
		case r >= 'ａ' && r <= 'ｚ':
			r = r - 'ａ' + 'a'
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// searchStocks は証券コード前方一致・銘柄名部分一致で候補を最大limit件返す。
func searchStocks(q string, limit int) []*discordgo.ApplicationCommandOptionChoice {
	q = strings.TrimSpace(q)
	if q == "" {
		out := make([]*discordgo.ApplicationCommandOptionChoice, 0, limit)
		for _, st := range stocks {
			out = append(out, choiceOf(st))
			if len(out) >= limit {
				break
			}
		}
		return out
	}
	qCode := normalizeTicker(q)
	qName := fold(q)
	var prefix, contains []*discordgo.ApplicationCommandOptionChoice
	for _, st := range stocks {
		if strings.HasPrefix(st.code, qCode) {
			prefix = append(prefix, choiceOf(st))
			if len(prefix) >= limit {
				break
			}
		}
	}
	for _, st := range stocks {
		if len(prefix)+len(contains) >= limit {
			break
		}
		if !strings.HasPrefix(st.code, qCode) && strings.Contains(st.search, qName) {
			contains = append(contains, choiceOf(st))
		}
	}
	return append(prefix, contains...)
}

func choiceOf(st stock) *discordgo.ApplicationCommandOptionChoice {
	label := st.code + " " + st.name
	if r := []rune(label); len(r) > 100 { // Discordの選択肢名は最大100文字
		label = string(r[:100])
	}
	return &discordgo.ApplicationCommandOptionChoice{Name: label, Value: st.code}
}

// handleAutocomplete は /chart の ticker 入力に対する候補を返す。
func handleAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if data.Name != "chart" {
		return
	}
	var focused string
	for _, opt := range data.Options {
		// 空フォーカス時に Value が nil のことがあるため型アサーションを安全に行う
		// （StringValue() は Value.(string) で panic するため使わない）。
		if opt.Focused && opt.Name == "ticker" {
			if v, ok := opt.Value.(string); ok {
				focused = v
			}
		}
	}
	choices := searchStocks(focused, 25)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	}); err != nil {
		log.Printf("autocomplete応答失敗 (q=%q, 候補=%d): %v", focused, len(choices), err)
	} else {
		log.Printf("autocomplete q=%q 候補=%d件", focused, len(choices))
	}
}

// tickerRe は正規化後の証券コード（半角英数字4文字）を検証する。
// 従来の4桁数字に加え、新形式の英数字コード（例: 130A）も許容する。
var tickerRe = regexp.MustCompile(`^[0-9A-Z]{4}$`)

// normalizeTicker は証券コードを全角→半角・大文字化・トリムして正規化する。
func normalizeTicker(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '０' && r <= '９':
			r = r - '０' + '0'
		case r >= 'Ａ' && r <= 'Ｚ':
			r = r - 'Ａ' + 'A'
		case r >= 'ａ' && r <= 'ｚ':
			r = r - 'ａ' + 'a'
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// respondEphemeral は本人にだけ見えるエラー応答を返す。
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// ashiOrder は足種メニューの表示順（mapは順序が不定のため）。
var ashiOrder = []string{"daily", "weekly", "monthly", "yearly", "5min", "1min"}

// onInteraction は /chart 関連の全 interaction をディスパッチする。
func onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommandAutocomplete:
		handleAutocomplete(s, i)
	case discordgo.InteractionApplicationCommand:
		handleChartCommand(s, i)
	case discordgo.InteractionMessageComponent:
		handleComponent(s, i)
	case discordgo.InteractionModalSubmit:
		handleModalSubmit(s, i)
	}
}

// handleChartCommand は /chart 本体。ticker 未指定なら検索モーダル、
// ticker のみなら足種メニュー、ticker+ashi 揃っていれば直接取得する。
func handleChartCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if data.Name != "chart" {
		return
	}
	var ticker, ashi string
	for _, opt := range data.Options {
		v, _ := opt.Value.(string)
		switch opt.Name {
		case "ticker":
			ticker = v
		case "ashi":
			ashi = v
		}
	}

	// 何も指定がなければ検索モーダルを開く（ナビゲーション開始）。
	if strings.TrimSpace(ticker) == "" {
		s.InteractionRespond(i.Interaction, searchModal())
		return
	}

	ticker = normalizeTicker(ticker)
	if !tickerRe.MatchString(ticker) {
		respondEphemeral(s, i, "証券コードは半角英数字4文字で指定してください（例: 7203）")
		return
	}

	// ticker のみ → 足種選択メニューを表示。
	if ashi == "" {
		d := ashiMenuData(ticker)
		d.Flags = discordgo.MessageFlagsEphemeral
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: d,
		})
		return
	}

	// ticker+ashi 揃っている → 直接取得（従来のパワーユーザー用パス）。
	info, ok := ashiMap[ashi]
	if !ok {
		respondEphemeral(s, i, "不明な足種: "+ashi)
		return
	}
	deferAndCapture(s, i, ticker, ashi, info.selector, info.label)
}

// handleModalSubmit は検索モーダル送信を受け、候補の銘柄メニューを返す。
func handleModalSubmit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	d := i.ModalSubmitData()
	if d.CustomID != "search" {
		return
	}
	data := stockMenuData(modalTextValue(d.Components, "q"))
	data.Flags = discordgo.MessageFlagsEphemeral
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	})
}

// handleComponent はセレクトメニューの選択を処理する。
func handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	d := i.MessageComponentData()
	switch {
	case d.CustomID == "stock":
		if len(d.Values) == 0 {
			return
		}
		// 銘柄が選ばれた → 足種メニューに差し替える。
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: ashiMenuData(d.Values[0]),
		})
	case strings.HasPrefix(d.CustomID, "ashi|"):
		code := strings.TrimPrefix(d.CustomID, "ashi|")
		if len(d.Values) == 0 || !tickerRe.MatchString(code) {
			return
		}
		info, ok := ashiMap[d.Values[0]]
		if !ok {
			return
		}
		// 3秒以内にackしつつ、メニューを「取得中」に差し替える（再クリック防止）。
		content := fmt.Sprintf("📊 %s (%s) を取得中...", code, info.label)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    content,
				Components: []discordgo.MessageComponent{},
			},
		})
		// チャートを取得してチャンネルに公開投稿。
		imgBuf, err := captureChart(code, info.selector)
		if err != nil {
			msg := "エラー: " + err.Error()
			s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: msg,
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}
		if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("**%s** - %s", code, info.label),
			Files: []*discordgo.File{
				{Name: fmt.Sprintf("%s_%s.png", code, d.Values[0]), Reader: bytes.NewReader(imgBuf)},
			},
		}); err != nil {
			log.Printf("画像送信失敗: %v", err)
		}
	}
}

// deferAndCapture は defer 応答→取得→画像で編集する（ticker+ashi直接指定パス）。
func deferAndCapture(s *discordgo.Session, i *discordgo.InteractionCreate, code, ashi, selector, label string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("defer応答失敗: %v", err)
		return
	}
	imgBuf, err := captureChart(code, selector)
	if err != nil {
		msg := "エラー: " + err.Error()
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
		return
	}
	content := fmt.Sprintf("**%s** - %s", code, label)
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Files:   []*discordgo.File{{Name: fmt.Sprintf("%s_%s.png", code, ashi), Reader: bytes.NewReader(imgBuf)}},
	}); err != nil {
		log.Printf("画像送信失敗: %v", err)
	}
}

// searchModal は銘柄検索用のモーダルを返す。
func searchModal() *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "search",
			Title:    "銘柄を検索",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "q",
						Label:       "銘柄名 または 証券コード",
						Style:       discordgo.TextInputShort,
						Placeholder: "例: トヨタ / 7203",
						Required:    true,
					},
				}},
			},
		},
	}
}

// stockMenuData は検索結果の銘柄セレクトメニュー（該当なしはメッセージのみ）を返す。
func stockMenuData(query string) *discordgo.InteractionResponseData {
	matches := searchStocks(query, 25)
	if len(matches) == 0 {
		return &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("「%s」に一致する銘柄が見つかりませんでした。もう一度 /chart で試してください。", query),
		}
	}
	opts := make([]discordgo.SelectMenuOption, 0, len(matches))
	for _, m := range matches {
		opts = append(opts, discordgo.SelectMenuOption{Label: m.Name, Value: m.Value.(string)})
	}
	return &discordgo.InteractionResponseData{
		Content: "銘柄を選択してください",
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					MenuType:    discordgo.StringSelectMenu,
					CustomID:    "stock",
					Placeholder: "銘柄を選択",
					Options:     opts,
				},
			}},
		},
	}
}

// ashiMenuData は足種セレクトメニュー（custom_id に証券コードを埋め込む）を返す。
func ashiMenuData(code string) *discordgo.InteractionResponseData {
	opts := make([]discordgo.SelectMenuOption, 0, len(ashiOrder))
	for _, key := range ashiOrder {
		opts = append(opts, discordgo.SelectMenuOption{Label: ashiMap[key].label, Value: key})
	}
	return &discordgo.InteractionResponseData{
		Content: fmt.Sprintf("**%s** の足種を選択してください", code),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					MenuType:    discordgo.StringSelectMenu,
					CustomID:    "ashi|" + code,
					Placeholder: "足種を選択",
					Options:     opts,
				},
			}},
		},
	}
}

// modalTextValue はモーダル送信データから指定 customID のテキスト入力値を取り出す。
func modalTextValue(components []discordgo.MessageComponent, customID string) string {
	for _, row := range components {
		ar, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, c := range ar.Components {
			if ti, ok := c.(*discordgo.TextInput); ok && ti.CustomID == customID {
				return ti.Value
			}
		}
	}
	return ""
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

	ticker := normalizeTicker(args[1])
	if !tickerRe.MatchString(ticker) {
		s.ChannelMessageSend(m.ChannelID, "証券コードは半角英数字4文字で指定してください（例: `!chart 7203`）")
		return
	}
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

// captureSem は同時キャプチャ数を制限するセマフォ。共有Braveインスタンスに
// 無制限のタブを開くとOOMするため、並行数を絞ってキューイングする。
// （Botはトークンあたり1ゲートウェイ接続=実質1podなので、並行処理は
// このプロセス内のgoroutine+タブで行い、その総量をここで抑える。）
var captureSem chan struct{}

// initCaptureSem は同時実行上限を環境変数 MAX_CONCURRENT（既定3）で初期化する。
func initCaptureSem() {
	n := 3
	if v := os.Getenv("MAX_CONCURRENT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			n = p
		}
	}
	captureSem = make(chan struct{}, n)
	log.Printf("同時キャプチャ上限: %d", n)
}

// warmUp はBrave起動直後に一度だけkabutanを開き、ブラウザ/ページ読み込みの
// コールドスタートを起動時に消化しておく。スクショはClip方式で軽いため撮らず、
// navigate+要素待ちだけに留めて起動を遅延させない（失敗しても無視して続行）。
func warmUp() {
	start := time.Now()
	ctx, cancel := chromedp.NewContext(browserCtx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, err := page.Navigate("https://kabutan.jp/stock/chart?code=7203").Do(ctx)
			return err
		}),
		chromedp.WaitVisible("#kc_area", chromedp.ByQuery),
	)
	if err != nil {
		log.Printf("ウォームアップ失敗（無視して続行）: %v", err)
		return
	}
	log.Printf("ウォームアップ完了 %.1fs", time.Since(start).Seconds())
}

func captureChart(ticker, ashiSelector string) ([]byte, error) {
	url := fmt.Sprintf("https://kabutan.jp/stock/chart?code=%s", ticker)

	// 同時実行数を制限（共有BraveのOOM防止）。上限に達していれば空くまで待つ。
	select {
	case captureSem <- struct{}{}:
	default:
		log.Printf("[%s] 同時実行上限に達したため待機中", ticker)
		captureSem <- struct{}{}
	}
	defer func() { <-captureSem }()

	// 毎回新しいタブを開く。タイムアウトはフェーズごとに分離し、
	// 遅い読み込みがスクショの時間を食い潰さないようにする。
	tabCtx, cancel := chromedp.NewContext(browserCtx)
	defer cancel()

	// フェーズ1: 読み込み。Navigateはload完了（広告・トラッカー含む）を待たず
	// 即座に返し、#kc_area の出現をWaitVisibleで待つ。
	t0 := time.Now()
	loadCtx, loadCancel := context.WithTimeout(tabCtx, 90*time.Second)
	defer loadCancel()
	err := chromedp.Run(loadCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, errText, _, err := page.Navigate(url).Do(ctx)
			if err != nil {
				return err
			}
			if errText != "" {
				return fmt.Errorf("navigate: %s", errText)
			}
			return nil
		}),
		chromedp.WaitVisible("#kc_area", chromedp.ByQuery),
	)
	if err != nil {
		if loadCtx.Err() == context.DeadlineExceeded {
			log.Printf("[%s] 読み込みタイムアウト %.1fs", ticker, time.Since(t0).Seconds())
			return nil, fmt.Errorf("ティッカー「%s」は存在しないか、チャートを読み込めませんでした", ticker)
		}
		return nil, fmt.Errorf("読み込み失敗: %w", err)
	}
	log.Printf("[%s] 読み込み完了 %.1fs", ticker, time.Since(t0).Seconds())

	// フェーズ2: 足種切り替え + レイアウト計測
	t1 := time.Now()
	var rect map[string]float64
	prepCtx, prepCancel := context.WithTimeout(tabCtx, 20*time.Second)
	defer prepCancel()
	err = chromedp.Run(prepCtx,
		// JSでクリックイベントを発火（ラジオボタン形式対応）
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector('%s').click()`, ashiSelector), nil),
		chromedp.Sleep(1500*time.Millisecond),
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
	)
	if err != nil {
		return nil, fmt.Errorf("足種切り替え失敗: %w", err)
	}
	if rect == nil {
		return nil, fmt.Errorf("チャート要素が見つかりませんでした")
	}
	// チャート未描画時などに rect が異常値になると Clip キャプチャが広大な領域を
	// 描こうとして 30秒タイムアウトするため、サニティクランプする。
	cx, cy := rect["x"], rect["y"]
	cw, ch := rect["width"], rect["height"]
	if cx < 0 {
		cx = 0
	}
	if cy < 0 {
		cy = 0
	}
	if cw <= 0 || cw > 2000 {
		cw = 1920
	}
	if ch <= 0 || ch > 3000 {
		ch = 3000
	}
	log.Printf("[%s] 足種切り替え完了 %.1fs (rect w=%.0f h=%.0f → clip w=%.0f h=%.0f)",
		ticker, time.Since(t1).Seconds(), rect["width"], rect["height"], cw, ch)

	// フェーズ3: チャート領域だけをClipでキャプチャ。
	// ページ全体を描画しないので縦長・重い銘柄でも時間が読める＆高速。
	// CaptureBeyondViewportでビューポート外（スクロール先）もドキュメント座標で切り出す。
	t2 := time.Now()
	var buf []byte
	shotCtx, shotCancel := context.WithTimeout(tabCtx, 30*time.Second)
	defer shotCancel()
	err = chromedp.Run(shotCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		buf, e = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(true).
			WithClip(&page.Viewport{
				X:      cx,
				Y:      cy,
				Width:  cw,
				Height: ch,
				Scale:  1,
			}).
			Do(ctx)
		return e
	}))
	if err != nil {
		if shotCtx.Err() == context.DeadlineExceeded {
			log.Printf("[%s] スクショタイムアウト %.1fs", ticker, time.Since(t2).Seconds())
		}
		return nil, fmt.Errorf("スクリーンショット取得失敗: %w", err)
	}
	log.Printf("[%s] スクショ完了 %.1fs", ticker, time.Since(t2).Seconds())

	return buf, nil
}
