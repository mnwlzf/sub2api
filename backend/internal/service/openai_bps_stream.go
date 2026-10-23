package service

// BPS 响应转换与 SSE 集成。
//
// 客户端看到的必须是 OpenAI Responses 协议：文本实时输出；工具调用在完整
// 解析、schema 校验、Redis 保存完成后才以 function_call/custom_tool_call
// 事件交付；上游的 run_officejs 载体名与 OfficeJS 代码绝不泄漏给客户端。
//
// 硬约束：
//   - 一次响应最多交付一个客户端工具调用；
//   - 异常断流补 response.failed，绝不伪造 response.completed；
//   - 上游未知条目/事件按 bps_unsupported_tool/bps_unsupported_event 失败。
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIBPSUsageContextKey 是上游终态响应里 usage 的 gin 上下文键。
//
// 即使响应随后因为结构化校验失败/断流而报错，已取得的 usage 仍会写在这里，
// 供计费/日志中间件读取（结构化输出失败不丢用量）。
const OpenAIBPSUsageContextKey = "openai_bps_usage"

// forwardOpenAIBPSProtocolResponse 把上游响应按 BPS 协议转换后写给客户端。
//
// 返回 error 仅表示本地读写失败（协议错误已在函数内写成 JSON 错误信封或
// response.failed 事件，返回 nil），与阶段 3 的 ForwardOpenAIBPSResponse 一致。
func forwardOpenAIBPSProtocolResponse(ctx context.Context, c *gin.Context, store *OpenAIBPSSessionStore, prepared *openAIBPSPreparedRequest, resp *http.Response) error {
	if c == nil || c.Writer == nil {
		return errors.New("openai bps: forward protocol response: gin context is missing")
	}
	if prepared == nil {
		return errors.New("openai bps: forward protocol response: prepared request is missing")
	}
	if resp == nil || resp.Body == nil {
		return errors.New("openai bps: forward protocol response: upstream response is missing")
	}
	defer func() { _ = resp.Body.Close() }()
	if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
		c.Header("x-request-id", requestID)
	}
	// 上游错误：状态码与响应体原样透传（BPS 错误体已是 OpenAI 兼容信封）。
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(openAIBPSMaxJSONResponseBytes)+1))
		if err != nil {
			return fmt.Errorf("openai bps: read upstream error body: %w", err)
		}
		if len(data) > openAIBPSMaxJSONResponseBytes {
			return fmt.Errorf("openai bps: upstream error body exceeds %d bytes", openAIBPSMaxJSONResponseBytes)
		}
		if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
			c.Header("Content-Type", contentType)
		}
		c.Status(resp.StatusCode)
		_, err = c.Writer.Write(data)
		return err
	}

	pipeline := &openAIBPSResponsePipeline{
		ctx:          ctx,
		c:            c,
		store:        store,
		prepared:     prepared,
		seenItems:    map[string]bool{},
		pendingTools: map[string]bool{},
	}
	err := openAIBPSReadResponseEvents(resp, openAIBPSMaxSSEEventBytes, pipeline.handle)
	if err == nil && !pipeline.completed {
		err = infraerrors.Newf(http.StatusBadGateway, openAIBPSStreamInterruptedCode,
			"BPS upstream stream ended before response.completed")
	}
	if err == nil {
		return nil
	}
	if pipeline.started {
		// 客户端已收到 SSE 事件：只能以 response.failed 终止，绝不能补 completed。
		return pipeline.writeFailure(err)
	}
	WriteOpenAIBPSError(c, openAIBPSNormalizeProtocolError(err))
	return nil
}

// openAIBPSNormalizeProtocolError 把内部/上游解析错误收敛为对外的 BPS 错误码。
func openAIBPSNormalizeProtocolError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) {
		return err
	}
	return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse,
		"BPS returned a malformed response").WithCause(err)
}

// openAIBPSResponsePipeline 是响应转换状态机（一次请求一个实例）。
type openAIBPSResponsePipeline struct {
	ctx        context.Context
	c          *gin.Context
	store      *OpenAIBPSSessionStore
	prepared   *openAIBPSPreparedRequest
	started    bool
	completed  bool
	sequence   int
	responseID string
	seenItems  map[string]bool
	// pendingTools 记录上游流里已经出现过、但终态响应必须仍然包含的工具 item。
	pendingTools map[string]bool
}

