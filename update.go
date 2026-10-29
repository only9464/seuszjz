package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 更新检查只读 GitHub 上「最新正式发布」的版本号与变更列表：
// 不下载任何东西, 也不会碰学校系统那边的请求。
const (
	releasesAPI  = "https://api.github.com/repos/only9464/seuszjz/releases/latest"
	releasesPage = "https://github.com/only9464/seuszjz/releases/latest"
	// 更新弹窗里最多列多少条变更, 免得一次贴一大篇。
	maxNotes = 12
	// 更新检查的超时: 校园网访问 GitHub 常常很慢, 但也不能让界面等太久。
	updateTimeout = 10 * time.Second
)

var errNoBrowser = errors.New("当前环境不支持打开系统浏览器")

// UpdateInfo 是一次更新检查的结果。
type UpdateInfo struct {
	Current string `json:"current"` // 当前运行版本 (由 -X main.Version 注入)
	Latest  string `json:"latest"`  // GitHub 上最新的正式版本号 (去掉前缀 v)
	// HasUpdate 为 true 表示远端版本比本地新。
	HasUpdate   bool     `json:"hasUpdate"`
	URL         string   `json:"url"`         // 发布页地址
	PublishedAt string   `json:"publishedAt"` // 发布时间 (RFC3339)
	Notes       []string `json:"notes"`       // 只保留「变更」里的功能条目
	CheckedAt   string   `json:"checkedAt"`
	Error       string   `json:"error"` // 非致命错误 (网络不通等), 界面按「检查失败」展示
}

// UpdateService 提供「检查更新」能力。只在用户登录之后由前端主动触发, 不自动下载。
type UpdateService struct{ core *core }

func NewUpdateService(c *core) *UpdateService { return &UpdateService{core: c} }

// Info 返回当前版本号与发布页地址 (不发网络请求), 让界面先有东西可渲染。
func (s *UpdateService) Info() UpdateInfo {
	return UpdateInfo{Current: Version, URL: releasesPage}
}

// OpenReleases 用系统默认浏览器打开发布页; 服务器模式下前端会退回 window.open。
func (s *UpdateService) OpenReleases() error {
	if s.core == nil || s.core.app == nil {
		return errNoBrowser
	}
	return s.core.app.Browser.OpenURL(releasesPage)
}

// releasePayload 是 GitHub Releases API 返回里我们关心的字段。
type releasePayload struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

// Check 请求 GitHub 上的最新正式版, 与本地版本比较。
// 网络层面的失败不算错误: 结果里带 Error 字段, 界面显示「检查失败」即可。
func (s *UpdateService) Check(ctx context.Context) (UpdateInfo, error) {
	out := UpdateInfo{
		Current:   Version,
		URL:       releasesPage,
		CheckedAt: time.Now().Format(time.RFC3339),
	}

	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		out.Error = "请求构造失败: " + err.Error()
		return out, nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "seuszjz/"+Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		out.Error = "暂时连不上 GitHub，稍后再试"
		return out, nil
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		out.Error = "GitHub 接口访问过于频繁，稍后再试"
		return out, nil
	default:
		out.Error = fmt.Sprintf("GitHub 返回 %s", resp.Status)
		return out, nil
	}

	var payload releasePayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		out.Error = "解析发布信息失败"
		return out, nil
	}
	// 草稿与预发布不作为「有更新」的依据。
	if payload.TagName == "" || payload.Draft || payload.Prerelease {
		return out, nil
	}

	out.Latest = strings.TrimPrefix(strings.TrimPrefix(payload.TagName, "v"), "V")
	out.HasUpdate = newerVersion(out.Latest, Version)
	if payload.HTMLURL != "" {
		out.URL = payload.HTMLURL
	}
	out.PublishedAt = payload.PublishedAt
	out.Notes = releaseNotes(payload.Body)
	return out, nil
}

// parseVersion 把 v1.2.3 / 1.2.3-beta.1 解析成 [1,2,3]。
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return out, false
	}
	parts := strings.Split(v, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newerVersion 判断 latest 是否比 current 新; 任一侧解析不出来时一律当作「没有更新」。
//
// 本地构建的版本号是 "dev" (见 service.go 的 Version), 解析不出数字, 所以开发时不会
// 收到更新提示; 正式发布的版本号由 CI 注入 tag 版本 (如 0.0.5), 更新提示正常。
func newerVersion(latest, current string) bool {
	l, okLatest := parseVersion(latest)
	c, okCurrent := parseVersion(current)
	if !okLatest || !okCurrent {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// releaseNotes 从发布说明里只挑功能性的变更条目:
// 下载表格、Docker 镜像命令、一级大标题这些都不进更新弹窗。
func releaseNotes(body string) []string {
	notes := make([]string, 0, maxNotes)
	appendItem := func(line string) bool {
		if len(notes) >= maxNotes {
			return false
		}
		notes = append(notes, line)
		return true
	}

	inChanges := false
	sawChangesHeading := false
	inFence := false

	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
			inChanges = strings.Contains(heading, "变更") ||
				strings.Contains(heading, "更新") ||
				strings.Contains(heading, "chang")
			if inChanges {
				sawChangesHeading = true
			}
			continue
		}
		if inChanges && (strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")) {
			if item := strings.TrimSpace(line[2:]); item != "" {
				if !appendItem(item) {
					break
				}
			}
		}
	}

	// 发布说明没有「变更」小标题时, 退化成收全文里的 bullet (仍跳过表格与代码块)。
	if len(notes) == 0 && !sawChangesHeading {
		inFence = false
		for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				if item := strings.TrimSpace(line[2:]); item != "" {
					if !appendItem(item) {
						break
					}
				}
			}
		}
	}

	return notes
}
