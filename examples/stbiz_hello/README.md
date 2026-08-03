# stbiz_hello 最小业务微服务模板

`stbiz_hello` 是 RockSys 体系中最小的业务微服务，用于验证底座（rocksys）与业务侧
通过 HTTP + X-Trace-Id 的全链路透传。

## 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查，返回 `{"status":"ok"}` |
| POST | `/api/hello` | 业务示例，返回 `{"msg":"hello","trace_id":"..."}` |

- `trace_id`：从请求头 `X-Trace-Id` 读取，未携带则生成；随响应头 `X-Trace-Id` 返回。
- 详细契约见 `contracts/openapi/stbiz_hello.yaml`。

## 启动

```bash
# 1. 安装依赖
pip install fastapi uvicorn

# 2. 启动服务（监听 localhost:9000，地址见 config.yaml）
cd examples/stbiz_hello
python app.py
```

## 验证

```bash
# 直接访问业务服务
curl http://127.0.0.1:9000/health
# -> {"status":"ok"}

curl -X POST http://127.0.0.1:9000/api/hello -H 'X-Trace-Id: demo-123' -v
# -> 200 {"msg":"hello","trace_id":"demo-123"}
# -> 响应头带 X-Trace-Id: demo-123

# 经底座代理访问（全链路透传）
go run ./cmd/rocksys --upstream http://127.0.0.1:9000 &
curl http://localhost:8080/api/hello -v
# -> 响应带 X-Trace-Id 头，链路日志含同一 trace_id
```

## 配置

`config.yaml` 控制监听地址：

```yaml
server:
  host: "127.0.0.1"
  port: 9000
```

环境变量优先级高于配置文件（如 `ROCKBIZ_SERVER_HOST` / `SERVER_HOST`），
由 `rockbiz.config.load_config` 处理。
