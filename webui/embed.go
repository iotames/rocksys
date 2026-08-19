//go:build !dev

// Package webui 内嵌管理控制台静态资源（纯静态单页，ElementUI 风格，无框架）。
// 由 cmd/rocksys 装配时经 adminapi.RegisterWebUI 注册到管理接口根路径（如 127.0.0.1:19527/）。
//
// ★ 双模式（开发免编译热重载）：
//   - 生产模式（默认，本文件）：go:embed 编译期嵌入 index.html 与 assets/，
//     改前端代码必须重新编译才能生效（make build/run/release）。
//   - 开发模式（-tags dev，见 embed_dev.go）：改用 os.DirFS 实时读磁盘，
//     改前端代码刷新浏览器即见，无需重新编译、无需重启（make dev）。
package webui

import "embed"

//go:embed index.html assets
var FS embed.FS
