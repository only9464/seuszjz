package seu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// callAPI 请求业务接口, 并识别「会话失效」。
func (s *Session) callAPI(ctx context.Context, req request) (map[string]any, error) {
	resp, err := s.do(ctx, req)
	if err != nil {
		return nil, err
	}
	// 先看状态码: 5xx / 429 是服务端临时故障 (开抢峰值很常见), 会话其实还好好的,
	// 不能按「会话失效」处理, 否则会触发一次完全没必要的重新登录。
	if resp.status == http.StatusTooManyRequests || resp.status >= 500 {
		return nil, fmt.Errorf("%w (HTTP %d, body=%q)",
			ErrTransient, resp.status, bodySnippet(resp.body))
	}
	if resp.isRedirect() || !resp.looksLikeJSON() {
		s.markExpired()
		// 把现场证据带在错误里: 是「被重定向到登录页」(会话被踢) 还是
		// 「网关/风控返回 HTML」一眼可辨, 不用再靠猜。
		return nil, fmt.Errorf("%w (HTTP %d, location=%q, body=%q)",
			ErrSessionExpired, resp.status, resp.header.Get("Location"), bodySnippet(resp.body))
	}
	var data map[string]any
	if err := json.Unmarshal(resp.body, &data); err != nil {
		return nil, fmt.Errorf("接口返回无法解析 (HTTP %d): %s", resp.status, resp.snippet())
	}
	return data, nil
}

func (s *Session) markExpired() {
	s.mu.Lock()
	s.loggedIn = false
	s.mu.Unlock()
}

// listRaw 按页拉取列表接口, 按 WID/HD_WID 去重。
func (s *Session) listRaw(ctx context.Context, endpoint string, pageSize int) ([]map[string]any, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	items := make([]map[string]any, 0, pageSize)
	seen := map[string]bool{}
	for page := 1; page <= 50; page++ {
		data, err := s.callAPI(ctx, request{
			method: "GET",
			target: endpoint,
			query: url.Values{
				"pageIndex": {strconv.Itoa(page)},
				"pageSize":  {strconv.Itoa(pageSize)},
			},
		})
		if err != nil {
			return nil, err
		}
		rows, _ := data["datas"].([]any)
		for _, row := range rows {
			record, ok := row.(map[string]any)
			if !ok {
				continue
			}
			key := fieldString(record, "WID")
			if key == "" {
				key = fieldString(record, "HD_WID")
			}
			if key == "" {
				raw, _ := json.Marshal(record)
				key = string(raw)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, record)
		}
		total := anyInt(data["total"])
		if len(rows) == 0 || len(items) >= total {
			break
		}
	}
	return items, nil
}

// Activities 拉取全部讲座。
func (s *Session) Activities(ctx context.Context) ([]Lecture, time.Time, error) {
	rows, err := s.listRaw(ctx, EPActivity, 50)
	if err != nil {
		return nil, time.Time{}, err
	}
	now := time.Now()
	lectures := make([]Lecture, 0, len(rows))
	for _, row := range rows {
		lecture := ToLecture(row, now)
		if lecture.WID == "" {
			continue
		}
		lectures = append(lectures, lecture)
	}
	// 用服务器时间重新推导一次状态
	clock := ServerClock(lectures)
	for i := range lectures {
		lectures[i].Status = DeriveStatus(lectures[i], clock)
	}
	sortLectures(lectures)
	return lectures, clock, nil
}

// lectureSortRank 是列表的第一排序键: 先按大类分组, 再在组内比时间。
//
// 0 已满 / 1 可预约 / 2 未开放 是用户关心的三大类; 「已预约」「已结束」这些
// 用户没有归类的状态排在它们之后 (已预约在「我的预约」页里看更直接)。
// 后端接口按发布顺序返回数据 (基本倒序), 所以这里统一排一次。
func lectureSortRank(status string) int {
	switch status {
	case StatusFull:
		return 0
	case StatusAvailable:
		return 1
	case StatusNotOpen:
		return 2
	case StatusBooked, StatusCheckedIn, StatusDefaulted:
		return 3
	default: // ended / cancelled 及其它
		return 4
	}
}

// sortLectures 先按大类分组, 组内按讲座时间正序 (同一时间再按名称), 保证顺序稳定。
func sortLectures(items []Lecture) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		aRank, bRank := lectureSortRank(a.Status), lectureSortRank(b.Status)
		if aRank != bRank {
			return aRank < bRank
		}
		aAt, aOK := ParseTime(a.StartAt)
		bAt, bOK := ParseTime(b.StartAt)
		switch {
		case aOK && bOK:
			if !aAt.Equal(bAt) {
				return aAt.Before(bAt)
			}
		case aOK != bOK:
			// 没有讲座时间的排在有时间的后面, 但仍在同一大类里。
			return aOK
		}
		return a.Name < b.Name
	})
}

