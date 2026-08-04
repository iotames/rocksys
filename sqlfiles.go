// Package sqlfiles 提供编译期嵌入的 SQL 脚本文件系统。
//
// 约定：项目所有数据库查询语句统一放在根目录 sql/<dbtype>/ 下，
// 编译时经 embed 嵌入二进制（默认零配置可运行），运行时可由外置目录覆盖（见 internal/db）。
package sqlfiles

import "embed"

//go:embed all:sql
var FS embed.FS
