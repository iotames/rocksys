"""config：业务微服务配置加载模块。

加载优先级：环境变量 > yaml 文件 > 默认值。
- yaml 文件：顶层为 key -> value 的扁平结构（支持嵌套 dict/list）。
- 环境变量：优先读取 ``ROCKBIZ_<KEY>``（全大写），其次 ``<KEY>``。
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any, Dict, Optional

_ENV_PREFIX = "ROCKBIZ_"


def _to_python_type(raw: str) -> Any:
    """把环境变量字符串按 yaml 语义转换成 bool/int/float/list/dict。"""
    lowered = raw.strip().lower()
    if lowered in ("true", "false"):
        return lowered == "true"
    if lowered in ("null", "none", ""):
        return None
    try:
        return int(raw)
    except ValueError:
        pass
    try:
        return float(raw)
    except ValueError:
        pass
    if raw.strip().startswith(("[", "{")):
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            pass
    return raw


def _set_nested(cfg: Dict[str, Any], path: str, value: Any) -> None:
    """按点号路径写入嵌套配置，如 'server.port' -> cfg['server']['port']。"""
    keys = [k for k in path.split(".") if k]
    node = cfg
    for k in keys[:-1]:
        child = node.setdefault(k, {})
        if not isinstance(child, dict):
            child = {}
            node[k] = child
        node = child
    node[keys[-1]] = value


def _merge_env(cfg: Dict[str, Any]) -> Dict[str, Any]:
    """用环境变量覆盖配置。

    环境变量名去前缀（ROCKBIZ_）后：
    - 无 ``__`` 时对应顶层键（覆盖原始 yaml 加载结果）。
    - 含 ``__`` 时表示嵌套路径，如 ``ROCKBIZ_SERVER__PORT`` -> ``server.port``。
    """
    for env_name, raw in os.environ.items():
        upper = env_name.upper()
        if not upper.startswith(_ENV_PREFIX):
            continue
        key = upper[len(_ENV_PREFIX):].strip()
        if not key:
            continue
        if "__" in key:
            path = ".".join(k.lower() for k in key.split("__"))
            _set_nested(cfg, path, _to_python_type(raw))
        elif key in cfg:
            cfg[key] = _to_python_type(raw)
    return cfg


def load_config(path: Optional[str] = None) -> Dict[str, Any]:
    """加载配置：环境变量 > yaml。

    参数:
        path: yaml 配置文件路径。为 None 或文件不存在时返回空配置（仅环境变量）。
    返回:
        配置字典。
    """
    cfg: Dict[str, Any] = {}
    if path:
        p = Path(path)
        if p.is_file():
            try:
                import yaml  # 惰性导入，避免顶层依赖 PyYAML
            except ImportError as exc:  # pragma: no cover
                raise RuntimeError("load_config 需要 PyYAML，请先 pip install pyyaml") from exc
            with p.open("r", encoding="utf-8") as f:
                data = yaml.safe_load(f) or {}
            if not isinstance(data, dict):
                raise ValueError(f"配置文件顶层必须是映射，实际为 {type(data).__name__}: {path}")
            cfg.update(data)
    return _merge_env(cfg)
