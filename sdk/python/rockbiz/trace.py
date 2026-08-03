"""trace：全链路追踪 ID 的上下文传递模块。

基于标准库 contextvars 实现，保证协程/异步场景下 trace_id 不串线。
"""

from __future__ import annotations

import uuid
from contextvars import ContextVar

__all__ = ["get_trace_id", "set_trace_id", "new_trace_id"]

_trace_id_var: ContextVar[str] = ContextVar("rockbiz_trace_id", default="")


def get_trace_id() -> str:
    """返回当前上下文的 trace_id；未设置时返回空字符串。"""
    return _trace_id_var.get()


def set_trace_id(trace_id: str) -> None:
    """在当前上下文设置 trace_id。"""
    _trace_id_var.set(trace_id)


def new_trace_id() -> str:
    """生成新的 trace_id（uuid4 hex，32 位）。"""
    return uuid.uuid4().hex
