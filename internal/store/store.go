// Package store 保存账号偏好与登录会话。
//
// 这里不再写任何文件: 数据只活在内存里, 由前端把它当作一份快照存进浏览器的
// localStorage, 下次启动再原样交回来 (Restore)。这样运行程序的机器上不会多出
// 配置/会话文件, 服务器(容器)版本也不需要挂卷。
package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"seuszjz/internal/seu"
)

// Version 是快照格式版本, 前端原样保存, 升级时可以据此迁移。
//
// v2: 拟人化改成默认开启 (取码后随机等待 900~1200ms), 旧快照升级时一并迁移。
const Version = 2

// Config 是用户偏好设置。
type Config struct {
	Username    string `json:"username"`
	Password    string `json:"password"` // 混淆后的密文, 以 v1: 开头
	Remember    bool   `json:"remember"`
	AutoLogin   bool   `json:"autoLogin"`
	AutoCaptcha bool   `json:"autoCaptcha"`
	// WaitMs 是「还没开放 / 名额已满」时的轮询间隔 (毫秒): 这段应该慢, 省请求也不容易被限流。
	WaitMs int `json:"waitMs"`
	// AttemptMs 是「已经可以预约」时两次提交之间的间隔 (毫秒): 这段应该快。
	AttemptMs int `json:"attemptMs"`
	// Interval 是旧版的单间隔 (秒), 只在读老配置时用一次, 之后写回为空。
	Interval int `json:"interval,omitempty"`
	Cooldown int `json:"cooldown"` // 被限流后的冷却 (秒)
	// HumanizeDelay 为真时, 取完验证码到提交之间随机等 [HumanizeMinMs, HumanizeMaxMs] (默认关闭)。
	HumanizeDelay bool `json:"humanizeDelay"`
	// HumanizeMinMs / HumanizeMaxMs 是上面那个随机等待的区间 (毫秒), 默认 300 / 800。
	HumanizeMinMs int `json:"humanizeMinMs"`
	HumanizeMaxMs int `json:"humanizeMaxMs"`
	// HumanizeWarmList 为真时, 提交前先刷新一次讲座列表 (默认关闭)。
	HumanizeWarmList bool `json:"humanizeWarmList"`
	// GrabLog 为真时把抢座日志写到本地文件 (发行版默认关闭; dev 版固定开启)。
	GrabLog bool   `json:"grabLog"`
	Theme   string `json:"theme"`   // dark / light
	NetMode string `json:"netMode"` // auto / campus / offcampus
}

// Default 返回默认配置。
func Default() Config {
	return Config{
		AutoLogin:   true,
		AutoCaptcha: true,
		WaitMs:      500,
		AttemptMs:   300,
		Cooldown:    300,
		// 拟人化: 默认开启, 取码到提交之间随机等 900~1200ms, 让时序更像人工。
		HumanizeDelay: true,
		HumanizeMinMs: 900,
		HumanizeMaxMs: 1200,
		Theme:         "dark",
	}
}

// Session 是 Cookie 缓存。
type Session struct {
	Username string            `json:"username"`
	Cookies  []seu.SavedCookie `json:"cookies"`
	User     seu.UserInfo      `json:"user"`
	SavedAt  time.Time         `json:"savedAt"`
	// TrustOnly 表示这里只存了「设备信任」Cookie (退出登录后留下的),
	// 不能拿去免登录, 只用于下次密码登录时带上, 免得又被要求二次验证。
	TrustOnly bool `json:"trustOnly,omitempty"`
}

// State 是一份完整快照, 前端把它整个存进 localStorage。
type State struct {
	Version int      `json:"version"`
	Config  Config   `json:"config"`
	Session *Session `json:"session,omitempty"`
}

// Store 是内存里的账号状态, 所有方法并发安全。
type Store struct {
	mu sync.Mutex
	st State
}

// New 创建存储对象 (初始是默认配置, 没有任何登录信息)。
func New() *Store {
	return &Store{st: State{Version: Version, Config: Default()}}
}

// Load 返回当前配置。
func (s *Store) Load() Config {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.st.Config
	cfg.normalize()
	return cfg
}

// Save 更新配置 (密码会先做混淆)。
func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg.normalize()
	if cfg.Password != "" && !strings.HasPrefix(cfg.Password, "v1:") {
		enc, err := seal(cfg.Password)
		if err != nil {
			return err
		}
		cfg.Password = enc
	}
	if !cfg.Remember {
		cfg.Password = ""
	}
	s.st.Config = cfg
	return nil
}

// Password 返回明文密码 (没有保存时返回空串)。
func (s *Store) Password(cfg Config) string {
	if cfg.Password == "" {
		return ""
	}
	if !strings.HasPrefix(cfg.Password, "v1:") {
		return cfg.Password
	}
	plain, err := open(cfg.Password)
	if err != nil {
		return ""
	}
	return plain
}

