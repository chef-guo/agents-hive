---
name: <skill-name>
description: Use when <triggering conditions for this skill>.
---

# <Skill Name>

## Overview

一句话说明这个 skill 的边界和用途。

## Inputs

- `name`
- `description`
- `--global` 是否显式启用
- 需要的资源目录：`scripts/`、`references/`、`assets/`

## Output Plan

1. 目标目录：`./skills/<name>/`
2. 生成 `SKILL.md`
3. 按需创建资源目录
4. 明确列出每个文件的写入动作

## Write Steps

1. 创建目录 `./skills/<name>/`
2. 写入 `./skills/<name>/SKILL.md`
3. 如需要，再写入 `./skills/<name>/scripts/`
4. 如需要，再写入 `./skills/<name>/references/`
5. 如需要，再写入 `./skills/<name>/assets/`

## Notes

- 默认不写入全局路径
- 只有显式 `--global` 时才切换安装位置
- 不依赖 skill 调用自动执行写盘
