# 水环境观测档案（温度/盐度/校准时刻 + 区段/时间窗 + 版本链）

## 背景与现状

- 试验数据通路是 `MeasurementArchive`（`internal/service/measurements.go`）：`MeasuredRun = Operation + []Evidence`，JSON 文件 `operator-measurements.json` **先落盘后发布**（临时文件 + rename），重启后重载。
- 水槽区段状态在 `internal/tank`（水深、DepthRevision、CalmSince），但温度、盐度、校准时刻完全没有进入试验数据。
- 分析入口是 `GET /api/measured-runs/{id}/analysis` → `MeasurementArchive.Analyze` → `MeasurementAnalysis`（波高统计 + 力均值）。

## 目标

1. 新增水环境观测档案：接收现场采样（温度、盐度、校准时刻），标记适用区段与有效时间窗。
2. 试验分析时给出：对应环境条件（matched）、时间缺口（gaps）、修正依据（correction basis）。
3. 环境记录可更正（supersede），历史原值保留可查。

## 设计

### 1. 领域模型 — 新建 `internal/model/environment.go`

```go
type EnvironmentSample struct {
    ID           EventID   `json:"id"`
    Sections     []string  `json:"sections"`      // 适用区段（可多个，兼容混匀水槽）
    SourceID     string    `json:"source_id"`     // 采样仪器
    TemperatureC float64   `json:"temperature_c"`
    SalinityPPT  float64   `json:"salinity_ppt"`
    CalibratedAt time.Time `json:"calibrated_at"` // 仪器校准时刻
    SampledAt    time.Time `json:"sampled_at"`    // 现场采样时刻
    ValidFrom    time.Time `json:"valid_from"`    // 适用时间窗
    ValidTo      time.Time `json:"valid_to"`
    Supersedes   EventID   `json:"supersedes,omitempty"`    // 更正链：本记录取代谁
    SupersededBy EventID   `json:"superseded_by,omitempty"` // 被谁取代（空=当前有效）
    RecordedAt   time.Time `json:"recorded_at"`
    Durable      bool      `json:"durable"`
}
```

附 `NewEnvironmentSample` 构造器与 `Effective()` / `Covers(at)` / `AppliesTo(section)` 方法，风格对齐 `model/evidence.go`。

### 2. 档案服务 — 新建 `internal/service/environment.go`

`EnvironmentArchive` 复用 `MeasurementArchive` 的全部既有模式：

- `OpenEnvironmentArchive(directory)`：持久化到同一数据目录下的 `water-environment.json`；空目录 = 纯内存（同测量档案）。
- `Record(input)`：校验 → 处理 supersede → 整份拷贝 → **先 commit 落盘再更新内存**；`Durable` 标记与测量一致。
  - 校验：至少一个非空区段；`source_id` 非空；温度/盐度有限且盐度 ≥ 0；校准/采样时刻非零且不晚于现在（1 分钟容差，同测量）；`valid_from < valid_to`；`supersedes` 若给必须存在且当前有效（防止链分叉）。
  - 更正语义：新记录 `Supersedes=旧ID` 追加；旧记录 `SupersededBy=新ID`；两者同次原子提交。**旧值永不删除、永不改数值**。
- `List(section, at, includeSuperseded)`：当前有效记录，可按区段/时刻过滤；`includeSuperseded=true` 时含历史。
- `History(id)`：沿 `Supersedes`/`SupersededBy` 走出版本链（最旧→最新），供"历史原值仍可查"。
- `Coverage(section, from, to)`：供分析使用——
  - `matched`：该区段有效记录中时间窗与试验窗相交者；
  - `gaps`：试验窗 `[from,to]` 内无任何记录覆盖的子区间（区间扫描算法）；
  - `correction_basis`：每条命中记录的 ID、仪器、校准时刻、采样时刻、温度、盐度及其覆盖的子区间——即密度/声速等修正的依据；
  - `covered`：无缺口且窗口非空。

### 3. 分析集成 — 改 `internal/service/measurements.go` + `runtime.go`

- `MeasurementAnalysis` 增加字段 `Environment EnvironmentCoverage \`json:"environment"\``；`MeasurementArchive.Analyze` 本身不填（保持零值）。
- `Runtime` 增加 `Environment *EnvironmentArchive` 字段；`OpenRuntime(dir)` 同时打开两个档案；`NewRuntime()` 两者皆内存版。
- `Runtime.AnalyzeMeasuredRun`：先走原有 `Measurements.Analyze`，再取该 run 全部 evidence 的 `RecordedAt` 最小/最大值作为试验窗，调用 `Environment.Coverage(operation.Section, from, to)` 填入。无 evidence 时窗口为零值、matched/gaps 为空（语义：尚无试验时刻可对照）。
- `Runtime` 增加委托方法：`RecordEnvironmentSample` / `EnvironmentSamples` / `EnvironmentSampleHistory`。

### 4. HTTP API — 新建 `internal/api/environment.go`，改 `router.go`

- `EnvironmentService` 接口，并入 `Dependencies`。
- 路由：
  - `POST /api/environment-samples` — 录入采样（可带 `supersedes` 更正）
  - `GET  /api/environment-samples?section=&at=&all=` — 查当前有效记录（`all=true` 含历史）
  - `GET  /api/environment-samples/{id}/history` — 版本链（历史原值）
- `GET /api/measured-runs/{id}/analysis` 响应自动多出 `environment` 块，路由本身不变。
- 不做 HTML 页面（测量功能同样只有 API，保持一致）。

### 5. 测试

- `internal/service/environment_test.go`：
  - 录入→重开档案→数据仍在且 `Durable=true`；
  - 各类非法输入被拒绝且不改变档案；
  - supersede 后：List 默认只见新值，`all=true` 与 `History` 可见原值；重载后链仍完整；
  - Coverage：全覆盖 / 部分缺口区间正确 / 无命中；correction basis 内容正确；
  - `Runtime.AnalyzeMeasuredRun` 带出 environment 块。
- `internal/api/environment_test.go`：POST→列表→history→试验分析含 environment，镜像现有 `measurements_test.go` 风格。

### 6. 文档

- `README.md` 增加一段：新端点、字段含义、supersede/历史语义、分析中的 environment 块。

## 涉及文件

| 文件 | 动作 |
|---|---|
| `internal/model/environment.go` | 新建 |
| `internal/service/environment.go` | 新建 |
| `internal/service/environment_test.go` | 新建 |
| `internal/api/environment.go` | 新建 |
| `internal/api/environment_test.go` | 新建 |
| `internal/service/measurements.go` | 加 `Environment` 字段 |
| `internal/service/runtime.go` | 接线 + 委托方法 |
| `internal/api/router.go` | 挂路由、扩接口 |
| `README.md` | 补文档 |

## 验证

`go build ./... && go vet ./... && go test ./...`（vendor 目录已存在，默认 `-mod=vendor`）。
