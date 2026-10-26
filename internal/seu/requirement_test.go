package seu

import "testing"

// sample 造一条「我的预约」记录, 默认是已打卡的素养系列讲座。
func sample(series, name string, opts ...func(*MyActivity)) MyActivity {
	item := MyActivity{
		WID:        name,
		Name:       name,
		Series:     series,
		Published:  true,
		CheckedIn:  true,
		StartAt:    "2026-10-01 18:30:00",
		EndAt:      "2026-10-01 20:00:00",
		ServerTime: "2026-10-26 12:00:00",
	}
	for _, opt := range opts {
		opt(&item)
	}
	item.Status = DeriveMyStatus(item)
	return item
}

// DeriveMyStatus 仅用于测试构造, 与 ToMyActivity 的状态推导保持一致。
func DeriveMyStatus(item MyActivity) string {
	switch {
	case !item.Published:
		return StatusCancelled
	case item.Defaulted:
		return StatusDefaulted
	case item.CheckedIn:
		return StatusCheckedIn
	default:
		return StatusBooked
	}
}

func bookedOn(date string) func(*MyActivity) {
	return func(item *MyActivity) {
		item.CheckedIn = false
		item.StartAt = date + " 18:30:00"
		item.EndAt = date + " 20:00:00"
	}
}

func defaulted() func(*MyActivity) {
	return func(item *MyActivity) {
		item.CheckedIn = false
		item.Defaulted = true
	}
}

func unpublished() func(*MyActivity) {
	return func(item *MyActivity) { item.Published = false }
}

// TestRequirementEmpty 检查没有任何记录时的基线。
func TestRequirementEmpty(t *testing.T) {
	req := EvaluateRequirement(nil)
	if req.TotalNeed != RequirementTotal || req.TotalDone != 0 || req.TotalMissing != RequirementTotal {
		t.Fatalf("空列表结果不对: %+v", req)
	}
	if len(req.Categories) != 3 {
		t.Fatalf("应有 3 个必修类别, 实际 %d", len(req.Categories))
	}
	for _, item := range req.Categories {
		if item.Missing != 1 || item.Need != 1 {
			t.Fatalf("%s 的缺口计算不对: %+v", item.Name, item)
		}
	}
	if req.Complete() {
		t.Fatal("空列表不应判定为完成")
	}
}

// TestRequirementSeriesFilter 检查只有素养系列计入, 沙龙等活动即使打卡也不算。
func TestRequirementSeriesFilter(t *testing.T) {
	items := []MyActivity{
		sample("人文与科学素养系列讲座_法律", "【线下】【法律】新时代中国检察制度"),
		sample(`"SEU咖啡间"系列沙龙活动`, "【沙龙】某沙龙"),
		sample("人文与科学素养系列讲座_其他", "【线下】【前沿科技】某前沿讲座"),
	}
	req := EvaluateRequirement(items)
	if req.TotalDone != 2 {
		t.Fatalf("素养系列应计 2 次, 实际 %d", req.TotalDone)
	}
	if req.Ignored != 1 {
		t.Fatalf("非素养系列应记 1 条 ignored, 实际 %d", req.Ignored)
	}
}

// TestRequirementOnlyCheckedIn 检查只算已打卡: 已约未开始算待打卡, 违约单独计数。
func TestRequirementOnlyCheckedIn(t *testing.T) {
	items := []MyActivity{
		sample("人文与科学素养系列讲座_艺术", "【线下】【艺术】昆曲的腔调", bookedOn("2026-11-02")),
		sample("人文与科学素养系列讲座_法律", "【线下】【法律】某法律讲座", defaulted()),
	}
	req := EvaluateRequirement(items)
	if req.TotalDone != 0 {
		t.Fatalf("未打卡不应计入, 实际 %d", req.TotalDone)
	}
	if req.Pending != 1 {
		t.Fatalf("未结束的已约场次应记 pending=1, 实际 %d", req.Pending)
	}
	if req.Defaulted != 1 {
		t.Fatalf("违约应记 1 次, 实际 %d", req.Defaulted)
	}
}

// TestRequirementPastNotCheckedIn 检查「已过期且没打卡也没标违约」的场次不计入任何一类。
func TestRequirementPastNotCheckedIn(t *testing.T) {
	items := []MyActivity{
		sample("人文与科学素养系列讲座_艺术", "【线下】【艺术】过期未打卡", bookedOn("2026-09-01")),
	}
	req := EvaluateRequirement(items)
	if req.TotalDone != 0 || req.Pending != 0 {
		t.Fatalf("过期未打卡应两边都不计: %+v", req)
	}
}

