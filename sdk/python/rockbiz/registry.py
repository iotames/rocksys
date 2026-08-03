"""registry：服务注册与发现占位实现。

本批次仅提供最小占位：内存注册表 + register/discover 接口签名，
对接底座（rocksys）的服务发现能力留待后续批次实现。
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Dict, List, Optional


@dataclass
class ServiceEndpoint:
    """单个服务实例信息。"""

    host: str
    port: int
    meta: Dict[str, str] = field(default_factory=dict)


class ServiceRegistry:
    """最小服务注册中心占位：内存存储，支持按服务名注册与发现。"""

    def __init__(self, base_url: str = ""):
        self.base_url = base_url.rstrip("/")
        self._services: Dict[str, List[ServiceEndpoint]] = {}
        self._lock = threading.Lock()

    def register(self, service_name: str, endpoint: ServiceEndpoint) -> None:
        """注册服务实例。"""
        with self._lock:
            self._services.setdefault(service_name, []).append(endpoint)

    def deregister(self, service_name: str, endpoint: ServiceEndpoint) -> None:
        """注销服务实例。"""
        with self._lock:
            endpoints = self._services.get(service_name, [])
            self._services[service_name] = [
                e for e in endpoints if not (e.host == endpoint.host and e.port == endpoint.port)
            ]

    def discover(self, service_name: str) -> List[ServiceEndpoint]:
        """发现指定服务的全部可用实例（占位：直接返回内存数据）。"""
        with self._lock:
            return list(self._services.get(service_name, []))

    def discover_one(self, service_name: str) -> Optional[ServiceEndpoint]:
        """发现一个可用实例（简单轮询第一个）。"""
        endpoints = self.discover(service_name)
        return endpoints[0] if endpoints else None

    def services(self) -> List[str]:
        """已注册的服务名列表。"""
        with self._lock:
            return list(self._services.keys())
