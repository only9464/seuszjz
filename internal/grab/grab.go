// Package grab 实现「蹲讲座 + 自动预约」的后台循环。
package grab

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"time"

	"seuszjz/internal/seu"
)

// 抢座阶段。
const (
	PhaseIdle       = "idle"
	PhaseStarting   = "starting"
	PhaseWaiting    = "waiting"
	PhaseNeedCode   = "need_code"
	PhaseSubmitting = "submitting"
	PhaseLimited    = "limited"
	PhaseSuccess    = "success"
	PhaseFailed     = "failed"
	PhaseStopped    = "stopped"
)

var codePattern = regexp.MustCompile(`^[0-9A-Za-z]{4}$`)

const (
	// maxLogs 是状态里保留的日志条数 (界面只渲染这些)。
	maxLogs = 400
	// logsClearAt 是自动清屏阈值: 累计超过就整段清掉只留一条标记。
	// 提交间隔可以低到 50ms, 不设上限的话几秒钟就能攒出上万条。
	logsClearAt = 1000
)

// LogEntry 是一条抢座日志。
type LogEntry struct {
	At    string `json:"at"`
	Level string `json:"level"` // info / ok / warn / error
	Text  string `json:"text"`
}

// State 是抢座状态的快照, 会通过事件推送给前端。
type State struct {
	Running      bool       `json:"running"`
	WID          string     `json:"wid"`
	Name         string     `json:"name"`
	Phase        string     `json:"phase"`
	Message      string     `json:"message"`
	Round        int        `json:"round"` // 已经轮询了多少轮
	Attempts     int        `json:"attempts"`
	LogsDropped  int        `json:"logsDropped"` // 因刷屏被自动清掉的日志条数
	CaptchaCount int        `json:"captchaCount"`
	Captcha      string     `json:"captcha"` // data:image/jpeg;base64,... 需要手动输入时展示
	CaptchaAt    string     `json:"captchaAt"`
	StartedAt    string     `json:"startedAt"`
	UpdatedAt    string     `json:"updatedAt"`
	WaitMs       int        `json:"waitMs"`    // 等待开放时的轮询间隔 (毫秒)
	AttemptMs    int        `json:"attemptMs"` // 开放后两次提交的间隔 (毫秒)
	Cooldown     int        `json:"cooldown"`
	AutoCaptcha  bool       `json:"autoCaptcha"`
	Success      bool       `json:"success"`
	Logs         []LogEntry `json:"logs"`
}

// API 是抢座循环依赖的远端能力。
type API interface {
	Activities(ctx context.Context) ([]seu.Lecture, time.Time, error)
	FetchCaptcha(ctx context.Context) ([]byte, error)
	Reserve(ctx context.Context, wid, code string) (seu.Result, error)
}

// Options 是启动参数。
type Options struct {
	WID string
	// WaitInterval 是等待开放 / 等退订时的轮询间隔 (慢节奏)。
	WaitInterval time.Duration
	// AttemptInterval 是已经可以预约时, 两次提交之间的间隔 (快节奏)。
	AttemptInterval time.Duration
	Cooldown        time.Duration
	// HumanizeDelay 为真时, 取完验证码到提交之间随机等 [HumanizeMin, HumanizeMax],
	// 让「取码 → 提交」的间隔看起来更像人工输入 (默认关闭)。
	HumanizeDelay bool
	// HumanizeMin / HumanizeMax 是上面那个随机等待的区间, 由用户在设置里填 (毫秒)。
	HumanizeMin time.Duration
	HumanizeMax time.Duration
	// HumanizeWarmList 为真时, 提交前先刷新一次讲座列表, 模拟前端「进页面再提交」的请求序列
	// (默认关闭)。
	HumanizeWarmList bool
	// Solve 为 nil 表示只能手动输入验证码。
	Solve   func(ctx context.Context, jpeg []byte) (string, error)
	OnState func(State)
	OnDone  func(State)
	// OnLog 在每条日志写入内存后回调一次 (可为 nil), 供上层落盘做持久化日志。
	OnLog func(LogEntry)
}

