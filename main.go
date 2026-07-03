package main

import (
	"log"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

// main 初始化桌面应用、加载图标，并启动主窗口。
func main() {
	myApp := app.NewWithID("com.github.iamsxm.be5000-ssh-ui")
	myApp.SetIcon(loadAppIcon())

	routerApp := newRouterSSHApp(myApp)
	routerApp.window.Resize(fyne.NewSize(760, 520))
	routerApp.window.CenterOnScreen()
	routerApp.window.ShowAndRun()
}

// loadAppIcon 从仓库资源中加载应用图标，失败时回退到 Fyne 默认图标。
func loadAppIcon() fyne.Resource {
	icon, err := fyne.LoadResourceFromPath("xiaomi_icon.png")
	if err != nil {
		log.Printf("加载应用图标失败: %v", err)
		return nil
	}
	return icon
}
