package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"seuszjz/internal/grab"
	"seuszjz/internal/ocr"
	"seuszjz/internal/seu"
	"seuszjz/internal/store"
)

// Version 是应用版本号, 显示在设置页。
//
// 必须是变量而不是常量: 发布时由 CI 用 -ldflags "-X main.Version=1.2.3" 注入 git tag 对应的版本。
// 本地 `wails3 task build` 不注入, 统一显示 "dev" —— 仓库里不再跟着 tag 改版本号,
// 版本号只在发布时由 tag / APP_VERSION 决定。
var Version = "dev"

const (
	loginTimeout = 150 * time.Second
	callTimeout  = 60 * time.Second
	ocrTimeout   = 45 * time.Second
)

var errNeedLogin = errors.New("尚未登录, 请先在登录页完成统一身份认证")

// core 是三个服务共享的运行时。
//
// netMu 串行化所有远端请求 (登录 + 列表 + 抢座), stateMu 只保护内存里的状态,
// 这样登录过程中前端轮询 Status 不会被阻塞。
type core struct {
	app   *application.App
	win   *application.WebviewWindow
	store *store.Store
	ocr   *ocr.Solver
	eng   *grab.Engine

	netMu sync.Mutex

	stateMu sync.RWMutex
	cfg     store.Config
	pass    string
	sess    *seu.Session
	user    seu.UserInfo
	cached  *store.Session
	// network 是最近一次登录实际走的路径 (campus / offcampus), 供界面与日志展示。
	network string

	// pendingSess/pendingUser/pendingPass 暂存「等二次验证」的会话:
	// 密码登录返回非可信设备时留着这个会话 (同一份 Cookie), 用户输完短信码后继续。
	pendingSess *seu.Session
	pendingUser string
	pendingPass string

	// legacyDone 标记旧版磁盘配置只尝试导入一次。
	legacyDone bool

	ocrMu   sync.RWMutex
	ocrOK   bool
	ocrInfo string
}

func newCore(app *application.App) *core {
	return &core{
		app:   app,
		store: store.New(),
		ocr:   ocr.New(),
		eng:   grab.New(),
		// 账号与会话不落盘: 启动时是空的, 真实数据等前端把 localStorage 里的
		// 快照交回来 (Bootstrap 里的 adopt) 再填。
		cfg: store.Default(),
	}
}

// adopt 用前端交回的快照恢复账号与会话。
//
// 每次页面加载都会走一遍: 已经登录着就忽略 (内存里的会话比快照新), 否则以
// 交回的快照为准。没有快照时, 顺手把旧版本写在磁盘上的配置导入一次。
func (c *core) adopt(blob string) {
	c.stateMu.Lock()
	live := c.sess != nil
	importLegacy := !live && !c.legacyDone && strings.TrimSpace(blob) == ""
	if importLegacy {
		c.legacyDone = true
	}
	c.stateMu.Unlock()

	if live {
		return
	}
	switch {
	case importLegacy:
		if ok, err := c.store.ImportLegacy(); err != nil {
			c.logf("导入旧版配置失败: %v", err)
		} else if ok {
			c.logf("已从 %s 导入旧版账号信息, 之后不再读写磁盘文件", store.LegacyDir())
		}
	default:
		if err := c.store.Restore([]byte(blob)); err != nil {
			c.logf("恢复本地登录数据失败: %v", err)
		}
	}
	c.reloadFromStore()
}

// reloadFromStore 把快照里的内容同步到内存字段。
func (c *core) reloadFromStore() {
	cfg := c.store.Load()
	pass := c.store.Password(cfg)

	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	cfg.Password = pass
	c.cfg, c.pass = cfg, pass
	if sess, ok := c.store.LoadSession(); ok {
		c.cached = &sess
		c.user = sess.User
		if c.cfg.Username == "" {
			c.cfg.Username = sess.Username
		}
		return
	}
	c.cached = nil
	c.user = seu.UserInfo{}
}

// persist 把当前状态序列化成快照交给前端, 由前端写进 localStorage。
func (c *core) persist() {
	snapshot, err := c.store.Snapshot()
	if err != nil {
		c.logf("生成登录状态快照失败: %v", err)
		return
	}
	c.emit("state:changed", snapshot)
}

// startup 在窗口创建之后调用 (需要 app.Event 可用)。
func (c *core) startup() {
	go c.detectOCR()
}

