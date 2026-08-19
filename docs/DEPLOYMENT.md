# 生产部署

> 目录规划、systemd 服务、安全建议、升级重启、多副本与日志留存。由 README.md「生产部署」章节下沉而来。

## 目录规划

```text
/opt/rocksys/
├── bin/rocksys              # 编译产物（或 cross-build 产物）
├── rocksys.env              # 配置文件
├── logs/                    # 访问日志（obs 启用后，按天切分）
├── rules/                   # WAF 规则外置目录（可选）
├── sql/                     # SQL 脚本外置目录（可选）
└── rocksys.db               # 默认 SQLite 数据库（自动创建）
```

## systemd 服务

```ini
# /etc/systemd/system/rocksys.service
[Unit]
Description=RockSys Gateway
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/rocksys
ExecStart=/opt/rocksys/bin/rocksys --config /opt/rocksys/rocksys.env
Restart=always
RestartSec=3
# 管理接口令牌（可选）
Environment=ROCKSYS_ADMIN_TOKEN=change-me

[Install]
WantedBy=multi-user.target
```

```bash
sudo cp bin/rocksys /opt/rocksys/bin/
sudo systemctl daemon-reload
sudo systemctl enable --now rocksys
```

## 安全建议

1. **管理接口仅监听回环**（默认 `127.0.0.1:19527`），严禁 `ROCKSYS_ADMIN` 设为 `0.0.0.0` 暴露外网。
2. 远程管理用 SSH 隧道：`ssh -L 19527:127.0.0.1:19527 user@host` 后浏览器访问 `http://127.0.0.1:19527/`。
3. 回环地址本机免鉴权；静态 token（`ROCKSYS_ADMIN_TOKEN`）仅用于非回环部署的远程调用鉴权。
4. 对外暴露的监听端口建议置于防火墙 / 安全组之后。

## 升级与优雅重启

```bash
# 1. 替换二进制
sudo cp bin/rocksys /opt/rocksys/bin/rocksys
# 2. 优雅重启（SIGTERM 触发排空，在途请求不丢失，30s 超时）
sudo systemctl restart rocksys
```

配置与前端均内嵌/外置，升级二进制即可，无需迁移数据（状态为内存态，日志落盘保留）。

## 多副本

转发层无状态，可多副本水平扩展：

```bash
./bin/rocksys --listen :8081 --config rocksys.env &
./bin/rocksys --listen :8082 --config rocksys.env &
```

前方用负载均衡器 / DNS 轮询分发；配置集中下发（同一配置文件或环境变量）。

## 日志与留存

- obs 启用后：访问日志默认写入 `access_log` 表（`OBS_STORE=db`，复用 `DB_DRIVER`/`DB_DSN`；数据库不可用时回退 JSONL 文件并告警）。`OBS_STORE=file` 已弃用，将不再被支持。WebUI「日志」页支持按时间范围（精确到分）+ 路径精确/模糊过滤查询。
- 指标：`GET /admin/metrics`（1 分钟滑动窗口），WebUI「观测 · 指标」查看趋势。
- 业务日志与网关日志分离；如需聚合到统一平台，可对接日志采集器消费 `logs/` 目录。
