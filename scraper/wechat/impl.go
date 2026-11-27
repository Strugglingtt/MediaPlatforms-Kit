package wechat

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"MediaPlatforms-Kit/scraper"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type Result = scraper.Result
type Video = scraper.Video
type Account = scraper.Account
type PendingLogin = scraper.PendingLogin

const (
	LoginStageScanned = scraper.LoginStageScanned
	LoginStageTimeout = scraper.LoginStageTimeout
	wechatHomeURL     = "https://channels.weixin.qq.com/platform"
	wechatListURL     = "https://channels.weixin.qq.com/platform/post/list"
	wechatUA          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

var (
	logInfo           = scraper.LogInfo
	logErr            = scraper.LogErr
	errF              = scraper.Errf
	urlParse          = scraper.UrlParse
	toInt             = scraper.ToInt
	normalizeTime     = scraper.NormalizeTime
	evalJSON          = scraper.EvalJSON
	listFrames        = scraper.ListFrames
	frameText         = scraper.FrameText
	currentURL        = scraper.CurrentURL
	validVideoID      = scraper.ValidVideoID
	videoIDFromURL    = scraper.VideoIDFromURL
	resolveURL        = scraper.ResolveURL
	safeEval          = scraper.SafeEval
	nowStr            = scraper.NowStr
	loginWaitDuration = scraper.LoginWaitDuration
	wechatBrowserArgs = []string{"no-sandbox", "disable-setuid-sandbox", "disable-dev-shm-usage", "disable-blink-features=AutomationControlled"}
	wechatNumRe       = regexp.MustCompile(`^\d+(?:\.\d+)?[万亿]?$`)
	wechatIDRe        = regexp.MustCompile(`视频号ID[:：]\s*([A-Za-z0-9_\-]+)`)
	wechatFollowerRe  = regexp.MustCompile(`关注者\s*([\d.]+[万亿]?)`)
	wechatCountRe     = regexp.MustCompile(`视频\s*[（(]\s*(\d+)\s*[)）]`)
	dateRe            = regexp.MustCompile(`^(\d{4})年(\d{1,2})月(\d{1,2})日\s*(\d{1,2}):(\d{2})$`)
	wechatNonTitles   = map[string]bool{
		"发表视频": true, "视频管理": true, "特效创作工具": true, "合集": true,
		"置顶": true, "分享": true, "评论管理": true, "修改描述和封面": true,
		"可见权限": true, "删除": true, "全部视频": true,
	}
)

const wechatStealthJS = `
	Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
`

const itemsJS = `() => {
  const out = [];
  document.querySelectorAll('div.post-feed-item').forEach((it) => {
    const t = it.querySelector('.post-title, .post-desc, .post-description, [class*="post-title"], [class*="post-desc"]');
    const tm = it.querySelector('.time-label');
    const lines = (it.innerText || '').split(/\n+/).map(s => s.trim()).filter(Boolean);
    const timeIndex = lines.findIndex(s => /^\d{4}年\d{1,2}月\d{1,2}日/.test(s));
    const fallbackTitle = lines.slice(0, timeIndex < 0 ? 0 : timeIndex).find(s =>
      s.length > 1 && s.length < 200 && !/^(?:\d{1,2}:\d{2}|\d+(?:\.\d+)?[万亿]?|编辑|修改描述和封面|置顶|分享|删除|可见权限|已发表)$/.test(s));
    const d = {};
    it.querySelectorAll('.post-data .data-item').forEach((row) => {
      const el = row.querySelector('.icon');
      if (!el) return;
      const cls = el.getAttribute('class') || '';
      let href = '';
      if (el.tagName && el.tagName.toLowerCase() === 'svg') {
        const u = el.querySelector('use');
        if (u) href = (u.getAttribute('xlink:href') || u.getAttribute('href') || '');
      }
      let key = '';
      if (cls.indexOf('eyes-on') >= 0) key = 'views';
      else if (cls.indexOf('like') >= 0) key = 'likes';
      else if (cls.indexOf('comment') >= 0) key = 'comments';
      else if (href.indexOf('share') >= 0) key = 'shares';
      else if (href.indexOf('thumb') >= 0) key = 'favorites';
      if (!key) return;
      const c = row.querySelector('.count');
      d[key] = c ? String(c.textContent).trim() : '0';
    });
    const idNode = it.matches('[data-video-id], [data-post-id]') ? it : it.querySelector('[data-video-id], [data-post-id]');
    const post = it.__vue__ && it.__vue__.$props && it.__vue__.$props.post;
    out.push({
      video_id: post && typeof post.exportId === 'string' && /^export\/[A-Za-z0-9_-]+$/.test(post.exportId)
        ? post.exportId.slice('export/'.length) : (idNode ? (idNode.getAttribute('data-video-id') || idNode.getAttribute('data-post-id') || '') : ''),
      links: [...it.querySelectorAll('a[href]')].map(a => a.href),
      title: (t ? String(t.textContent).trim() : '') || fallbackTitle || '',
      publish_time: tm ? String(tm.textContent).trim() : '',
      stats: d,
    });
  });
  return out;
}`

const wechatQRSrcJS = `() => {
  const img = document.querySelector('img.web_qrcode_img, img.js_qrcode_img');
  return img ? (img.getAttribute('src') || '') : '';
}`

const wechatScrollJS = `() => {
  const cands = [document.scrollingElement, document.body];
  document.querySelectorAll('*').forEach((e) => {
    if (e.scrollHeight > e.clientHeight + 50) cands.push(e);
  });
  cands.forEach((e) => { try { e.scrollTop = e.scrollHeight; } catch (_) {} });
  try { window.scrollTo(0, document.body.scrollHeight); } catch (_) {}
}`

type wechatRawItem struct {
	VideoID     string
	Title       string
	PublishTime string
	Views       string
	Likes       string
	Comments    string
	Shares      string
	Favorites   string
}

func parseWechatTextItems(text string) []wechatRawItem {
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			lines = append(lines, s)
		}
	}
	items := []wechatRawItem{}
	for i, line := range lines {
		m := dateRe.FindStringSubmatch(line)
		if m == nil || i == 0 {
			continue
		}
		var nums []string
		for j := i + 1; j < len(lines) && len(nums) < 5 && wechatNumRe.MatchString(lines[j]); j++ {
			nums = append(nums, lines[j])
		}
		if len(nums) == 0 {
			continue
		}
		title := lines[i-1]
		if wechatNonTitles[title] {
			title = ""
		}
		pick := func(k int) string {
			if k < len(nums) {
				return nums[k]
			}
			return "0"
		}
		items = append(items, wechatRawItem{
			Title: title, PublishTime: normalizeTime(line), Views: pick(0), Likes: pick(1),
			Comments: pick(2), Shares: pick(3), Favorites: pick(4),
		})
	}
	return items
}

