# rockbiz SDK

RockBiz 业务微服务 Python SDK，为 `stbiz_*` 业务微服务提供通用能力。

## 模块

| 模块 | 功能 |
|------|------|
| `config` | `load_config`：配置加载，环境变量 > yaml |
| `logging` | `get_logger`：结构化 JSON 日志，自动带 trace_id |
| `client` | `HttpClient`：HTTP 客户端，默认 timeout=5s，自动携带 X-Trace-Id |
| `trace` | `get_trace_id`/`set_trace_id`：基于 contextvars 的上下文传递 |
| `outbox` | 本地消息 Outbox（占位实现） |
| `registry` | 服务注册与发现（占位实现） |

## 用法

```python
from rockbiz.config import load_config
from rockbiz.logging import get_logger
from rockbiz.client import HttpClient
from rockbiz.trace import get_trace_id, set_trace_id

cfg = load_config("config.yaml")          # 环境变量 > yaml
log = get_logger(__name__)

client = HttpClient(base_url="http://other-svc", default_timeout=5)
log.info("请求收到", extra={"path": "/api/hello"})
resp = client.post("/api/action", json_body={"k": 1})   # 自动带 X-Trace-Id
```

## 环境要求

- Python ≥ 3.10
- 依赖：PyYAML（仅 `config.load_config` 需要）

## 安装

```bash
pip install -e .
```

## 验证

```bash
python3 -c "import rockbiz; print('ok')"
```