// TestRequirementCategories 检查三类必修的识别与补齐。
func TestRequirementCategories(t *testing.T) {
	items := []MyActivity{
		sample("人文与科学素养系列讲座_法律", "【线下】【法律】法律讲座"),
		sample("人文与科学素养系列讲座-艺术类", "【线下】【艺术】艺术讲座"),    // 系列名用「-」分隔 + 「类」后缀
		sample("人文与科学素养系列讲座_其他", "【线下】【心理健康】心理健康讲座"), // 系列写「其他」, 靠标题【】标记识别
		sample("人文与科学素养系列讲座_其他", "【线下】【前沿科技】前沿讲座"),
	}
	req := EvaluateRequirement(items)
	got := map[string]int{}
	for _, item := range req.Categories {
		got[item.Name] = item.Done
	}
	want := map[string]int{"法律": 1, "艺术": 1, "心理健康": 1}
	for name, count := range want {
		if got[name] != count {
			t.Fatalf("%s 已听 %d 次, 期望 %d (%+v)", name, got[name], count, req.Categories)
		}
	}
	if req.TotalDone != 4 {
		t.Fatalf("总计应 4 次, 实际 %d", req.TotalDone)
	}
	// 三类齐了但只有 4 次, 整体仍未完成。
	if req.TotalMissing != RequirementTotal-4 {
		t.Fatalf("应还差 %d 次, 实际 %d", RequirementTotal-4, req.TotalMissing)
	}
	if req.Complete() {
		t.Fatal("三类齐备但不足 8 次, 不应判完成")
	}
}

// TestLectureCategory 检查类别标签的解析, 以及不猜关键词。
func TestLectureCategory(t *testing.T) {
	cases := []struct {
		series, name, want string
	}{
		{"人文与科学素养系列讲座_法律", "【线下】【法律】x", "法律"},
		{"人文与科学素养系列讲座-艺术类", "【线下】x", "艺术"},
		{"人文与科学素养系列讲座_心理健康", "x", "心理健康"},
		{"人文与科学素养系列讲座_其他", "【线下】【心理健康】x", "心理健康"},
		{"人文与科学素养系列讲座_其他", "【线下】【前沿科技】x", ""},
		{`"SEU咖啡间"系列沙龙活动`, "【沙龙】x", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := LectureCategory(c.series, c.name); got != c.want {
			t.Errorf("LectureCategory(%q, %q) = %q, 期望 %q", c.series, c.name, got, c.want)
		}
	}
}

// TestRequirementComplete 检查 8 次 + 三类齐备时判定为完成。
func TestRequirementComplete(t *testing.T) {
	items := []MyActivity{
		sample("人文与科学素养系列讲座_法律", "【线下】【法律】法律"),
		sample("人文与科学素养系列讲座_艺术", "【线下】【艺术】艺术"),
		sample("人文与科学素养系列讲座_心理健康", "【线下】【心理健康】心理"),
		sample("人文与科学素养系列讲座_其他", "【线下】【其他】1"),
		sample("人文与科学素养系列讲座_其他", "【线下】【其他】2"),
		sample("人文与科学素养系列讲座_其他", "【线下】【其他】3"),
		sample("人文与科学素养系列讲座_其他", "【线下】【其他】4"),
		sample("人文与科学素养系列讲座_其他", "【线下】【其他】5"),
	}
	req := EvaluateRequirement(items)
	if req.TotalDone != 8 || req.TotalMissing != 0 {
		t.Fatalf("应满 8 次: %+v", req)
	}
	if !req.Complete() {
		t.Fatal("8 次 + 三类齐备应判定完成")
	}
}

// TestRequirementCancelled 检查已取消 (FBZT=-1) 的记录被忽略。
func TestRequirementCancelled(t *testing.T) {
	items := []MyActivity{
		sample("人文与科学素养系列讲座_法律", "【线下】【法律】已取消", unpublished()),
	}
	req := EvaluateRequirement(items)
	if req.TotalDone != 0 || req.Ignored != 0 {
		t.Fatalf("已取消的讲座不应计入: %+v", req)
	}
}
