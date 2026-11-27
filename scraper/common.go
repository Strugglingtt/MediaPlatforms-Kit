// Package scraper provides shared browser scraping and result utilities.
package scraper

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	launcherFlags "github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

func logInfo(v ...interface{})              { log.Println(v...) }
func logErr(v ...interface{})               { log.Println(v...) }
func urlParse(raw string) (*url.URL, error) { return url.Parse(raw) }
func LogInfo(v ...interface{})              { logInfo(v...) }
func LogErr(v ...interface{})               { logErr(v...) }
func UrlParse(raw string) (*url.URL, error) { return urlParse(raw) }

type ScrapeError struct{ msg string }

func (e *ScrapeError) Error() string { return e.msg }
func errf(format string, args ...interface{}) error {
	return &ScrapeError{msg: fmt.Sprintf(format, args...)}
}
func Errf(format string, args ...interface{}) error { return errf(format, args...) }

type Account struct {
	Name      *string `json:"name,omitempty"`
	Followers *int64  `json:"followers,omitempty"`
	SphID     *string `json:"sph_id,omitempty"`
}
type Video struct {
	VideoID     string `json:"video_id"`
	Title       string `json:"title"`
	PublishTime string `json:"publish_time"`
	Views       *int64 `json:"views"`
	Likes       *int64 `json:"likes"`
	Comments    *int64 `json:"comments"`
	Shares      *int64 `json:"shares"`
	Favorites   *int64 `json:"favorites"`
}
type Result struct {
	Platform      string        `json:"platform,omitempty"`
	Account       *Account      `json:"account"`
	FetchedAt     string        `json:"fetched_at"`
	VideoCount    int           `json:"video_count"`
	Videos        []*Video      `json:"videos"`
	LoginRequired bool          `json:"login_required,omitempty"`
	QRImage       string        `json:"qr_image,omitempty"`
	QRPath        string        `json:"qr_path,omitempty"`
	Message       string        `json:"message,omitempty"`
	Session       *PendingLogin `json:"-"`
}

const (
	LoginStageWaiting = "waiting_scan"
	LoginStageScanned = "scanned"
	LoginStageDone    = "done"
	LoginStageTimeout = "timeout"
	LoginStageFailed  = "failed"
	LoginStageIdle    = "idle"
)

type PendingLogin struct {
	mu                              sync.RWMutex
	qrImage, qrPath, message, stage string
	started, updatedAt, finished    time.Time
	result                          *Result
	errMsg                          string
	done                            chan struct{}
}

func newPendingLogin(qrImage, qrPath, message string) *PendingLogin {
	return &PendingLogin{qrImage: qrImage, qrPath: qrPath, message: message, stage: LoginStageWaiting, started: time.Now(), updatedAt: time.Now(), done: make(chan struct{})}
}
func NewPendingLogin(qrImage, qrPath, message string) *PendingLogin {
	return newPendingLogin(qrImage, qrPath, message)
}

type LoginStatus struct {
	Platform    string  `json:"platform"`
	Stage       string  `json:"stage"`
	Waiting     bool    `json:"waiting_scan"`
	QRImage     string  `json:"qr_image,omitempty"`
	QRPath      string  `json:"qr_path,omitempty"`
	QRUpdatedAt string  `json:"qr_updated_at,omitempty"`
	Message     string  `json:"message,omitempty"`
	StartedAt   string  `json:"started_at,omitempty"`
	FinishedAt  string  `json:"finished_at,omitempty"`
	Elapsed     int     `json:"elapsed_seconds"`
	VideoCount  int     `json:"video_count,omitempty"`
	Error       string  `json:"error,omitempty"`
	Result      *Result `json:"result,omitempty"`
}

