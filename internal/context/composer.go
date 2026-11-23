package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

type PromptComposer struct {
	workDir     string
	planMode    bool
	taskDir     string
	skillLoader *SkillLoader
}

// NewPromptComposer 按 Session 的计划状态和任务目录生成 System Prompt。
// 与 ch13 快照不同：计划文件放在每会话独立目录，避免多个飞书聊天互相覆盖。
func NewPromptComposer(workDir string, planMode bool, taskDir string) *PromptComposer {
	return &PromptComposer{
		workDir:     workDir,
		planMode:    planMode,
		taskDir:     taskDir,
		skillLoader: NewSkillLoader(workDir),
	}
}

func (c *PromptComposer) Build() schema.Message {
	var promptBuilder strings.Builder

	promptBuilder.WriteString(`# 核心身份
你名叫 go-tiny-claw，一个由驾驭工程驱动的骨灰级研发助手。
你具备极简主义哲学，拒绝废话。你能通过系统提供的内置工具，创建、读取、修改和执行工作区中的代码。

# 核心纪律 (CRITICAL)
1. 如需检查文件是否存在，请使用 bash 的 ls 或 test -f，而不是对目录使用 read_file。
2. 创建新文件时，务必使用 write_file，并同时提供 path 和 content 参数。
3. 编辑文件前务必先读取现有文件，以理解上下文。
4. 无论何时你需要写代码或创建文件，都要直接使用 write_file 工具。
5. 遇到工具执行报错时，仔细阅读 stderr，尝试自己修正命令并重试。
6. 始终用中文回复，以便传达你的进展和想法。
`)

	if c.planMode {
		// 【核心重构】：引入状态嗅探与断点续传的条件分支逻辑。
		taskDir := c.taskDir
		if taskDir == "" {
			// PlanMode 缺少任务目录时使用稳定的默认目录，避免计划写到工作区根目录。
			taskDir = filepath.ToSlash(filepath.Join(".claw", "tasks", "default"))
		}

		planPath := filepath.ToSlash(filepath.Join(taskDir, "PLAN.md"))
		todoPath := filepath.ToSlash(filepath.Join(taskDir, "TODO.md"))

		promptBuilder.WriteString(fmt.Sprintf(`
# 长程任务与状态外部化强制规范 (Plan Mode: ON)

!!! 警告：本模式下，你绝对不能依赖自己的短期记忆。你必须将架构思路和执行进度持久化到当前任务目录。 !!!

当前任务状态目录: %s
计划文件: %s
待办文件: %s

当你收到一条新指令被唤醒时，必须按照以下顺序执行：

**[STEP 1: 强制环境嗅探]**
- 第一时间使用 bash 的 test -f 检查计划文件和待办文件是否存在。
- 分支 A（全新任务）：如果文件不存在，先使用 write_file 创建计划文件，写下你的理解、架构设计和技术选型；再创建待办文件，拆解成 Markdown Checkbox 步骤，例如 "- [ ] 步骤1"。
- 分支 B（断点续传）：如果文件已经存在，绝对不要覆盖。立即使用 read_file 阅读计划和待办，找到第一个未完成项后继续。

**[STEP 2: 严格单步执行与实时打勾]**
- 每完成一个可验证的子任务后，必须立即使用 edit_file 将待办文件中对应项从 "- [ ]" 改为 "- [x]"。
- 不要等所有代码写完后再集中打勾。

**[STEP 3: 迷失时自救]**
- 如果遇到报错或不知道下一步，立即重新读取待办文件确认当前位置。
`, taskDir, planPath, todoPath))
	}

	// 3. 加载项目专属规范 (AGENTS.md)
	agentsMDPath := filepath.Join(c.workDir, "AGENTS.md")
	content, err := os.ReadFile(agentsMDPath)
	if err == nil {
		promptBuilder.WriteString("\n# 项目专属指南 (来自 AGENTS.md)\n")
		promptBuilder.WriteString("以下是当前工作区特有的架构规范与注意事项，你的行为必须绝对符合以下要求：\n")
		promptBuilder.WriteString("```markdown\n")
		promptBuilder.WriteString(string(content))
		promptBuilder.WriteString("\n```\n")
	}

	// 4. 动态加载技能外挂 (Skills)
	skillsContent := c.skillLoader.LoadAll()
	if skillsContent != "" {
		promptBuilder.WriteString(skillsContent)
	}

	return schema.Message{
		Role:    schema.RoleSystem,
		Content: promptBuilder.String(),
	}
}
