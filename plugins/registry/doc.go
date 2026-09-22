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
// 历史：曾把实例列表转为路由规则配置项联动旧 dispatch DSL；该联动已随
// 路由 DSL 整体移除（dispatch 规则源迁至数据库路由四表，见
// docs/plan/route_dispatch/STEP5_assembly_hotswap.md），registry 服务发现本体保留。
package registry
