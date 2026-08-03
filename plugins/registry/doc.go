// Package registry RockRegistry：服务注册与发现（独立进程组件，第 17 章）。
//
// 关键类型：
//   - Instance：注册表中的一个服务实例。
//   - StaticTable：从 YAML/JSON 文件加载静态实例列表（解析失败返回空表）。
//   - Server：内置轻量注册服务（标准库 http）：POST /register 注册、PUT /heartbeat 心跳续约；
//     心跳超时（默认 30s）未续约自动摘除（后台 goroutine 扫描）。
//   - Watcher：实例变更通知回调 func(instances []Instance)。
//   - Registry：实现 hotswap.Component（独立组件，不挂 chain）。
//
// 与 dispatch 的联动（第 17 章）：registry 不直接依赖 dispatch。实例变更时把最新实例列表
// 转为 DISPATCH_RULES 格式字符串（<Prefix>=<Upstream>，Prefix 约定 /api/<name>/），
// 经 conf.Manager.Set("DISPATCH_RULES", ...) 写入配置通道，dispatch 通过
// conf.Manager.Watch 订阅后走流程 C 重建 RouteTable，实现配置热更通道解耦。
package registry
