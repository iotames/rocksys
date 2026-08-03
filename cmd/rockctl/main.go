package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultAdminAddr = "127.0.0.1:19527"
	envAdminAddr     = "ROCKSYS_ADMIN"      // 管理接口地址环境变量
	envAdminToken    = "ROCKSYS_ADMIN_TOKEN" // 管理接口令牌环境变量
	httpTimeout      = 5 * time.Second
)

// usage 打印 CLI 帮助（中文）。
func usage() {
	fmt.Print(`rockctl — RockSys 运维命令行

用法：rockctl [--admin <地址>] <子命令> [参数]

全局参数:
  --admin <addr>  管理接口地址，默认 127.0.0.1:19527
                  优先级：--admin > 环境变量 ROCKSYS_ADMIN > 内置默认值

子命令:
  switch on   <comp>  开启组件           POST /admin/switch/on   {"name":"<comp>"}
  switch off  <comp>  关闭组件           POST /admin/switch/off  {"name":"<comp>"}
  switch list         列出组件状态       GET  /admin/switch/list
  config get          查看当前配置       GET  /admin/config
  config set <KEY> <v> 热改配置          PUT  /admin/config     {"<KEY>":"<v>"}
                       <KEY> 须为注册名全名（如 ROCKSYS_UPSTREAM）
  script publish <f>  发布 Lua 脚本      POST /admin/script/publish
  script rollback     回滚脚本           POST /admin/script/rollback

示例:
  rockctl switch list
  rockctl config set ROCKSYS_UPSTREAM http://127.0.0.1:9001

输出直接打印 admin API 的原始 JSON 响应（便于管道处理）；失败时退出码非零。
`)
}

// client 封装对 admin API 的 HTTP 请求（baseURL 可注入，便于测试）。
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

// newClient 构造 client，校验 baseURL 并补全 scheme。
func newClient(baseURL, token string) (*client, error) {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("管理接口地址为空")
	}
	return &client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: httpTimeout},
	}, nil
}

// do 发起 HTTP 请求，方法为 GET/POST/PUT；body 若非 nil 则以 JSON 序列化发送。
// 返回响应体原始字节。非 2xx 状态码返回错误（附带响应体）。
func (c *client) do(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 admin API 失败: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("admin API 返回 %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// getAdminAddr 决定管理接口地址：--admin 参数优先，其次环境变量，缺省内置默认。
func getAdminAddr(flagAdmin string) string {
	if flagAdmin != "" {
		return flagAdmin
	}
	if v := os.Getenv(envAdminAddr); v != "" {
		return v
	}
	return defaultAdminAddr
}

// performGET 执行 GET 请求并打印响应。
func performGET(c *client, path string) error {
	data, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// performPOST 执行 POST 请求并打印响应。
func performPOST(c *client, path string, body any) error {
	data, err := c.do(http.MethodPost, path, body)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// performPUT 执行 PUT 请求并打印响应。
func performPUT(c *client, path string, body any) error {
	data, err := c.do(http.MethodPut, path, body)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// runSwitch 处理 switch 子命令。
func runSwitch(c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("switch 缺子命令，可用：on|off|list")
	}
	switch args[0] {
	case "on":
		if len(args) != 2 {
			return errors.New("用法：rockctl switch on <comp>")
		}
		return performPOST(c, "/admin/switch/on", map[string]string{"name": args[1]})
	case "off":
		if len(args) != 2 {
			return errors.New("用法：rockctl switch off <comp>")
		}
		return performPOST(c, "/admin/switch/off", map[string]string{"name": args[1]})
	case "list":
		if len(args) != 1 {
			return errors.New("用法：rockctl switch list（无需额外参数）")
		}
		return performGET(c, "/admin/switch/list")
	default:
		return fmt.Errorf("未知 switch 子命令：%s（可用：on|off|list）", args[0])
	}
}

// runConfig 处理 config 子命令。
func runConfig(c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("config 缺子命令，可用：get|set")
	}
	switch args[0] {
	case "get":
		if len(args) != 1 {
			return errors.New("用法：rockctl config get")
		}
		return performGET(c, "/admin/config")
	case "set":
		if len(args) != 3 {
			return errors.New("用法：rockctl config set <KEY> <v>")
		}
		return performPUT(c, "/admin/config", map[string]string{args[1]: args[2]})
	default:
		return fmt.Errorf("未知 config 子命令：%s（可用：get|set）", args[0])
	}
}

// runScript 处理 script 子命令。
func runScript(c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("script 缺子命令，可用：publish|rollback")
	}
	switch args[0] {
	case "publish":
		if len(args) != 2 {
			return errors.New("用法：rockctl script publish <文件>")
		}
		source, err := os.ReadFile(args[1])
		if err != nil {
			return fmt.Errorf("读取脚本文件失败: %w", err)
		}
		return performPOST(c, "/admin/script/publish", map[string]string{
			"name":   "rule1",
			"source": string(source),
		})
	case "rollback":
		if len(args) != 1 {
			return errors.New("用法：rockctl script rollback")
		}
		return performPOST(c, "/admin/script/rollback", map[string]string{"name": "rule1"})
	default:
		return fmt.Errorf("未知 script 子命令：%s（可用：publish|rollback）", args[0])
	}
}

// main 入口：解析命令行并分发子命令。
func main() {
	args := os.Args[1:]

	// 解析全局 --admin 参数（允许 --admin=addr 或 --admin addr 两种形态）。
	flagAdmin := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			usage()
			return
		case a == "--admin":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "错误：--admin 需要地址参数")
				os.Exit(2)
			}
			flagAdmin = args[i+1]
			i++
		case strings.HasPrefix(a, "--admin="):
			flagAdmin = strings.TrimPrefix(a, "--admin=")
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "未知参数：%s\n", a)
			usage()
			os.Exit(2)
		default:
			rest = append(rest, a)
		}
	}
	args = rest

	if len(args) == 0 {
		usage()
		os.Exit(0)
	}

	c, err := newClient(getAdminAddr(flagAdmin), os.Getenv(envAdminToken))
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(2)
	}

	switch args[0] {
	case "switch":
		err = runSwitch(c, args[1:])
	case "config":
		err = runConfig(c, args[1:])
	case "script":
		err = runScript(c, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知子命令：%s\n", args[0])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}