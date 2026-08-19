//go:build dev

// 开发模式（make dev / -tags dev）WebUI 静态资源来源：改用文件系统实时读磁盘，
// 替代编译期 go:embed（见 embed.go 生产模式）。改前端代码（index.html / assets/）
// 刷新浏览器即见效果，无需重新编译、无需重启。
//
// ★ 注意：os.DirFS 路径为相对运行期工作目录（开发规范下即 bin/）的相对路径，
//   默认假设工作目录为 bin/，webui 源码位于项目根目录的 webui/（即 ../webui）。
//   如工作目录变化，请同步调整 DevDir。
package webui

import "os"

// DevDir 开发模式 webui 源码目录（相对工作目录 bin/ 的相对路径，勿以 / 结尾）。
const DevDir = "../webui"

// FS 开发模式用文件系统替代 embed.FS，支持前端免编译热重载。
var FS = os.DirFS(DevDir)
