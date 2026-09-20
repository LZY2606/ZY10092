# 离线瓦片测绘拼接台（Pair-wiseGSB）

一个从零实现、零第三方依赖（仅 Go 标准库）的本地系统：把多个离线瓦片数据包登记到
**统一 XYZ 索引**，找出空洞 / 重复覆盖 / 日期倒置 / 边界像素接缝，按来源优先级或
锁定版本生成候选拼接，并用**内容寻址 + 事件溯源**产出崩溃安全、可往返验证的构建。

- 后端：Go（`net/http`、`archive/tar`、`image/png`，无外部依赖）
- 网页：原生 HTML/JS/Canvas，可缩放平移、点击瓦片追溯全部候选输入
- 持久化：只增事件日志（一事件一文件、原子 rename）+ 内容寻址块存储
- 测试：地理语义、存储失败路径、事件溯源/幂等、端到端 HTTP 与往返重导入

## 快速开始

```bash
go mod download
go test ./... -count=1
go run ./cmd/server --listen 127.0.0.1:5218
```

浏览器打开 <http://127.0.0.1:5218>。

默认数据目录为 `.gsb-data`（可用 `--data` 修改）。页面不内置任何示例数据：
第 1 栏「生成并导入」会在**请求时**合成一个真实的离线包 tar（PNG 瓦片 + manifest），
你也可以上传自己的 tar。所有状态来自运行中的后端。

## 离线包格式（tar）

```
manifest.json
tiles/<z>/<x>/<y>.png
...
```

`manifest.json`：

```json
{
  "name": "team-alpha-survey",
  "zoom": 2,
  "scheme": "xyz",
  "projection": "EPSG:3857",
  "license": "CC-BY-4.0",
  "source": "team-alpha",
  "west": 170, "south": -10, "east": -170, "north": 10,
  "captured": "2026-05-01T00:00:00Z",
  "tiles": [
    { "x": 3, "y": 1, "path": "tiles/2/3/1.png" },
    { "x": 0, "y": 1, "path": "tiles/2/0/1.png", "license": "CC-BY-NC-4.0" }
  ]
}
```

单瓦片可用 `license` / `captured` 覆盖包级默认值（图像相同不代表授权相同）。

## 统一索引与边界语义

- **坐标方向**：输入接受 `xyz`（原点左上，y 向下）或 `tms`（原点左下，y 向上）。
  入库时统一归一化为 XYZ，并保留瓦片的原始 `scheme` 便于追溯。
  `y_xyz = 2^z - 1 - y_tms`。
- **不做取模环绕**：`x/y` 落在 `[0, 2^z)` 之外的瓦片一律**隔离（quarantine）**，
  绝不会取模后落到世界另一侧。每个隔离项记录原因（`out_of_range`、
  `missing_blob`、`duplicate_in_package` …），不进入统一索引、不计入覆盖。
- **反子午线**：用显式语义表达，不靠坐标环绕。`west > east` 表示跨越 180° 经线，
  范围被拆成**两条不环绕的矩形带**（一条靠世界右缘、一条靠左缘），
  范围对象带 `wrap:true`。因此「接缝/空洞」都在真实相邻关系上计算。
- **极区不可表示**：仅接受 Web Mercator（`EPSG:3857`，含 `EPSG:900913` 等别名），
  其它投影直接拒绝。纬度 `|lat| ≥ 85.05112878` 超出墨卡托范围的条带以
  `polar_unrepresentable` 显式登记为被排除部分，索引只包含可表示的部分。

## 分析规则

在某区域（名字 + zoom + 地理范围 + 规则）上：

- **空洞 holes**：区域内没有任何候选的瓦片。
- **重复覆盖 duplicates**：同一输出坐标存在多个候选（记录各自哈希与许可）。
- **日期倒置 date_inversion**：按规则选中的候选比某个被拒绝候选更旧。
- **许可冲突 license_conflict**：同位置候选许可不同；当**像素完全相同但元数据
  （许可/来源）冲突**时额外给 `same_pixels_metadata_conflict`——
  图像相同不代表授权相同，仍必须提示。
- **边界接缝 seams**：解码相邻选中瓦片的共享边，逐像素比较得到差异比例 `0..1`
  驱动热度；即使边界像素一致，许可/来源不一致也会标记为元数据接缝。

**规则变化只重算该区域之后产生的计划/构建**；已发布构建保留自己的 `rules_hash`、
瓦片编号和来源摘要，不被回溯修改。

## 候选拼接与构建生命周期

每个构建严格分栏显示四种状态：`pending`（待确认）、`accepted`（已接受/已发布）、
`rejected`（被拒绝）、`recovering/building/failed`（恢复中/写入中/失败）。

1. 设定区域与规则 → 「生成候选拼接」：每格候选按 *锁定包 → 优先级顺序 →
   更新时间新者优先 → 包 ID* 排序。
