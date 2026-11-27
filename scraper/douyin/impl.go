package douyin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"MediaPlatforms-Kit/scraper"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const douyinHome = "https://creator.douyin.com/creator-micro/home"

const rowsJS = `() => {
  const date = /(?:^|[^\d])(?:20\d{2}[-/.年]\d{1,2}[-/.月]\d{1,2}日?)(?:\s*\d{1,2}:\d{2})?(?!\d)/;
  const visible = e => !!(e.getClientRects().length) && getComputedStyle(e).visibility !== 'hidden';
  const text = e => (e.innerText || '').trim();
  const candidates = [...document.querySelectorAll('article, li, tr, [role="row"]')]
    .filter(e => visible(e) && date.test(text(e)) && text(e).length < 900);
  for (const e of document.querySelectorAll('div')) {
    if (!visible(e) || !e.parentElement || !date.test(text(e)) || text(e).length > 900) continue;
    const peers = [...e.parentElement.children].filter(x => x.tagName === e.tagName && visible(x) && date.test(text(x)));
    const singleCard = /编辑作品|设置权限|作品置顶|删除作品/.test(text(e)) &&
      /播放|点赞|评论|分享/.test(text(e));
    if ((peers.length >= 2 || singleCard) && !peers.some(x => x !== e && e.contains(x))) candidates.push(e);
  }
  const rows = [];
  for (const e of candidates) {
    if (candidates.some(x => x !== e && e.contains(x) && text(x).length >= 10)) continue;
    const lines = text(e).split(/\n+/).map(s => s.trim()).filter(Boolean);
    const when = lines.find(s => date.test(s));
    if (!when) continue;
    const uiText = /^(?:视频管理|内容管理|作品管理|编辑(?:作品)?|设置权限|删除(?:作品|视频)?|查看(?:数据)?|数据分析|置顶(?:作品)?|作品置顶|已发布|审核中|审核未通过|私密|仅自己可见|公开|草稿|发布失败|设为置顶)$/;
    const validTitle = s => {
      const value = s.replace(/\s+/g, ' ').trim();
      return value.length > 2 && value.length < 200 && !date.test(value) &&
        !/^[\d\s:：.,，.万亿%-]+$/.test(value) && !uiText.test(value);
    };
    const i = lines.indexOf(when);
    const title = lines.slice(0, i).find(validTitle);
    const hadUiWord = lines.slice(0, i).some(s => uiText.test(s));
    if ((!title && !hadUiWord) || lines.length > 45 || !lines.some(s => /(?:播放|点赞|评论|分享|转发|收藏)/.test(s))) continue;
    const idNode = e.matches('[data-video-id], [data-aweme-id]') ? e : e.querySelector('[data-video-id], [data-aweme-id]');
    const domId = idNode ? (idNode.getAttribute('data-video-id') || idNode.getAttribute('data-aweme-id') || '') : '';
    const publishTime = (when.match(date) || [])[0].trim();
    const expected = publishTime.match(/20\d{2}|\d{1,2}/g).map(Number);
    const matchesTime = value => {
      const stamp = Number(value);
      if (!Number.isFinite(stamp) || stamp <= 0) return false;
      const d = new Date(stamp * 1000 + 8 * 3600000);
      return d.getUTCFullYear() === expected[0] && d.getUTCMonth() + 1 === expected[1] &&
        d.getUTCDate() === expected[2] && (expected.length < 5 ||
        d.getUTCHours() === expected[3] && d.getUTCMinutes() === expected[4]);
    };
    let fiberId = '';
    for (let element = e, level = 0; element && level < 4 && !fiberId; element = element.parentElement, level++) {
      const ids = new Set();
      for (const key of Object.keys(element).filter(k => k.startsWith('__reactFiber$'))) {
        const root = element[key];
        const queue = [root];
        const seen = new Set();
        while (queue.length && seen.size < 350) {
          const node = queue.shift();
          if (!node || seen.has(node)) continue;
          seen.add(node);
          const data = node.pendingProps && node.pendingProps.data;
          if (data && matchesTime(data.create_time) && typeof data.aweme_id === 'string' &&
              /^\d{10,}$/.test(data.aweme_id) &&
              (title && (String(data.desc || '').replace(/\s+/g, ' ').includes(title.replace(/\s+/g, ' ')) || /^\d+张图$/.test(title)))) {
            ids.add(data.aweme_id);
          }
          if (node.child) queue.push(node.child);
          if (node !== root && node.sibling) queue.push(node.sibling);
        }
      }
      if (ids.size === 1) fiberId = [...ids][0];
      if (ids.size > 1) break;
    }
    rows.push({video_id: domId || fiberId,
      links: [...e.querySelectorAll('a[href]')].map(a => a.href),
      title: title ? title.replace(/\s+/g, ' ').trim() : '', publish_time: publishTime, lines});
  }
  return rows;
}`