func (c *core) detectOCR() {
	ctx, cancel := context.WithTimeout(context.Background(), ocrTimeout)
	defer cancel()
	ok := c.ocr.Available(ctx)
	info := c.ocr.Describe()
	c.ocrMu.Lock()
	c.ocrOK, c.ocrInfo = ok, info
	c.ocrMu.Unlock()
	c.emit("ocr:ready", OCRStatus{Available: ok, Info: info})
}

func (c *core) emit(name string, data any) {
	if c.app == nil {
		return
	}
	c.app.Event.Emit(name, data)
}

func (c *core) logf(format string, args ...any) {
	log.Printf(format, args...)
}

func (c *core) shutdown() {
	c.eng.Stop()
	c.ocr.Close()
}

// --- 状态 -------------------------------------------------------------------

func (c *core) currentUser() seu.UserInfo {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.user
}

func (c *core) session() *seu.Session {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.sess
}

func (c *core) settings() Settings {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return Settings{
		Username:    c.cfg.Username,
		Remember:    c.cfg.Remember,
		AutoLogin:   c.cfg.AutoLogin,
		AutoCaptcha: c.cfg.AutoCaptcha,
		WaitMs:      c.cfg.WaitMs,
		AttemptMs:   c.cfg.AttemptMs,
		Cooldown:    c.cfg.Cooldown,
		Theme:       c.cfg.Theme,
		NetMode:     string(seu.ParseNetMode(c.cfg.NetMode)),
		HasPassword: c.pass != "",
	}
}

func (c *core) status() Status {
	c.ocrMu.RLock()
	ok, info := c.ocrOK, c.ocrInfo
	c.ocrMu.RUnlock()

	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return Status{
		LoggedIn:      c.sess != nil && c.sess.LoggedIn(),
		User:          c.user,
		SavedUsername: c.cfg.Username,
		HasSession:    c.cached != nil,
		OcrAvailable:  ok,
		OcrInfo:       info,
		Version:       Version,
	}
}

func (c *core) ocrSolver() *ocr.Solver { return c.ocr }

// networkOf 返回最近一次登录走的网络路径。
func (c *core) networkOf() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.network
}

// netModePref 读取用户选的网络环境 (空值 = 自动探测)。
func (c *core) netModePref() seu.NetMode {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return seu.ParseNetMode(c.cfg.NetMode)
}

// --- 登录 -------------------------------------------------------------------

func (c *core) newSession(username, password string) *seu.Session {
	return seu.NewSession(seu.Config{
		Username: username,
		Password: password,
		Timeout:  30 * time.Second,
		NetMode:  c.netModePref(),
		Logf: func(format string, args ...any) {
			c.logf("[seu] "+format, args...)
		},
	})
}

// login 用账号密码登录并持久化会话。netMu 由调用方 (或本函数) 持有。
func (c *core) login(ctx context.Context, username, password string) (seu.UserInfo, error) {
	c.netMu.Lock()
	defer c.netMu.Unlock()
	return c.loginLocked(ctx, username, password)
}

func (c *core) loginLocked(ctx context.Context, username, password string) (seu.UserInfo, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return seu.UserInfo{}, errors.New("请填写学号和密码")
	}

	sess := c.newSession(username, password)
	progress := func(stage, msg string) { c.emit("login:progress", msg) }
	if err := sess.Login(ctx, progress); err != nil {
		if errors.Is(err, seu.ErrStage2Required) {
			// 学校要求二次验证: 把这个会话留着, 用户输完短信码后接着用。
			c.stateMu.Lock()
			c.pendingSess, c.pendingUser, c.pendingPass = sess, username, password
			c.stateMu.Unlock()
		}
		return seu.UserInfo{}, err
	}
	return c.finishLoginLocked(ctx, sess, username, password)
}

// finishLoginLocked 保存登录成功的会话 (loginLocked / submitStage2Locked 共用)。
func (c *core) finishLoginLocked(ctx context.Context, sess *seu.Session, username, password string) (seu.UserInfo, error) {
	_ = ctx

	user := sess.User()
	if user.Username == "" {
		user.Username = username
	}
	c.logf("登录成功: %s (%s)", user.DisplayName, user.Username)

	c.stateMu.Lock()
	c.sess = sess
	c.user = user
	c.network = string(sess.Network())
	c.cfg.Username = username
	c.pass = password
	c.cfg.Password = password
	cached := store.Session{Username: username, Cookies: sess.Cookies(), User: user}
	c.cached = &cached
	cfg := c.cfg
	c.stateMu.Unlock()

	_ = c.store.SaveSession(cached)
	if err := c.store.Save(cfg); err != nil {
		c.logf("保存配置失败: %v", err)
	}
	c.persist()
	return user, nil
}