func (p *PendingLogin) Status(platform string, includeResult bool) LoginStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st := LoginStatus{Platform: platform, Stage: p.stage, Waiting: p.stage == LoginStageWaiting || p.stage == LoginStageScanned, QRImage: p.qrImage, QRPath: p.qrPath, Message: p.message, StartedAt: p.started.Format("2006-01-02 15:04:05"), Error: p.errMsg}
	if !p.updatedAt.IsZero() {
		st.QRUpdatedAt = p.updatedAt.Format("2006-01-02 15:04:05")
	}
	end := p.finished
	if end.IsZero() {
		end = time.Now()
	} else {
		st.FinishedAt = end.Format("2006-01-02 15:04:05")
	}
	st.Elapsed = int(end.Sub(p.started).Seconds())
	if p.result != nil {
		st.VideoCount = p.result.VideoCount
		if includeResult {
			st.Result = p.result
		}
	}
	return st
}
func (p *PendingLogin) Stage() string { p.mu.RLock(); defer p.mu.RUnlock(); return p.stage }
func (p *PendingLogin) setStage(stage, message string) {
	p.mu.Lock()
	p.stage = stage
	if message != "" {
		p.message = message
	}
	p.mu.Unlock()
}
func (p *PendingLogin) SetStage(stage, message string) { p.setStage(stage, message) }
func (p *PendingLogin) setResult(res *Result, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finished = time.Now()
	if err != nil {
		p.stage = LoginStageFailed
		p.errMsg = err.Error()
		p.message = "扫码成功，但采集数据失败: " + err.Error()
	} else {
		p.stage = LoginStageDone
		p.result = res
		p.message = "扫码登录成功，数据已在同一浏览器会话内采集完成"
		if res != nil && res.Message != "" {
			p.message = res.Message
		}
	}
}
func (p *PendingLogin) SetResult(res *Result, err error) { p.setResult(res, err) }
func (p *PendingLogin) setOutcome(stage, message string) {
	p.mu.Lock()
	p.stage = stage
	p.finished = time.Now()
	p.message = message
	p.mu.Unlock()
}
func (p *PendingLogin) SetOutcome(stage, message string) { p.setOutcome(stage, message) }
func (p *PendingLogin) QR() (image, path, message string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.qrImage, p.qrPath, p.message
}
func (p *PendingLogin) updateQR(image, message string) {
	p.mu.Lock()
	p.qrImage = image
	if message != "" {
		p.message = message
	}
	p.updatedAt = time.Now()
	p.mu.Unlock()
}
func (p *PendingLogin) UpdateQR(image, message string) { p.updateQR(image, message) }
func (p *PendingLogin) UpdatedAt() time.Time           { p.mu.RLock(); defer p.mu.RUnlock(); return p.updatedAt }
func (p *PendingLogin) Done() <-chan struct{}          { return p.done }
func (p *PendingLogin) finish() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}
func (p *PendingLogin) Finish() { p.finish() }
func loginWaitDuration() time.Duration {
	if v := os.Getenv("LOGIN_WAIT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 10 * time.Minute
}
func LoginWaitDuration() time.Duration { return loginWaitDuration() }
func QRDataURL(bin []byte) string {
	if len(bin) == 0 {
		return ""
	}
	mime := "image/png"
	if len(bin) >= 3 && bin[0] == 0xff && bin[1] == 0xd8 && bin[2] == 0xff {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(bin)
}
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }
func NowStr() string { return nowStr() }

const dyNumber = `(\d[\d,]*(?:\.\d+)?\s*[万亿]?)`
const DyNumber = dyNumber

var dyNumFull = regexp.MustCompile(`^\s*` + dyNumber + `\s*$`)

func parseNumber(value string) *int64 {
	if value == "" {
		return nil
	}
	m := dyNumFull.FindStringSubmatch(value)
	if m == nil {
		return nil
	}
	s := strings.NewReplacer(",", "", " ", "").Replace(m[1])
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "亿"):
		mult, s = 1e8, strings.TrimSuffix(s, "亿")
	case strings.HasSuffix(s, "万"):
		mult, s = 1e4, strings.TrimSuffix(s, "万")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	v := int64(f * mult)
	return &v
}
func ParseNumber(value string) *int64 { return parseNumber(value) }

