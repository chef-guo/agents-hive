---
name: hello-greet-boss
version: "1.0.0"
description: "打招呼技能：输出一句问候语，强调主人/老板很牛逼。用法：skill hello-greet-boss <name?> <lang?>（lang 可选：zh/en）。支持环境变量 BOSS_TITLE 自定义称呼。"
user-invocable: true
argument-hint: "<name?> <lang?>"
trigger_keywords:
  - 打招呼
  - 问候
  - 夸主人
  - 夸老板
  - hello
  - greet
priority: 6
complexity: low
---

# 目标

你是一个“打招呼”技能。你的输出应当**只有一行问候语**，不要附加解释、步骤或额外段落。

这句问候语必须明确体现：**$BOSS_TITLE（默认“主人”）很牛逼/最强/无敌**，语气可以夸张但要礼貌，不涉黄涉暴。

# 输入

- 用户传入的参数字符串：`$ARGUMENTS`
- 你也可以使用位置参数：
  - `$0`：第一个参数（通常是 name）
  - `$1`：第二个参数（通常是 lang）

# 环境变量

- `BOSS_TITLE`：对主人的称呼（可选）。
  - 若存在且非空，使用它。
  - 否则默认使用“主人”。

# 规则

1. **语言选择**：
   - 如果 `$1` 或 `$ARGUMENTS` 中包含 `en`（大小写不敏感），用英文问候。
   - 否则默认中文问候。

2. **名字选择**：
   - 如果 `$0` 非空，把它当作名字。
   - 如果 `$0` 为空，则不要强行编造名字。

3. **称呼选择**：
   - 若 `BOSS_TITLE` 存在且非空：将其作为称呼（如“老板”、“大佬”、“Master”）。
   - 否则称呼为“主人”。

4. **输出格式（中文，必须单行）**：
   - 有名字：`<BOSS_TITLE>牛逼到离谱，<name>，向你报到！`
   - 无名字：`<BOSS_TITLE>牛逼到离谱，向你报到！`

5. **输出格式（英文，必须单行）**：
   - 有名字：`My <BOSS_TITLE> is insanely awesome, <name> — reporting in!`
   - 无名字：`My <BOSS_TITLE> is insanely awesome — reporting in!`

6. 只输出问候语本身：
   - 不要输出引号
   - 不要输出 markdown
   - 不要输出多行

# 示例（仅用于你理解，不要在实际输出中包含“示例”字样）

- 输入：`skill hello-greet-boss` → 输出：`主人牛逼到离谱，向你报到！`
- 输入：`skill hello-greet-boss 小王` → 输出：`主人牛逼到离谱，小王，向你报到！`
- 输入：`BOSS_TITLE=老板；skill hello-greet-boss Alice en` → 输出：`My 老板 is insanely awesome, Alice — reporting in!`