// openAIBPSReadResponseEvents 统一读取 SSE 与 unary JSON 响应。
//
// SSE 事件按空行边界分发，支持多行 data；JSON 响应被包装成一个 completed/
// failed 事件。绝不在干净 EOF 时伪造终止事件：EOF 前没有终止事件时返回 nil，
// 由调用方判定为断流。
func openAIBPSReadResponseEvents(resp *http.Response, maxBytes int, consume func(string, map[string]any) error) error {
	if maxBytes <= 0 {
		maxBytes = openAIBPSMaxSSEEventBytes
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
		if err != nil {
			return err
		}
		if len(data) > maxBytes {
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS response exceeds the maximum size")
		}
		var response map[string]any
		if openAIBPSDecode(data, &response) != nil || response == nil {
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS returned a malformed JSON response")
		}
		typ := "response.completed"
		if stringValue(response["status"]) != "completed" {
			typ = "response.failed"
		}
		return consume(typ, map[string]any{"type": typ, "response": response})
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), maxBytes)
	var (
		eventName  string
		dataLines  []string
		eventBytes int
		terminal   bool
	)
	dispatch := func() error {
		typ := openAIBPSSSEEventType(eventName, dataLines)
		payloadText := strings.TrimSpace(strings.Join(dataLines, "\n"))
		eventName = ""
		dataLines = nil
		eventBytes = 0
		if payloadText == "" || payloadText == "[DONE]" {
			return nil
		}
		var payload map[string]any
		if openAIBPSDecode([]byte(payloadText), &payload) != nil || payload == nil {
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS returned a malformed SSE event")
		}
		if typ == "" {
			typ = stringValue(payload["type"])
		}
		if typ == "" {
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS returned an SSE event without a type")
		}
		if err := consume(typ, payload); err != nil {
			return err
		}
		terminal = openAIBPSTerminalSSEEvent(typ)
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			if terminal {
				return nil
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			eventBytes += len(line) + 1
			if eventBytes > maxBytes {
				return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS SSE event exceeds the maximum size")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(dataLines) > 0 || eventName != "" {
		return dispatch()
	}
	return nil
}

// handle 处理一个上游事件。
func (p *openAIBPSResponsePipeline) handle(typ string, payload map[string]any) error {
	if p.completed {
		return nil
	}
	switch typ {
	case "response.failed", "response.incomplete", "error":
		if response, ok := payload["response"].(map[string]any); ok {
			// 上游已经给出的 usage 必须保留：失败/校验不通过也要能计费。
			p.recordUsage(response)
		}
		return openAIBPSUpstreamProtocolError(OpenAIBPSErrorResponseFailed, "BPS failed to complete the response")
	case "response.completed", "response.done":
		response, ok := payload["response"].(map[string]any)
		if !ok || stringValue(response["status"]) != "completed" {
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS terminal event is not a completed response")
		}
		p.recordUsage(response)
		if err := p.checkPendingTools(response); err != nil {
			return err
		}
		transformed, err := openAIBPSTransformResponse(p.ctx, p.store, p.prepared, response)
		if err != nil {
			return err
		}
		if !p.prepared.Stream {
			p.c.JSON(http.StatusOK, transformed)
			p.completed = true
			return nil
		}
		if err := p.emitCompleted(transformed); err != nil {
			return err
		}
		p.completed = true
		return nil
	}

	item, _ := payload["item"].(map[string]any)
	itemKind := stringValue(item["type"])
	if typ == "response.output_item.done" && (itemKind == "function_call" || itemKind == "custom_tool_call") {
		if len(p.pendingTools) >= 1024 {
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidToolCall, "BPS returned too many pending tool items")
		}
		p.pendingTools[openAIBPSToolItemKey(item)] = true
	}
	// 压缩请求：摘要文本绝不能出现在客户端可见事件里，只交付终态压缩引用。
	if p.prepared.Compact {
		return nil
	}
	// 结构化输出：最终文本/拒答事件缓冲到终态校验通过后再交付（普通文本仍
	// 实时转发）。response.created 等非文本事件照常开始 SSE，因此校验失败时
	// 客户端收到 response.failed 而不是伪造的 completed。
	if p.prepared.Structured != nil && openAIBPSStructuredMessageEvent(typ, item) {
		return nil
	}
	// 原生工具事件一律缓冲到终态：只有解析/校验/落库都成功后客户端才能看到。
	if strings.HasPrefix(typ, "response.function_call_arguments.") || strings.HasPrefix(typ, "response.custom_tool_call_input.") {
		return nil
	}
	if item != nil {
		switch itemKind {
		case "function_call", "custom_tool_call":
			return nil
		case "message", "reasoning":
			if typ == "response.output_item.done" {
				p.seenItems[stringValue(item["id"])] = true
			}
		default:
			return openAIBPSUpstreamProtocolError(OpenAIBPSErrorUnsupportedTool, "BPS returned an unsupported output item")
		}
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if id := stringValue(response["id"]); id != "" {
			p.responseID = id
		}
		// 不向上游模型名/内部条目泄漏给客户端。
		response["model"] = p.prepared.ClientModel
		response["output"] = []any{}
		if p.prepared.Structured != nil {
			// 返回客户端保留请求声明的格式（response.created/completed 一致）。
			delete(response, "output_text")
			response["text"] = map[string]any{"format": p.prepared.Structured.Format}
		}
	}
	if !openAIBPSSafeStreamingEvent(typ) {
		return openAIBPSUpstreamProtocolError(OpenAIBPSErrorUnsupportedEvent, "BPS returned an unsupported streaming event")
	}
	return p.emit(typ, payload)
}