func extractWechatItems(scope *rod.Page) []wechatRawItem {
	items := []wechatRawItem{}
	var raw []struct {
		VideoID     string            `json:"video_id"`
		Links       []string          `json:"links"`
		Title       string            `json:"title"`
		PublishTime string            `json:"publish_time"`
		Stats       map[string]string `json:"stats"`
	}
	if !evalJSON(scope, itemsJS, &raw) {
		raw = nil
	}
	for _, r := range raw {
		title := strings.TrimSpace(r.Title)
		ptime := normalizeTime(r.PublishTime)
		if title == "" && ptime == "" {
			continue
		}
		get := func(k string) string {
			if v, ok := r.Stats[k]; ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
			return "0"
		}
		id := strings.TrimSpace(r.VideoID)
		id = strings.TrimPrefix(id, "export/")
		if !validVideoID(id) {
			id = ""
		}
		if id == "" {
			for _, link := range r.Links {
				if id = videoIDFromURL("wechat", link); id != "" {
					break
				}
			}
		}
		items = append(items, wechatRawItem{
			VideoID: id, Title: title, PublishTime: ptime, Views: get("views"), Likes: get("likes"),
			Comments: get("comments"), Shares: get("shares"), Favorites: get("favorites"),
		})
	}
	textItems := parseWechatTextItems(frameText(scope))
	if len(items) >= len(textItems) {
		return items
	}
	return textItems
}

