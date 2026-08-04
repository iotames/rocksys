// Package webui 内嵌管理控制台静态资源（纯静态单页，ElementUI 风格，无框架）。
// 由 cmd/rocksys 装配时经 adminapi.RegisterWebUI 注册到管理接口根路径（如 127.0.0.1:19527/）。
package webui

import "embed"

//go:embed index.html assets
var FS embed.FS
