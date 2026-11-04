package grab

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"seuszjz/internal/seu"
)

// probeAPI 记录调用次数, 用来观察两种节奏下的请求频率。
type probeAPI struct {
	mu         sync.Mutex
	lecture    seu.Lecture
	reserve    func() seu.Result
	reserveErr func() error
	fetchHook  func()
	activity   int
	reserves   int
}

func (p *probeAPI) Activities(context.Context) ([]seu.Lecture, time.Time, error) {
	p.mu.Lock()
	p.activity++
	lecture := p.lecture
	p.mu.Unlock()
	return []seu.Lecture{lecture}, time.Now(), nil
}

func (p *probeAPI) FetchCaptcha(context.Context) ([]byte, error) {
	p.mu.Lock()
	hook := p.fetchHook
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return []byte("jpeg"), nil
}

func (p *probeAPI) Reserve(context.Context, string, string) (seu.Result, error) {
	p.mu.Lock()
	p.reserves++
	hook := p.reserve
	errHook := p.reserveErr
	p.mu.Unlock()
	if errHook != nil {
		if err := errHook(); err != nil {
			return seu.Result{}, err
		}
	}
	if hook != nil {
		return hook(), nil
	}
	return seu.Result{Success: true, Kind: "ok", Message: "预约成功"}, nil
}

func (p *probeAPI) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activity, p.reserves
}

func started(t *testing.T, api API, opts Options) *Engine {
	t.Helper()
	eng := New()
	opts.Solve = func(context.Context, []byte) (string, error) { return "1234", nil }
	if err := eng.Start(context.Background(), api, opts); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	return eng
}

// TestIntervalsExposed 检查两个间隔都会被透出到状态里, 供界面显示。
func TestIntervalsExposed(t *testing.T) {
	api := &probeAPI{lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusNotOpen}}
	eng := started(t, api, Options{WID: "a", WaitInterval: 250 * time.Millisecond, AttemptInterval: 80 * time.Millisecond})
	defer eng.Stop()

	state := eng.State()
	if state.WaitMs != 250 || state.AttemptMs != 80 {
		t.Fatalf("状态里的间隔不对: waitMs=%d attemptMs=%d", state.WaitMs, state.AttemptMs)
	}
}

// TestDefaultIntervals 检查不传间隔时的默认值 (等开放 1s, 开放后 300ms)。
func TestDefaultIntervals(t *testing.T) {
	api := &probeAPI{lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusEnded}}
	eng := started(t, api, Options{WID: "a"})
	defer eng.Stop()

	state := eng.State()
	if state.WaitMs != 1000 || state.AttemptMs != 300 {
		t.Fatalf("默认间隔不对: waitMs=%d attemptMs=%d", state.WaitMs, state.AttemptMs)
	}
}

// TestWaitPacing 检查「还没开放」时按等待间隔慢轮询, 而不是按提交间隔狂查。
func TestWaitPacing(t *testing.T) {
	api := &probeAPI{lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusNotOpen}}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    200 * time.Millisecond,
		AttemptInterval: 20 * time.Millisecond,
	})
	time.Sleep(650 * time.Millisecond)
	eng.Stop()

	activity, _ := api.counts()
	// 200ms 一轮, 650ms 内应约 3~4 次; 若错用 20ms 的提交间隔会多达 30 次。
	if activity < 2 || activity > 6 {
		t.Fatalf("等待阶段的轮询次数异常: %d 次 (应约 3~4 次)", activity)
	}
}

// TestAttemptPacing 检查「已开放但提交失败」时按提交间隔快速重试。
func TestAttemptPacing(t *testing.T) {
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserve: func() seu.Result {
			return seu.Result{Success: false, Kind: "vcode", Message: "验证码错误"}
		},
	}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    5 * time.Second,
		AttemptInterval: 40 * time.Millisecond,
	})
	time.Sleep(300 * time.Millisecond)
	eng.Stop()

	_, reserves := api.counts()
	// 6 次重试 × 40ms ≈ 240ms, 300ms 内应已完成多次提交。
	if reserves < 3 {
		t.Fatalf("开放后的重试太慢: 只提交了 %d 次", reserves)
	}
}

