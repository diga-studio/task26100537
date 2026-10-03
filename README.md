# equityrank

汽车经销商权益机会排行榜库。纯进程内实现, 由上层直接调用, 无任何外部服务依赖。

## 功能

- 维护客户清单(编号、车型键、里程、贷款结清金额、是否超贷款期限 75%), 只支持修改已有客户。
- 管理若干不可变估值版本, 每条记录按车型键与左闭右开里程区间确定车辆价值(整数分)。
- 权益 = 车辆价值 - 贷款结清金额; 分层阈值 ±150000 分; 机会分 = 50 + 权益/10000 向下取整,
  超期限 75% 加 10 分, 截断到 [0, 100]。
- 排行榜按机会分降序、权益降序、客户编号升序, 返回前 50 名。
- 三类操作均并发安全:
  - `ApplyUpdates`: 一批客户修改整体提交, 产生全局递增的客户数据修订号。
  - `Switch`: 异步切换/回滚估值版本; 后受理请求取代此前未发布的请求,
    调用方通过 `SwitchRequest.Wait()` 区分最终已发布(`SwitchPublished`)或被取代(`SwitchSuperseded`)。
  - `Leaderboard`: 返回估值版本、客户数据修订号与前 50 名榜单。

## 一致性保证

- 重算期间查询继续使用已发布版本; 新版本发布时包含发布前已提交的全部客户修改。
- 每次查询在单个临界区内捕获 (版本, 修订号, 客户数据快照), 随后在锁外计算,
  因此整份榜单严格等价于查询期间某一时刻、用该版本与该修订号对应的完整客户清单从头计算的结果。
- 被取代的切换请求即使计算完成也不会发布。

## 使用示例

```go
s, err := equityrank.New(customers, versions, "v1", nil)
if err != nil { /* ... */ }

rev, err := s.ApplyUpdates([]equityrank.CustomerUpdate{
    {ID: 42, Mileage: ptrInt64(12000)},
})

req, _ := s.Switch("v2")
status := req.Wait() // SwitchPublished 或 SwitchSuperseded

lb := s.Leaderboard() // lb.Version / lb.Revision / lb.Entries
```

## 运行测试

```sh
go test ./...            # 全部单元测试与规模耗时验证
go test -race ./...      # 含竞态检测
go test -run TestPerfUnderLoad -v .   # 仅性能验证(5 万客户 / 20 版本 / 每版 1 万条)
```

性能验证覆盖: 持续每秒 100 次客户修改 + 100 次排行榜查询时两类调用 p95 < 200ms,
停止提交切换后最后受理的请求在 5 秒内发布。本机实测(Apple Silicon): 修改 p95 约 2ms,
查询 p95 约 10ms, 最后的切换发布约 1ms。
