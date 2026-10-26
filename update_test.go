package main

import (
	"context"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		latest  string
		current string
		want    bool
	}{
		{"0.0.4", "0.0.3", true},
		{"v0.0.4", "0.0.3", true},
		{"0.0.3", "0.0.3", false},
		{"0.0.2", "0.0.3", false},
		{"0.1.0", "0.0.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.0.4-beta.1", "0.0.3", true},
		{"", "0.0.3", false},
		{"abc", "0.0.3", false},
		{"0.0.3", "abc", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if v, ok := parseVersion("v1.2.3-beta.1"); !ok || v != [3]int{1, 2, 3} {
		t.Fatalf("parseVersion = %v, %v", v, ok)
	}
	if _, ok := parseVersion("v"); ok {
		t.Fatal("parseVersion(\"v\") 应当失败")
	}
}

func TestReleaseNotes(t *testing.T) {
	body := strings.Join([]string{
		"v0.0.4",
		"",
		"## 下载",
		"",
		"| 文件 | 说明 |",
		"| --- | --- |",
		"| `seuszjz.exe` | Windows 桌面版 |",
		"",
		"## Docker",
		"",
		"```bash",
		"docker run -d big-long-command",
		"```",
		"",
		"## 变更",
		"",
		"- 抢座成功后窗口闪烁提醒",
		"- 修复校园网直连时用户名退化成学号",
		"",
	}, "\n")

	notes := releaseNotes(body)
	want := []string{"抢座成功后窗口闪烁提醒", "修复校园网直连时用户名退化成学号"}
	if len(notes) != len(want) {
		t.Fatalf("releaseNotes = %v, want %v", notes, want)
	}
	for i := range want {
		if notes[i] != want[i] {
			t.Fatalf("notes[%d] = %q, want %q", i, notes[i], want[i])
		}
	}
}

func TestReleaseNotesFallsBackToBullets(t *testing.T) {
	body := "- 第一条\n- 第二条\n\n| 表格 | 行 |\n"
	notes := releaseNotes(body)
	if len(notes) != 2 || notes[0] != "第一条" {
		t.Fatalf("releaseNotes = %v", notes)
	}
}

func TestUpdateServiceInfo(t *testing.T) {
	svc := NewUpdateService(nil)
	info := svc.Info()
	if info.Current != Version {
		t.Fatalf("Current = %q, want %q", info.Current, Version)
	}
	if info.URL == "" {
		t.Fatal("URL 不应为空")
	}
	if info.HasUpdate {
		t.Fatal("Info 不应当声称有更新")
	}
	if err := svc.OpenReleases(); err == nil {
		t.Fatal("没有 app 时 OpenReleases 应当报错")
	}
}

func TestUpdateServiceCheckHandlesOffline(t *testing.T) {
	// 用一个必失败的超时上下文模拟「连不上」, 只验证不会 panic 且带回了当前版本。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	info, err := NewUpdateService(nil).Check(ctx)
	if err != nil {
		t.Fatalf("Check 不应当返回 error: %v", err)
	}
	if info.Current != Version {
		t.Fatalf("Current = %q, want %q", info.Current, Version)
	}
	if info.Error == "" {
		t.Fatal("取消的上下文应当产生 Error")
	}
	if info.HasUpdate {
		t.Fatal("检查失败时不应声称有更新")
	}
}