2. 「创建构建」进入 `pending`，此时**没有任何输出可见**。
3. 「开始写入」进入 `building`，逐块做内容寻址复制；失败则落为 `failed`，
   旧状态与已提交块仍可读取。
4. 「接受并发布」校验所有块齐全后原子提交清单（manifest），构建才可见且不可变。
   也可「拒绝」。
5. 点击任意输出瓦片可在画布下看到该位置的**全部候选输入**（包、哈希、许可、
   采集日期、原始方案）。

## 从一次被中断的写入恢复（重点）

写入是**内容寻址**的（块名为其 sha256），并采用「临时文件 → fsync → 原子 rename
→ fsync 目录」。每个块写完后才追加一条 `block_written` 事件。因此崩溃时：

- 已 rename 的块必然完整，未 rename 的临时文件留在 `tmp/`，重开时被清理；
- 事件日志与块都没有「写了一半但可见」的状态，**旧状态始终可读**。

恢复步骤：

1. 直接重新启动同一个数据目录：
   ```bash
   go run ./cmd/server --listen 127.0.0.1:5218
   ```
   启动回放日志后，任何停在 `building`/`recovering` 的构建会被标记为
   **`recovering`**（页面第 4 栏单独显示），不会假装成功也不会静默丢块。
2. 在该构建上点「恢复并继续」（或 `POST /api/builds/{id}/resume`）：
   - 对每个已标记写入的块，按内容哈希重新校验（缺失/损坏则视为未写）；
   - 从第一个缺失块继续，不重写已验证块；
   - 全部齐全后原子提交清单，状态转为 `accepted`。

测试覆盖：`internal/app/build_test.go` 的 `TestFailedWriteKeepsOldStateThenResume`
（可恢复的普通写入失败）与 `TestRestartMarksRecoveringAndResumes`
（模拟硬崩溃后重开进程，先显示 recovering 再恢复发布）。

## 幂等与可追溯

- 所有写命令接受 `Idempotency-Key` 头（或 `?idem=`）。同一幂等键重复到达时，
  **只返回第一次已确定的结果**（字节一致），不会产生第二个包/区域/构建。
  首次响应随事件一起持久化，重放后语义不变。
- 每次状态变化都有一条只增事件（`package_imported`、`region_upserted`、
  `plan_prepared`、`build_created`、`build_started`、`block_written`、
  `build_failed`、`build_recovered`、`build_accepted`、`build_rejected`）。
  页面底部「事件溯源日志」可查看序号、类型、时间、幂等键与说明；
  也可 `GET /api/events`。

## 导出 / 重新导入往返

`GET /api/builds/{id}/export` 导出自包含 tar（`manifest.json` + `blocks/<sha>`）。
`POST /api/builds/reimport?origin=<原构建ID>`（multipart 字段 `package`）重新导入：

- 校验每个块的内容哈希；
- 生成一个新的已接受构建，但**瓦片编号、范围边界、来源摘要与原始一致**，
  响应里给出 `same_tiles / same_bounds / same_sources` 三个布尔值。

## HTTP API 摘要

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/packages/generate` | 运行时合成并导入测试包（JSON） |
| POST | `/api/packages/import` | 上传离线包 tar（multipart `package` 或原始 tar） |
| GET  | `/api/packages` / `/api/packages/{id}` | 列表 / 详情（含隔离项） |
| POST | `/api/regions` | 设定区域、优先级、锁定版本 |
| GET  | `/api/analysis/{name}` | 空洞/重复/倒置/许可冲突/接缝 |
| POST | `/api/plans/{name}` | 生成候选拼接 |
| GET  | `/api/provenance/{name}?z&x&y` | 一个输出瓦片的全部候选 |
| POST | `/api/builds` | 创建构建（pending） |
| POST | `/api/builds/{id}/start` `/accept` `/reject` `/resume` | 生命周期 |
| GET  | `/api/builds/{id}/export` / `/manifest` | 导出 tar / 清单 |
| POST | `/api/builds/reimport` | 重新导入并比对 |
| GET  | `/api/blocks/{sha}` | 读取内容块 |
| GET  | `/api/events` | 事件溯源日志 |

## 目录结构

```
cmd/server/         入口（--listen / --data）
internal/geo/       统一索引、XYZ/TMS、反子午线、极区、边界像素
internal/store/     内容寻址块 + 只增事件日志（崩溃安全）
internal/app/       事件溯源状态机、导入、分析、候选、构建/恢复、导出
internal/web/       HTTP API 与嵌入的网页（app/*）
```

## 持久化布局（`.gsb-data`）

```
blocks/<xx>/<sha>   内容寻址不可变块（瓦片 PNG、构建 manifest）
journal/NNN.event   每事件一个原子文件
snapshots/          预留快照
tmp/                临时写入，重开时清理
```