// stage2Pending 报告是否有等待二次验证的会话。
func (c *core) stage2Pending() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.pendingSess != nil
}

// sendStage2Code 给等待中的账号发短信验证码。
func (c *core) sendStage2Code(ctx context.Context) error {
	c.netMu.Lock()
	defer c.netMu.Unlock()

	c.stateMu.RLock()
	sess, username := c.pendingSess, c.pendingUser
	c.stateMu.RUnlock()
	if sess == nil {
		return errors.New("还没有待验证的登录, 请先输入账号密码")
	}
	c.logf("发送二次验证码: %s", username)
	return sess.SendStage2Code(ctx)
}

// submitStage2Locked 用短信验证码完成登录。
func (c *core) submitStage2Locked(ctx context.Context, code string) (seu.UserInfo, error) {
	c.stateMu.RLock()
	sess, username, password := c.pendingSess, c.pendingUser, c.pendingPass
	c.stateMu.RUnlock()
	if sess == nil {
		return seu.UserInfo{}, errors.New("还没有待验证的登录, 请先输入账号密码")
	}

	progress := func(stage, msg string) { c.emit("login:progress", msg) }
	if err := sess.LoginWithStage2(ctx, code, progress); err != nil {
		return seu.UserInfo{}, err
	}

	c.stateMu.Lock()
	c.pendingSess, c.pendingUser, c.pendingPass = nil, "", ""
	c.stateMu.Unlock()
	c.logf("二次验证通过")
	return c.finishLoginLocked(ctx, sess, username, password)
}

// restoreSession 用缓存的 Cookie 试恢复会话。
func (c *core) restoreSession(ctx context.Context) (seu.UserInfo, bool) {
	c.stateMu.RLock()
	cached := c.cached
	c.stateMu.RUnlock()
	if cached == nil || len(cached.Cookies) == 0 {
		return seu.UserInfo{}, false
	}

	c.netMu.Lock()
	defer c.netMu.Unlock()

	c.stateMu.RLock()
	cached = c.cached
	c.stateMu.RUnlock()
	if cached == nil {
		return seu.UserInfo{}, false
	}

	sess := c.newSession(cached.Username, "")
	sess.LoadCookies(cached.Cookies)
	if _, _, err := sess.Activities(ctx); err != nil {
		// 网络不通 != 会话失效: 校园网/校外切换时如果把本地会话删了, 用户回来
		// 还得重新输密码。只有服务端明确不认这个会话才清缓存。
		if seu.IsUnreachable(err) {
			c.logf("网络不可用, 保留本地会话: %v", err)
			return seu.UserInfo{}, false
		}
		c.logf("Cookie 会话已失效: %v", err)
		c.store.ClearSession()
		c.stateMu.Lock()
		c.cached = nil
		c.stateMu.Unlock()
		c.persist()
		return seu.UserInfo{}, false
	}

	user := cached.User
	if user.Username == "" {
		user.Username = cached.Username
	}
	sess.Restore(user)
	c.stateMu.Lock()
	c.sess = sess
	c.user = user
	c.stateMu.Unlock()
	c.logf("复用本地会话: %s", user.DisplayName)
	return user, true
}

// reloginLocked 用保存的密码重新登录。netMu 由调用方持有。
func (c *core) reloginLocked(ctx context.Context) error {
	c.stateMu.RLock()
	username, password := c.cfg.Username, c.pass
	c.stateMu.RUnlock()
	if username == "" || password == "" {
		return errNeedLogin
	}
	c.logf("会话失效, 尝试自动重新登录")
	_, err := c.loginLocked(ctx, username, password)
	return err
}

// withSession 在已登录会话上执行一次远端调用, 遇到会话失效时自动重登一次。
func (c *core) withSession(ctx context.Context, fn func(*seu.Session) error) error {
	c.netMu.Lock()
	defer c.netMu.Unlock()

	sess := c.session()
	if sess == nil {
		return errNeedLogin
	}
	err := fn(sess)
	if errors.Is(err, seu.ErrSessionExpired) {
		if reloginErr := c.reloginLocked(ctx); reloginErr != nil {
			return reloginErr
		}
		if sess = c.session(); sess == nil {
			return errNeedLogin
		}
		return fn(sess)
	}
	return err
}