// TestLogThrottle 检查「状态没变」不会每轮都写日志, 而是合并计数。
func TestLogThrottle(t *testing.T) {
	api := &probeAPI{lecture: seu.Lecture{
		WID: "a", Name: "A", Status: seu.StatusNotOpen,
		BookStartAt: "2030-01-01 12:00:00",
	}}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    20 * time.Millisecond,
		AttemptInterval: 10 * time.Millisecond,
	})
	time.Sleep(400 * time.Millisecond)
	state := eng.State()
	eng.Stop()

	// 20ms 一轮, 400ms 约 15~20 轮; 日志应当远少于轮询次数 (只在第 1 轮与每 10 轮写一次)。
	if state.Round < 8 {
		t.Fatalf("轮询次数太少: %d", state.Round)
	}
	if len(state.Logs) > 6 {
		t.Fatalf("日志节流失效: %d 条日志 / %d 轮轮询", len(state.Logs), state.Round)
	}
	for _, entry := range state.Logs {
		t.Logf("  样例日志: %s [%s]", entry.At, entry.Text)
	}
}

// TestLogAutoClear 检查日志超过阈值会被自动清屏, 并记录丢弃条数。
func TestLogAutoClear(t *testing.T) {
	eng := New()
	for i := 0; i < logsClearAt+50; i++ {
		eng.logf("info", "第 N 条测试日志")
	}
	state := eng.State()
	if state.LogsDropped == 0 {
		t.Fatal("超过阈值后应当自动清屏并记录丢弃条数")
	}
	if len(state.Logs) > maxLogs {
		t.Fatalf("清屏后仍保留 %d 条, 超过上限 %d", len(state.Logs), maxLogs)
	}
	t.Logf("自动清屏后: 保留 %d 条, 丢弃 %d 条", len(state.Logs), state.LogsDropped)
}

// TestClearLogs 检查手动清屏。
func TestClearLogs(t *testing.T) {
	eng := New()
	eng.logf("info", "a")
	eng.logf("info", "b")
	eng.ClearLogs()
	state := eng.State()
	if len(state.Logs) != 0 {
		t.Fatalf("手动清屏后应没有日志, 实际 %d 条", len(state.Logs))
	}
	if state.LogsDropped != 2 {
		t.Fatalf("应记录丢弃 2 条, 实际 %d", state.LogsDropped)
	}
}

// TestZeroCooldown 检查冷却填 0 时不会卡住, 而是立即重试。
func TestZeroCooldown(t *testing.T) {
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserve: func() seu.Result {
			return seu.Result{Success: false, Kind: "limited", Message: "操作频繁"}
		},
	}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    5 * time.Second,
		AttemptInterval: 20 * time.Millisecond,
		Cooldown:        0,
	})
	if got := eng.State().Cooldown; got != 0 {
		eng.Stop()
		t.Fatalf("状态里的冷却应为 0, 实际 %d", got)
	}
	time.Sleep(250 * time.Millisecond)
	eng.Stop()

	_, reserves := api.counts()
	// 冷却为 0 时应立刻重试: 250ms 内至少提交 3 次; 若被当成默认 5 分钟就只会提交 1 次。
	if reserves < 3 {
		t.Fatalf("冷却为 0 时重试太慢: 只提交了 %d 次", reserves)
	}
}

// TestSetTargetWhileRunning 覆盖「运行中切换目标」: 不需要先停止重开。
func TestSetTargetWhileRunning(t *testing.T) {
	api := &probeAPI{lecture: seu.Lecture{
		WID: "a", Name: "讲座A", Status: seu.StatusFull, Booked: 10, Capacity: 10,
	}}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    2 * time.Second, // 让第一轮结束就进入睡眠, 便于稳定断言
		AttemptInterval: time.Second,
	})
	defer eng.Stop()

	if got := eng.State().WID; got != "a" {
		t.Fatalf("初始目标 = %q, 期望 a", got)
	}

	// 切到新目标: 状态立即更新, 无需停止任务。
	if err := eng.SetTarget("b", "讲座B"); err != nil {
		t.Fatalf("SetTarget 失败: %v", err)
	}
	state := eng.State()
	if state.WID != "b" {
		t.Fatalf("切换后 WID = %q, 期望 b", state.WID)
	}
	if !state.Running {
		t.Fatal("切换目标不应终止任务")
	}
	if state.Message != "已切换目标: 讲座B" {
		t.Fatalf("切换后 Message = %q", state.Message)
	}
	found := false
	for _, entry := range state.Logs {
		if strings.Contains(entry.Text, "已切换抢座目标") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("日志里没有切换记录: %+v", state.Logs)
	}

	// 参数校验。
	if err := eng.SetTarget("  ", ""); err == nil {
		t.Fatal("空目标应当报错")
	}
	if err := eng.SetTarget("b", "讲座B"); err != nil {
		t.Fatalf("切到同一目标应当幂等, 实际: %v", err)
	}
}

