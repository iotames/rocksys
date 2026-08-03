"""client：HTTP 客户端模块。

基于标准库 urllib.request 实现，无需第三方依赖。
- 默认 timeout=5s。
- 请求自动携带 X-Trace-Id（从 trace 上下文读取，未设置则生成）。
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any, Dict, Optional

from .trace import get_trace_id, new_trace_id, set_trace_id


class HttpError(Exception):
    """HTTP 非 2xx 响应错误。"""

    def __init__(self, status: int, body: str, headers: Dict[str, str]):
        self.status = status
        self.body = body
        self.headers = headers
        super().__init__(f"HTTP {status}: {body[:200]}")


class HttpResponse:
    """HTTP 响应封装。"""

    def __init__(self, status: int, headers: Dict[str, str], body: bytes):
        self.status = status
        self.headers = headers
        self.body = body

    def json(self) -> Any:
        """解析响应体为 JSON，解析失败抛出 ValueError。"""
        return json.loads(self.body.decode("utf-8"))

    def text(self) -> str:
        return self.body.decode("utf-8")

    def __repr__(self) -> str:  # pragma: no cover
        return f"<HttpResponse status={self.status} body={self.text()!r}>"


class HttpClient:
    """最小 HTTP 客户端：post/get 请求，自动携带 X-Trace-Id。"""

    def __init__(self, base_url: str = "", default_timeout: float = 5.0):
        self.base_url = base_url.rstrip("/")
        self.default_timeout = default_timeout

    # ---- 内部 ----

    def _build_headers(self, headers: Optional[Dict[str, str]]) -> Dict[str, str]:
        merged = dict(headers or {})
        trace_id = get_trace_id()
        if not trace_id:
            trace_id = new_trace_id()
            set_trace_id(trace_id)
        if "X-Trace-Id" not in merged and "X-Trace-Id".lower() not in {
            k.lower() for k in merged
        }:
            merged["X-Trace-Id"] = trace_id
        merged.setdefault("Accept", "application/json")
        return merged

    def _url(self, path: str) -> str:
        return f"{self.base_url}/{path.lstrip('/')}"

    def _request(
        self,
        method: str,
        path: str,
        headers: Optional[Dict[str, str]],
        timeout: Optional[float],
        data: Optional[bytes],
    ) -> HttpResponse:
        req = urllib.request.Request(
            self._url(path),
            data=data,
            method=method,
            headers=self._build_headers(headers),
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout or self.default_timeout) as resp:
                return HttpResponse(
                    status=resp.status,
                    headers=dict(resp.headers.items()),
                    body=resp.read(),
                )
        except urllib.error.HTTPError as exc:
            raise HttpError(
                status=exc.code,
                body=exc.read().decode("utf-8", errors="replace"),
                headers=dict(exc.headers.items()),
            ) from exc

    # ---- 对外 API ----

    def post(
        self,
        path: str,
        json_body: Optional[Dict[str, Any]] = None,
        headers: Optional[Dict[str, str]] = None,
        timeout: Optional[float] = None,
    ) -> HttpResponse:
        """发送 POST 请求。json_body 为 dict 时自动序列化并设置 Content-Type。"""
        data = None
        hdrs = dict(headers or {})
        if json_body is not None:
            data = json.dumps(json_body, ensure_ascii=False).encode("utf-8")
            hdrs.setdefault("Content-Type", "application/json")
        return self._request("POST", path, hdrs, timeout, data)

    def get(
        self,
        path: str,
        headers: Optional[Dict[str, str]] = None,
        timeout: Optional[float] = None,
    ) -> HttpResponse:
        """发送 GET 请求。"""
        return self._request("GET", path, headers, timeout, None)