// Engine 是抢座循环。
type Engine struct {
	mu     sync.Mutex
	api    API
	state  State
	cancel context.CancelFunc
	done   chan struct{}
	manual chan string
	reload chan struct{}
	opts   Options

	// lastStatus / sameCount 用于把「连续多轮状态没变」合并成一条日志。
	lastStatus string
	sameCount  int
	// logged 是本次任务累计写入的日志条数, 用于按累计量触发自动清屏
	// (只看当前长度的话, 环形截断会让长度永远到不了阈值)。
	logged int
}

// New 创建一个引擎。
func New() *Engine {
	return &Engine{state: State{Phase: PhaseIdle, Logs: []LogEntry{}}}
}

// State 返回当前状态快照。
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cloneLocked()
}

func (e *Engine) cloneLocked() State {
	out := e.state
	logs := make([]LogEntry, len(e.state.Logs))
	copy(logs, e.state.Logs)
	out.Logs = logs
	return out
}

// Running 表示循环是否在跑。
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Running
}

// Start 启动抢座循环; 已有循环在跑时返回错误。
func (e *Engine) Start(ctx context.Context, api API, opts Options) error {
	e.mu.Lock()
	if e.state.Running {
		e.mu.Unlock()
		return errors.New("抢座任务已在运行")
	}
	if opts.WaitInterval <= 0 {
		opts.WaitInterval = time.Second
	}
	if opts.AttemptInterval <= 0 {
		opts.AttemptInterval = 300 * time.Millisecond
	}
	if opts.Cooldown < 0 {
		opts.Cooldown = 0 // 0 表示「被限流后不冷却」, 由用户显式选择
	}
	e.api = api
	e.opts = opts
	e.lastStatus = ""
	e.sameCount = 0
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	e.manual = make(chan string, 4)
	e.reload = make(chan struct{}, 4)
	e.state = State{
		Running:     true,
		WID:         opts.WID,
		Phase:       PhaseStarting,
		Message:     "正在启动",
		StartedAt:   now(),
		UpdatedAt:   now(),
		WaitMs:      int(opts.WaitInterval / time.Millisecond),
		AttemptMs:   int(opts.AttemptInterval / time.Millisecond),
		Cooldown:    int(opts.Cooldown / time.Second),
		AutoCaptcha: opts.Solve != nil,
		Logs:        []LogEntry{},
	}
	snapshot := e.cloneLocked()
	e.mu.Unlock()

	e.emit(snapshot)
	go e.run(runCtx, e.done)
	return nil
}

// Stop 停止循环并等待 goroutine 退出。
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	// 清空句柄，避免重复 Stop 又等一次；run 里的 defer 已捕获 done，关闭不受影响。
	e.cancel, e.done = nil, nil
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// SubmitCode 提交手动输入的验证码。
func (e *Engine) SubmitCode(code string) error {
	code = strings.TrimSpace(code)
	if !codePattern.MatchString(code) {
		return errors.New("请输入 4 位验证码")
	}
	select {
	case e.manual <- code:
		return nil
	default:
		return errors.New("当前不需要输入验证码")
	}
}

// SetTarget 在运行中切换抢座目标: 下一轮轮询就会换成新的讲座, 不需要先停止。
//
// 轮询循环每轮都用 currentWID() 重新定位目标, 所以只改 State.WID 即可;
// 同时重置状态行去重计数, 免得新目标沿用上一个目标的「已连续 N 轮」。
// name 只用于日志和界面展示 (调用方从讲座列表里取), 可以为空。
func (e *Engine) SetTarget(wid, name string) error {
	wid = strings.TrimSpace(wid)
	if wid == "" {
		return errors.New("请先选择讲座")
	}

	e.mu.Lock()
	if !e.state.Running {
		e.mu.Unlock()
		return errors.New("抢座未在运行")
	}
	if e.state.WID == wid {
		e.mu.Unlock()
		return nil
	}
	name = strings.TrimSpace(name)
	e.state.WID = wid
	e.state.Name = name
	e.state.Phase = PhaseWaiting
	e.state.Captcha = ""
	e.state.CaptchaAt = ""
	e.lastStatus = ""
	e.sameCount = 0
	if name != "" {
		e.state.Message = "已切换目标: " + name
	} else {
		e.state.Message = "已切换目标, 等待下一轮轮询"
	}
	e.state.UpdatedAt = now()
	snapshot := e.cloneLocked()
	e.mu.Unlock()

	e.emit(snapshot)
	if name != "" {
		e.logf("info", "已切换抢座目标: "+name)
	} else {
		e.logf("info", "已切换抢座目标")
	}
	return nil
}

