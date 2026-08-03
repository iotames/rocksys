"""outbox：本地消息 Outbox 占位实现。

本批次仅提供最小占位：定义 Outbox 数据模型与收发接口签名，
消息可靠投递（本地持久化 + 轮询发送）留待后续批次实现。
"""

from __future__ import annotations

import json
import threading
import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional
from uuid import uuid4


@dataclass
class OutboxRecord:
    """Outbox 中的一条待发消息记录。"""

    msg_id: str = field(default_factory=lambda: uuid4().hex)
    topic: str = ""
    payload: Dict[str, Any] = field(default_factory=dict)
    created_at: float = field(default_factory=time.time)
    status: str = "pending"  # pending / sent / failed
    retries: int = 0


class Outbox:
    """最小 Outbox 占位：内存队列 + 后台发送线程。

    说明：当前实现为占位，仅在内存中收发，用于打通业务侧到底座
    的异步消息模式；持久化与 at-least-once 语义后续迭代补齐。
    """

    def __init__(self, batch_size: int = 100):
        self._records: List[OutboxRecord] = []
        self._lock = threading.Lock()
        self._batch_size = batch_size

    def enqueue(self, topic: str, payload: Dict[str, Any]) -> OutboxRecord:
        """入队一条待发消息。"""
        record = OutboxRecord(topic=topic, payload=payload)
        with self._lock:
            self._records.append(record)
        return record

    def poll(self, limit: Optional[int] = None) -> List[OutboxRecord]:
        """取出一批待发消息（标记为发送中）。"""
        limit = limit or self._batch_size
        with self._lock:
            batch = [r for r in self._records if r.status == "pending"][:limit]
            for r in batch:
                r.status = "sending"
            return batch

    def ack(self, msg_ids: List[str]) -> None:
        """确认已发送，标记为 sent。"""
        with self._lock:
            for r in self._records:
                if r.msg_id in msg_ids:
                    r.status = "sent"

    def fail(self, msg_ids: List[str], retry_delay: float = 1.0) -> None:
        """标记失败并稍后重试。"""
        with self._lock:
            for r in self._records:
                if r.msg_id in msg_ids:
                    r.status = "pending"
                    r.retries += 1

    def pending_count(self) -> int:
        """待发消息数量。"""
        with self._lock:
            return sum(1 for r in self._records if r.status in ("pending", "sending"))

    def to_json(self) -> str:
        """序列化当前队列（占位：用于调试/快照）。"""
        return json.dumps([r.__dict__ for r in self._records], ensure_ascii=False)