const qrJS = `() => {
  const matches = [];
  for (const e of document.querySelectorAll('img, canvas')) {
    const b = e.getBoundingClientRect();
    if (b.width < 90 || b.height < 90 || b.width > 700 || b.height > 700 ||
        b.width / b.height < .65 || b.width / b.height > 1.5 ||
        !e.getClientRects().length || getComputedStyle(e).visibility === 'hidden') continue;
    const nearby = (e.parentElement && e.parentElement.parentElement && e.parentElement.parentElement.innerText || '').slice(0, 250);
    const attrs = [e.alt, e.title, e.id, e.className, e.parentElement && e.parentElement.className].join(' ');
    const score = /二维码|扫码|登录|QR/i.test(nearby) ? 2 : 0;
    if (score || /二维码|扫码|QR/i.test(attrs)) matches.push({index: [...document.querySelectorAll('img, canvas')].indexOf(e), score});
  }
  return matches.sort((a, b) => b.score - a.score);
}`

const qrValidJS = `() => {
  const attrs = [this.alt, this.title, this.id, this.className,
    this.parentElement && this.parentElement.className].join(' ');
  if (/avatar|头像/i.test(attrs)) return false;
  if (this.tagName === 'CANVAS') return true;
  const src = this.getAttribute('src') || '';
  if (/^(data:|blob:)/i.test(src)) return true;
  if (/qr[_-]?code|二维码/i.test(src + ' ' + attrs)) return true;
  return false;
}`

const qrExpiredJS = `() => {
  const t = (document.body && document.body.innerText) || '';
  return /二维码已(经)?(过期|失效)|二维码失效|刷新二维码/.test(t);
}`

const qrRefreshJS = `() => {
  const visible = e => !!(e.getClientRects().length) && getComputedStyle(e).visibility !== 'hidden';
  const cands = [];
  const walk = root => {
    if (!root || !root.querySelectorAll) return;
    for (const e of root.querySelectorAll('button, a, div, span, p')) {
      const t = (e.innerText || '').replace(/\s+/g, '');
      if (!visible(e) || !t || t.length > 12) continue;
      if (/点击刷新|刷新二维码|重新刷新|二维码失效/.test(t)) cands.push(e);
    }
  };
  walk(document);
  for (const f of document.querySelectorAll('iframe')) {
    try { walk(f.contentDocument); } catch (err) {}
  }
  if (!cands.length) return false;
  let best = cands[0], bestArea = Infinity;
  for (const e of cands) {
    const r = e.getBoundingClientRect();
    const area = r.width * r.height;
    if (area > 0 && area < bestArea) { best = e; bestArea = area; }
  }
  const r = best.getBoundingClientRect();
  const opts = {bubbles: true, cancelable: true, view: window,
    clientX: r.left + r.width / 2, clientY: r.top + r.height / 2};
  for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click']) {
    try {
      const ev = type.indexOf('pointer') === 0 ? new PointerEvent(type, opts) : new MouseEvent(type, opts);
      best.dispatchEvent(ev);
    } catch (err) {}
  }
  return true;
}`

const qrCoveredJS = `() => {
  const pat = /二维码失效|已过期|点击刷新|刷新二维码/;
  const r = this.getBoundingClientRect();
  const cx = r.left + r.width / 2, cy = r.top + r.height / 2;
  try {
    for (const e of document.elementsFromPoint(cx, cy)) {
      if (pat.test(e.innerText || '')) return true;
    }
  } catch (err) {}
  let p = this.parentElement;
  for (let i = 0; i < 4 && p; i++, p = p.parentElement) {
    if (pat.test(p.innerText || '')) return true;
  }
  return false;
}`

