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

// 真实发布说明（v0.0.3）的结构：下载表格 + Docker 代码块 + 变更列表。
// 更新弹窗里只应该出现「变更」里的条目。
func TestReleaseNotesRealReleaseBody(t *testing.T) {
	body := "东南大学讲座助手 v0.0.3\n\n" +
		"## 下载\n\n" +
		"| 文件 | 说明 |\n" +
		"| --- | --- |\n" +
		"| `seuszjz.exe` | Windows 桌面版，双击即可运行（无控制台窗口） |\n" +
		"| `SHA256SUMS.txt` | 以上文件的 SHA-256 校验和 |\n\n" +
		"## Docker\n\n" +
		"```bash\n" +
		"docker run -d --name seuszjz -p 8080:8080 ghcr.io/only9464/seuszjz:0.0.3\n" +
		"```\n\n" +
		"## 变更\n\n" +
		"- fix(release): 1.1.1 —— 校园网登录修复 + 毕业要求检查\n"

	notes := releaseNotes(body)
	if len(notes) != 1 {
		t.Fatalf("releaseNotes = %v, want 1 条", notes)
	}
	if notes[0] != "fix(release): 1.1.1 —— 校园网登录修复 + 毕业要求检查" {
		t.Fatalf("notes[0] = %q", notes[0])
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