// RefreshCaptcha 让正在等待的循环换一张验证码。
func (e *Engine) RefreshCaptcha() {
	select {
	case e.reload <- struct{}{}:
	default:
	}
}

func (e *Engine) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	defer func() {
		e.mu.Lock()
		e.state.Running = false
		if e.state.Phase != PhaseSuccess && e.state.Phase != PhaseFailed {
			e.state.Phase = PhaseStopped
		}
		if e.state.Phase == PhaseStopped {
			e.state.Message = "已停止"
		}
		e.state.Captcha = ""
		e.state.UpdatedAt = now()
		snapshot := e.cloneLocked()
		e.mu.Unlock()
		e.emit(snapshot)
		if e.opts.OnDone != nil {
			e.opts.OnDone(snapshot)
		}
	}()

	e.logf("info", "开始蹲守目标讲座")
	for {
		if ctx.Err() != nil {
			return
		}
		stop, err := e.round(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			e.fail(err.Error())
			return
		}
		if stop {
			return
		}
	}
}

// round 执行一轮检查, 返回是否结束。
func (e *Engine) round(ctx context.Context) (bool, error) {
	e.update(func(s *State) { s.Round++ })
	activities, clock, err := e.api.Activities(ctx)
	if err != nil {
		return false, fmt.Errorf("读取讲座列表失败: %w", err)
	}
	wid := e.currentWID()
	var target *seu.Lecture
	for i := range activities {
		if activities[i].WID == wid {
			target = &activities[i]
			break
		}
	}
	if target == nil {
		return true, errors.New("目标讲座已从列表消失 (可能已下架)")
	}
	e.setName(target.Name)

	if target.Joined {
		e.succeed(target, "已经预约过该讲座")
		return true, nil
	}

	switch target.Status {
	case seu.StatusEnded, seu.StatusCancelled:
		return true, errors.New("讲座已结束或已取消")
	case seu.StatusFull, seu.StatusNotOpen:
		text := fmt.Sprintf("名额已满 %d/%d, 等待退订", target.Booked, target.Capacity)
		if target.Status == seu.StatusNotOpen {
			text = "尚未开放预约, 等待中"
			if start, ok := seu.ParseTime(target.BookStartAt); ok {
				if left := start.Sub(clock); left > 0 {
					text = fmt.Sprintf("尚未开放 · 预约 %s 开放 · 还有 %s",
						target.BookStartAt, shortDuration(left))
				}
			}
		}
		e.logStatus(text)
		e.update(func(s *State) {
			s.Phase = PhaseWaiting
			s.Message = text
			s.Captcha = ""
		})
		return false, e.sleep(ctx, e.opts.WaitInterval)
	case seu.StatusAvailable:
		return e.attempt(ctx, target)
	default:
		// 其它状态 (已预约/已打卡等) 视作无需再抢
		e.succeed(target, "当前状态无需继续抢座: "+target.Status)
		return true, nil
	}
}

