package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"seuszjz/internal/seu"
)

func TestSnapshotRoundTrip(t *testing.T) {
	src := New()
	if err := src.Save(Config{
		Username:    "220000001",
		Password:    "s3cret",
		Remember:    true,
		AutoLogin:   true,
		AutoCaptcha: true,
		WaitMs:      5000,
		AttemptMs:   200,
		Cooldown:    120,
		Theme:       "light",
	}); err != nil {
		t.Fatalf("保存配置: %v", err)
	}
	if err := src.SaveSession(Session{
		Username: "220000001",
		Cookies:  []seu.SavedCookie{{Name: "JSESSIONID", Value: "abc", Host: "ehall.seu.edu.cn", Path: "/"}},
		User:     seu.UserInfo{Username: "220000001", DisplayName: "张三"},
	}); err != nil {
		t.Fatalf("保存会话: %v", err)
	}

	blob, err := src.Snapshot()
	if err != nil {
		t.Fatalf("生成快照: %v", err)
	}

	// 换一台机器: 快照里不能有跟机器绑定的东西。
	t.Setenv("COMPUTERNAME", "OTHER-PC")
	t.Setenv("USERNAME", "someone-else")

	dst := New()
	if err := dst.Restore([]byte(blob)); err != nil {
		t.Fatalf("恢复快照: %v", err)
	}

	cfg := dst.Load()
	if cfg.Username != "220000001" || cfg.WaitMs != 5000 || cfg.AttemptMs != 200 || cfg.Cooldown != 120 || cfg.Theme != "light" {
		t.Fatalf("配置恢复不正确: %+v", cfg)
	}
	if got := dst.Password(cfg); got != "s3cret" {
		t.Fatalf("密码跨机器解不开: %q", got)
	}
	sess, ok := dst.LoadSession()
	if !ok || len(sess.Cookies) != 1 || sess.User.DisplayName != "张三" {
		t.Fatalf("会话恢复不正确: %+v ok=%v", sess, ok)
	}
}

func TestSnapshotOmitsSessionWhenCleared(t *testing.T) {
	s := New()
	if err := s.SaveSession(Session{Username: "u", Cookies: []seu.SavedCookie{{Name: "a", Value: "b"}}}); err != nil {
		t.Fatalf("保存会话: %v", err)
	}
	s.ClearSession()

	blob, err := s.Snapshot()
	if err != nil {
		t.Fatalf("生成快照: %v", err)
	}
	var st State
	if err := json.Unmarshal([]byte(blob), &st); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if st.Session != nil {
		t.Fatalf("清除会话后快照里不应再有 session: %s", blob)
	}
	if _, ok := s.LoadSession(); ok {
		t.Fatal("清除会话后 LoadSession 仍返回数据")
	}
}

func TestRestoreEmptyKeepsDefaults(t *testing.T) {
	s := New()
	if err := s.SaveSession(Session{Username: "u", Cookies: []seu.SavedCookie{{Name: "a", Value: "b"}}}); err != nil {
		t.Fatalf("保存会话: %v", err)
	}
	if err := s.Restore(nil); err != nil {
		t.Fatalf("空快照不应报错: %v", err)
	}
	if !s.Load().AutoLogin || s.Load().WaitMs != Default().WaitMs {
		t.Fatalf("默认配置不正确: %+v", s.Load())
	}
	if _, ok := s.LoadSession(); !ok {
		t.Fatal("空快照不应该清掉已有状态")
	}
}

func TestRestorePartialSnapshotUsesDefaults(t *testing.T) {
	s := New()
	if err := s.Restore([]byte(`{"version":1,"config":{"username":"220000001","remember":true}}`)); err != nil {
		t.Fatalf("恢复快照: %v", err)
	}
	cfg := s.Load()
	if cfg.Username != "220000001" || !cfg.Remember {
		t.Fatalf("字段没恢复: %+v", cfg)
	}
	// 快照里没写的字段应该落到默认值, 而不是零值。
	if !cfg.AutoLogin || !cfg.AutoCaptcha || cfg.WaitMs != Default().WaitMs || cfg.Cooldown != 300 || cfg.Theme != "dark" {
		t.Fatalf("缺失字段没有取默认值: %+v", cfg)
	}
	// 拟人化默认开启, 区间 900~1200ms。
	if !cfg.HumanizeDelay || cfg.HumanizeMinMs != 900 || cfg.HumanizeMaxMs != 1200 {
		t.Fatalf("拟人化默认值不对: %+v", cfg)
	}
}

func TestRestoreRejectsBrokenSnapshot(t *testing.T) {
	s := New()
	if err := s.Restore([]byte(`{not json`)); err == nil {
		t.Fatal("损坏的快照应该报错")
	}
	if err := s.Restore([]byte(`{"version":99,"config":{}}`)); err == nil {
		t.Fatal("更高版本的快照应该报错")
	}
	if cfg := s.Load(); cfg.Username != "" {
		t.Fatalf("报错时不应该改内存里的状态: %+v", cfg)
	}
}