func (c *core) requestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), callTimeout)
}

// --- grab.API 实现 ----------------------------------------------------------

func (c *core) Activities(ctx context.Context) ([]seu.Lecture, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var (
		lectures []seu.Lecture
		server   time.Time
	)
	err := c.withSession(ctx, func(s *seu.Session) error {
		list, now, err := s.Activities(ctx)
		if err == nil {
			lectures, server = list, now
		}
		return err
	})
	return lectures, server, err
}

func (c *core) FetchCaptcha(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var image []byte
	err := c.withSession(ctx, func(s *seu.Session) error {
		data, err := s.FetchCaptcha(ctx)
		if err == nil {
			image = data
		}
		return err
	})
	return image, err
}

func (c *core) Reserve(ctx context.Context, wid, code string) (seu.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result seu.Result
	err := c.withSession(ctx, func(s *seu.Session) error {
		res, err := s.Reserve(ctx, wid, code)
		if err == nil {
			result = res
		}
		return err
	})
	return result, err
}

// --- 服务: 账号 -------------------------------------------------------------

// SessionService 负责登录、状态与偏好设置。
type SessionService struct {
	core *core
}

func NewSessionService(c *core) *SessionService { return &SessionService{core: c} }

// Bootstrap 返回启动所需的全部状态。
//
// 参数是前端从 localStorage 读到的快照, 原样交回来即可; 返回值里的 State 是
// 后端认可的当前快照, 前端直接覆盖写回 localStorage。
func (s *SessionService) Bootstrap(blob string) (Bootstrap, error) {
	c := s.core
	c.adopt(blob)

	snapshot, err := c.store.Snapshot()
	if err != nil {
		c.logf("生成登录状态快照失败: %v", err)
	}
	return Bootstrap{
		Status:   c.status(),
		Settings: c.settings(),
		Grab:     c.eng.State(),
		State:    snapshot,
	}, nil
}

// Status 返回当前登录状态。
func (s *SessionService) Status() (Status, error) { return s.core.status(), nil }

// Settings 返回当前偏好设置。
func (s *SessionService) Settings() (Settings, error) { return s.core.settings(), nil }

// Login 使用学号 + 密码登录 (Wails 自动注入 context)。
func (s *SessionService) Login(ctx context.Context, username, password string, remember bool) (LoginResult, error) {
	c := s.core
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	// 先改记住密码开关, 登录成功后的 store.Save 才会按新值处理。
	c.stateMu.Lock()
	c.cfg.Remember = remember
	if !remember {
		c.pass = ""
		c.cfg.Password = ""
	}
	c.stateMu.Unlock()

	user, err := c.login(ctx, username, password)
	if err != nil {
		if errors.Is(err, seu.ErrStage2Required) {
			return LoginResult{User: seu.UserInfo{Username: username}, NeedStage2: true}, nil
		}
		return LoginResult{}, err
	}
	return LoginResult{User: user, Source: "manual", Network: c.networkOf()}, nil
}

// SendStage2Code 给当前账号发一条二次验证短信 (学校要求非可信设备验证时使用)。
func (s *SessionService) SendStage2Code(ctx context.Context) (LoginResult, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	c := s.core
	if err := c.sendStage2Code(ctx); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{NeedStage2: true, Source: "stage2"}, nil
}

// SubmitStage2 提交短信验证码并完成登录; 成功后会写回会话缓存。
func (s *SessionService) SubmitStage2(ctx context.Context, code string) (LoginResult, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	c := s.core
	code = strings.TrimSpace(code)
	if code == "" {
		return LoginResult{}, errors.New("请填写短信验证码")
	}

	c.netMu.Lock()
	user, err := c.submitStage2Locked(ctx, code)
	c.netMu.Unlock()
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: user, Source: "stage2", Network: c.networkOf()}, nil
}

// AutoLogin 依次尝试 Cookie 缓存与已保存的密码。
func (s *SessionService) AutoLogin(ctx context.Context) (LoginResult, error) {
	c := s.core
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	if user, ok := c.restoreSession(ctx); ok {
		return LoginResult{User: user, Source: "cache"}, nil
	}

	c.stateMu.RLock()
	username, password := c.cfg.Username, c.pass
	c.stateMu.RUnlock()
	if username == "" || password == "" {
		return LoginResult{}, errNeedLogin
	}

	user, err := c.login(ctx, username, password)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: user, Source: "password", Network: c.networkOf()}, nil
}