const qrReadyJS = `() => {
  if (this.tagName === 'IMG') return this.complete && this.naturalWidth > 0;
  if (!this.width || !this.height) return false;
  try {
    const pixels = this.getContext('2d').getImageData(0, 0, this.width, this.height).data;
    return pixels.some((value, index) => index % 4 === 3 && value > 0);
  } catch { return false; }
}`

const creatorContentJS = `() => {
  const text = (document.body && document.body.innerText) || '';
  return /(?:粉丝数|粉丝|作品数|获赞数|总播放量)\s*[:：]?\s*\d[\d,.]*\s*[万亿]?/.test(text);
}`

const douyinScrollJS = `() => {
  const root = document.scrollingElement;
  if (root) root.scrollTop = root.scrollHeight;
  for (const el of document.querySelectorAll('*')) {
    if (el.scrollHeight > el.clientHeight + 100 && el.clientHeight > 150)
      el.scrollTop = el.scrollHeight;
  }
}`

const menuHasJS = `(label) => {
  const visible = e => !!(e.getClientRects().length) && getComputedStyle(e).visibility !== 'hidden';
  for (const e of document.querySelectorAll('*')) {
    if ((e.innerText || '').trim() === label && visible(e)) return true;
  }
  return false;
}`

const menuClickJS = `(label) => {
  const visible = e => !!(e.getClientRects().length) && getComputedStyle(e).visibility !== 'hidden';
  let target = null;
  for (const e of document.querySelectorAll('*')) {
    if ((e.innerText || '').trim() !== label || !visible(e)) continue;
    if (!target || target.contains(e)) target = e;
  }
  if (!target) return false;
  target.click();
  return true;
}`

type Result = scraper.Result
type Video = scraper.Video
type Account = scraper.Account
type PendingLogin = scraper.PendingLogin

const (
	LoginStageScanned = scraper.LoginStageScanned
	LoginStageTimeout = scraper.LoginStageTimeout
	dyNumber          = scraper.DyNumber
)

var (
	logInfo           = scraper.LogInfo
	logErr            = scraper.LogErr
	errf              = scraper.Errf
	nowStr            = scraper.NowStr
	loginWaitDuration = scraper.LoginWaitDuration
	parseNumber       = scraper.ParseNumber
	validVideoID      = scraper.ValidVideoID
	videoIDFromURL    = scraper.VideoIDFromURL
	listFrames        = scraper.ListFrames
	evalJSON          = scraper.EvalJSON
	evalText          = scraper.EvalText
	currentURL        = scraper.CurrentURL
	safeEval          = scraper.SafeEval
	safeElementEval   = scraper.SafeElementEval
	loadState         = scraper.LoadState
	launchBrowser     = scraper.LaunchBrowser
	WriteStatus       = scraper.WriteStatus
	SaveState         = scraper.SaveState
	ApplyStorage      = scraper.ApplyStorage
	QRDataURL         = scraper.QRDataURL
	newPendingLogin   = scraper.NewPendingLogin
)

func keepDouyinLoginAlive(page *rod.Page, b *rod.Browser, opts DouyinOptions, session *PendingLogin) {
	defer session.Finish()
	defer b.Close()

	deadline := time.Now().Add(loginWaitDuration())
	round := 0
	for time.Now().Before(deadline) {
		if found := waitDouyinMenu(page, 5); found != nil {
			if e := SaveState(b, opts.CookiePath); e != nil {
				logErr("[douyin] 保存 cookie 失败: " + e.Error())
			} else {
				logInfo("[douyin] 扫码登录成功，cookie 已保存:", opts.CookiePath)
			}
			_ = os.Remove(opts.QRPath)
			if opts.LoginOnly {
				WriteStatus(opts.StatusPath, "douyin", false, "登录成功")
				session.SetResult(douyinLoginResult(), nil)
				return
			}
			WriteStatus(opts.StatusPath, "douyin", false, "登录成功，正在采集数据")
			session.SetStage(LoginStageScanned, "扫码成功，正在采集视频数据")

			t0 := time.Now()
			res, err := collectDouyin(page, b, opts)
			if err != nil {
				logErr("[douyin] 扫码后采集失败:", err)
				session.SetResult(nil, err)
			} else {
				logInfo("[douyin] 扫码后采集完成，条数:", res.VideoCount, "耗时:", time.Since(t0).Round(time.Millisecond))
				session.SetResult(res, nil)
			}
			return
		}
		round++
		if round%3 == 0 {
			refreshDouyinQR(page, session)
		}
	}
	_ = os.Remove(opts.QRPath)
	WriteStatus(opts.StatusPath, "douyin", true, "扫码登录超时")
	session.SetOutcome(LoginStageTimeout, "扫码登录超时，请重新请求接口获取新的二维码")
	logInfo("[douyin] 等待扫码登录超时，浏览器已关闭")
}

