// 脚本/外挂文件逐级加载机制（运行时实时管理类能力，与组件生命周期同属 hotswap 包）。
//
// 与 internal/hotswap 组件热切不同：本文件负责"纯文本文件（如 SQL）的逐级覆盖加载"，
// 参考 todo/hotswap/script.go 机制，泛化嵌入源为 fs.FS 接口。
//
// ★ 统一收敛红线（车同文，书同轨）：
//   - 全项目所有"内嵌兜底、外挂文件覆写"的加载（SQL、WAF 规则、可信代理等）
//     必须统一经本文件的收敛入口 NewScriptDir / EmbeddedScriptDir 构造，禁止在其他包
//     散点直接构造 ScriptDir。
//   - 外挂覆写目录统一为配置项 HOT_SCRIPTS_DIR（默认 "hotscripts"，相对工作目录），
//     各业务使用固定子目录：sql/（数据访问 SQL）、rules/（WAF 规则）、
//     trusted_proxies/（可信代理）。
//
// 加载顺序（逐级覆盖）：
//  1. 优先从外置目录 dirList 中查找（可热修改，无需重新编译）；
//  2. 找不到（或文件内容为空/读取失败）时，回退到编译期嵌入的 embedFS。
package hotswap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ScriptDir 脚本目录访问器：外置目录优先、嵌入文件兜底。
type ScriptDir struct {
	embedFS fs.FS
	dirList []string
}

var onesd *ScriptDir
var once sync.Once

// hotScriptsDir 外挂脚本统一根目录（HOT_SCRIPTS_DIR 配置值，默认 "hotscripts"，相对工作目录）。
// ★ 全项目红线：所有外挂覆写目录统一位于本根目录下，禁止各业务自建外置根。
var hotScriptsDir = "hotscripts"

// SetHotScriptsDir 装配方注入 HOT_SCRIPTS_DIR 配置值（相对工作目录，默认 "hotscripts"）。
// 空值忽略（保持默认）；装配期调用一次（测试可调用以隔离外挂目录）。
func SetHotScriptsDir(dir string) {
	if dir != "" {
		hotScriptsDir = dir
	}
}

// HotScriptsDir 返回当前外挂脚本统一根目录（HOT_SCRIPTS_DIR，相对工作目录）。
func HotScriptsDir() string { return hotScriptsDir }

// GetScriptDir 获取脚本目录单例。
func GetScriptDir(sd *ScriptDir) *ScriptDir {
	once.Do(func() {
		onesd = sd
	})
	if onesd == nil {
		panic("ScriptDir is nil")
	}
	return onesd
}

// NewScriptDir 初始化程序运行时所需的外部脚本文件目录（★ 全项目统一收敛入口）。
// 如果在给定的所有目录中找不到所需文件，则从 embedFS 中获取。
// embedFS 为嵌入文件系统（embed.FS 或其子目录 fs.Sub 均可），作为内嵌兜底；
// subDir 为业务固定子目录：外挂覆写目录 = HOT_SCRIPTS_DIR/subDir（相对工作目录，
// 默认 hotscripts/subDir）。moreDirs 为追加的子目录（同样位于 HOT_SCRIPTS_DIR 下，
// 按序查找）。查找顺序：外挂目录（HOT_SCRIPTS_DIR/subDir 等）→ 嵌入 embedFS。
//
// 典型业务子目录：sql/（数据访问 SQL）、rules/（WAF 规则）、
// trusted_proxies/（可信代理）。
func NewScriptDir(embedFS fs.FS, subDir string, moreDirs ...string) *ScriptDir {
	dirList := make([]string, 0, 1+len(moreDirs))
	for _, d := range append([]string{subDir}, moreDirs...) {
		dirList = append(dirList, filepath.Join(hotScriptsDir, d))
	}
	return &ScriptDir{embedFS: embedFS, dirList: dirList}
}

// EmbeddedScriptDir 仅使用编译期嵌入脚本的 ScriptDir（无外挂覆写；
// 用于测试与"无外置目录"场景，GetScriptBytes 直接回退嵌入文件）。
func EmbeddedScriptDir(embedFS fs.FS) *ScriptDir {
	return &ScriptDir{embedFS: embedFS}
}

// OkDir 检查 d 是否为存在的目录。
func (s ScriptDir) OkDir(d string) error {
	info, err := os.Stat(d)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", d)
		}
		return nil
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("dir(%s) does not exist", d)
	}
	return err
}

// OkNormalFile 检查 d 是否为存在的普通文件。
func (s ScriptDir) OkNormalFile(d string) error {
	info, err := os.Stat(d)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is not a directory", d)
		}
		return nil
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("file(%s) does not exist", d)
	}
	return err
}

// GetScriptText 获取脚本文件的纯文本内容。
// 优先从 dirList 目录列表中查找文件；如找不到，最后从内嵌文件中读取。
func (s ScriptDir) GetScriptText(fpath string) (string, error) {
	b, err := s.GetScriptBytes(fpath)
	return string(b), err
}

// GetScriptBytes 获取脚本文件的字节内容。
// 优先从 dirList 目录列表中查找文件；如找不到，最后从内嵌文件中读取。
func (s ScriptDir) GetScriptBytes(fpath string) ([]byte, error) {
	filepaths := make([]string, 0, len(s.dirList))
	for _, d := range s.dirList {
		filepaths = append(filepaths, filepath.Join(d, fpath))
	}
	realfpath := s.GetFirstExistFile(filepaths...)
	if realfpath != "" {
		// 找到目标文件
		if b, err := os.ReadFile(realfpath); err == nil && len(b) != 0 {
			return b, nil
		}
	}
	// 找不到文件，或文件内容为空/读取错误，则从内嵌文件系统读取
	return fs.ReadFile(s.embedFS, fpath)
}

// GetFirstExistFile 从给定的多个文件中获取第一个存在的文件。
func (s ScriptDir) GetFirstExistFile(filelist ...string) string {
	for _, f := range filelist {
		if s.OkNormalFile(f) == nil {
			return f
		}
	}
	return ""
}

// DecodeJson 解码 json 文件。
func (s ScriptDir) DecodeJson(fpath string, v any) error {
	b, err := s.GetScriptBytes(fpath)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// GetSQL 获取 sql 文本。
// replaceList 字符串列表，依次替换 SQL 文本中的 ? 占位符。
// 注意：仅在需要把 SQL 片段（如表名）直接拼入时使用；参数化查询请直接用
// GetScriptText 取文本后经 database/sql 参数化执行，避免 SQL 注入。
func (s ScriptDir) GetSQL(fpath string, replaceList ...string) (string, error) {
	sqlTxt, err := s.GetScriptText(fpath)
	if err != nil {
		return "", err
	}
	for _, replaceStr := range replaceList {
		sqlTxt = strings.Replace(sqlTxt, "?", replaceStr, 1)
	}
	return sqlTxt, nil
}

// LsDirByEmbedFS 列出嵌入文件系统中一层/二层的文件名（用于调试与校验）。
func (s ScriptDir) LsDirByEmbedFS() []string {
	var filenames []string
	entries, err := fs.ReadDir(s.embedFS, ".")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		filenames = append(filenames, entry.Name())
		if entry.IsDir() {
			subEntries, err := fs.ReadDir(s.embedFS, entry.Name())
			if err != nil {
				panic(err)
			}
			for _, subEntry := range subEntries {
				filenames = append(filenames, entry.Name()+"/"+subEntry.Name())
			}
		}
	}
	return filenames
}
