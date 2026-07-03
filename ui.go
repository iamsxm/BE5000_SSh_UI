package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type routerSSHApp struct {
	app           fyne.App
	window        fyne.Window
	localIPSelect *widget.SelectEntry
	routerIPEntry *widget.Entry
	tokenEntry    *widget.Entry
	logEntry      *widget.Entry
	statusLabel   *widget.Label
	progress      *widget.ProgressBarInfinite
	runButton     *widget.Button
	closeButton   *widget.Button
	logText       binding.String
}

// newRouterSSHApp 创建应用状态、控件树和事件绑定。
func newRouterSSHApp(myApp fyne.App) *routerSSHApp {
	window := myApp.NewWindow("BE5000 SSH UI")
	logText := binding.NewString()
	_ = logText.Set("")

	ui := &routerSSHApp{
		app:     myApp,
		window:  window,
		logText: logText,
	}
	ui.buildControls()
	window.SetContent(ui.buildLayout())
	return ui
}

// buildControls 初始化所有可交互控件，并设置默认值与占位提示。
func (ui *routerSSHApp) buildControls() {
	localIPs, err := getLocalIPs()
	if err != nil || len(localIPs) == 0 {
		localIPs = []string{"请手动输入本机 IPv4 地址"}
	}

	ui.localIPSelect = widget.NewSelectEntry(localIPs)
	ui.localIPSelect.SetPlaceHolder("选择或输入本机 IPv4 地址")
	if len(localIPs) > 0 && isValidIPv4(localIPs[0]) {
		ui.localIPSelect.SetText(localIPs[0])
	}

	ui.routerIPEntry = widget.NewEntry()
	ui.routerIPEntry.SetText("192.168.31.1")
	ui.routerIPEntry.SetPlaceHolder("路由器 IPv4 地址")

	ui.tokenEntry = widget.NewEntry()
	ui.tokenEntry.SetPlaceHolder("Web 管理后台 stok= 后面的 32 位 token")

	ui.logEntry = widget.NewMultiLineEntry()
	ui.logEntry.Bind(ui.logText)
	ui.logEntry.SetPlaceHolder("执行日志会显示在这里")
	ui.logEntry.SetMinRowsVisible(10)
	ui.logEntry.Wrapping = fyne.TextWrapWord

	ui.statusLabel = widget.NewLabel("准备就绪")
	ui.progress = widget.NewProgressBarInfinite()
	ui.progress.Hide()

	ui.runButton = widget.NewButtonWithIcon("开始执行", theme.ConfirmIcon(), ui.startCrack)
	ui.runButton.Importance = widget.HighImportance
	ui.closeButton = widget.NewButtonWithIcon("关闭", theme.CancelIcon(), func() {
		ui.window.Close()
		ui.app.Quit()
	})
}

// buildLayout 组合现代化工具界面：左侧参数输入，右侧状态说明与日志输出。
func (ui *routerSSHApp) buildLayout() fyne.CanvasObject {
	header := container.NewVBox(
		widget.NewLabelWithStyle("BE5000 SSH UI", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("面向合法自有设备的本地执行工具。请确认路由器、网络与授权范围无误。"),
		widget.NewSeparator(),
	)

	form := widget.NewForm(
		widget.NewFormItem("本机 IP", ui.localIPSelect),
		widget.NewFormItem("路由器 IP", ui.routerIPEntry),
		widget.NewFormItem("路由器 Token", ui.tokenEntry),
	)

	actions := container.NewHBox(
		ui.runButton,
		ui.closeButton,
		layout.NewSpacer(),
		widget.NewButtonWithIcon("刷新 IP", theme.ViewRefreshIcon(), ui.refreshLocalIPs),
	)

	left := container.NewVBox(
		widget.NewCard("连接参数", "所有字段都需要填写，端口固定为 8888。", form),
		widget.NewCard("执行操作", "执行期间会启动本地 HTTP 服务并等待路由器回连。", actions),
	)

	status := container.NewVBox(
		container.NewHBox(themeInfoIcon(), ui.statusLabel, layout.NewSpacer()),
		ui.progress,
	)
	right := container.NewVBox(
		widget.NewCard("当前状态", "长时间无响应时请检查防火墙、端口占用和路由器可达性。", status),
		widget.NewCard("日志输出", "仅记录本次执行的关键步骤。", ui.logEntry),
	)

	split := container.NewHSplit(left, right)
	split.Offset = 0.42

	return container.NewBorder(header, nil, nil, nil, split)
}

// refreshLocalIPs 重新枚举本机 IPv4 地址，便于用户切换网络后快速更新。
func (ui *routerSSHApp) refreshLocalIPs() {
	localIPs, err := getLocalIPs()
	if err != nil || len(localIPs) == 0 {
		dialog.ShowError(errors.New("未能获取本机 IPv4 地址，请手动输入"), ui.window)
		return
	}
	ui.localIPSelect.SetOptions(localIPs)
	ui.localIPSelect.SetText(localIPs[0])
}

// startCrack 校验输入并在后台执行任务，避免阻塞桌面界面。
func (ui *routerSSHApp) startCrack() {
	config := CrackConfig{
		LocalIP:  strings.TrimSpace(ui.localIPSelect.Text),
		RouterIP: strings.TrimSpace(ui.routerIPEntry.Text),
		Token:    strings.TrimSpace(ui.tokenEntry.Text),
	}
	if err := config.Validate(); err != nil {
		dialog.ShowError(err, ui.window)
		return
	}

	ui.setBusy(true, "执行中：正在准备 payload 与本地 HTTP 服务")
	_ = ui.logText.Set("")

	go func() {
		writer := newBindingLogWriter(ui.logText)
		logger := log.New(writer, "", log.LstdFlags)
		err := executeCrack(config, logger)
		if err != nil {
			ui.setBusy(false, "执行失败，请查看日志")
			dialog.ShowError(err, ui.window)
			return
		}
		ui.setBusy(false, "执行完成：SSH 服务已触发开启")
		dialog.ShowInformation("执行完成", fmt.Sprintf(sshInfo, config.RouterIP), ui.window)
	}()
}

// setBusy 统一切换忙碌状态，保证按钮、进度条和状态文本同步。
func (ui *routerSSHApp) setBusy(busy bool, message string) {
	ui.statusLabel.SetText(message)
	if busy {
		ui.runButton.Disable()
		ui.progress.Show()
		return
	}
	ui.runButton.Enable()
	ui.progress.Hide()
}

// themeInfoIcon 创建带一致图标风格的信息按钮，占位用于状态行视觉对齐。
func themeInfoIcon() fyne.CanvasObject {
	info := widget.NewIcon(theme.InfoIcon())
	return info
}

type bindingLogWriter struct {
	mu      sync.Mutex
	logText binding.String
}

// newBindingLogWriter 创建线程安全的日志写入器，将标准日志追加到绑定文本。
func newBindingLogWriter(logText binding.String) *bindingLogWriter {
	return &bindingLogWriter{logText: logText}
}

// Write 实现 io.Writer，将新增日志追加到 UI 日志区域。
func (writer *bindingLogWriter) Write(p []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	current, _ := writer.logText.Get()
	_ = writer.logText.Set(current + string(p))
	return len(p), nil
}