type rowRaw struct {
	VideoID     string   `json:"video_id"`
	Links       []string `json:"links"`
	Title       string   `json:"title"`
	PublishTime string   `json:"publish_time"`
	Lines       []string `json:"lines"`
}

var dyMetricsLabels = []struct {
	Key   string
	Words []string
}{
	{"views", []string{"播放量", "播放"}},
	{"likes", []string{"点赞量", "点赞"}},
	{"comments", []string{"评论量", "评论"}},
	{"shares", []string{"分享量", "分享", "转发"}},
	{"favorites", []string{"收藏量", "收藏"}},
}

var labelReCache = map[string]*regexp.Regexp{}

func labelRe(label string) *regexp.Regexp {
	if re, ok := labelReCache[label]; ok {
		return re
	}
	q := regexp.QuoteMeta(label)
	re := regexp.MustCompile(`^\s*(?:` + q + `\s*[:：]?\s*` + dyNumber + `|` + dyNumber + `\s*` + q + `)\s*$`)
	labelReCache[label] = re
	return re
}

func parseRow(row rowRaw) *Video {
	metrics := map[string]*int64{
		"views": nil, "likes": nil, "comments": nil, "shares": nil, "favorites": nil,
	}
	for _, m := range dyMetricsLabels {
		for i, line := range row.Lines {
			for _, label := range m.Words {
				if line == label && i+1 < len(row.Lines) {
					metrics[m.Key] = parseNumber(row.Lines[i+1])
				} else if g := labelRe(label).FindStringSubmatch(line); g != nil {
					if v := parseNumber(g[1]); v != nil {
						metrics[m.Key] = v
					} else {
						metrics[m.Key] = parseNumber(g[2])
					}
				}
				if metrics[m.Key] != nil {
					break
				}
			}
			if metrics[m.Key] != nil {
				break
			}
		}
	}
	id := strings.TrimSpace(row.VideoID)
	if !validVideoID(id) {
		id = ""
	}
	if id == "" {
		for _, link := range row.Links {
			if id = videoIDFromURL("douyin", link); id != "" {
				break
			}
		}
	}
	return &Video{
		VideoID: id, Title: row.Title, PublishTime: row.PublishTime,
		Views: metrics["views"], Likes: metrics["likes"], Comments: metrics["comments"],
		Shares: metrics["shares"], Favorites: metrics["favorites"],
	}
}

var douyinNameRe = regexp.MustCompile(`^(?:账号名称|抖音号名称|昵称)\s*[:：]\s*(.*)$`)
var douyinFollowerRe = regexp.MustCompile(`(?:粉丝数|粉丝)\s*[:：]?\s*` + dyNumber)

func parseDouyinAccount(text string) (*string, *int64) {
	var name *string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if m := douyinNameRe.FindStringSubmatch(line); m != nil {
			n := strings.TrimSpace(m[1])
			if n != "" {
				name = &n
			}
			break
		}
	}
	var followers *int64
	if m := douyinFollowerRe.FindStringSubmatch(text); m != nil {
		followers = parseNumber(m[1])
	}
	return name, followers
}

