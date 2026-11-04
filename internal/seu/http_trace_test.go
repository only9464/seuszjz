package seu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestHTTPTraceLogsFailure 检查协议级日志会记录状态码、耗时和重定向目标 ——
// 这正是排查「抢座时被踢 / 被拦截 / 限流」的关键证据。
func TestHTTPTraceLogsFailure(t *testing.T) {
	var (
		mu    sync.Mutex
		lines []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Tengine")
		w.Header().Set("X-Cache", "MISS")
		http.Redirect(w, r, "https://auth.seu.edu.cn/dist/", http.StatusFound)
	}))
	defer srv.Close()

	sess := NewSession(Config{
		Debug: true,
		Logf: func(format string, args ...any) {
			mu.Lock()
			lines = append(lines, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	})

	if _, err := sess.do(context.Background(), request{method: "POST", target: srv.URL + "/yySave.do"}); err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()

	for _, want := range []string{
		"POST ",
		"-> 302 ",
		"ms", // 耗时
		"location=\"https://auth.seu.edu.cn/dist/\"",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("协议日志缺少 %q:\n%s", want, joined)
		}
	}
}

// TestSetDebugTogglesLogging 检查运行中可以随时打开/关闭协议日志。
func TestSetDebugTogglesLogging(t *testing.T) {
	var (
		mu    sync.Mutex
		count int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	sess := NewSession(Config{
		Debug: false,
		Logf: func(format string, args ...any) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	})

	if _, err := sess.do(context.Background(), request{method: "GET", target: srv.URL + "/a.do"}); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if got := readCount(&mu, &count); got != 0 {
		t.Fatalf("未打开追踪时不该有日志, 实际 %d 行", got)
	}

	sess.SetDebug(true)
	if _, err := sess.do(context.Background(), request{method: "GET", target: srv.URL + "/b.do"}); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if got := readCount(&mu, &count); got == 0 {
		t.Fatal("打开追踪后应当有日志")
	}
}

func readCount(mu *sync.Mutex, count *int) int {
	mu.Lock()
	defer mu.Unlock()
	return *count
}

// TestCallAPISessionExpiredCarriesEvidence 检查会话失效错误里带上了状态码与重定向目标。
func TestCallAPISessionExpiredCarriesEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://auth.seu.edu.cn/dist/", http.StatusFound)
	}))
	defer srv.Close()

	sess := NewSession(Config{})
	data, err := sess.callAPI(context.Background(), request{method: "POST", target: srv.URL + "/yySave.do"})
	if err == nil {
		t.Fatalf("应当返回会话失效, 实际拿到 %v", data)
	}
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("错误类型不对: %v", err)
	}
	for _, want := range []string{"HTTP 302", "auth.seu.edu.cn/dist/"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息缺少 %q: %v", want, err)
		}
	}
}

// TestCallAPI503IsTransient 检查 503 被判定为「临时故障」而不是「会话失效」——
// 2026-11-04 那次抢座失败就是 yySave.do 返回 503 被误判导致去重新登录。
func TestCallAPI503IsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html><title>Error</title>An error occurred.</html>"))
	}))
	defer srv.Close()

	sess := NewSession(Config{})
	_, err := sess.callAPI(context.Background(), request{method: "POST", target: srv.URL + "/yySave.do"})
	if err == nil {
		t.Fatal("应当返回错误")
	}
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("503 应当判为临时故障, 实际: %v", err)
	}
	if errors.Is(err, ErrSessionExpired) {
		t.Fatalf("503 不该判为会话失效: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("错误信息应带上状态码: %v", err)
	}
	if !strings.Contains(err.Error(), "Server=nginx") || !strings.Contains(err.Error(), "Retry-After=2") {
		t.Fatalf("错误信息应带上响应头: %v", err)
	}
}

// TestLoginKeepsTrustCookies 检查密码登录不会清掉 Cookie 罐里的「可信设备」Cookie。
func TestLoginKeepsTrustCookies(t *testing.T) {
	sess := NewSession(Config{})
	sess.LoadCookies([]SavedCookie{
		{Name: "TRUST_DEVICE_abc", Value: "1", Host: "auth.seu.edu.cn"},
		{Name: "GS_SESSIONID", Value: "sid", Host: "ehall.seu.edu.cn"},
	})

	// 模拟一次密码登录的开头: 只清登录态。
	sess.resetState()

	names := map[string]bool{}
	for _, cookie := range sess.Cookies() {
		names[cookie.Name] = true
	}
	if !names["TRUST_DEVICE_abc"] || !names["GS_SESSIONID"] {
		t.Fatalf("登录时不该清掉 Cookie, 实际: %v", names)
	}

	// 退出登录 (Reset) 才真正清空。
	sess.Reset()
	if got := len(sess.Cookies()); got != 0 {
		t.Fatalf("Reset 后 Cookie 应该清空, 实际还有 %d 个", got)
	}
}

// TestTrustCookiesFilter 检查只挑设备信任类 Cookie。
func TestTrustCookiesFilter(t *testing.T) {
	in := []SavedCookie{
		{Name: "TRUST_DEVICE_feef", Host: "auth.seu.edu.cn"},
		{Name: "AGENTMD5_abc", Host: "auth.seu.edu.cn"},
		{Name: "TGT", Host: "auth.seu.edu.cn"},
		{Name: "GS_SESSIONID", Host: "ehall.seu.edu.cn"},
		{Name: "sid", Host: "vpn.seu.edu.cn"},
	}
	out := TrustCookies(in)
	if len(out) != 2 {
		t.Fatalf("应当只保留 2 个信任 Cookie, 实际 %d: %v", len(out), out)
	}
	names := map[string]bool{out[0].Name: true, out[1].Name: true}
	if !names["TRUST_DEVICE_feef"] || !names["AGENTMD5_abc"] {
		t.Fatalf("过滤结果不对: %v", out)
	}
}