func (c *Config) normalize() {
	// 旧版只有一个「秒」间隔: 迁移成「等开放」的默认值, 开放后的节奏单独给。
	if c.WaitMs <= 0 {
		if c.Interval > 0 {
			c.WaitMs = c.Interval * 1000
		} else {
			c.WaitMs = 500
		}
	}
	if c.WaitMs < 200 {
		c.WaitMs = 200
	}
	if c.WaitMs > 60000 {
		c.WaitMs = 60000
	}
	if c.AttemptMs <= 0 {
		c.AttemptMs = 300
	}
	if c.AttemptMs < 50 {
		c.AttemptMs = 50
	}
	if c.AttemptMs > 10000 {
		c.AttemptMs = 10000
	}
	c.Interval = 0
	// 冷却允许填 0: 表示被限流后不做等待, 立即重试 (用户自担风险)。
	if c.Cooldown < 0 {
		c.Cooldown = 0
	}
	if c.Cooldown > 3600 {
		c.Cooldown = 3600
	}
	// 拟人化等待区间: 默认 300~800ms, 允许用户改; 越界或反了就纠正。
	if c.HumanizeMinMs <= 0 {
		c.HumanizeMinMs = 900
	}
	if c.HumanizeMaxMs <= 0 {
		c.HumanizeMaxMs = 1200
	}
	if c.HumanizeMinMs > 30000 {
		c.HumanizeMinMs = 30000
	}
	if c.HumanizeMaxMs > 30000 {
		c.HumanizeMaxMs = 30000
	}
	if c.HumanizeMaxMs < c.HumanizeMinMs {
		c.HumanizeMinMs, c.HumanizeMaxMs = c.HumanizeMaxMs, c.HumanizeMinMs
	}
	if c.Theme != "light" {
		c.Theme = "dark"
	}
}

// LoadSession 返回会话缓存。
func (s *Store) LoadSession() (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.st.Session == nil || len(s.st.Session.Cookies) == 0 {
		return Session{}, false
	}
	return *s.st.Session, true
}

// SaveSession 写入会话缓存。
func (s *Store) SaveSession(sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess.SavedAt = time.Now()
	s.st.Session = &sess
	return nil
}

// ClearSession 清除会话缓存。
func (s *Store) ClearSession() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.st.Session = nil
}

// Snapshot 把整份状态序列化, 交给前端存进 localStorage。
func (s *Store) Snapshot() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(s.st)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Restore 用前端交回的快照恢复状态。空数据视为「没有存过」, 直接返回。
func (s *Store) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	// 从默认值开始解码: 快照里缺失的字段保持默认, 而不是零值。
	st := State{Version: Version, Config: Default()}
	if err := json.Unmarshal(data, &st); err != nil {
		return errors.New("本地登录数据已损坏, 请重新登录一次")
	}
	if st.Version > Version {
		return errors.New("本地登录数据来自更新的版本, 请升级程序")
	}
	// v1 的快照里 humanizeDelay 还是"默认关闭 + 300~800ms"。既然现在默认改成开启,
	// 升级时统一迁移一次, 否则老用户永远拿不到新默认值 (他们从没手动改过这几项)。
	if st.Version < 2 {
		st.Config.HumanizeDelay = true
		st.Config.HumanizeMinMs = 900
		st.Config.HumanizeMaxMs = 1200
	}
	st.Version = Version
	st.Config.normalize()
	if st.Session != nil && len(st.Session.Cookies) == 0 {
		st.Session = nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = st
	return nil
}

// ImportLegacy 一次性导入旧版本写在磁盘上的 config.json / session.json。
//
// 迁移到 localStorage 之后这两个文件就不再被使用了; 这里只是把里面的账号与
// 会话捞出来, 交给前端存成快照, 不会删除它们 (需要的话手动清掉即可)。
func (s *Store) ImportLegacy() (bool, error) {
	dir := LegacyDir()
	if dir == "" {
		return false, nil
	}

	st := State{Version: Version, Config: Default()}
	found := false
	if data, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		if err := json.Unmarshal(data, &st.Config); err == nil {
			found = true
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "session.json")); err == nil {
		var sess Session
		if err := json.Unmarshal(data, &sess); err == nil && len(sess.Cookies) > 0 {
			st.Session = &sess
			found = true
		}
	}
	if !found {
		return false, nil
	}

	st.Config.normalize()
	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
	return true, nil
}

// LegacyDir 返回旧版本的数据目录, 仅用于一次性导入。
func LegacyDir() string {
	// 覆盖项: 老版本在容器里用挂载卷保存配置与会话, 例如 SEUSZJZ_DATA_DIR=/data。
	if dir := os.Getenv("SEUSZJZ_DATA_DIR"); dir != "" {
		return dir
	}
	if base := os.Getenv("APPDATA"); base != "" {
		return filepath.Join(base, "seuszjz")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "seuszjz")
	}
	return ""
}

// 明文密码只做混淆, 避免直接出现在 localStorage 里 (不是安全边界)。
//
// 密钥必须是固定值: 快照存的是浏览器而不是这台机器, 用机器名派生会让换机、
// 换容器之后解不开密码。
var currentKey = sha256.Sum256([]byte("seuszjz|local-store|seu-lecture|v2"))

// legacyKey 是旧版本(存文件时)用的机器绑定密钥, 只用来解开老数据。
func legacyKey() []byte {
	seed := "seuszjz|" + os.Getenv("COMPUTERNAME") + "|" + os.Getenv("USERNAME") + "|seu-lecture"
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

func seal(plain string) (string, error) {
	out, err := encrypt(currentKey[:], plain)
	if err != nil {
		return "", err
	}
	return "v1:" + out, nil
}

func open(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if err != nil {
		return "", err
	}
	keys := [][]byte{currentKey[:], legacyKey()}
	for _, key := range keys {
		if plain, err := decrypt(key, raw); err == nil {
			return plain, nil
		}
	}
	return "", errors.New("密码无法解密, 请重新输入")
}

func encrypt(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func decrypt(key, raw []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("密文长度异常")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
