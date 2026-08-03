"""rockbiz：RockBiz 业务微服务 SDK。

为 stbiz_* 业务微服务提供通用能力：
- config：配置加载（环境变量 > yaml）
- logging：结构化 JSON 日志（自动带 trace_id）
- client：HTTP 客户端（timeout=5s，自动带 X-Trace-Id）
- trace：trace_id 上下文传递（contextvars）
- outbox：本地消息 Outbox（占位）
- registry：服务注册与发现（占位）
"""

from .config import load_config
from .client import HttpClient
from .logging import get_logger
from .trace import get_trace_id, set_trace_id

__all__ = [
    "load_config",
    "get_logger",
    "HttpClient",
    "get_trace_id",
    "set_trace_id",
]

__version__ = "0.1.0"
