// internal/tools/registry.go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

// BaseTool 是所有具体工具必须实现的通用接口
type BaseTool interface {
	// Name 返回工具的全局唯一名称 (大模型通过这个名字调用它)
	Name() string

	// Definition 返回用于提交给大模型的工具元信息和参数 JSON Schema
	Definition() schema.ToolDefinition

	// Execute 接收大模型吐出的 JSON 参数，执行具体业务逻辑
	// 注意：参数是 json.RawMessage，反序列化由各个具体工具内部自行处理
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry 定义了工具的注册与分发接口
type Registry interface {
	// Register 挂载一个新的工具到系统中
	Register(tool BaseTool)

	// Use 挂载一个执行前 Middleware。
	Use(middleware MiddlewareFunc)

	// GetAvailableTools 返回当前系统挂载的所有工具的 Schema，供 Main Loop 交给 Provider
	GetAvailableTools() []schema.ToolDefinition

	// Execute 实际路由并执行模型请求的工具调用
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

// registryImpl 是 Registry 接口的默认实现
type registryImpl struct {
	// 使用 map 以工具的 Name 作为 Key 进行快速 O(1) 路由查找
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		log.Printf("[Warning] 工具 '%s' 已经被注册，将被覆盖。\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 成功挂载工具: %s\n", name)
}

func (r *registryImpl) Use(middleware MiddlewareFunc) {
	if middleware == nil {
		return
	}
	r.middlewares = append(r.middlewares, middleware)
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	var defs []schema.ToolDefinition
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 1. 路由查找：如果在注册表中找不到该工具，这是模型产生了幻觉，直接向模型抛出错误
	tool, exists := r.tools[call.Name]
	if !exists {
		errMsg := fmt.Sprintf("Error: 系统中不存在名为 '%s' 的工具。", call.Name)
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     errMsg,
			IsError:    true, // 标记为错误，模型看到后会尝试纠正
			ErrorCode:  schema.ErrorCodeToolNotFound,
		}
	}

	// 2. 【核心防线】先执行策略与人工审批，任何拒绝都不能进入 Tool.Execute。
	if denied := r.authorize(ctx, call); denied != nil {
		return *denied
	}

	// 3. 执行工具逻辑：将原始的 JSON 字节流直接丢给具体工具
	output, err := tool.Execute(ctx, call.Arguments)

	// 4. 封装结果：将执行结果或底层物理错误封装后返回给 Main Loop
	if err != nil {
		var toolErr *ToolError
		if errors.As(err, &toolErr) {
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     toolErr.ModelOutput(call.Name),
				IsError:    true,
				ErrorCode:  toolErr.Code,
				Retryable:  toolErr.Retryable,
			}
		}

		errMsg := fmt.Sprintf("Error executing %s: %v", call.Name, err)
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     errMsg,
			IsError:    true,
			ErrorCode:  schema.ErrorCodeToolInternal,
		}
	}

	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     output,
		IsError:    false,
	}
}

func (r *registryImpl) authorize(ctx context.Context, call schema.ToolCall) *schema.ToolResult {
	for _, middleware := range r.middlewares {
		decision, err := middleware(ctx, call)
		if err != nil {
			result := blockedToolResult(
				call,
				schema.ErrorCodeToolInternal,
				fmt.Sprintf("策略检查失败，已拒绝执行: %v", err),
			)
			return &result
		}

		switch decision.Effect {
		case PolicyAllow:
			continue

		case PolicyDeny:
			code := decision.ErrorCode
			if code == "" {
				code = schema.ErrorCodePolicyDenied
			}
			reason := decision.Reason
			if reason == "" {
				reason = "该操作被安全策略拒绝。"
			}
			result := blockedToolResult(call, code, reason)
			return &result

		case PolicyAsk:
			approver, ok := approverFromContext(ctx)
			if !ok {
				result := blockedToolResult(
					call,
					schema.ErrorCodeApprovalUnavailable,
					"该操作需要人工审批，但当前运行没有可用的审批通道。",
				)
				return &result
			}

			approval, err := approver.RequestApproval(ctx, ApprovalRequest{
				ToolCall: call,
				Reason:   decision.Reason,
			})
			if err != nil {
				code := schema.ErrorCodeApprovalUnavailable
				if ctx.Err() != nil {
					code = schema.ErrorCodeApprovalCanceled
				}
				result := blockedToolResult(
					call,
					code,
					fmt.Sprintf("审批通道失败，操作未执行: %v", err),
				)
				return &result
			}
			if approval.Allowed {
				if ctx.Err() != nil {
					result := blockedToolResult(
						call,
						schema.ErrorCodeApprovalCanceled,
						"审批已返回，但 Agent 运行已取消，操作未执行。",
					)
					return &result
				}
				if approval.Fingerprint != "" &&
					approval.Fingerprint != ToolCallFingerprint(call) {
					result := blockedToolResult(
						call,
						schema.ErrorCodeApprovalMismatch,
						"审批绑定的工具动作与当前调用不一致，已拒绝执行。",
					)
					return &result
				}
				log.Printf("[Registry] ✅ 工具 %s 已获人工批准\n", call.Name)
				continue
			}

			code := approval.Code
			if code == "" {
				code = schema.ErrorCodeApprovalRejected
			}
			reason := approval.Reason
			if reason == "" {
				reason = "人工审批未通过，操作已拒绝。"
			}
			result := blockedToolResult(call, code, reason)
			return &result

		default:
			result := blockedToolResult(
				call,
				schema.ErrorCodePolicyDenied,
				"策略返回了未知结果，按 fail-closed 原则拒绝执行。",
			)
			return &result
		}
	}
	return nil
}

func blockedToolResult(call schema.ToolCall, code schema.ErrorCode, reason string) schema.ToolResult {
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     fmt.Sprintf("执行被系统拦截 [%s]: %s", code, reason),
		IsError:    true,
		ErrorCode:  code,
		Retryable:  false,
	}
}