// Logout 退出登录并清除本地会话缓存。
func (s *SessionService) Logout() (Status, error) {
	c := s.core
	c.eng.Stop()

	c.stateMu.Lock()
	if c.sess != nil {
		c.sess.Reset()
	}
	c.sess = nil
	c.user = seu.UserInfo{}
	c.cached = nil
	c.stateMu.Unlock()

	c.store.ClearSession()
	c.persist()
	return c.status(), nil
}

// ForgetLogin 清掉保存的密码与登录会话 (localStorage 里的快照会同步更新)。
func (s *SessionService) ForgetLogin() (Settings, error) {
	c := s.core
	c.eng.Stop()

	c.stateMu.Lock()
	if c.sess != nil {
		c.sess.Reset()
	}
	c.sess = nil
	c.user = seu.UserInfo{}
	c.cached = nil
	c.pass = ""
	c.cfg.Password = ""
	cfg := c.cfg
	c.stateMu.Unlock()

	c.store.ClearSession()
	if err := c.store.Save(cfg); err != nil {
		return c.settings(), err
	}
	c.persist()
	return c.settings(), nil
}

// SaveSettings 保存偏好设置。
func (s *SessionService) SaveSettings(ctx context.Context, in Settings) (Settings, error) {
	c := s.core

	c.stateMu.Lock()
	c.cfg.Username = strings.TrimSpace(in.Username)
	c.cfg.Remember = in.Remember
	c.cfg.AutoLogin = in.AutoLogin
	c.cfg.AutoCaptcha = in.AutoCaptcha
	c.cfg.WaitMs = in.WaitMs
	c.cfg.AttemptMs = in.AttemptMs
	c.cfg.Cooldown = in.Cooldown
	c.cfg.Theme = in.Theme
	c.cfg.NetMode = string(seu.ParseNetMode(in.NetMode))
	if !in.Remember {
		c.pass = ""
		c.cfg.Password = ""
	}
	cfg := c.cfg
	c.stateMu.Unlock()

	if err := c.store.Save(cfg); err != nil {
		return c.settings(), err
	}
	c.persist()
	return c.settings(), nil
}

// --- 服务: 讲座 -------------------------------------------------------------

// LectureService 负责讲座列表、预约与取消。
type LectureService struct {
	core *core
}

func NewLectureService(c *core) *LectureService { return &LectureService{core: c} }

// Overview 拉取讲座列表 + 我的预约 + 通知公告。
func (s *LectureService) Overview(ctx context.Context) (Overview, error) {
	c := s.core
	ctx, cancel := context.WithTimeout(ctx, callTimeout*2)
	defer cancel()

	var out Overview
	err := c.withSession(ctx, func(sess *seu.Session) error {
		lectures, server, err := sess.Activities(ctx)
		if err != nil {
			return err
		}
		out.Lectures = lectures
		if !server.IsZero() {
			out.ServerTime = server.Format(seu.TimeLayout)
		}
		mine, _, err := sess.MyActivities(ctx)
		if err != nil {
			return err
		}
		out.Mine = mine
		out.Requirement = seu.EvaluateRequirement(mine)
		notices, _, err := sess.Notices(ctx)
		if err != nil {
			return err
		}
		out.Notices = notices
		return nil
	})
	if err != nil {
		return Overview{}, err
	}
	out.FetchedAt = seu.Now().Format("2006-01-02 15:04:05")
	if out.Lectures == nil {
		out.Lectures = []seu.Lecture{}
	}
	if out.Mine == nil {
		out.Mine = []seu.MyActivity{}
	}
	if out.Notices == nil {
		out.Notices = []seu.Notice{}
	}
	return out, nil
}

// Check 预约前置校验。
func (s *LectureService) Check(ctx context.Context, wid string) (seu.Result, error) {
	c := s.core
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result seu.Result
	err := c.withSession(ctx, func(sess *seu.Session) error {
		res, err := sess.CheckAppointment(ctx, wid)
		if err == nil {
			result = res
		}
		return err
	})
	return result, err
}

// Captcha 取一张新的验证码图片。
func (s *LectureService) Captcha(ctx context.Context) (Captcha, error) {
	image, err := s.core.FetchCaptcha(ctx)
	if err != nil {
		return Captcha{}, err
	}
	return Captcha{Image: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image)}, nil
}