func parseWechatAccount(text string) *Account {
	acc := &Account{}
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			lines = append(lines, s)
		}
	}
	for i, ln := range lines {
		if ln == "视频号 · 助手" && i+1 < len(lines) {
			n := lines[i+1]
			acc.Name = &n
			break
		}
	}
	if m := wechatIDRe.FindStringSubmatch(text); m != nil {
		acc.SphID = &m[1]
	}
	if m := wechatFollowerRe.FindStringSubmatch(text); m != nil {
		v := toInt(m[1])
		acc.Followers = &v
	}
	return acc
}

func findWechatContentFrame(page *rod.Page) *rod.Page {
	frames := listFrames(page)
	for _, fr := range frames {
		if strings.Contains(frameText(fr), "视频管理") {
			return fr
		}
	}
	best, bestLen := frames[0], len(frameText(frames[0]))
	for _, fr := range frames[1:] {
		if t := frameText(fr); len(t) > bestLen {
			bestLen, best = len(t), fr
		}
	}
	return best
}

func waitWechatContentFrame(page *rod.Page, timeoutS int) *rod.Page {
	for i := 0; i < timeoutS; i++ {
		fr := findWechatContentFrame(page)
		if strings.Contains(frameText(fr), "视频管理") {
			return fr
		}
		time.Sleep(1 * time.Second)
	}
	return findWechatContentFrame(page)
}

func isWechatLoginPage(page *rod.Page) bool {
	if strings.Contains(currentURL(page), "login") {
		return true
	}
	for _, fr := range listFrames(page) {
		u := currentURL(fr)
		if strings.Contains(u, "login.html") || strings.Contains(u, "qrconnect") {
			return true
		}
	}
	return false
}