// recordUsage 把上游已经给出的 usage 写进 gin 上下文：即使随后因为结构化
// 校验失败/断流返回错误，调用方（计费/日志中间件）仍能读到这次用量。
func (p *openAIBPSResponsePipeline) recordUsage(response map[string]any) {
	if p == nil || p.c == nil || response == nil {
		return
	}
	if usage, exists := response["usage"]; exists && usage != nil {
		p.c.Set(OpenAIBPSUsageContextKey, usage)
	}
}

// checkPendingTools 校验上游流里出现过的工具 item 在终态响应中仍然存在。
func (p *openAIBPSResponsePipeline) checkPendingTools(response map[string]any) error {
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if kind := stringValue(item["type"]); kind != "function_call" && kind != "custom_tool_call" {
			continue
		}
		delete(p.pendingTools, openAIBPSToolItemKey(item))
	}
	if len(p.pendingTools) > 0 {
		return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidToolCall, "BPS completed response omitted an original tool item")
	}
	return nil
}

// emitCompleted 输出终态响应：工具 item 在转换/校验/落库完成后才以事件交付。
func (p *openAIBPSResponsePipeline) emitCompleted(transformed map[string]any) error {
	if !p.started {
		initial := openAIBPSCloneMap(transformed)
		initial["output"] = []any{}
		initial["status"] = "in_progress"
		if err := p.emit("response.created", map[string]any{"response": initial}); err != nil {
			return err
		}
	}
	output, _ := transformed["output"].([]any)
	for index, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := stringValue(item["type"])
		if kind != "function_call" && kind != "custom_tool_call" {
			if p.seenItems[stringValue(item["id"])] {
				continue
			}
			if err := p.emitBufferedItem(index, item); err != nil {
				return err
			}
			continue
		}
		added := openAIBPSCloneMap(item)
		added["status"] = "in_progress"
		field, prefix := "arguments", "response.function_call_arguments"
		if kind == "custom_tool_call" {
			field, prefix = "input", "response.custom_tool_call_input"
		}
		added[field] = ""
		if err := p.emit("response.output_item.added", map[string]any{"output_index": index, "item": added}); err != nil {
			return err
		}
		if err := p.emit(prefix+".delta", map[string]any{"item_id": item["id"], "output_index": index, "delta": item[field]}); err != nil {
			return err
		}
		if err := p.emit(prefix+".done", map[string]any{"item_id": item["id"], "output_index": index, field: item[field]}); err != nil {
			return err
		}
		if err := p.emit("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return err
		}
	}
	return p.emit("response.completed", map[string]any{"response": transformed})
}

// emitBufferedItem 为上游没有逐项流式输出的条目补齐事件序列。
func (p *openAIBPSResponsePipeline) emitBufferedItem(index int, item map[string]any) error {
	added := openAIBPSCloneMap(item)
	added["status"] = "in_progress"
	if stringValue(item["type"]) == "message" {
		added["content"] = []any{}
	}
	if err := p.emit("response.output_item.added", map[string]any{"output_index": index, "item": added}); err != nil {
		return err
	}
	if stringValue(item["type"]) == "message" {
		parts, _ := item["content"].([]any)
		for partIndex, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			partType := stringValue(part["type"])
			if partType != "output_text" && partType != "refusal" {
				continue
			}
			field, prefix := "text", "response.output_text"
			if partType == "refusal" {
				field, prefix = "refusal", "response.refusal"
			}
			fields := func() map[string]any {
				return map[string]any{"item_id": item["id"], "output_index": index, "content_index": partIndex}
			}
			payload := fields()
			empty := openAIBPSCloneMap(part)
			empty[field] = ""
			payload["part"] = empty
			if err := p.emit("response.content_part.added", payload); err != nil {
				return err
			}
			payload = fields()
			payload["delta"] = part[field]
			if err := p.emit(prefix+".delta", payload); err != nil {
				return err
			}
			payload = fields()
			payload[field] = part[field]
			if err := p.emit(prefix+".done", payload); err != nil {
				return err
			}
			payload = fields()
			payload["part"] = part
			if err := p.emit("response.content_part.done", payload); err != nil {
				return err
			}
		}
	}
	return p.emit("response.output_item.done", map[string]any{"output_index": index, "item": item})
}

