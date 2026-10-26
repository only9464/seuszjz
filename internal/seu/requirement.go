package seu

import (
	"strings"
	"time"
)

// 毕业要求 (学校规定): 研究生在中期考核前应至少选听 8 次「人文与科学素养系列讲座」,
// 其中法律、艺术、心理健康讲座各至少 1 次。
//
// 两条容易踩错的口径:
//   - 只有素养系列计入 8 次; 沙龙、咖啡间、购票优惠这类活动不算。
//   - 只算「已打卡」(SFDK=1); 已预约未打卡、违约都不计入。
const (
	// RequirementTotal 是总计需要选听的次数。
	RequirementTotal = 8
	// suyangSeries 是计入次数的系列名关键字, 官网数据形如
	// 「人文与科学素养系列讲座_法律」「人文与科学素养系列讲座-艺术类」。
	suyangSeries = "人文与科学素养"
)

// requiredCategories 是必须各选听一次的三类讲座。
var requiredCategories = []string{"法律", "艺术", "心理健康"}

// RequirementItem 是某一必修类别的完成情况。
type RequirementItem struct {
	Name    string `json:"name"`    // 法律 / 艺术 / 心理健康
	Need    int    `json:"need"`    // 该类别需要几场 (恒为 1)
	Done    int    `json:"done"`    // 已打卡几场
	Missing int    `json:"missing"` // 还差几场
}

// Requirement 是毕业要求的完成情况, 由「我的预约」列表计算得出。
type Requirement struct {
	TotalNeed    int               `json:"totalNeed"`    // 需要 8 次
	TotalDone    int               `json:"totalDone"`    // 素养系列里已打卡的次数
	TotalMissing int               `json:"totalMissing"` // 还差几次
	Pending      int               `json:"pending"`      // 已预约但讲座还没结束, 尚未打卡
	Defaulted    int               `json:"defaulted"`    // 素养系列里违约的次数
	Ignored      int               `json:"ignored"`      // 已打卡但不属于素养系列, 不计入
	Categories   []RequirementItem `json:"categories"`   // 三类必修讲座
}

// Complete 表示 8 次与三类必修是否全部满足。
func (r Requirement) Complete() bool {
	if r.TotalDone < r.TotalNeed {
		return false
	}
	for _, item := range r.Categories {
		if item.Missing > 0 {
			return false
		}
	}
	return true
}

// IsSuyangSeries 判断一场活动是否属于「人文与科学素养系列讲座」。
// 只有这类活动计入 8 次要求。
func IsSuyangSeries(series string) bool {
	return strings.Contains(series, suyangSeries)
}

// EvaluateRequirement 根据「我的预约」列表算出毕业要求完成情况。
//
// 统计口径: 素养系列 + 已打卡 → 计入次数与类别; 素养系列 + 违约 → 只记违约;
// 素养系列 + 已约但讲座未结束 → 记为「待打卡」; 非素养系列即使打卡也不计入。
func EvaluateRequirement(items []MyActivity) Requirement {
	req := Requirement{TotalNeed: RequirementTotal, Categories: []RequirementItem{}}
	done := make(map[string]int, len(requiredCategories))
	for _, name := range requiredCategories {
		done[name] = 0
	}

	for _, item := range items {
		if !item.Published {
			continue // 已取消的讲座
		}
		if !IsSuyangSeries(item.Series) {
			if item.CheckedIn {
				req.Ignored++
			}
			continue
		}
		switch {
		case item.Defaulted:
			req.Defaulted++
		case item.CheckedIn:
			req.TotalDone++
			if name := LectureCategory(item.Series, item.Name); name != "" {
				if _, ok := done[name]; ok {
					done[name]++
				}
			}
		case stillUpcoming(item):
			req.Pending++
		}
	}

	if req.TotalDone < req.TotalNeed {
		req.TotalMissing = req.TotalNeed - req.TotalDone
	}
	for _, name := range requiredCategories {
		item := RequirementItem{Name: name, Need: 1, Done: done[name]}
		if item.Done < item.Need {
			item.Missing = item.Need - item.Done
		}
		req.Categories = append(req.Categories, item)
	}
	return req
}

// LectureCategory 从官方标签里取讲座类别 (法律 / 艺术 / 心理健康)。
//
// 数据源只用官网自带的标签, 不做关键词猜测:
//  1. 系列名的副标题: 「人文与科学素养系列讲座_法律」「…-艺术类」;
//  2. 标题里的【…】标记: 「【线下】【法律】…」。
//
// 都取不到时返回空字符串, 表示这场讲座没有可识别的类别 (不计入三类要求)。
func LectureCategory(series, name string) string {
	if label := seriesLabel(series); label != "" {
		return label
	}
	for _, label := range bracketLabels(name) {
		if isRequiredCategory(label) {
			return label
		}
	}
	return ""
}

// seriesLabel 取系列名最后一个分隔符之后的副标题, 并去掉「类」后缀。
func seriesLabel(series string) string {
	cut := strings.LastIndexAny(series, "_-")
	if cut < 0 || cut >= len(series)-1 {
		return ""
	}
	label := strings.TrimSuffix(strings.TrimSpace(series[cut+1:]), "类")
	if isRequiredCategory(label) {
		return label
	}
	return ""
}

// bracketLabels 取出标题里所有【…】标记 (如 【线下】【法律】 → ["线下", "法律"])。
func bracketLabels(name string) []string {
	var labels []string
	for {
		start := strings.Index(name, "【")
		if start < 0 {
			return labels
		}
		rest := name[start+len("【"):]
		end := strings.Index(rest, "】")
		if end < 0 {
			return labels
		}
		labels = append(labels, strings.TrimSpace(rest[:end]))
		name = rest[end+len("】"):]
	}
}

func isRequiredCategory(label string) bool {
	for _, name := range requiredCategories {
		if label == name {
			return true
		}
	}
	return false
}

// stillUpcoming 判断一场讲座是否还没结束 (以接口返回的服务器时间为准)。
func stillUpcoming(item MyActivity) bool {
	end, ok := ParseTime(item.EndAt)
	if !ok {
		if end, ok = ParseTime(item.StartAt); !ok {
			return true
		}
	}
	now := time.Now()
	if server, ok := ParseTime(item.ServerTime); ok {
		now = server
	}
	return now.Before(end)
}
