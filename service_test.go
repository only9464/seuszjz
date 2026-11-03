package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"seuszjz/internal/seu"
	"seuszjz/internal/store"
)

// newTestCore 建一个不碰真实旧配置的运行时 (app 为 nil, emit 直接返回)。
func newTestCore(t *testing.T) *core {
	t.Helper()
	t.Setenv("SEUSZJZ_DATA_DIR", t.TempDir())
	return newCore(nil)
}

func sampleSnapshot(t *testing.T) string {
	t.Helper()
	src := store.New()
	err := src.Save(store.Config{
		Username:    "220000001",
		Password:    "s3cret",
		Remember:    true,
		AutoLogin:   true,
		AutoCaptcha: true,
		WaitMs:      7000,
		AttemptMs:   250,
		Cooldown:    60,
		Theme:       "light",
	})
	if err != nil {
		t.Fatalf("保存配置: %v", err)
	}
	err = src.SaveSession(store.Session{
		Username: "220000001",
		Cookies:  []seu.SavedCookie{{Host: "ehall.seu.edu.cn", Path: "/", Name: "JSESSIONID", Value: "abc"}},
		User:     seu.UserInfo{Username: "220000001", DisplayName: "张三"},
	})
	if err != nil {
		t.Fatalf("保存会话: %v", err)
	}
	blob, err := src.Snapshot()
	if err != nil {
		t.Fatalf("生成快照: %v", err)
	}
	return blob
}

func TestBootstrapWithoutSavedState(t *testing.T) {
	svc := NewSessionService(newTestCore(t))

	boot, err := svc.Bootstrap("")
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if boot.Status.HasSession || boot.Status.LoggedIn || boot.Settings.HasPassword {
		t.Fatalf("没有快照时不应有登录信息: %+v", boot.Status)
	}
	if !boot.Settings.AutoLogin || boot.Settings.WaitMs != store.Default().WaitMs || boot.Settings.Theme != "dark" {
		t.Fatalf("默认设置不正确: %+v", boot.Settings)
	}

	var st store.State
	if err := json.Unmarshal([]byte(boot.State), &st); err != nil {
		t.Fatalf("返回的快照不是合法 JSON: %v (%q)", err, boot.State)
	}
	if st.Version != store.Version || st.Session != nil {
		t.Fatalf("空快照内容不正确: %+v", st)
	}
}

func TestBootstrapRestoresSnapshot(t *testing.T) {
	c := newTestCore(t)
	svc := NewSessionService(c)
	blob := sampleSnapshot(t)

	boot, err := svc.Bootstrap(blob)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !boot.Status.HasSession || boot.Status.SavedUsername != "220000001" {
		t.Fatalf("会话没恢复: %+v", boot.Status)
	}
	if boot.Status.User.DisplayName != "张三" {
		t.Fatalf("用户信息没恢复: %+v", boot.Status.User)
	}
	if boot.Settings.WaitMs != 7000 || boot.Settings.Cooldown != 60 || boot.Settings.Theme != "light" || !boot.Settings.HasPassword {
		t.Fatalf("设置没恢复: %+v", boot.Settings)
	}

	// 恢复出来的密码要能直接用于自动重登。
	c.stateMu.RLock()
	pass, username := c.pass, c.cfg.Username
	c.stateMu.RUnlock()
	if username != "220000001" || pass != "s3cret" {
		t.Fatalf("密码/学号没恢复: username=%q pass=%q", username, pass)
	}

	// 前端拿到的快照要能原样再交回来 (幂等)。
	if boot.State == "" {
		t.Fatal("Bootstrap 应该把快照交回给前端")
	}
	again, err := svc.Bootstrap(boot.State)
	if err != nil {
		t.Fatalf("再次 Bootstrap: %v", err)
	}
	if !again.Status.HasSession || again.Settings.WaitMs != 7000 {
		t.Fatalf("快照不能再交回: %+v %+v", again.Status, again.Settings)
	}
}

func TestBootstrapIgnoresSnapshotWhenLoggedIn(t *testing.T) {
	c := newTestCore(t)
	svc := NewSessionService(c)

	// 模拟已经登录: 内存里有活跃会话时, 别被浏览器交回的旧快照覆盖。
	c.stateMu.Lock()
	c.sess = seu.NewSession(seu.Config{Username: "live"})
	c.stateMu.Unlock()

	boot, err := svc.Bootstrap(sampleSnapshot(t))
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if boot.Status.SavedUsername != "" || boot.Settings.WaitMs != store.Default().WaitMs || boot.Status.HasSession {
		t.Fatalf("登录后不应再采纳快照: %+v %+v", boot.Status, boot.Settings)
	}
}

func TestBootstrapImportsLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEUSZJZ_DATA_DIR", dir)

	cfg, err := json.Marshal(store.Config{Username: "220000001", Remember: true, WaitMs: 9000, AttemptMs: 200})
	if err != nil {
		t.Fatalf("序列化配置: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	sess, err := json.Marshal(store.Session{
		Username: "220000001",
		Cookies:  []seu.SavedCookie{{Host: "ehall.seu.edu.cn", Path: "/", Name: "JSESSIONID", Value: "abc"}},
	})
	if err != nil {
		t.Fatalf("序列化会话: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), sess, 0o600); err != nil {
		t.Fatalf("写会话: %v", err)
	}

	svc := NewSessionService(newCore(nil))
	boot, err := svc.Bootstrap("")
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if boot.Status.SavedUsername != "220000001" || !boot.Status.HasSession || boot.Settings.WaitMs != 9000 {
		t.Fatalf("旧版配置没导进来: %+v %+v", boot.Status, boot.Settings)
	}
	var st store.State
	if err := json.Unmarshal([]byte(boot.State), &st); err != nil {
		t.Fatalf("导出的快照不合法: %v", err)
	}
	if st.Session == nil || len(st.Session.Cookies) == 0 {
		t.Fatal("导出的快照里应该带上 Cookie, 好让前端存进 localStorage")
	}
	// 只读不写: 旧文件保持原样, 不删除。
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("不应该动旧文件: %v", err)
	}
}