// emit 写一个 SSE 事件并 flush；非流式客户端直接丢弃（终态用 JSON 写出）。
func (p *openAIBPSResponsePipeline) emit(typ string, payload map[string]any) error {
	if !p.prepared.Stream {
		return nil
	}
	if err := p.ensureSSEStarted(); err != nil {
		return err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["type"] = typ
	payload["sequence_number"] = p.sequence
	p.sequence++
	if response, ok := payload["response"].(map[string]any); ok {
		if id := stringValue(response["id"]); id != "" {
			p.responseID = id
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("openai bps: encode sse event: %w", err)
	}
	if _, err := fmt.Fprintf(p.c.Writer, "event: %s\ndata: %s\n\n", typ, data); err != nil {
		return fmt.Errorf("openai bps: write sse event: %w", err)
	}
	p.flush()
	return nil
}

// ensureSSEStarted 在第一次写事件前设置 SSE 响应头与 200 状态码。
func (p *openAIBPSResponsePipeline) ensureSSEStarted() error {
	if p.started {
		return nil
	}
	p.c.Header("Content-Type", "text/event-stream")
	p.c.Header("Cache-Control", "no-cache")
	p.c.Header("X-Accel-Buffering", "no")
	p.c.Status(http.StatusOK)
	p.started = true
	return nil
}

func (p *openAIBPSResponsePipeline) flush() {
	if flusher, ok := p.c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// writeFailure 在已开始的 SSE 流上补 response.failed 终止事件。
func (p *openAIBPSResponsePipeline) writeFailure(err error) error {
	if ensureErr := p.ensureSSEStarted(); ensureErr != nil {
		return ensureErr
	}
	_, status := infraerrors.ToHTTP(openAIBPSNormalizeProtocolError(err))
	code := strings.TrimSpace(status.Reason)
	if code == "" {
		code = OpenAIBPSErrorUpstream
	}
	message := strings.TrimSpace(status.Message)
	if message == "" {
		message = "BPS upstream request failed"
	}
	payload := map[string]any{
		"type":            "response.failed",
		"sequence_number": p.sequence,
		"response": map[string]any{
			"id":     openAIBPSFailureResponseID(p.responseID),
			"object": "response",
			"status": "failed",
			"error": map[string]any{
				"type":    "server_error",
				"code":    code,
				"message": message,
			},
		},
	}
	p.sequence++
	data, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		return fmt.Errorf("openai bps: encode stream failure: %w", encodeErr)
	}
	if _, writeErr := fmt.Fprintf(p.c.Writer, "event: response.failed\ndata: %s\n\n", data); writeErr != nil {
		return fmt.Errorf("openai bps: write stream failure: %w", writeErr)
	}
	p.flush()
	return nil
}

// openAIBPSToolItemKey 生成流内工具 item 的稳定身份键（id + call_id）。
func openAIBPSToolItemKey(item map[string]any) string {
	return stringValue(item["id"]) + "\x00" + stringValue(item["call_id"])
}

// openAIBPSCloneMap 浅拷贝 map（事件 payload 需要就地改写而不污染原值）。
func openAIBPSCloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// openAIBPSSafeStreamingEvent 报告哪些上游事件可以原样转发给客户端。
func openAIBPSSafeStreamingEvent(typ string) bool {
	switch typ {
	case "response.created", "response.in_progress", "response.queued",
		"response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.output_text.annotation.added",
		"response.refusal.delta", "response.refusal.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done":
		return true
	default:
		return false
	}
}

// openAIBPSFailureResponseID 返回 response.failed 事件里的 response id 兜底。
func openAIBPSFailureResponseID(responseID string) string {
	if strings.TrimSpace(responseID) == "" {
		return "resp_bps_error"
	}
	return responseID
}
