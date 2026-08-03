"""stbiz_hello：最小业务微服务模板。

接口：
- GET  /health     -> {"status":"ok"}
- POST /api/hello  -> {"msg":"hello","trace_id":"..."}

trace_id 从请求头 X-Trace-Id 读取，未携带则生成，随响应头返回。
"""

from __future__ import annotations

import sys
from pathlib import Path

from fastapi import FastAPI, Header, Request
from fastapi.responses import JSONResponse

BASE_DIR = Path(__file__).resolve().parent
if str(BASE_DIR.parent.parent / "sdk" / "python") not in sys.path:
    sys.path.insert(0, str(BASE_DIR.parent.parent / "sdk" / "python"))

from rockbiz.config import load_config
from rockbiz.logging import get_logger
from rockbiz.trace import new_trace_id

app = FastAPI(title="stbiz_hello", version="1.0.0", docs_url="/docs")
log = get_logger("stbiz_hello")


@app.get("/health")
async def health(request: Request):
    log.info("健康检查", extra={"trace_id": request.headers.get("X-Trace-Id", "")})
    return JSONResponse(status_code=200, content={"status": "ok"})


@app.post("/api/hello")
async def api_hello(
    request: Request,
    x_trace_id: str = Header(default="", alias="X-Trace-Id"),
):
    trace_id = x_trace_id or new_trace_id()
    log.info("收到业务请求", extra={"trace_id": trace_id, "path": "/api/hello"})
    return JSONResponse(
        status_code=200,
        content={"msg": "hello", "trace_id": trace_id},
        headers={"X-Trace-Id": trace_id},
    )


def main() -> None:
    """读取配置并启动服务（监听 localhost:9000）。"""
    cfg = load_config(str(BASE_DIR / "config.yaml"))
    server = cfg.get("server", {})
    host = server.get("host", "127.0.0.1")
    port = int(server.get("port", 9000))
    log.info("stbiz_hello 启动", extra={"host": host, "port": port})

    import uvicorn

    uvicorn.run(app, host=host, port=port)


if __name__ == "__main__":
    main()