func (e *Engine) attempt(ctx context.Context, target *seu.Lecture) (bool, error) {
	e.update(func(s *State) {
		s.Phase = PhaseSubmitting
		s.Message = "检测到可预约, 正在提交"
		s.Attempts++
	})

	for try := 0; try < 6; try++ {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		// 「提交间隔」现在表示「轮询发现可预约之后, 等多久再发出第一次提交」,
		// 同时也作为后续重试之间的间隔 —— 先等再动手, 避免开放瞬间零延迟连发。
		if try == 0 && e.opts.AttemptInterval > 0 {
			e.logf("info", fmt.Sprintf("检测到可预约, %dms 后开始提交", e.opts.AttemptInterval.Milliseconds()))
		}
		if err := e.sleep(ctx, e.opts.AttemptInterval); err != nil {
			return true, err
		}
		fetchAt := time.Now()
		jpeg, err := e.api.FetchCaptcha(ctx)
		if err != nil {
			return false, fmt.Errorf("获取验证码失败: %w", err)
		}
		fetchMs := time.Since(fetchAt).Milliseconds()
		e.update(func(s *State) {
			s.CaptchaCount++
		})

		// 拟人化目标间隔从「取码完成」开始算: 识别、手动输入、刷新列表花掉的时间都算在内,
		// 最后只补足剩余部分 —— 这样「取码 → 提交」的真实间隔才等于用户设置的区间。
		readyAt := time.Now()
		humanTarget := time.Duration(0)
		if e.opts.HumanizeDelay {
			humanTarget = humanDelay(e.opts.HumanizeMin, e.opts.HumanizeMax)
		}

		code := ""
		solveMs := int64(0)
		if e.opts.Solve != nil {
			solveAt := time.Now()
			solved, solveErr := e.opts.Solve(ctx, jpeg)
			solveMs = time.Since(solveAt).Milliseconds()
			if solveErr == nil && codePattern.MatchString(solved) {
				code = solved
			} else if solveErr != nil {
				e.logf("warn", "自动识别失败: "+solveErr.Error())
			} else {
				e.logf("warn", "自动识别结果无效: "+solved)
			}
		}

		if code == "" {
			manual, err := e.waitManual(ctx, jpeg)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return true, err
				}
				if errors.Is(err, errReload) {
					continue
				}
				if errors.Is(err, errExpired) {
					e.logf("warn", "验证码等待超时, 重新获取")
					continue
				}
				return false, err
			}
			code = manual
		}

		// 拟人化 (默认关闭): 提交前先刷新一次列表, 模拟前端「进页面 → 取码 → 提交」的请求序列。
		if e.opts.HumanizeWarmList {
			warmAt := time.Now()
			if _, _, err := e.api.Activities(ctx); err != nil {
				e.logf("warn", "拟人化: 提交前刷新列表失败 ("+err.Error()+"), 继续提交")
			} else {
				e.logf("info", fmt.Sprintf("拟人化: 提交前刷新一次列表, 耗时 %dms",
					time.Since(warmAt).Milliseconds()))
			}
		}
		// 拟人化 (默认关闭): 补足到目标间隔再提交, 不会因为识别耗时把间隔拉长。
		if e.opts.HumanizeDelay {
			used := time.Since(readyAt)
			if rest := humanTarget - used; rest > 0 {
				e.logf("info", fmt.Sprintf("拟人化: 取码后已用 %dms, 再补 %dms (目标 %dms)",
					used.Milliseconds(), rest.Milliseconds(), humanTarget.Milliseconds()))
				if err := e.sleep(ctx, rest); err != nil {
					return true, err
				}
			} else {
				e.logf("info", fmt.Sprintf("拟人化: 取码后已用 %dms, 超过目标 %dms, 直接提交",
					used.Milliseconds(), humanTarget.Milliseconds()))
			}
		}

		submitAt := time.Now()
		result, err := e.api.Reserve(ctx, target.WID, code)
		if err != nil {
			if errors.Is(err, ErrRetryable) {
				// 服务端临时故障 (开抢峰值最常见的 503): 会话还在, 别去重登,
				// 等下一个间隔再取码重试即可。
				e.logf("warn", "提交接口临时故障, 稍后重试: "+err.Error())
				continue
			}
			return false, fmt.Errorf("提交预约失败: %w", err)
		}
		submitMs := time.Since(submitAt).Milliseconds()

		// 每次尝试用一行摘要代替原来多条零散日志; 前 3 次都记, 之后每 3 次记一条,
		// 非「验证码错误」的结论 (成功/已满/限流/关闭) 一律记。
		recognized := "手动"
		if code != "" {
			recognized = code
		}
		if solveMs > 0 {
			recognized = fmt.Sprintf("%s/%dms", recognized, solveMs)
		}
		if try < 3 || try%3 == 0 || result.Kind != "vcode" {
			e.logf(levelFor(result), fmt.Sprintf(
				"第 %d 次尝试：取码 %dms · 识别 %s · 提交 %dms → %s",
				try+1, fetchMs, recognized, submitMs, result.Message,
			))
		}

		switch result.Kind {
		case "ok":
			if result.Success {
				e.succeed(target, "预约成功")
				return true, nil
			}
		case "limited":
			e.update(func(s *State) {
				s.Phase = PhaseLimited
				s.Message = "请求过于频繁, 冷却中"
				s.Captcha = ""
			})
			if e.opts.Cooldown <= 0 {
				e.logf("warn", "触发限流, 冷却已设为 0, 立即重试")
				return false, nil
			}
			e.logf("warn", fmt.Sprintf("触发限流, 冷却 %s", e.opts.Cooldown))
			return false, e.sleep(ctx, e.opts.Cooldown)
		case "vcode":
			e.update(func(s *State) { s.Captcha = "" })
			continue
		case "full":
			e.update(func(s *State) {
				s.Phase = PhaseWaiting
				s.Message = "名额已满, 继续等待"
				s.Captcha = ""
			})
			return false, e.sleep(ctx, e.opts.WaitInterval)
		case "duplicated":
			e.succeed(target, result.Message)
			return true, nil
		case "closed":
			return true, fmt.Errorf("预约通道已关闭: %s", result.Message)
		default:
			if result.Success {
				e.succeed(target, result.Message)
				return true, nil
			}
			if result.Kind == "other" && strings.Contains(result.Message, "验证码") {
				continue
			}
			return false, e.sleep(ctx, e.opts.AttemptInterval)
		}
	}

	e.update(func(s *State) {
		s.Phase = PhaseWaiting
		s.Message = "多次提交未成功, 继续等待"
		s.Captcha = ""
	})
	return false, e.sleep(ctx, e.opts.WaitInterval)
}