func TestPasswordDroppedWhenNotRemember(t *testing.T) {
	s := New()
	if err := s.Save(Config{Username: "u", Password: "s3cret", Remember: true}); err != nil {
		t.Fatalf("保存配置: %v", err)
	}
	if got := s.Password(s.Load()); got != "s3cret" {
		t.Fatalf("密码没存住: %q", got)
	}
	if err := s.Save(Config{Username: "u", Password: "s3cret", Remember: false}); err != nil {
		t.Fatalf("保存配置: %v", err)
	}
	blob, err := s.Snapshot()
	if err != nil {
		t.Fatalf("生成快照: %v", err)
	}
	if st, _ := s.LoadSession(); st.Username != "" {
		t.Fatal("没有会话时不应返回数据")
	}
	if got := s.Password(s.Load()); got != "" {
		t.Fatalf("关掉记住密码后快照里不该有密码: %q", got)
	}
	if strings.Contains(blob, "s3cret") {
		t.Fatalf("快照里出现了明文密码: %s", blob)
	}
}

func TestOpenAcceptsLegacyMachineKey(t *testing.T) {
	// 旧版本用机器名+用户名派生密钥, 迁移时要能解开。
	t.Setenv("COMPUTERNAME", "OLD-PC")
	t.Setenv("USERNAME", "olduser")
	cipherText, err := encrypt(legacyKey(), "s3cret")
	if err != nil {
		t.Fatalf("加密: %v", err)
	}
	if got := New().Password(Config{Password: "v1:" + cipherText}); got != "s3cret" {
		t.Fatalf("旧版密文解不开: %q", got)
	}
}

func TestImportLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEUSZJZ_DATA_DIR", dir)

	cfg, err := json.Marshal(Config{Username: "220000001", Remember: true, Interval: 7})
	if err != nil {
		t.Fatalf("序列化配置: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	sess, err := json.Marshal(Session{
		Username: "220000001",
		Cookies:  []seu.SavedCookie{{Name: "JSESSIONID", Value: "abc"}},
	})
	if err != nil {
		t.Fatalf("序列化会话: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), sess, 0o600); err != nil {
		t.Fatalf("写会话: %v", err)
	}

	s := New()
	ok, err := s.ImportLegacy()
	if err != nil || !ok {
		t.Fatalf("导入旧文件失败: ok=%v err=%v", ok, err)
	}
	if got := s.Load(); got.Username != "220000001" || got.WaitMs != 7000 {
		t.Fatalf("配置没导进来: %+v", got)
	}
	if got, ok := s.LoadSession(); !ok || len(got.Cookies) != 1 {
		t.Fatalf("会话没导进来: %+v ok=%v", got, ok)
	}
	// 导入只读不写: 旧文件保持原样。
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("不应该删除旧文件: %v", err)
	}

	// 没有旧文件时安静返回。
	t.Setenv("SEUSZJZ_DATA_DIR", t.TempDir())
	ok, err = New().ImportLegacy()
	if err != nil || ok {
		t.Fatalf("没有旧文件时应该返回 false: ok=%v err=%v", ok, err)
	}
}

// TestRestoreMigratesHumanizeDefaults 检查 v1 快照升级到 v2 时, 拟人化会被打开。
func TestRestoreMigratesHumanizeDefaults(t *testing.T) {
	s := New()
	// v1: 拟人化当初默认关闭, 区间 300~800。
	legacy := `{"version":1,"config":{"username":"220000001","humanizeDelay":false,"humanizeMinMs":300,"humanizeMaxMs":800}}`
	if err := s.Restore([]byte(legacy)); err != nil {
		t.Fatalf("恢复 v1 快照: %v", err)
	}
	cfg := s.Load()
	if !cfg.HumanizeDelay || cfg.HumanizeMinMs != 900 || cfg.HumanizeMaxMs != 1200 {
		t.Fatalf("v1 快照没有迁移到新的拟人化默认值: %+v", cfg)
	}
	if cfg.Username != "220000001" {
		t.Fatalf("迁移时不该丢字段: %+v", cfg)
	}

	// v2 快照按用户实际选择, 不能被再次覆盖。
	current := `{"version":2,"config":{"humanizeDelay":false,"humanizeMinMs":500,"humanizeMaxMs":600}}`
	if err := s.Restore([]byte(current)); err != nil {
		t.Fatalf("恢复 v2 快照: %v", err)
	}
	cfg = s.Load()
	if cfg.HumanizeDelay || cfg.HumanizeMinMs != 500 || cfg.HumanizeMaxMs != 600 {
		t.Fatalf("v2 快照不应被迁移覆盖: %+v", cfg)
	}
}
