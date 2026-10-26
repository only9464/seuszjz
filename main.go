package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// frontend/dist 会被嵌进可执行文件, 所以桌面版和服务器版都是单文件部署。
//
//go:embed all:frontend/dist
var assets embed.FS

// 注册给前端的强类型事件 (bindings 会为它们生成 TS 类型)。
func init() {
	application.RegisterEvent[string]("login:progress")
	application.RegisterEvent[string]("grab:notice")
	application.RegisterEvent[string]("state:changed")
	application.RegisterEvent[OCRStatus]("ocr:ready")
}

func main() {
	// 同一份代码编出两个形态: 默认是桌面窗口; 加 -tags server 时框架把窗口实现
	// 换成空实现, 自动改为只跑 HTTP 服务, Center/Flash/Focus 等调用都变成空操作。
	// 监听地址用 WAILS_SERVER_HOST / WAILS_SERVER_PORT 控制, 默认 localhost:8080
	// (Docker 镜像里是 0.0.0.0:8080)。
	app := application.New(application.Options{
		Name:        "东南大学讲座助手",
		Description: "东南大学素质讲座预约助手",
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	c := newCore(app)

	app.RegisterService(application.NewService(NewSessionService(c)))
	app.RegisterService(application.NewService(NewLectureService(c)))
	app.RegisterService(application.NewService(NewGrabService(c)))
	app.RegisterService(application.NewService(NewUpdateService(c)))

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "东南大学讲座助手",
		Width:            1180,
		Height:           760,
		MinWidth:         960,
		MinHeight:        640,
		BackgroundColour: application.NewRGB(15, 17, 24),
		URL:              "/",
	})
	win.Center()
	c.win = win

	c.startup()

	app.OnShutdown(func() { c.shutdown() })

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
