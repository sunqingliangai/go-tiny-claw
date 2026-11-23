# go-tiny-claw 项目规划

## 项目概述
这是一个基于 Go 语言开发的 AI 助手项目，支持多种 LLM 提供商（OpenAI、Claude），提供文件操作、命令执行等工具能力。

## 当前项目状态
- 已完成基础架构搭建
- 已实现核心工具：文件读写、编辑、bash命令执行
- 已支持多种 LLM 提供商接口
- 已实现主循环引擎

## 技术架构
```
go-tiny-claw/
├── cmd/claw/main.go          # 主入口
├── internal/
│   ├── context/             # 上下文管理
│   ├── engine/              # 核心引擎层
│   ├── feishu/              # 飞书集成
│   ├── provider/            # LLM 适配层
│   ├── schema/              # 数据结构定义
│   └── tools/               # 工具层
└── README.md
```

## 下一步工作计划
1. 完善错误处理和日志记录
2. 添加更多工具支持
3. 优化性能和用户体验