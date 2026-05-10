---
name: skill-creator
description: Use when creating, updating, or packaging a new skill, especially when the user needs a local scaffold in ./skills/<name>/ or explicitly requests --global installation.
---

# Skill Creator

为创建 skill 提供本地工作流，只输出可执行的写入步骤，不替用户自动写盘。

## 默认目标

- 默认输出到 `./skills/<name>/`
- 只有用户显式要求 `--global` 时，才切换到全局安装路径
- 普通 skill 调用只返回结构、模板、校验点和文件写入步骤

## 先收集的信息

1. `name`
2. `description`
3. 是否需要 `scripts/`
4. 是否需要 `references/`
5. 是否需要 `assets/`
6. 是否显式要求 `--global`

## 输出内容

返回下面四部分：

1. 目录结构
2. `SKILL.md` frontmatter
3. 模板内容
4. 显式文件写入步骤

## 生成规则

- `name` 只使用小写字母、数字和连字符
- `description` 说明触发条件，不要写成流程说明
- 若用户未要求 `--global`，路径固定为 `./skills/<name>/`
- 不承诺 skill 调用会自动写盘
- 如需真正创建文件，后续必须显式调用 `write_file`、`apply_patch`，或未来专门的 scaffold 工具

## 模板与校验

- 参考 [templates/skill-template.md](templates/skill-template.md)
- 参考 [scripts/validate.sh](scripts/validate.sh)

## 交付格式

优先用简洁清单返回：

- 目标路径
- 目录树
- `SKILL.md` 内容
- 需要创建的附加文件
- 每个文件的写入步骤
- 校验步骤