var intNumRe = regexp.MustCompile(`([\d.]+)\s*([万亿]?)`)

func toInt(text string) int64 {
	if text == "" {
		return 0
	}
	m := intNumRe.FindStringSubmatch(strings.NewReplacer(",", "", " ", "").Replace(text))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch m[2] {
	case "万":
		n *= 10000
	case "亿":
		n *= 100000000
	}
	return int64(n)
}
func ToInt(text string) int64 { return toInt(text) }

var dateRe = regexp.MustCompile(`^(\d{4})年(\d{1,2})月(\d{1,2})日\s*(\d{1,2}):(\d{2})$`)

func normalizeTime(text string) string {
	t := strings.TrimSpace(text)
	m := dateRe.FindStringSubmatch(t)
	if m == nil {
		return t
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	return fmt.Sprintf("%s-%02d-%02d %02d:%s", m[1], atoi(m[2]), atoi(m[3]), atoi(m[4]), m[5])
}
func NormalizeTime(text string) string { return normalizeTime(text) }
func WriteStatus(path, platform string, loginRequired bool, message string) {
	if path == "" {
		return
	}
	payload := map[string]interface{}{"login_required": loginRequired, "message": message, "updated_at": nowStr()}
	if platform != "" {
		payload["platform"] = platform
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
func ReadStatus(path string) (bool, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	var st struct {
		LoginRequired bool   `json:"login_required"`
		Message       string `json:"message"`
	}
	if json.Unmarshal(data, &st) != nil {
		return false, ""
	}
	return st.LoginRequired, st.Message
}

type storedCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite,omitempty"`
}
type StoredCookie = storedCookie
type localStorageItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type LocalStorageItem = localStorageItem
type originStorage struct {
	Origin       string             `json:"origin"`
	LocalStorage []localStorageItem `json:"localStorage"`
}
type OriginStorage = originStorage
type StorageState struct {
	Cookies []StoredCookie  `json:"cookies"`
	Origins []OriginStorage `json:"origins"`
}
type storageState = StorageState

var extSameSiteMap = map[string]string{"no_restriction": "None", "none": "None", "lax": "Lax", "strict": "Strict", "unspecified": "Lax"}

func str(v interface{}) string  { s, _ := v.(string); return s }
func boolOf(v interface{}) bool { b, _ := v.(bool); return b }
func strDefault(v interface{}, def string) string {
	if s := str(v); s != "" {
		return s
	}
	return def
}
func anyToCookie(m map[string]interface{}) (storedCookie, bool) {
	ck := storedCookie{Name: str(m["name"]), Value: str(m["value"]), Domain: str(m["domain"]), Path: strDefault(m["path"], "/"), Secure: boolOf(m["secure"]), HTTPOnly: boolOf(m["httpOnly"])}
	if e, ok := m["expires"].(float64); ok && e > 0 {
		ck.Expires = e
	} else if e, ok := m["expirationDate"].(float64); ok && e > 0 {
		ck.Expires = e
	}
	ss := strings.ToLower(str(m["sameSite"]))
	if ss != "" {
		ck.SameSite = extSameSiteMap[ss]
		if ck.SameSite == "" {
			ck.SameSite = ss
		}
	}
	return ck, ck.Name != "" && ck.Domain != ""
}
func loadState(path string) (*storageState, error) {
	st := &storageState{Cookies: []storedCookie{}, Origins: []originStorage{}}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return st, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st, nil
	}
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errf("cookie 文件不是有效的 JSON: %s", path)
	}
	switch v := raw.(type) {
	case map[string]interface{}:
		cookies, ok := v["cookies"].([]interface{})
		if !ok {
			return nil, errf("--cookie 必须是独立的 storage_state JSON 文件")
		}
		for _, c := range cookies {
			if m, ok := c.(map[string]interface{}); ok {
				if ck, ok := anyToCookie(m); ok {
					st.Cookies = append(st.Cookies, ck)
				}
			}
		}
		if origins, ok := v["origins"].([]interface{}); ok {
			for _, o := range origins {
				m, ok := o.(map[string]interface{})
				if !ok {
					continue
				}
				origin := originStorage{Origin: str(m["origin"])}
				if items, ok := m["localStorage"].([]interface{}); ok {
					for _, it := range items {
						if im, ok := it.(map[string]interface{}); ok {
							origin.LocalStorage = append(origin.LocalStorage, localStorageItem{Name: str(im["name"]), Value: str(im["value"])})
						}
					}
				}
				if origin.Origin != "" && len(origin.LocalStorage) > 0 {
					st.Origins = append(st.Origins, origin)
				}
			}
		}
	case []interface{}:
		for _, c := range v {
			if m, ok := c.(map[string]interface{}); ok {
				if ck, ok := anyToCookie(m); ok {
					st.Cookies = append(st.Cookies, ck)
				}
			}
		}
	default:
		return nil, errf("无法识别的 cookie 文件格式: %s", path)
	}
	return st, nil
}
func LoadState(path string) (*StorageState, error) { return loadState(path) }
func sameSiteToProto(s string) proto.NetworkCookieSameSite {
	switch s {
	case "Strict":
		return proto.NetworkCookieSameSiteStrict
	case "None":
		return proto.NetworkCookieSameSiteNone
	default:
		return proto.NetworkCookieSameSiteLax
	}
}
func protoToSameSite(s proto.NetworkCookieSameSite) string {
	switch s {
	case proto.NetworkCookieSameSiteStrict:
		return "Strict"
	case proto.NetworkCookieSameSiteNone:
		return "None"
	default:
		return "Lax"
	}
}
func (s *StorageState) toRodCookies() []*proto.NetworkCookieParam {
	var out []*proto.NetworkCookieParam
	for _, c := range s.Cookies {
		p := &proto.NetworkCookieParam{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: sameSiteToProto(c.SameSite)}
		if c.Expires > 0 {
			p.Expires = proto.TimeSinceEpoch(c.Expires)
		}
		out = append(out, p)
	}
	return out
}
func (s *StorageState) ToRodCookies() []*proto.NetworkCookieParam { return s.toRodCookies() }
func captureOrigins(b *rod.Browser) []originStorage {
	seen := map[string]bool{}
	var out []originStorage
	pages, err := b.Pages()
	if err != nil {
		return out
	}
	for _, p := range pages {
		for _, fr := range listFrames(p) {
			var origin struct {
				Origin string `json:"origin"`
				Data   string `json:"data"`
			}
			if !evalJSON(fr, `() => ({origin: location.origin, data: JSON.stringify(localStorage)})`, &origin) || origin.Origin == "" || origin.Origin == "null" || seen[origin.Origin] {
				continue
			}
			var items []localStorageItem
			if json.Unmarshal([]byte(origin.Data), &items) != nil || len(items) == 0 {
				continue
			}
			seen[origin.Origin] = true
			out = append(out, originStorage{Origin: origin.Origin, LocalStorage: items})
		}
	}
	return out
}
func ApplyStorage(b *rod.Browser, st *StorageState) {
	for _, o := range st.Origins {
		p, err := b.Page(proto.TargetCreateTarget{})
		if err != nil {
			continue
		}
		if err := p.Timeout(20 * time.Second).Navigate(o.Origin + "/favicon.ico"); err == nil {
			for _, it := range o.LocalStorage {
				_, _ = safeEval(p, 5*time.Second, `(n, v) => { try { localStorage.setItem(n, v) } catch (e) {} }`, it.Name, it.Value)
			}
		}
		_ = p.Close()
	}
}
func SaveState(b *rod.Browser, path string) error {
	cookies, err := b.GetCookies()
	if err != nil {
		return err
	}
	st := storageState{Cookies: []storedCookie{}, Origins: []originStorage{}}
	for _, c := range cookies {
		st.Cookies = append(st.Cookies, storedCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: float64(c.Expires), HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: protoToSameSite(c.SameSite)})
	}
	if caps := captureOrigins(b); len(caps) > 0 {
		st.Origins = caps
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

var chromeCandidates = []string{
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files\Google\Chrome Beta\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome Beta\Application\chrome.exe`,
	`/usr/bin/google-chrome`, `/usr/bin/google-chrome-stable`, `/usr/bin/chromium`, `/usr/bin/chromium-browser`,
	`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
}

func FindChrome() string {
	if p := strings.TrimSpace(os.Getenv("CHROME_BIN")); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	for _, p := range chromeCandidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

type BrowserOptions struct {
	Headless    bool
	Bin         string
	UserAgent   string
	Args        []string
	UserDataDir string
}
type browserOptions = BrowserOptions

func linuxSandboxArgs() []string {
	return []string{"no-sandbox", "disable-setuid-sandbox", "disable-dev-shm-usage", "disable-gpu", "disable-blink-features=AutomationControlled"}
}
func hasDisplay() bool { return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" }
func forceHeadlessIfNoDisplay(opts browserOptions) browserOptions {
	if runtime.GOOS == "linux" && !opts.Headless && !hasDisplay() {
		logInfo("[browser] 当前环境没有图形界面（DISPLAY 未设置），已自动切换为 headless 模式")
		opts.Headless = true
	}
	return opts
}
func launchBrowser(opts browserOptions) (*rod.Browser, error) {
	opts = forceHeadlessIfNoDisplay(opts)
	b, err := launchBrowserOnce(opts)
	if err == nil || opts.UserDataDir == "" {
		return b, err
	}
	if msg := err.Error(); strings.Contains(msg, "Missing X server") || strings.Contains(msg, "DISPLAY") {
		return nil, errf("当前环境没有图形界面，无法以有头模式启动浏览器（容器/无桌面环境请使用 headless=true）: %v", err)
	}
	logInfo("[browser] 启动失败，尝试清理占用配置目录的残留浏览器后重试:", err)
	if n := ReleaseProfile(opts.UserDataDir); n > 0 {
		logInfo("[browser] 已清理残留浏览器进程数:", n)
	}
	time.Sleep(1200 * time.Millisecond)
	return launchBrowserOnce(opts)
}
func LaunchBrowser(opts BrowserOptions) (*rod.Browser, error) { return launchBrowser(opts) }
func launchBrowserOnce(opts browserOptions) (*rod.Browser, error) {
	l := launcher.New().Headless(opts.Headless).Leakless(false)
	if opts.Bin != "" {
		l = l.Bin(opts.Bin)
	} else if bin := FindChrome(); bin != "" {
		l = l.Bin(bin)
	}
	args := opts.Args
	if runtime.GOOS == "linux" {
		args = append(linuxSandboxArgs(), args...)
	}
	if opts.UserAgent != "" {
		l = l.Set("user-agent", opts.UserAgent)
	}
	if opts.UserDataDir != "" {
		l = l.Set("user-data-dir", opts.UserDataDir)
	}
	seen := map[string]bool{}
	for _, a := range args {
		if i := strings.IndexByte(a, '='); i >= 0 {
			if seen[a[:i]] {
				continue
			}
			seen[a[:i]] = true
			l = l.Set(launcherFlags.Flag(a[:i]), a[i+1:])
			continue
		}
		if seen[a] {
			continue
		}
		seen[a] = true
		l = l.Set(launcherFlags.Flag(a))
	}
	controlURL, err := l.Launch()
	if err != nil {
		return nil, err
	}
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		return nil, err
	}
	return b, nil
}
func listFrames(page *rod.Page) []*rod.Page {
	var out []*rod.Page
	var walk func(*rod.Page)
	walk = func(p *rod.Page) {
		out = append(out, p)
		var n int
		if !evalJSON(p, `() => document.querySelectorAll('iframe').length`, &n) || n == 0 {
			return
		}
		els, err := p.Timeout(time.Second).Elements("iframe")
		if err != nil {
			return
		}
		for _, el := range els {
			if fr, ok := safeFrame(el); ok {
				walk(fr)
			}
		}
	}
	walk(page)
	return out
}
func ListFrames(page *rod.Page) []*rod.Page { return listFrames(page) }
func safeFrame(el *rod.Element) (fr *rod.Page, ok bool) {
	defer func() {
		if recover() != nil {
			fr, ok = nil, false
		}
	}()
	fr, err := el.Frame()
	if err != nil || fr == nil {
		return nil, false
	}
	return fr, true
}
func evalJSON(p *rod.Page, js string, out interface{}, args ...interface{}) bool {
	res, err := safeEval(p, 15*time.Second, js, args...)
	if err != nil || res == nil {
		return false
	}
	return res.Value.Unmarshal(out) == nil
}
func EvalJSON(p *rod.Page, js string, out interface{}, args ...interface{}) bool {
	return evalJSON(p, js, out, args...)
}
func safeEval(p *rod.Page, timeout time.Duration, js string, args ...interface{}) (res *proto.RuntimeRemoteObject, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, errf("页面已失效: %v", r)
		}
	}()
	if p == nil {
		return nil, errf("页面为空")
	}
	return p.Timeout(timeout).Eval(js, args...)
}
func SafeEval(p *rod.Page, timeout time.Duration, js string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	return safeEval(p, timeout, js, args...)
}
func safeElementEval(el *rod.Element, js string) (res *proto.RuntimeRemoteObject, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, errf("元素已失效: %v", r)
		}
	}()
	if el == nil {
		return nil, errf("元素为空")
	}
	return el.Eval(js)
}
func SafeElementEval(el *rod.Element, js string) (*proto.RuntimeRemoteObject, error) {
	return safeElementEval(el, js)
}
func evalText(p *rod.Page, js string) string {
	var s string
	if evalJSON(p, js, &s) {
		return s
	}
	return ""
}
func EvalText(p *rod.Page, js string) string { return evalText(p, js) }
func currentURL(p *rod.Page) string {
	if u := evalText(p, `() => location.href`); u != "" {
		return u
	}
	if info, err := p.Info(); err == nil && info != nil {
		return info.URL
	}
	return ""
}
func CurrentURL(p *rod.Page) string { return currentURL(p) }
func frameText(p *rod.Page) string {
	return evalText(p, `() => (document.body && document.body.innerText) || ''`)
}
func FrameText(p *rod.Page) string { return frameText(p) }
func videoIDFromURL(platform, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range parts {
		part = strings.ToLower(part)
		if platform == "wechat" && host == "channels.weixin.qq.com" && part == "post" || platform == "douyin" && (host == "douyin.com" || strings.HasSuffix(host, ".douyin.com")) && (part == "video" || part == "post") {
			if i+1 < len(parts) {
				next := strings.ToLower(parts[i+1])
				if next == "list" || next == "edit" {
					continue
				}
				if next != "detail" && validVideoID(parts[i+1]) {
					return parts[i+1]
				}
			}
			for _, key := range []string{"video_id", "aweme_id", "id"} {
				if id := u.Query().Get(key); validVideoID(id) {
					return id
				}
			}
		}
	}
	return ""
}
func VideoIDFromURL(platform, raw string) string { return videoIDFromURL(platform, raw) }

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validVideoID(id string) bool { return videoIDPattern.MatchString(id) }
func ValidVideoID(id string) bool { return validVideoID(id) }
func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
		return ref
	}
	bu, err := url.Parse(base)
	if err != nil {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return bu.ResolveReference(ru).String()
}
func ResolveURL(base, ref string) string { return resolveURL(base, ref) }