func waitDouyinMenu(page *rod.Page, seconds int) *rod.Page {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for time.Now().Before(deadline) {
		for _, fr := range listFrames(page) {
			var has bool
			if !evalJSON(fr, menuHasJS, &has, "内容管理") || !has {
				continue
			}
			if u := currentURL(fr); strings.Contains(u, "//creator.douyin.com/") && douyinCreatorContent(page) {
				return fr
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

func douyinCreatorContent(page *rod.Page) bool {
	for _, fr := range listFrames(page) {
		var ok bool
		if evalJSON(fr, creatorContentJS, &ok) && ok {
			return true
		}
		var rows []rowRaw
		if evalJSON(fr, rowsJS, &rows) && len(rows) > 0 {
			return true
		}
	}
	return false
}

func findDouyinQR(page *rod.Page) *rod.Element {
	for _, fr := range listFrames(page) {
		var matches []struct {
			Index int `json:"index"`
			Score int `json:"score"`
		}
		if !evalJSON(fr, qrJS, &matches) || len(matches) == 0 {
			continue
		}
		els, err := fr.Timeout(3 * time.Second).Elements("img, canvas")
		if err != nil {
			continue
		}
		for _, m := range matches {
			if m.Index >= len(els) {
				continue
			}
			el := els[m.Index]
			if vis, err := el.Visible(); err != nil || !vis {
				continue
			}
			if r, err := safeElementEval(el, qrValidJS); err != nil || r == nil || !r.Value.Bool() {
				continue
			}
			return el
		}
	}
	return nil
}

func douyinQRExpired(page *rod.Page) bool {
	for _, fr := range listFrames(page) {
		var exp bool
		if evalJSON(fr, qrExpiredJS, &exp) && exp {
			return true
		}
	}
	return false
}

func douyinClickQRRefresh(page *rod.Page) bool {
	for _, fr := range listFrames(page) {
		var clicked bool
		if evalJSON(fr, qrRefreshJS, &clicked) && clicked {
			return true
		}
	}
	return false
}

func refreshDouyinQR(page *rod.Page, session *PendingLogin) {
	if douyinQRExpired(page) {
		if !douyinClickQRRefresh(page) {
			logInfo("[douyin] 检测到二维码过期，但未能点击刷新按钮，保留上一次的二维码")
			return
		}
		logInfo("[douyin] 检测到二维码过期，已点击刷新")
		time.Sleep(2 * time.Second)
	}
	var qr *rod.Element
	for i := 0; i < 8; i++ {
		qr = findDouyinQR(page)
		if qr != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if qr == nil {
		logInfo("[douyin] 刷新后未找到有效二维码元素，保留上一次的二维码")
		return
	}
	for i := 0; i < 6; i++ {
		if !douyinQRExpired(page) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if douyinQRExpired(page) {
		logInfo("[douyin] 点击刷新后过期遮罩仍未消失，保留上一次的二维码")
		return
	}
	if r, err := safeElementEval(qr, qrReadyJS); err != nil || r == nil || !r.Value.Bool() {
		logInfo("[douyin] 新二维码尚未渲染完成，保留上一次的二维码")
		return
	}
	if r, err := safeElementEval(qr, qrCoveredJS); err == nil && r != nil && r.Value.Bool() {
		logInfo("[douyin] 二维码上方仍有失效遮罩，保留上一次的二维码")
		return
	}
	bin, err := qr.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil || len(bin) == 0 {
		return
	}
	_, path, msg := session.QR()
	if err := os.WriteFile(path, bin, 0o644); err == nil {
		session.UpdateQR(QRDataURL(bin), msg)
	}
}

func douyinDiagnostic(page *rod.Page) string {
	type diag struct {
		URL           string `json:"url"`
		Text          string `json:"text"`
		Images        int    `json:"images"`
		Canvases      int    `json:"canvases"`
		Frames        int    `json:"frames"`
		CandidateRows int    `json:"candidate_rows"`
	}
	var parts []string
	for _, fr := range listFrames(page) {
		var d diag
		if !evalJSON(fr, `() => ({url: location.href, text: (document.body?.innerText || '').slice(0, 180), images: document.querySelectorAll('img').length, canvases: document.querySelectorAll('canvas').length, frames: document.querySelectorAll('iframe').length})`, &d) {
			parts = append(parts, `{"unavailable": true}`)
			continue
		}
		var rows []rowRaw
		if evalJSON(fr, rowsJS, &rows) {
			d.CandidateRows = len(rows)
		}
		if b, err := json.Marshal(d); err == nil {
			parts = append(parts, string(b))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func collectDouyinList(page *rod.Page) []*Video {
	videos := []*Video{}
	index := map[string]int{}
	idIndex := map[string]int{}
	stable := 0
	merge := func(rows []rowRaw) {
		for _, row := range rows {
			video := parseRow(row)
			key := video.Title + "\x00" + video.PublishTime
			idx, found := 0, false
			if video.VideoID != "" {
				idx, found = idIndex[video.VideoID]
			}
			if !found {
				if candidate, ok := index[key]; ok && (video.VideoID == "" || videos[candidate].VideoID == "") {
					idx, found = candidate, true
				}
			}
			if found {
				dst := videos[idx]
				if dst.VideoID == "" && video.VideoID != "" {
					dst.VideoID = video.VideoID
					idIndex[video.VideoID] = idx
				}
				if dst.Views == nil {
					dst.Views = video.Views
				}
				if dst.Likes == nil {
					dst.Likes = video.Likes
				}
				if dst.Comments == nil {
					dst.Comments = video.Comments
				}
				if dst.Shares == nil {
					dst.Shares = video.Shares
				}
				if dst.Favorites == nil {
					dst.Favorites = video.Favorites
				}
			} else {
				index[key] = len(videos)
				if video.VideoID != "" {
					idIndex[video.VideoID] = len(videos)
				}
				videos = append(videos, video)
			}
		}
	}
	for _, fr := range listFrames(page) {
		var rows []rowRaw
		if evalJSON(fr, rowsJS, &rows) {
			merge(rows)
		}
	}
	for iter := 0; iter < 35; iter++ {
		for _, fr := range listFrames(page) {
			_, _ = safeEval(fr, 5*time.Second, douyinScrollJS)
		}
		time.Sleep(900 * time.Millisecond)
		before := len(videos)
		rowsTotal := 0
		for _, fr := range listFrames(page) {
			var rows []rowRaw
			if evalJSON(fr, rowsJS, &rows) {
				rowsTotal += len(rows)
				merge(rows)
			}
		}
		if len(videos) == before && rowsTotal <= len(videos) {
			stable++
		} else {
			stable = 0
		}
		if stable >= 2 {
			break
		}
	}
	return videos
}

// DouyinOptions 抖音抓取参数。
type DouyinOptions struct {
	CookiePath string
	ProfileDir string
	Headless   bool
	QRPath     string
	StatusPath string
	NoWaitScan bool
	LoginOnly  bool
}

func douyinLoginResult() *Result {
	return &Result{Platform: "douyin", FetchedAt: nowStr(), Videos: []*Video{}, Message: "登录成功"}
}

// ScrapeDouyin 从抖音创作者中心可见页面采集视频数据；仅使用浏览器可见 DOM。
func ScrapeDouyin(opts DouyinOptions) (res *Result, err error) {
	WriteStatus(opts.StatusPath, "douyin", false, "正在检测登录状态")
	if opts.QRPath != "" {
		_ = os.Remove(opts.QRPath)
	}
	defer func() {
		if err != nil {
			if opts.QRPath != "" {
				_ = os.Remove(opts.QRPath)
			}
			WriteStatus(opts.StatusPath, "douyin", false, "抓取失败: "+err.Error())
		}
	}()

	state, err := loadState(opts.CookiePath)
	if err != nil {
		return nil, err
	}
	profileHasState := scraper.PrepareProfile(opts.ProfileDir)
	b, err := launchBrowser(scraper.BrowserOptions{Headless: opts.Headless, UserDataDir: opts.ProfileDir})
	if err != nil {
		return nil, errf("浏览器启动失败: %v（配置目录 %s 可能被其他浏览器进程占用；可用 videoscraper cleanup 清理残留进程后重试）", err, opts.ProfileDir)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			b.Close()
		}
	}()

	if profileHasState {
		logInfo("[douyin] 使用持久化浏览器配置:", opts.ProfileDir)
	} else {
		if cookies := state.ToRodCookies(); len(cookies) > 0 {
			if err := b.SetCookies(cookies); err != nil {
				return nil, err
			}
		}
		t0 := time.Now()
		ApplyStorage(b, state)
		logInfo("[douyin] localStorage 恢复耗时:", time.Since(t0).Round(time.Millisecond))
	}

	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, err
	}
	if err := page.Timeout(60 * time.Second).Navigate(douyinHome); err != nil {
		return nil, err
	}
	t1 := time.Now()
	found := waitDouyinMenu(page, 20)
	logInfo("[douyin] 菜单就绪耗时:", time.Since(t1).Round(time.Millisecond))
	if found == nil && profileHasState && findDouyinQR(page) == nil {
		logInfo("[douyin] 首屏未检测到创作者菜单，刷新后重试一次")
		if e := page.Timeout(45 * time.Second).Navigate(douyinHome); e == nil {
			found = waitDouyinMenu(page, 20)
			logInfo("[douyin] 刷新后菜单检测:", found != nil)
		}
	}
	if found == nil {
		var qr *rod.Element
		for i := 0; i < 15; i++ {
			qr = findDouyinQR(page)
			if qr != nil {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if qr == nil {
			return nil, errf("未找到创作者专属内容，无法确认已登录，且未找到可见的登录二维码；DOM 诊断: %s", douyinDiagnostic(page))
		}
		if opts.QRPath == "" {
			return nil, errf("未确认创作者登录，页面存在二维码，请指定 qr_path 保存截图后人工扫码")
		}
		ready := false
		for i := 0; i < 20; i++ {
			if r, err := safeElementEval(qr, qrReadyJS); err == nil && r != nil && r.Value.Bool() {
				ready = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ready {
			return nil, errf("登录二维码图片未加载或画布为空，无法截图")
		}
		if dir := filepath.Dir(opts.QRPath); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		bin, err := qr.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		if err != nil || len(bin) == 0 {
			return nil, errf("登录二维码截图未生成，无法等待扫码")
		}
		if err := os.WriteFile(opts.QRPath, bin, 0o644); err != nil {
			return nil, err
		}
		if opts.NoWaitScan {
			session := newPendingLogin(QRDataURL(bin), opts.QRPath, "登录已过期，请扫描返回的二维码（二维码在后台持续等待，最长 10 分钟）")
			WriteStatus(opts.StatusPath, "douyin", true, "等待扫码登录")
			logInfo("[douyin] 已返回二维码，浏览器保持运行等待扫码，最长", loginWaitDuration())
			handedOff = true
			go keepDouyinLoginAlive(page, b, opts, session)
			message := "登录已过期，请扫描返回的二维码；扫码成功后浏览器会自动保存登录状态并继续采集数据"
			if opts.LoginOnly {
				message = "登录已过期，请扫描返回的二维码；扫码成功后浏览器会自动保存登录状态"
			}
			return &Result{Platform: "douyin", FetchedAt: nowStr(), LoginRequired: true,
				QRImage: QRDataURL(bin), QRPath: opts.QRPath, Message: message, Session: session}, nil
		}
		WriteStatus(opts.StatusPath, "douyin", true, "等待扫码登录")
		logInfo("登录二维码已保存，请人工扫码；最多等待 2 分钟。")
		found = waitDouyinMenu(page, 120)
		if found == nil {
			return nil, errf("等待扫码登录超时，未确认已进入创作者首页")
		}
		if e := SaveState(b, opts.CookiePath); e != nil {
			logErr("保存 cookie 失败: " + e.Error())
		}
		_ = os.Remove(opts.QRPath)
		WriteStatus(opts.StatusPath, "douyin", false, "登录成功")
	}
	if opts.LoginOnly {
		WriteStatus(opts.StatusPath, "douyin", false, "登录成功")
		return douyinLoginResult(), nil
	}
	return collectDouyin(page, b, opts)
}

func collectDouyin(page *rod.Page, b *rod.Browser, opts DouyinOptions) (*Result, error) {
	bodyText := evalText(page, `() => document.body.innerText || ''`)
	name, followers := parseDouyinAccount(bodyText)
	clicked := false
	for _, fr := range listFrames(page) {
		var ok bool
		if evalJSON(fr, menuClickJS, &ok, "内容管理") && ok {
			clicked = true
			break
		}
	}
	if !clicked {
		return nil, errf("未能点击「内容管理」菜单")
	}
	contentDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(contentDeadline) {
		rowsReady := false
		for _, fr := range listFrames(page) {
			var rows []rowRaw
			if evalJSON(fr, rowsJS, &rows) && len(rows) > 0 {
				rowsReady = true
				break
			}
		}
		if rowsReady {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	t2 := time.Now()
	videos := collectDouyinList(page)
	logInfo("[douyin] 列表采集耗时:", time.Since(t2).Round(time.Millisecond), "条数:", len(videos))
	if len(videos) == 0 {
		return nil, errf("无法可靠识别视频列表及标题/发布时间，不会输出 0 条伪成功；DOM 诊断: %s", douyinDiagnostic(page))
	}
	if e := SaveState(b, opts.CookiePath); e != nil {
		logErr("保存 cookie 失败: " + e.Error())
	}
	WriteStatus(opts.StatusPath, "douyin", false, "抓取完成")
	return &Result{Platform: "douyin", Account: &Account{Name: name, Followers: followers},
		FetchedAt: nowStr(), VideoCount: len(videos), Videos: videos}, nil
}