var (
	errExpired = errors.New("验证码过期")
	errReload  = errors.New("刷新验证码")
	// ErrRetryable 表示这次提交遇到服务端临时故障 (5xx / 429), 会话没坏,
	// 等一个「发现后等待」再取码重试即可, 不该把整个抢座任务判死。
	ErrRetryable = errors.New("提交接口临时故障")
)

// waitManual 把验证码展示给用户并等待输入。
func (e *Engine) waitManual(ctx context.Context, jpeg []byte) (string, error) {
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)
	e.update(func(s *State) {
		s.Phase = PhaseNeedCode
		s.Message = "请输入图片中的验证码"
		s.Captcha = dataURL
		s.CaptchaAt = now()
	})

	timeout := time.NewTimer(55 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-e.reload:
			return "", errReload
		case <-timeout.C:
			e.update(func(s *State) { s.Captcha = "" })
			return "", errExpired
		case code := <-e.manual:
			e.update(func(s *State) {
				s.Captcha = ""
				s.Phase = PhaseSubmitting
				s.Message = "正在提交验证码 " + code
			})
			return code, nil
		}
	}
}

func (e *Engine) succeed(target *seu.Lecture, message string) {
	e.update(func(s *State) {
		s.Phase = PhaseSuccess
		s.Success = true
		s.Message = message
		s.Captcha = ""
	})
	e.logf("ok", message+" | "+target.Name)
}

func (e *Engine) fail(message string) {
	e.update(func(s *State) {
		s.Phase = PhaseFailed
		s.Message = message
		s.Captcha = ""
	})
	e.logf("error", message)
}

func (e *Engine) setName(name string) {
	changed := false
	e.update(func(s *State) {
		if s.Name != name {
			s.Name = name
			s.Message = "目标: " + name
			changed = true
		}
	})
	if changed {
		// 目标变了就记一行, 这样日志里能直接看到当前在抢哪一讲。
		e.logf("info", "当前抢座目标: "+name)
	}
}

func (e *Engine) currentWID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.WID
}