// MyActivities 拉取我的预约。
func (s *Session) MyActivities(ctx context.Context) ([]MyActivity, time.Time, error) {
	rows, err := s.listRaw(ctx, EPMy, 50)
	if err != nil {
		return nil, time.Time{}, err
	}
	items := make([]MyActivity, 0, len(rows))
	clock := time.Now()
	for _, row := range rows {
		item := ToMyActivity(row)
		if item.WID == "" {
			continue
		}
		if t, ok := ParseTime(item.ServerTime); ok {
			clock = t
		}
		items = append(items, item)
	}
	return items, clock, nil
}

// Notices 拉取通知公告。
func (s *Session) Notices(ctx context.Context) ([]Notice, time.Time, error) {
	rows, err := s.listRaw(ctx, EPNotice, 50)
	if err != nil {
		return nil, time.Time{}, err
	}
	items := make([]Notice, 0, len(rows))
	clock := time.Now()
	for _, row := range rows {
		notice := ToNotice(row)
		items = append(items, notice)
		if t, ok := ParseTime(normalizeDate(notice.Date)); ok {
			clock = t
		}
	}
	return items, clock, nil
}

// CheckAppointment 预约前置校验 (只读)。返回 success/msg 供界面提示。
func (s *Session) CheckAppointment(ctx context.Context, wid string) (Result, error) {
	data, err := s.callAPI(ctx, request{
		method:  "POST",
		target:  EPCheck,
		form:    url.Values{"wid": {wid}},
		referer: AppURL,
	})
	if err != nil {
		return Result{}, err
	}
	return classify(data), nil
}

// FetchCaptcha 获取预约验证码图片 (JPEG 字节)。
func (s *Session) FetchCaptcha(ctx context.Context) ([]byte, error) {
	data, err := s.callAPI(ctx, request{
		method: "GET",
		target: EPVcode,
		query:  url.Values{"_": {strconv.FormatInt(nowMillis(), 10)}},
	})
	if err != nil {
		return nil, err
	}
	if !truthy(data["success"]) {
		return nil, fmt.Errorf("获取验证码失败: %s", messageOf(data))
	}
	raw := fieldString(data, "result")
	if idx := strings.Index(raw, ","); idx >= 0 {
		raw = raw[idx+1:]
	}
	image, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("验证码图片解码失败")
	}
	if len(image) == 0 {
		return nil, errors.New("验证码图片为空")
	}
	return image, nil
}

// Reserve 提交预约。
func (s *Session) Reserve(ctx context.Context, wid, vcode string) (Result, error) {
	payload, err := json.Marshal(map[string]string{"HD_WID": wid, "vcode": vcode})
	if err != nil {
		return Result{}, err
	}
	data, err := s.callAPI(ctx, request{
		method:  "POST",
		target:  EPYYSave,
		form:    url.Values{"paramJson": {string(payload)}},
		referer: AppURL,
	})
	if err != nil {
		return Result{}, err
	}
	return classify(data), nil
}

// Cancel 取消预约。wid 为活动 WID。
func (s *Session) Cancel(ctx context.Context, wid string) (Result, error) {
	payload, err := json.Marshal(map[string]string{"HD_WID": wid})
	if err != nil {
		return Result{}, err
	}
	data, err := s.callAPI(ctx, request{
		method:  "POST",
		target:  EPDelete,
		form:    url.Values{"hdyyJson": {string(payload)}},
		referer: AppURL,
	})
	if err != nil {
		return Result{}, err
	}
	return classify(data), nil
}

// classify 依据业务响应判断结果类型。
func classify(data map[string]any) Result {
	result := Result{Success: truthy(data["success"]), Message: messageOf(data)}
	if result.Message == "" {
		if result.Success {
			result.Message = "操作成功"
		} else {
			result.Message = "操作失败"
		}
	}
	switch {
	case result.Success:
		result.Kind = "ok"
	case strings.Contains(result.Message, "频繁"):
		result.Kind = "limited"
	case strings.Contains(result.Message, "验证码"):
		result.Kind = "vcode"
	case strings.Contains(result.Message, "已满") || strings.Contains(result.Message, "名额"):
		result.Kind = "full"
	case strings.Contains(result.Message, "结束") || strings.Contains(result.Message, "过期") ||
		strings.Contains(result.Message, "未开始") || strings.Contains(result.Message, "不可预约"):
		result.Kind = "closed"
	case strings.Contains(result.Message, "预约过") || strings.Contains(result.Message, "重复"):
		result.Kind = "duplicated"
	default:
		result.Kind = "other"
	}
	return result
}

func anyInt(v any) int {
	text := rawString(v)
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	total := 0
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return total
		}
		total = total*10 + int(ch-'0')
	}
	return total
}

func nowMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func normalizeDate(value string) string {
	text := strings.TrimSpace(value)
	if len(text) == len("2006-01-02") {
		return text + " 00:00:00"
	}
	return text
}