func downloadQR(b *rod.Browser, rawURL string) ([]byte, error) {
	u, err := urlParse(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", wechatUA)
	req.Header.Set("Referer", "https://open.weixin.qq.com/")
	if cookies, err := b.GetCookies(); err == nil {
		var parts []string
		for _, c := range cookies {
			d := strings.TrimPrefix(c.Domain, ".")
			if d != "" && strings.HasSuffix(u.Host, d) {
				parts = append(parts, c.Name+"="+c.Value)
			}
		}
		if len(parts) > 0 {
			req.Header.Set("Cookie", strings.Join(parts, "; "))
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errF("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func wechatCaptureQR(page *rod.Page, b *rod.Browser, qrPath string) error {
	if dir := filepath.Dir(qrPath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	qrSrc, qrFrame := "", (*rod.Page)(nil)
	for i := 0; i < 20; i++ {
		for _, fr := range listFrames(page) {
			var src string
			if evalJSON(fr, wechatQRSrcJS, &src) && src != "" {
				qrSrc, qrFrame = src, fr
				break
			}
		}
		if qrSrc != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if qrSrc == "" {
		log.Println("警告：未能定位二维码图片，将保存整页截图作为兜底。")
		return saveScreenshot(page, false, qrPath)
	}
	fullURL := resolveURL(currentURL(qrFrame), qrSrc)
	if bin, err := downloadQR(b, fullURL); err == nil && len(bin) > 100 {
		if err := os.WriteFile(qrPath, bin, 0o644); err == nil {
			log.Printf("二维码已保存至: %s\n       来源: %s", qrPath, fullURL)
			return nil
		}
	} else if err != nil {
		log.Println("下载二维码失败:", err)
	}
	if qrFrame != nil {
		if el, err := qrFrame.Timeout(3 * time.Second).Element("img.web_qrcode_img, img.js_qrcode_img"); err == nil {
			if bin, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0); err == nil && len(bin) > 0 {
				_ = os.WriteFile(qrPath, bin, 0o644)
				log.Printf("二维码已通过元素截图保存至: %s", qrPath)
				return nil
			}
		}
	}
	log.Println("元素截图失败，改用整页截图")
	if err := saveScreenshot(page, false, qrPath); err != nil {
		return err
	}
	log.Printf("整页截图已保存至: %s", qrPath)
	return nil
}

func saveScreenshot(p *rod.Page, fullPage bool, path string) error {
	bin, err := p.Timeout(30*time.Second).Screenshot(fullPage, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return err
	}
	return os.WriteFile(path, bin, 0o644)
}

func keepWechatLoginAlive(page *rod.Page, b *rod.Browser, opts WechatOptions, session *PendingLogin) {
	defer session.Finish()
	defer b.Close()
	deadline := time.Now().Add(loginWaitDuration())
	round := 0
	for time.Now().Before(deadline) {
		u := currentURL(page)
		if strings.Contains(u, "/platform") || strings.Contains(u, "/post/list") {
			time.Sleep(3 * time.Second)
			if err := scraper.SaveState(b, opts.CookiePath); err != nil {
				logErr("[wechat] 保存 cookie 失败:", err)
			} else {
				logInfo("[wechat] 扫码登录成功，cookie 已保存:", opts.CookiePath)
			}
			_ = os.Remove(opts.QRPath)
			if opts.LoginOnly {
				scraper.WriteStatus(opts.StatusPath, "", false, "登录成功")
				session.SetResult(wechatLoginResult(), nil)
				return
			}
			scraper.WriteStatus(opts.StatusPath, "", false, "登录成功，正在采集数据")
			session.SetStage(LoginStageScanned, "扫码成功，正在采集视频数据")
			res, err := collectWechat(page, b, opts)
			if err != nil {
				logErr("[wechat] 扫码后采集失败:", err)
				session.SetResult(nil, err)
			} else {
				logInfo("[wechat] 扫码后采集完成，条数:", res.VideoCount)
				session.SetResult(res, nil)
			}
			return
		}
		round++
		if round%4 == 0 {
			if err := wechatCaptureQR(page, b, opts.QRPath); err == nil {
				if bin, err := os.ReadFile(opts.QRPath); err == nil {
					_, _, msg := session.QR()
					session.UpdateQR(scraper.QRDataURL(bin), msg)
				}
			}
		}
		time.Sleep(5 * time.Second)
	}
	_ = os.Remove(opts.QRPath)
	scraper.WriteStatus(opts.StatusPath, "", true, "扫码登录超时")
	session.SetOutcome(LoginStageTimeout, "扫码登录超时，请重新请求接口获取新的二维码")
	logInfo("[wechat] 等待扫码登录超时，浏览器已关闭")
}

type WechatOptions struct {
	CookiePath string
	ProfileDir string
	Headless   bool
	QRPath     string
	StatusPath string
	NoWaitScan bool
	LoginOnly  bool
}

func wechatLoginResult() *Result {
	return &Result{Platform: "wechat", FetchedAt: nowStr(), Videos: []*Video{}, Message: "登录成功"}
}

func ScrapeWechat(opts WechatOptions) (res *Result, err error) {
	defer func() {
		if err != nil {
			scraper.WriteStatus(opts.StatusPath, "", true, "抓取失败: "+err.Error())
		}
	}()
	chrome := scraper.FindChrome()
	if chrome == "" {
		log.Println("未找到本机 Chrome，将使用 rod 自动下载的浏览器（服务器环境建议使用）")
	}
	state, err := scraper.LoadState(opts.CookiePath)
	if err != nil {
		return nil, err
	}
	profileHasState := scraper.PrepareProfile(opts.ProfileDir)
	b, err := scraper.LaunchBrowser(scraper.BrowserOptions{
		Headless: opts.Headless, Bin: chrome, UserAgent: wechatUA,
		Args: wechatBrowserArgs, UserDataDir: opts.ProfileDir,
	})
	if err != nil {
		return nil, errF("浏览器启动失败: %v（配置目录 %s 可能被其他浏览器进程占用；可用 videoscraper cleanup 清理残留进程后重试）", err, opts.ProfileDir)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			b.Close()
		}
	}()
	if profileHasState {
		logInfo("[wechat] 使用持久化浏览器配置:", opts.ProfileDir)
	} else {
		if cookies := state.ToRodCookies(); len(cookies) > 0 {
			if err := b.SetCookies(cookies); err != nil {
				return nil, err
			}
		}
		scraper.ApplyStorage(b, state)
	}
	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, err
	}
	if _, err := page.EvalOnNewDocument(wechatStealthJS); err != nil {
		return nil, err
	}
	_ = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: 1440, Height: 900, DeviceScaleFactor: 1, Mobile: false,
	})
	if err := page.Timeout(60 * time.Second).Navigate(wechatListURL); err != nil {
		return nil, err
	}
	time.Sleep(4 * time.Second)
	if isWechatLoginPage(page) {
		if opts.QRPath == "" {
			return nil, errF("登录已失效，且未指定 qr_path 无法截取二维码。")
		}
		log.Println("检测到登录失效，正在获取登录二维码...")
		scraper.WriteStatus(opts.StatusPath, "", true, "登录已失效，等待扫码")
		if err := wechatCaptureQR(page, b, opts.QRPath); err != nil {
			return nil, errF("获取登录二维码失败: %v", err)
		}
		if opts.NoWaitScan {
			qrBin, _ := os.ReadFile(opts.QRPath)
			session := scraper.NewPendingLogin(scraper.QRDataURL(qrBin), opts.QRPath, "登录已过期，请扫描返回的二维码（二维码在后台持续等待，最长 10 分钟）")
			scraper.WriteStatus(opts.StatusPath, "", true, "等待扫码登录")
			logInfo("[wechat] 已返回二维码，浏览器保持运行等待扫码，最长", loginWaitDuration())
			handedOff = true
			go keepWechatLoginAlive(page, b, opts, session)
			message := "登录已过期，请扫描返回的二维码；扫码成功后浏览器会自动保存登录状态并继续采集数据"
			if opts.LoginOnly {
				message = "登录已过期，请扫描返回的二维码；扫码成功后浏览器会自动保存登录状态"
			}
			return &Result{
				Platform: "wechat", FetchedAt: nowStr(), LoginRequired: true,
				QRImage: scraper.QRDataURL(qrBin), QRPath: opts.QRPath, Message: message, Session: session,
			}, nil
		}
		log.Println("【重要】请打开二维码图片扫码，脚本将等待 2 分钟...")
		ok := false
		for i := 0; i < 120; i++ {
			u := currentURL(page)
			if strings.Contains(u, "/platform") || strings.Contains(u, "/post/list") {
				ok = true
				break
			}
			time.Sleep(1 * time.Second)
		}
		if !ok {
			return nil, errF("等待扫码超时或失败（可能扫码太慢或未检测到跳转）")
		}
		log.Println("检测到 URL 跳转，登录成功！")
		time.Sleep(3 * time.Second)
		if err := scraper.SaveState(b, opts.CookiePath); err != nil {
			logErr("保存 cookie 失败:", err)
		} else {
			log.Printf("新的 Cookie 已成功保存至: %s", opts.CookiePath)
		}
		_ = os.Remove(opts.QRPath)
		scraper.WriteStatus(opts.StatusPath, "", false, "登录成功")
		if opts.LoginOnly {
			return wechatLoginResult(), nil
		}
		if !strings.Contains(currentURL(page), wechatListURL) {
			if err := page.Timeout(60 * time.Second).Navigate(wechatListURL); err != nil {
				return nil, err
			}
			time.Sleep(4 * time.Second)
		}
	}
	if opts.LoginOnly {
		scraper.WriteStatus(opts.StatusPath, "", false, "登录成功")
		return wechatLoginResult(), nil
	}
	return collectWechat(page, b, opts)
}

func wechatDiagnostic(page *rod.Page) string {
	var parts []string
	for _, fr := range listFrames(page) {
		var info struct {
			Rows int    `json:"rows"`
			Text string `json:"text"`
		}
		if !evalJSON(fr, `() => ({rows: document.querySelectorAll('div.post-feed-item').length, text: (document.body?.innerText || '').slice(0, 180)})`, &info) {
			continue
		}
		parts = append(parts, fmt.Sprintf("url=%q rows=%d text=%q", currentURL(fr), info.Rows, info.Text))
	}
	return strings.Join(parts, "; ")
}

func collectWechat(page *rod.Page, b *rod.Browser, opts WechatOptions) (*Result, error) {
	if !strings.Contains(currentURL(page), wechatListURL) {
		if err := page.Timeout(60 * time.Second).Navigate(wechatListURL); err != nil {
			return nil, err
		}
		time.Sleep(4 * time.Second)
	}
	scope := waitWechatContentFrame(page, 40)
	collectItems := func() []wechatRawItem {
		var best []wechatRawItem
		for _, fr := range listFrames(page) {
			if items := extractWechatItems(fr); len(items) > len(best) {
				best = items
			}
		}
		return best
	}
	videos := collectItems()
	for i := 0; i < 20 && len(videos) == 0; i++ {
		time.Sleep(1 * time.Second)
		videos = collectItems()
	}
	stable := 0
	for i := 0; i < 30; i++ {
		_, _ = safeEval(scope, 5*time.Second, wechatScrollJS)
		time.Sleep(1500 * time.Millisecond)
		newItems := collectItems()
		if len(newItems) > len(videos) {
			videos = newItems
			stable = 0
		} else {
			if len(newItems) == len(videos) {
				videos = newItems
			}
			stable++
			if stable >= 2 && len(videos) > 0 {
				break
			}
		}
	}
	if len(videos) == 0 {
		logInfo("[wechat] 列表为空，重新加载列表页重试一次")
		if err := page.Timeout(60 * time.Second).Navigate(wechatListURL); err == nil {
			time.Sleep(4 * time.Second)
			_ = waitWechatContentFrame(page, 20)
			for i := 0; i < 10 && len(videos) == 0; i++ {
				time.Sleep(1 * time.Second)
				videos = collectItems()
			}
		}
	}
	text := frameText(scope)
	if m := wechatCountRe.FindStringSubmatch(text); m != nil {
		if n, _ := strconv.Atoi(m[1]); n != len(videos) {
			log.Printf("提示: 页面显示视频数 %s，本次解析 %d 条", m[1], len(videos))
		}
	}
	logInfo("[wechat] 列表采集完成，条数:", len(videos))
	if len(videos) == 0 {
		return nil, errF("视频列表未识别，无法确认采集结果；页面诊断: %s", wechatDiagnostic(page))
	}
	if err := page.Timeout(60 * time.Second).Navigate(wechatHomeURL); err != nil {
		return nil, err
	}
	time.Sleep(5 * time.Second)
	account := parseWechatAccount(frameText(page))
	if err := scraper.SaveState(b, opts.CookiePath); err != nil {
		logErr("保存 cookie 失败:", err)
	}
	scraper.WriteStatus(opts.StatusPath, "", false, "抓取完成")
	result := &Result{
		Platform: "wechat", Account: account, FetchedAt: nowStr(),
		VideoCount: len(videos), Videos: []*Video{},
	}
	for _, v := range videos {
		views := toInt(v.Views)
		likes := toInt(v.Likes)
		comments := toInt(v.Comments)
		shares := toInt(v.Shares)
		favorites := toInt(v.Favorites)
		result.Videos = append(result.Videos, &Video{
			VideoID: v.VideoID, Title: v.Title, PublishTime: v.PublishTime,
			Views: &views, Likes: &likes, Comments: &comments,
			Shares: &shares, Favorites: &favorites,
		})
	}
	return result, nil
}