func (e *Engine) logf(level, text string) {
	e.mu.Lock()
	e.logged++
	dropped := 0
	logs := append(e.state.Logs, LogEntry{At: now(), Level: level, Text: text})
	switch {
	case e.logged%logsClearAt == 0:
		// 刷屏保护: 每累计 logsClearAt 条就整段清掉, 只留一条标记供界面显示。
		dropped = len(logs)
		logs = []LogEntry{{
			At:    now(),
			Level: "info",
			Text:  fmt.Sprintf("—— 日志已自动清屏（丢弃 %d 条）——", dropped),
		}}
	case len(logs) > maxLogs:
		logs = logs[len(logs)-maxLogs:]
	}
	entry := LogEntry{At: now(), Level: level, Text: text}
	e.state.Logs = logs
	e.state.LogsDropped += dropped
	e.state.UpdatedAt = now()
	snapshot := e.cloneLocked()
	e.mu.Unlock()

	e.emit(snapshot)
	if e.opts.OnLog != nil {
		e.opts.OnLog(entry)
	}
}

// logStatus 记录「这一轮查到的状态」, 做两层节流, 避免几百毫秒一轮时刷屏:
//   - 状态变了: 立刻写一条;
//   - 状态没变: 每 10 轮才合并写一条, 带上持续次数。
func (e *Engine) logStatus(text string) {
	e.mu.Lock()
	changed := e.lastStatus != text
	if changed {
		e.lastStatus = text
		e.sameCount = 1
	} else {
		e.sameCount++
	}
	count := e.sameCount
	e.mu.Unlock()

	if changed {
		e.logf("info", text)
		return
	}
	if count%10 == 0 {
		e.logf("info", fmt.Sprintf("… 状态未变（已连续 %d 轮）：%s", count, text))
	}
}

// ClearLogs 手动清屏。
func (e *Engine) ClearLogs() {
	e.mu.Lock()
	cleared := len(e.state.Logs)
	e.state.Logs = []LogEntry{}
	e.state.LogsDropped += cleared
	e.state.UpdatedAt = now()
	snapshot := e.cloneLocked()
	e.mu.Unlock()
	e.emit(snapshot)
}

func (e *Engine) update(fn func(*State)) {
	e.mu.Lock()
	fn(&e.state)
	e.state.UpdatedAt = now()
	snapshot := e.cloneLocked()
	e.mu.Unlock()
	e.emit(snapshot)
}

func (e *Engine) emit(state State) {
	if e.opts.OnState != nil {
		e.opts.OnState(state)
	}
}

func (e *Engine) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = time.Second
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func now() string { return seu.Now().Format("15:04:05.000") }

// humanDelay 在 [min, max] 之间取一个随机等待时长; 区间非法或相等时退化为固定值。
func humanDelay(min, max time.Duration) time.Duration {
	if min < 0 {
		min = 0
	}
	if max < min {
		min, max = max, min
	}
	span := int((max - min) / time.Millisecond)
	if span <= 0 {
		return min
	}
	return min + time.Duration(rand.IntN(span+1))*time.Millisecond
}

// shortDuration 把时长压成「2 小时 5 分」这样的两段式文案。
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0 秒"
	}
	switch {
	case d >= time.Hour:
		h := int(d / time.Hour)
		if m := int((d % time.Hour) / time.Minute); m > 0 {
			return fmt.Sprintf("%d 小时 %d 分", h, m)
		}
		return fmt.Sprintf("%d 小时", h)
	case d >= time.Minute:
		m := int(d / time.Minute)
		if s := int((d % time.Minute) / time.Second); s > 0 {
			return fmt.Sprintf("%d 分 %d 秒", m, s)
		}
		return fmt.Sprintf("%d 分", m)
	default:
		return fmt.Sprintf("%d 秒", int(d/time.Second))
	}
}

func levelFor(result seu.Result) string {
	switch {
	case result.Success:
		return "ok"
	case result.Kind == "vcode":
		return "warn"
	case result.Kind == "limited":
		return "warn"
	default:
		return "error"
	}
}
