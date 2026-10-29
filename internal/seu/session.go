// Package seu implements the reverse-engineered protocol of the Southeast
// University graduate "humanities and science quality lecture" reservation
// system (ehall jzxxtjapp), including CAS login, the aTrust portal handshake
// and the ehall application session.
package seu

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Endpoints and constants mirrored from scripts/seu_lecture_proto.py.
const (
	AuthBase   = "https://auth.seu.edu.cn"
	PortalBase = "https://vpn.seu.edu.cn"

	CASService = PortalBase + ":443/passport/v1/auth/cas?sfDomain=CAS-auth"
	AppService = "http://ehall.seu.edu.cn/gsapp/sys/jzxxtjapp/*default/index.do"
	AppURL     = "https://ehall.seu.edu.cn/gsapp/sys/jzxxtjapp/*default/index.do"
	APIBase    = "https://ehall.seu.edu.cn/gsapp/sys/jzxxtjapp/hdyy/"
	// EhallRoot 只用于网络探测: 校外访问它会 302 到 aTrust 网关。
	EhallRoot = "https://ehall.seu.edu.cn/"

	EPActivity = APIBase + "queryActivityList.do"   // 讲座活动列表
	EPMy       = APIBase + "queryMyActivityList.do" // 我的预约
	EPNotice   = APIBase + "queryNotice.do"         // 通知公告
	EPSetting  = APIBase + "getSetting.do"          // 系统设置
	EPCheck    = APIBase + "appiontCheck.do"        // 预约前置校验
	EPVcode    = APIBase + "vcode.do"               // 预约验证码
	EPYYSave   = APIBase + "yySave.do"              // 提交预约
	EPDelete   = APIBase + "delete.do"              // 取消预约

	UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

	// TimeLayout 是接口里所有时间字段的格式。
	TimeLayout = "2006-01-02 15:04:05"
)

// NetMode 决定登录走哪条网络路径。
//
// 校外的路径是 CAS -> aTrust 网关 -> ehall; 校园网本来就在内网, 那个网关通常
// 连不上, 只需要 CAS -> ehall。默认 auto, 由探测决定走哪条。
type NetMode string

const (
	// NetAuto 先探一次 ehall 是否直连可达, 再决定路径 (默认)。
	NetAuto NetMode = "auto"
	// NetCampus 校园网: 直连 ehall, 跳过 aTrust 网关。
	NetCampus NetMode = "campus"
	// NetOffCampus 校外: 先过 aTrust 网关, 再访问 ehall。
	NetOffCampus NetMode = "offcampus"
)

// ParseNetMode 把配置里的字符串转成 NetMode, 空值和未知值都按 auto 处理。
func ParseNetMode(v string) NetMode {
	switch NetMode(strings.TrimSpace(v)) {
	case NetCampus:
		return NetCampus
	case NetOffCampus:
		return NetOffCampus
	default:
		return NetAuto
	}
}

// ErrSessionExpired 表示 ehall 会话已失效, 需要重新登录。
var ErrSessionExpired = errors.New("登录状态已失效, 请重新登录")

// ErrStage2Required 表示学校要求二次验证 (非可信设备)。
//
// 统一身份认证对「陌生设备」会返回 code 502 / info=非可信设备，要求输入短信验证码。
// 此时调用 SendStage2Code 发码, 再用 LoginWithStage2 带码重提即可; 带 rememberTrustDevice
// 成功后该设备指纹会被学校记住, 之后不再要求验证。
var ErrStage2Required = errors.New("需要二次验证")

// LoginError 表示登录流程中的可预期失败 (账号密码错误、需要二次校验等)。
type LoginError struct {
	Stage string
	Msg   string
	// Cause 是原始错误, 用来区分「网络不可达」和「业务拒绝」。
	Cause error
}

// Unwrap 让 errors.Is / errors.As 能穿透 LoginError 看到底层的网络错误。
func (e *LoginError) Unwrap() error { return e.Cause }

func (e *LoginError) Error() string {
	if e.Stage == "" {
		return e.Msg
	}
	return e.Stage + ": " + e.Msg
}

// Config 是会话配置。
type Config struct {
	Username string
	Password string
	Timeout  time.Duration
	Debug    bool
	// NetMode 指定网络环境; 留空等价于 NetAuto (自动探测)。
	NetMode NetMode
	// Logf 接收登录/接口调用过程中的调试信息, 可为 nil。
	Logf func(format string, args ...any)
}

// PortalParams 是 aTrust 网关的公共查询参数。
func portalParams() url.Values {
	return url.Values{
		"clientType": {"SDPBrowserClient"},
		"platform":   {"Linux"},
		"lang":       {"zh-CN"},
	}
}

// Session 持有一次登录产生的全部状态 (Cookie、CSRF、用户信息)。
type Session struct {
	mu  sync.Mutex
	cfg Config

	jar    *Jar
	client *http.Client // 不跟随重定向
	follow *http.Client // 跟随重定向

	csrf          string
	netMode       NetMode // 本次登录实际走的路径 (探测结果)
	portalReferer string
	user          UserInfo
	userRaw       map[string]any
	loggedIn      bool
}

