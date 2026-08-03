# contracts/openapi 契约规范

`contracts/openapi/` 下每业务服务一个 OpenAPI 3.0 契约文件（`*.yaml`），作为业务侧接口的唯一事实来源。

## 1. 通用约定

- **格式**：OpenAPI 3.0.3（yaml）。
- **命名**：`<服务名>.yaml`，如 `stbiz_hello.yaml`。
- **版本**：契约文件 `info.version` 采用 `x.y.z` 语义化版本，只增不改删。

## 2. 统一错误码

所有非 2xx 响应用统一结构（`{code, msg, data}`）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `code` | int | 业务错误码，`0` 表示成功，非 `0` 表示失败 |
| `msg` | string | 错误描述（中文，人可读） |
| `data` | any/null | 附加错误数据，可为空 |

- 成功响应使用 2xx，业务数据直接放响应体。
- 失败响应使用 4xx/5xx + 上述错误结构。
- 约定错误码段：
  - `1xxxx`：通用错误（参数、鉴权等）
  - `4xxxx`：业务错误
  - `5xxxx`：服务内部错误

## 3. X-Trace-Id 全链路透传

- **请求侧**：客户端请求头携带 `X-Trace-Id`；未携带时由服务生成。
- **响应侧**：所有响应（成功与失败）均返回 `X-Trace-Id` 响应头，与请求侧一致。
- 该 ID 贯穿底座（rocksys）与业务微服务（stbiz_*）全链路，用于日志串联排查。

## 4. 接口版本策略

**只增不改删**：

1. 新增接口：直接追加 `paths`。
2. 修改接口：**禁止改删既有字段**；新增可选字段或新版本接口（`/api/xxx/v2`）。
3. 删除接口：仅允许废弃（deprecated: true），保留字段定义。
4. 契约变更必须同步升级 `info.version`，并在变更说明中记录兼容性。

## 5. 校验方式

```bash
# 语法校验（pyyaml）
python3 -c "import yaml; yaml.safe_load(open('contracts/openapi/stbiz_hello.yaml'))"

# 规范校验（redocly / spectral，可选）
npx redocly lint contracts/openapi/stbiz_hello.yaml
npx spectral lint contracts/openapi/stbiz_hello.yaml
```

## 6. 现有契约

| 文件 | 服务 | 说明 |
|------|------|------|
| `stbiz_hello.yaml` | stbiz_hello | 最小业务微服务模板 |
