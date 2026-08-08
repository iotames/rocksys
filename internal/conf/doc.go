// Package conf 底座配置封装（基于 easyconf）。
//
// 配置中心红线：
//  1. 所有配置项必须经 Manager.Register 注册，禁止绕过本包直接读取环境变量；
//  2. 禁止在项目根目录运行程序：运行时文件跟随工作目录生成（程序源码不写死路径，
//     配置文件用相对工作目录的 .env/default.env；开发规范要求工作目录=bin/，故实际落在 bin/.env、bin/default.env）；
//  3. default.env 为全量默认值快照（SyncDefaultFile 装配完成后自动同步，代表代码真实兜底行为），参与取值（最低优先级兜底），优先级由 easyconf 决定；
//  4. 修改默认值必须改代码中的 Register 默认参数，或用 make gen-env 主动刷新 default.env。
package conf
