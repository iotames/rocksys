"""logging：结构化 JSON 日志模块。

基于标准库 logging，提供 JSON Formatter，自动注入当前上下文 trace_id。
用法::

    log = get_logger(__name__)
    log.info("请求收到", extra={"path": "/api/hello"})
"""

from __future__ import annotations

import json
import logging
import sys
import time
from datetime import datetime
from typing import Any, Dict, Mapping, Optional

from .trace import get_trace_id

_configured = False


class JsonFormatter(logging.Formatter):
    """输出单行 JSON 的日志格式化器，自动附带 trace_id。"""

    def format(self, record: logging.LogRecord) -> str:
        ts = datetime.fromtimestamp(record.created, tz=None)
        payload: Dict[str, Any] = {
            "time": ts.strftime("%Y-%m-%d %H:%M:%S.%f")[:-3],
            "level": record.levelname,
            "logger": record.name,
            "trace_id": get_trace_id() or None,
            "message": record.getMessage(),
        }
        for key, value in record.__dict__.items():
            if key.startswith("_") or key in (
                "name", "msg", "args", "levelname", "levelno", "pathname",
                "filename", "module", "exc_info", "exc_text", "stack_info",
                "lineno", "funcName", "created", "msecs", "relativeCreated",
                "thread", "threadName", "processName", "process", "message",
                "asctime", "taskName",
            ):
                continue
            if key == "trace_id":
                continue  # 保留内置 trace_id，不覆盖
            payload[key] = value
        return json.dumps(payload, ensure_ascii=False, default=str)


def _setup_root_handler() -> None:
    """确保全局已有 JSON handler，避免重复添加。"""
    global _configured
    if _configured:
        return
    handler = logging.StreamHandler(stream=sys.stdout)
    handler.setFormatter(JsonFormatter())
    logging.getLogger().addHandler(handler)
    _configured = True


def get_logger(name: str = "rockbiz") -> logging.Logger:
    """获取结构化 JSON 日志器（根 logger 已挂 JSON handler）。"""
    _setup_root_handler()
    return logging.getLogger(name)