// TestSetTargetRequiresRunning 覆盖「未运行时报错」。
func TestSetTargetRequiresRunning(t *testing.T) {
	eng := New()
	if err := eng.SetTarget("a", "讲座A"); err == nil {
		t.Fatal("未运行时 SetTarget 应当报错")
	}
}

// TestNowHasMillis 检查日志时间戳精确到毫秒。
func TestNowHasMillis(t *testing.T) {
	pattern := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3}$`)
	if !pattern.MatchString(now()) {
		t.Fatalf("现在的时间戳 = %q, 期望形如 15:04:05.123", now())
	}
}

// TestHumanDelayRange 检查随机等待落在指定区间, 且区间相等/反向时能退化。
func TestHumanDelayRange(t *testing.T) {
	min, max := 300*time.Millisecond, 800*time.Millisecond
	for i := 0; i < 200; i++ {
		d := humanDelay(min, max)
		if d < min || d > max {
			t.Fatalf("humanDelay(%v, %v) = %v, 超出区间", min, max, d)
		}
	}
	if d := humanDelay(500*time.Millisecond, 500*time.Millisecond); d != 500*time.Millisecond {
		t.Fatalf("区间相等时应固定等待, 实际 %v", d)
	}
	// 反向区间应当自动纠正, 仍然落在 [min, max] 内。
	for i := 0; i < 50; i++ {
		d := humanDelay(800*time.Millisecond, 300*time.Millisecond)
		if d < 300*time.Millisecond || d > 800*time.Millisecond {
			t.Fatalf("反向区间没被纠正: %v", d)
		}
	}
}

// TestHumanizeDelaySlowsSubmit 检查开启后「取码 → 提交」之间确实会等。
func TestHumanizeDelaySlowsSubmit(t *testing.T) {
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserve: func() seu.Result { return seu.Result{Success: true, Kind: "ok", Message: "预约成功"} },
	}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    time.Second,
		AttemptInterval: 10 * time.Millisecond,
		HumanizeDelay:   true,
		HumanizeMin:     120 * time.Millisecond,
		HumanizeMax:     140 * time.Millisecond,
	})
	time.Sleep(60 * time.Millisecond)
	// 取码之后应该还在等, 尚未提交。
	if _, reserves := api.counts(); reserves != 0 {
		t.Fatalf("拟人化等待期间不该提交, 实际已提交 %d 次", reserves)
	}
	time.Sleep(200 * time.Millisecond)
	eng.Stop()
	if _, reserves := api.counts(); reserves == 0 {
		t.Fatal("等待结束后应当提交")
	}
}

// TestHumanizeWarmList 检查开启后提交前会多刷一次列表。
func TestHumanizeWarmList(t *testing.T) {
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserve: func() seu.Result { return seu.Result{Success: true, Kind: "ok", Message: "预约成功"} },
	}
	eng := started(t, api, Options{
		WID:              "a",
		WaitInterval:     time.Second,
		AttemptInterval:  10 * time.Millisecond,
		HumanizeWarmList: true,
	})
	time.Sleep(200 * time.Millisecond)
	eng.Stop()

	activities, reserves := api.counts()
	if reserves == 0 {
		t.Fatal("应当至少提交一次")
	}
	// 一次成功的提交 = 1 次轮询 + 1 次「提交前刷新」, 所以列表请求至少 2 次。
	if activities < 2 {
		t.Fatalf("提交前没有额外刷新列表: 列表请求 %d 次", activities)
	}
	found := false
	for _, entry := range eng.State().Logs {
		if strings.Contains(entry.Text, "拟人化: 提交前刷新一次列表") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("日志里没有拟人化刷新记录")
	}
}

// TestAttemptIntervalDelaysFirstSubmit 检查「提交间隔」现在是「发现可预约后等多久再提交」。
func TestAttemptIntervalDelaysFirstSubmit(t *testing.T) {
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserve: func() seu.Result { return seu.Result{Success: true, Kind: "ok", Message: "预约成功"} },
	}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    time.Second,
		AttemptInterval: 150 * time.Millisecond,
	})
	defer eng.Stop()

	time.Sleep(80 * time.Millisecond)
	if _, reserves := api.counts(); reserves != 0 {
		t.Fatalf("发现可预约后应先等 150ms, 实际已经提交 %d 次", reserves)
	}

	time.Sleep(200 * time.Millisecond)
	if _, reserves := api.counts(); reserves == 0 {
		t.Fatal("等待结束后应当提交")
	}

	found := false
	for _, entry := range eng.State().Logs {
		if strings.Contains(entry.Text, "检测到可预约") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("日志里缺少「检测到可预约, 150ms 后开始提交」")
	}
}

// TestHumanizeDelayCoversRecognise 检查拟人化等待会把识别耗时算进去:
// 「取码完成 → 提交」的真实间隔应当贴近目标值, 而不是目标值 + 识别用时。
func TestHumanizeDelayCoversRecognise(t *testing.T) {
	var (
		mu        sync.Mutex
		fetchDone time.Time
		submitAt  time.Time
	)
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserve: func() seu.Result {
			mu.Lock()
			submitAt = time.Now()
			mu.Unlock()
			return seu.Result{Success: true, Kind: "ok", Message: "预约成功"}
		},
	}
	api.fetchHook = func() {
		mu.Lock()
		fetchDone = time.Now()
		mu.Unlock()
	}

	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    time.Second,
		AttemptInterval: 20 * time.Millisecond,
		HumanizeDelay:   true,
		HumanizeMin:     150 * time.Millisecond,
		HumanizeMax:     150 * time.Millisecond, // 固定值, 便于断言
	})
	time.Sleep(600 * time.Millisecond)
	eng.Stop()

	mu.Lock()
	defer mu.Unlock()
	if submitAt.IsZero() {
		for _, entry := range eng.State().Logs {
			t.Logf("LOG %s [%s] %s", entry.At, entry.Level, entry.Text)
		}
		t.Fatalf("没有发生提交, phase=%s message=%s", eng.State().Phase, eng.State().Message)
	}
	gap := submitAt.Sub(fetchDone)
	// 目标 150ms; 上界留 120ms 余量给识别与调度抖动（识别在测试里是瞬时的）。
	if gap < 150*time.Millisecond || gap > 270*time.Millisecond {
		t.Fatalf("取码到提交间隔 = %v, 期望接近 150ms", gap)
	}
}

// TestReserveTransientRetries 检查提交遇到 5xx/429 (ErrRetryable) 时不会结束任务,
// 而是等下一个间隔重试 —— 这正是「开放瞬间 yySave 返回 503」的场景。
func TestReserveTransientRetries(t *testing.T) {
	var (
		calls int
		mu    sync.Mutex
	)
	api := &probeAPI{
		lecture: seu.Lecture{WID: "a", Name: "A", Status: seu.StatusAvailable},
		reserveErr: func() error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls <= 2 {
				return fmt.Errorf("%w: 服务端临时故障 (HTTP 503)", ErrRetryable)
			}
			return nil
		},
	}
	eng := started(t, api, Options{
		WID:             "a",
		WaitInterval:    time.Second,
		AttemptInterval: 30 * time.Millisecond,
	})
	defer eng.Stop()

	time.Sleep(400 * time.Millisecond)

	_, reserves := api.counts()
	if reserves < 3 {
		t.Fatalf("临时故障后应当继续重试, 实际只提交 %d 次", reserves)
	}
	state := eng.State()
	if state.Phase == PhaseFailed {
		t.Fatalf("临时故障不该让任务失败: %s", state.Message)
	}
	warned := false
	for _, entry := range state.Logs {
		if strings.Contains(entry.Text, "提交接口临时故障") {
			warned = true
			break
		}
	}
	if !warned {
		t.Fatal("日志里应有「提交接口临时故障, 稍后重试」")
	}
}
