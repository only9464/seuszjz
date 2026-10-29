package main

import (
	"seuszjz/internal/grab"
	"seuszjz/internal/seu"
)

// Settings 是前端可编辑的偏好设置。
type Settings struct {
	Username    string `json:"username"`
	Remember    bool   `json:"remember"`
	AutoLogin   bool   `json:"autoLogin"`
	AutoCaptcha bool   `json:"autoCaptcha"`
	// WaitMs 是等待开放时的轮询间隔 (毫秒); AttemptMs 是开放后两次提交的间隔 (毫秒)。
	WaitMs    int    `json:"waitMs"`
	AttemptMs int    `json:"attemptMs"`
	Cooldown  int    `json:"cooldown"`
	Theme     string `json:"theme"`
	// NetMode 是网络环境偏好: auto / campus / offcampus。
	NetMode     string `json:"netMode"`
	HasPassword bool   `json:"hasPassword"`
}

// Status 是运行时状态。
type Status struct {
	LoggedIn      bool         `json:"loggedIn"`
	User          seu.UserInfo `json:"user"`
	SavedUsername string       `json:"savedUsername"`
	HasSession    bool         `json:"hasSession"`
	OcrAvailable  bool         `json:"ocrAvailable"`
	OcrInfo       string       `json:"ocrInfo"`
	Version       string       `json:"version"`
}

// Bootstrap 是启动时一次性返回的数据。
type Bootstrap struct {
	Status   Status     `json:"status"`
	Settings Settings   `json:"settings"`
	Grab     grab.State `json:"grab"`
	// State 是要前端存进 localStorage 的快照 (账号、密码、Cookie 全在里面)。
	State string `json:"state"`
}

// Overview 是一次刷新拿到的全部列表数据。
type Overview struct {
	ServerTime string           `json:"serverTime"`
	FetchedAt  string           `json:"fetchedAt"`
	Lectures   []seu.Lecture    `json:"lectures"`
	Mine       []seu.MyActivity `json:"mine"`
	Notices    []seu.Notice     `json:"notices"`
	// Requirement 是根据「我的预约」算出的毕业要求完成情况。
	Requirement seu.Requirement `json:"requirement"`
}

// LoginResult 是登录结果。
type LoginResult struct {
	User    seu.UserInfo `json:"user"`
	Source  string       `json:"source"`  // manual / cache / password / stage2
	Network string       `json:"network"` // campus / offcampus, 本次走的网络路径
	// NeedStage2 表示学校要求二次验证 (非可信设备): 前端应引导用户输入短信验证码。
	NeedStage2 bool `json:"needStage2"`
}

// Captcha 是一张验证码图片 (data URL)。
type Captcha struct {
	Image string `json:"image"`
}

// OCRStatus 是本地验证码识别能力的探测结果。
type OCRStatus struct {
	Available bool   `json:"available"`
	Info      string `json:"info"`
}