// Solve 用本地 OCR 识别一张验证码 (data URL)。OCR 不可用时返回空串。
func (s *LectureService) Solve(ctx context.Context, image string) (string, error) {
	c := s.core
	raw, err := decodeDataURL(image)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, err := c.ocrSolver().Solve(ctx, raw)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(code), nil
}

// Reserve 提交预约。
func (s *LectureService) Reserve(ctx context.Context, wid, code string) (seu.Result, error) {
	return s.core.Reserve(ctx, wid, strings.TrimSpace(code))
}

// Cancel 取消预约。
func (s *LectureService) Cancel(ctx context.Context, wid string) (seu.Result, error) {
	c := s.core
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result seu.Result
	err := c.withSession(ctx, func(sess *seu.Session) error {
		res, err := sess.Cancel(ctx, wid)
		if err == nil {
			result = res
		}
		return err
	})
	return result, err
}

// --- 服务: 抢座 -------------------------------------------------------------

// GrabService 负责自动抢座任务。
type GrabService struct {
	core *core
}

func NewGrabService(c *core) *GrabService { return &GrabService{core: c} }

// State 返回抢座任务当前状态。
func (s *GrabService) State() (grab.State, error) { return s.core.eng.State(), nil }

// Start 启动抢座任务。
func (s *GrabService) Start(ctx context.Context, wid string, waitMs, attemptMs, cooldown int, autoCaptcha bool) (grab.State, error) {
	c := s.core
	if strings.TrimSpace(wid) == "" {
		return grab.State{}, errors.New("请先选择要抢的讲座")
	}
	if c.session() == nil {
		return grab.State{}, errNeedLogin
	}

	opts := grab.Options{
		WID:             wid,
		WaitInterval:    clampMs(waitMs, 200, 60000, 1000),
		AttemptInterval: clampMs(attemptMs, 50, 10000, 300),
		Cooldown:        time.Duration(clampInt(cooldown, 0, 3600)) * time.Second,
	}
	if autoCaptcha {
		detectCtx, cancel := context.WithTimeout(ctx, ocrTimeout)
		available := c.ocrSolver().Available(detectCtx)
		cancel()
		if available {
			opts.Solve = func(ctx context.Context, jpeg []byte) (string, error) {
				return c.ocrSolver().Solve(ctx, jpeg)
			}
		} else {
			c.emit("grab:notice", "本地 OCR 不可用, 验证码需要手动输入")
		}
	}

	opts.OnState = func(state grab.State) { c.emit("grab:state", state) }
	opts.OnDone = func(state grab.State) {
		c.emit("grab:state", state)
		if state.Phase == grab.PhaseSuccess {
			if c.win != nil {
				c.win.Flash(true)
				c.win.Focus()
			}
			c.emit("grab:done", state)
		}
	}

	if err := c.eng.Start(context.Background(), c, opts); err != nil {
		return grab.State{}, err
	}
	return c.eng.State(), nil
}

// Stop 停止抢座任务。
func (s *GrabService) Stop() (grab.State, error) {
	c := s.core
	c.eng.Stop()
	return c.eng.State(), nil
}

// SubmitCode 手动提交验证码。
func (s *GrabService) SubmitCode(code string) (grab.State, error) {
	c := s.core
	if err := c.eng.SubmitCode(strings.TrimSpace(code)); err != nil {
		return c.eng.State(), err
	}
	return c.eng.State(), nil
}

// ClearLogs 清空抢座日志。
func (s *GrabService) ClearLogs() (grab.State, error) {
	c := s.core
	c.eng.ClearLogs()
	return c.eng.State(), nil
}

// RefreshCaptcha 换一张验证码。
func (s *GrabService) RefreshCaptcha() (grab.State, error) {
	c := s.core
	c.eng.RefreshCaptcha()
	return c.eng.State(), nil
}

// --- 小工具 -----------------------------------------------------------------

// clampInt 把整数限制在 [min, max] 区间 (冷却允许 0, 所以和 clampMs 分开)。
func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// clampMs 把毫秒数限制在合理区间, 非法值 (空输入/负数) 用默认值兜底。
func clampMs(value, min, max, fallback int) time.Duration {
	if value <= 0 {
		value = fallback
	}
	if value < min {
		value = min
	}
	if value > max {
		value = max
	}
	return time.Duration(value) * time.Millisecond
}

func decodeDataURL(data string) ([]byte, error) {
	if idx := strings.Index(data, "base64,"); idx >= 0 {
		data = data[idx+len("base64,"):]
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(data))
}