// NewSession 创建一个空会话。
func NewSession(cfg Config) *Session {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 40 * time.Second
	}
	jar := NewJar()
	noRedirect := func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	// 注意: 关闭 HTTP/2。ehall 前面的网关在 h2 下会一直不返回响应头,
	// 只有 HTTP/1.1 才能正常完成换票 (与浏览器/requests 的行为一致)。
	//
	// 拨号用自带的 dialer: ehall 在校园网里会解析出多个地址, 其中一部分连不通,
	// net.Dialer 逐地址串行尝试会卡在坏地址上 (详见 dialer.go)。
	var dialLog func(string, ...any)
	if cfg.Debug {
		dialLog = cfg.Logf
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext:           newDialer(dialLog).DialContext,
	}
	return &Session{
		cfg: cfg,
		jar: jar,
		client: &http.Client{
			Jar:           jar,
			Timeout:       cfg.Timeout,
			CheckRedirect: noRedirect,
			Transport:     transport,
		},
		follow: &http.Client{
			Jar:       jar,
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		csrf:          randomUUID(),
		portalReferer: PortalBase + "/portal/shortcut.html",
	}
}

// LoggedIn 返回会话是否已完成登录。
func (s *Session) LoggedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loggedIn
}

// User 返回登录后获取的门户用户信息。
func (s *Session) User() UserInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.user
}

// Reset 清空会话状态 (登录失败或登出时调用)。
func (s *Session) Reset() {
	s.mu.Lock()
	s.loggedIn = false
	s.user = UserInfo{}
	s.userRaw = nil
	s.mu.Unlock()
	s.jar.Clear()
}

func (s *Session) logf(format string, args ...any) {
	if s.cfg.Debug && s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

// Cookies 导出当前 Cookie, 用于持久化会话。
func (s *Session) Cookies() []SavedCookie { return s.jar.Export() }

// LoadCookies 载入持久化的 Cookie。
func (s *Session) LoadCookies(cookies []SavedCookie) { s.jar.Import(cookies) }

// Restore 在复用持久化 Cookie 成功后被调用, 把会话标记为已登录。
func (s *Session) Restore(user UserInfo) {
	s.mu.Lock()
	s.user = user
	s.loggedIn = true
	s.mu.Unlock()
}

// Network 返回本次登录实际走的网络路径 (自动探测的结果)。
func (s *Session) Network() NetMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.netMode
}

func (s *Session) setNetMode(mode NetMode) {
	s.mu.Lock()
	s.netMode = mode
	s.mu.Unlock()
}

// --- Cookie jar ------------------------------------------------------------

// SavedCookie 是可持久化的 Cookie。
type SavedCookie struct {
	Host     string    `json:"host"`
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"httpOnly,omitempty"`
}

// Jar 同时实现了 http.CookieJar 与导出/导入能力。
type Jar struct {
	inner http.CookieJar
	mu    sync.Mutex
	saved map[string]SavedCookie
}

// NewJar 创建一个新的 Cookie 容器。
func NewJar() *Jar {
	inner, _ := newCookieJar()
	return &Jar{inner: inner, saved: map[string]SavedCookie{}}
}

func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.inner.SetCookies(u, cookies)
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cookies {
		path := c.Path
		if path == "" {
			path = "/"
		}
		key := u.Host + "\x00" + path + "\x00" + c.Name
		if c.MaxAge < 0 || (c.MaxAge == 0 && c.Value == "") {
			delete(j.saved, key)
			continue
		}
		j.saved[key] = SavedCookie{
			Host:     u.Host,
			Path:     path,
			Name:     c.Name,
			Value:    c.Value,
			Expires:  c.Expires,
			Secure:   c.Secure,
			HTTPOnly: c.HttpOnly,
		}
	}
}

func (j *Jar) Cookies(u *url.URL) []*http.Cookie { return j.inner.Cookies(u) }

// Export 返回全部已知 Cookie。
func (j *Jar) Export() []SavedCookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]SavedCookie, 0, len(j.saved))
	now := time.Now()
	for _, c := range j.saved {
		if !c.Expires.IsZero() && c.Expires.Before(now) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Import 载入 Cookie。
func (j *Jar) Import(cookies []SavedCookie) {
	for _, c := range cookies {
		if c.Name == "" || c.Host == "" {
			continue
		}
		u := &url.URL{Scheme: "https", Host: c.Host, Path: c.Path}
		j.inner.SetCookies(u, []*http.Cookie{{
			Name:     c.Name,
			Value:    c.Value,
			Path:     c.Path,
			Domain:   "",
			Expires:  c.Expires,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		}})
		j.mu.Lock()
		j.saved[c.Host+"\x00"+c.Path+"\x00"+c.Name] = c
		j.mu.Unlock()
	}
}

// Clear 清空全部 Cookie。
func (j *Jar) Clear() {
	j.mu.Lock()
	j.saved = map[string]SavedCookie{}
	j.mu.Unlock()
	inner, _ := newCookieJar()
	j.mu.Lock()
	j.inner = inner
	j.mu.Unlock()
}

// Has 判断是否存在指定名称的 Cookie。
func (j *Jar) Has(name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range j.saved {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Names 返回 Cookie 名称集合, 便于诊断。
func (j *Jar) Names() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	seen := map[string]bool{}
	out := []string{}
	for _, c := range j.saved {
		if !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c.Name)
		}
	}
	return out
}
