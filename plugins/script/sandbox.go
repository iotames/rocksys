// Sandbox：Lua VM 沙箱化 + 白名单 API + 编译期静态检查。
package script

import (
	"fmt"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"rocksys/internal/chain"
)

// allowedGlobals VM 池复用后允许保留在全局表中的键（内建库 + 白名单 API）。
// 其余为脚本写入的全局变量，执行后清理，避免跨请求污染。
var allowedGlobals = map[string]struct{}{
	"_G": {}, "_VERSION": {}, "_GOPHER_LUA_VERSION": {}, "_printregs": {},
	"assert": {}, "collectgarbage": {}, "error": {}, "getfenv": {},
	"getmetatable": {}, "ipairs": {}, "pairs": {}, "next": {},
	"pcall": {}, "print": {}, "rawequal": {}, "rawget": {}, "rawset": {},
	"select": {}, "setfenv": {}, "setmetatable": {}, "tonumber": {},
	"tostring": {}, "type": {}, "unpack": {}, "xpcall": {}, "newproxy": {},
	"table": {}, "string": {}, "math": {}, "req": {}, "ctx": {},
}

// newVM 创建沙箱化 Lua VM：仅打开安全库（base/table/string/math），
// 并移除可访问文件系统或加载代码的危险函数，保证 §15 白名单之外一律不可用。
func (e *Engine) newVM() *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	lua.OpenBase(L)
	lua.OpenTable(L)
	lua.OpenString(L)
	lua.OpenMath(L)
	// 禁止：文件加载 / 代码动态加载 / 模块 require（§15 禁止 os/io/file/net/ffi 的兜底）。
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require", "module"} {
		L.SetGlobal(name, lua.LNil)
	}
	// print 重定向为空操作：避免脚本污染标准输出。
	L.SetGlobal("print", L.NewFunction(func(*lua.LState) int { return 0 }))
	return L
}

// installAPI 向 VM 注册白名单 API（§15）：
//   req.header(key) / req.path() / req.method()
//   ctx.set_target(target) / ctx.respond(code, body)
// responded 为共享标记：respond 写入响应后置位，Handle 据此中断链。
func installAPI(L *lua.LState, ctx *chain.Context, responded *bool) {
	req := L.NewTable()
	req.RawSetString("header", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(ctx.R.Header.Get(L.CheckString(1))))
		return 1
	}))
	req.RawSetString("path", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(ctx.R.URL.Path))
		return 1
	}))
	req.RawSetString("method", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(ctx.R.Method))
		return 1
	}))
	L.SetGlobal("req", req)

	ct := L.NewTable()
	ct.RawSetString("set_target", L.NewFunction(func(L *lua.LState) int {
		if ctx.DF != nil {
			ctx.DF.SetTarget(L.CheckString(1))
		}
		return 0
	}))
	ct.RawSetString("respond", L.NewFunction(func(L *lua.LState) int {
		if *responded {
			return 0 // 已响应，忽略重复写（防止 superfluous WriteHeader panic）
		}
		code := L.CheckInt(1)
		body := L.CheckString(2)
		*responded = true
		w := ctx.W
		if ctx.RespW != nil {
			w = ctx.RespW
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
		return 0
	}))
	L.SetGlobal("ctx", ct)
}

// cleanupGlobals 删除脚本写入的、不在 allowedGlobals 中的全局变量，
// 保证 VM 池复用不把上一请求的脚本状态泄漏给下一个请求。
func cleanupGlobals(L *lua.LState) {
	tb := L.G.Global
	var toRemove []lua.LValue
	tb.ForEach(func(k, v lua.LValue) {
		if s, ok := k.(lua.LString); ok {
			if _, keep := allowedGlobals[string(s)]; !keep {
				toRemove = append(toRemove, k)
			}
		} else {
			toRemove = append(toRemove, k)
		}
	})
	for _, k := range toRemove {
		tb.RawSet(k, lua.LNil)
	}
}

// compileScript 预编译 Lua 源码为 FunctionProto（§15 预编译缓存）：
// 发布时编译一次，运行期 LoadProto 还原执行，零编译成本。
func compileScript(name, source string) (*lua.FunctionProto, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	lf, err := L.LoadString(source)
	if err != nil {
		return nil, err
	}
	return lf.Proto, nil
}

// checkSandbox 编译前静态检查（§15）：禁止引用 os/io/file/net/ffi 模块。
// 命中 require("os")、os.execute 等模式即判定违规，发布失败。
func checkSandbox(source string) error {
	lower := strings.ToLower(source)
	for _, mod := range forbiddenModules {
		// require 引用（双引号 / 单引号 / 无引号三种形态）。
		for _, pat := range []string{
			`require("` + mod + `")`,
			`require '` + mod + `'`,
			`require(` + mod + `)`,
		} {
			if strings.Contains(lower, pat) {
				return fmt.Errorf("script: 沙箱拒绝：禁止引用模块 %q（require）", mod)
			}
		}
		// 模块表点访问（os.execute、io.open、net.http 等），带词边界防误伤。
		if moduleAccess(lower, mod) {
			return fmt.Errorf("script: 沙箱拒绝：禁止引用模块 %q（%s.xxx）", mod, mod)
		}
	}
	return nil
}

// moduleAccess 判断 lower 中是否存在 "<分隔符><mod>.<标识符>" 的访问模式。
// 要求 mod 前一字符非标识符、且点后紧跟标识符字符，避免 "profile."、"planet." 等误判。
func moduleAccess(lower, mod string) bool {
	needle := mod + "."
	for {
		i := strings.Index(lower, needle)
		if i < 0 {
			return false
		}
		pre := byte(' ')

		if i > 0 {
			pre = lower[i-1]
		}
		if !isIdentByte(pre) {
			if post := i + len(needle); post < len(lower) && isIdentByte(lower[post]) {
				return true
			}
		}
		lower = lower[i+len(needle):]
	}
}

// isIdentByte 判断字符是否为 Go/Lua 标识符字符（字母/数字/下划线）。
func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}