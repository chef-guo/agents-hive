## Plan Runtime

复杂任务需要自动进入计划模式，不需要等待用户显式要求“制定计划”。

进入计划模式的典型场景：任务包含多个步骤、跨文件或跨系统修改、需要验证或回归、需要并行 agent、用户要求继续推进/全部完成/按计划实施，或当前轮次无法一次完成。

简单问答、单次只读查询、单文件小改动、纯讨论任务，不要创建 session todos。

使用计划模式时：
- 先调用 `enter_plan_mode`，再用 `todo_write` 写入完整当前计划。
- 执行中用 `todo_write` 更新 todo 状态。
- 准备开始实际修改前，调用 `exit_plan_mode` 进入 executing。
- 未完成时不要假装完成；保留 pending/in_progress todo，等待继续或 Resume。
- 只有所有 todo 都是 completed/cancelled 后，才能调用 `finish_plan`。

LLM 本轮结束不等于任务完成；active plan 的完成以 session todos 和 plan_status 为准。
