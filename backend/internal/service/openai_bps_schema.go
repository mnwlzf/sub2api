package service

// BPS 客户端工具参数的 JSON Schema 校验。
//
// 这里刻意不引入第三方 schema 库（go.mod 不可改），只实现客户端工具参数
// 实际会用到的关键字子集，并保持两条硬约束：
//
//   - 外部 $ref 一律拒绝：不联网、不读本地文件，防止客户端用 schema 触发
//     SSRF/文件读取；
//   - 校验深度有上限：避免深层嵌套 schema/参数打爆栈。
//
// 未识别的关键字按 JSON Schema 语义当作注解忽略（annotations），这样新版
// 客户端多带一个描述性关键字不会导致工具声明失败。
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// openAIBPSMaxToolSchemaBytes 是单个工具参数 schema 的大小上限（与信封
	// 上限一致，避免超大 schema 占用内存）。
	openAIBPSMaxToolSchemaBytes = 1 << 20
	// openAIBPSMaxSchemaDepth 是 schema/参数嵌套深度上限。
	openAIBPSMaxSchemaDepth = 64
)

// openAIBPSJSONSchema 是编译后的客户端工具参数 schema。
type openAIBPSJSONSchema struct {
	root map[string]any
}

// openAIBPSCompileJSONSchema 校验并编译一份客户端工具参数 schema。
//
// 编译期就拒绝外部 $ref、非法正则和无法解析的内部指针，保证响应侧校验不会
// 因为 schema 本身的问题而失败（客户端错误应在请求准备阶段以 400 暴露）。
func openAIBPSCompileJSONSchema(raw any) (*openAIBPSJSONSchema, error) {
	schema, ok := raw.(map[string]any)
	if !ok || schema == nil {
		return nil, errors.New("tool parameters schema must be a JSON object")
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, errors.New("tool parameters schema is not JSON encodable")
	}
	if len(encoded) > openAIBPSMaxToolSchemaBytes {
		return nil, fmt.Errorf("tool parameters schema exceeds %d bytes", openAIBPSMaxToolSchemaBytes)
	}
	if err := openAIBPSValidateSchemaRefs(schema, schema, 0); err != nil {
		return nil, err
	}
	return &openAIBPSJSONSchema{root: schema}, nil
}

// Validate 校验一个 JSON 值（解码时使用 json.Number 保留精度）。
func (s *openAIBPSJSONSchema) Validate(value any) error {
	if s == nil || s.root == nil {
		return errors.New("tool parameters schema is missing")
	}
	return openAIBPSValidateSchemaValue(s.root, value, s.root, 0)
}

// openAIBPSValidateSchemaRefs 递归检查 schema 中的 $ref/pattern 是否合法。
func openAIBPSValidateSchemaRefs(node any, root map[string]any, depth int) error {
	if depth > openAIBPSMaxSchemaDepth {
		return errors.New("tool parameters schema is nested too deeply")
	}
	switch value := node.(type) {
	case bool:
		return nil
	case map[string]any:
		if raw, exists := value["$ref"]; exists {
			ref, ok := raw.(string)
			if !ok {
				return errors.New("tool parameters schema $ref must be a string")
			}
			target, err := openAIBPSResolveSchemaRef(ref, root)
			if err != nil {
				return err
			}
			if err := openAIBPSValidateSchemaRefs(target, root, depth+1); err != nil {
				return err
			}
		}
		if raw, exists := value["pattern"]; exists {
			pattern, ok := raw.(string)
			if !ok {
				return errors.New("tool parameters schema pattern must be a string")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return errors.New("tool parameters schema pattern is not a valid regular expression")
			}
		}
		for key, child := range value {
			switch key {
			case "$ref", "pattern", "const", "enum", "default", "examples":
				// const/enum/default 是取值而不是 schema 节点，不递归。
				continue
			}
			if err := openAIBPSValidateSchemaRefs(child, root, depth+1); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, child := range value {
			if err := openAIBPSValidateSchemaRefs(child, root, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		// 字符串/数字/布尔等叶子节点不是 schema 结构，忽略。
		return nil
	}
}

// openAIBPSResolveSchemaRef 解析文档内 JSON Pointer（#/$defs/name 等）。
// 外部 URL、file:// 和裸指针一律拒绝。
func openAIBPSResolveSchemaRef(ref string, root map[string]any) (any, error) {
	if ref == "#" {
		return root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, errors.New("external tool schema references are unsupported")
	}
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var current any = root
	for _, raw := range parts {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			child, ok := node[part]
			if !ok {
				return nil, errors.New("tool parameters schema reference cannot be resolved")
			}
			current = child
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, errors.New("tool parameters schema reference cannot be resolved")
			}
			current = node[index]
		default:
			return nil, errors.New("tool parameters schema reference cannot be resolved")
		}
	}
	return current, nil
}

// openAIBPSValidateSchemaValue 按 schema 校验 JSON 值。
func openAIBPSValidateSchemaValue(schema any, value any, root map[string]any, depth int) error {
	if depth > openAIBPSMaxSchemaDepth {
		return errors.New("tool arguments are nested too deeply")
	}
	switch node := schema.(type) {
	case bool:
		if !node {
			return errors.New("value is rejected by the tool schema")
		}
		return nil
	case map[string]any:
		return openAIBPSValidateSchemaObject(node, value, root, depth)
	default:
		return errors.New("tool parameters schema contains an unsupported node")
	}
}

func openAIBPSValidateSchemaObject(schema map[string]any, value any, root map[string]any, depth int) error {
	if ref, ok := schema["$ref"].(string); ok {
		target, err := openAIBPSResolveSchemaRef(ref, root)
		if err != nil {
			return err
		}
		return openAIBPSValidateSchemaValue(target, value, root, depth+1)
	}
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, sub := range allOf {
			if err := openAIBPSValidateSchemaValue(sub, value, root, depth+1); err != nil {
				return err
			}
		}
	}
	if anyOf, ok := schema["anyOf"].([]any); ok && len(anyOf) > 0 {
		matched := false
		for _, sub := range anyOf {
			if openAIBPSValidateSchemaValue(sub, value, root, depth+1) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("value does not match any allowed tool schema variant")
		}
	}
	if oneOf, ok := schema["oneOf"].([]any); ok && len(oneOf) > 0 {
		matches := 0
		for _, sub := range oneOf {
			if openAIBPSValidateSchemaValue(sub, value, root, depth+1) == nil {
				matches++
			}
		}
		if matches != 1 {
			return errors.New("value must match exactly one tool schema variant")
		}
	}
	if not, exists := schema["not"]; exists {
		if openAIBPSValidateSchemaValue(not, value, root, depth+1) == nil {
			return errors.New("value matches a forbidden tool schema")
		}
	}
	if rawType, exists := schema["type"]; exists {
		types, err := openAIBPSSchemaTypes(rawType)
		if err != nil {
			return err
		}
		matched := false
		for _, typ := range types {
			if openAIBPSJSONTypeMatches(value, typ) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value type does not match the tool schema (%s)", strings.Join(types, "|"))
		}
	}
	if rawEnum, exists := schema["enum"]; exists {
		enum, ok := rawEnum.([]any)
		if !ok {
			return errors.New("tool parameters schema enum must be an array")
		}
		if !openAIBPSJSONValueIn(value, enum) {
			return errors.New("value is not one of the allowed tool schema values")
		}
	}
	if rawConst, exists := schema["const"]; exists {
		if !openAIBPSJSONEqual(value, rawConst) {
			return errors.New("value does not match the tool schema constant")
		}
	}
	if err := openAIBPSValidateSchemaNumber(schema, value); err != nil {
		return err
	}
	if text, ok := value.(string); ok {
		if err := openAIBPSValidateSchemaString(schema, text); err != nil {
			return err
		}
	}
	if object, ok := value.(map[string]any); ok {
		if err := openAIBPSValidateSchemaObjectValue(schema, object, root, depth); err != nil {
			return err
		}
	}
	if array, ok := value.([]any); ok {
		if err := openAIBPSValidateSchemaArrayValue(schema, array, root, depth); err != nil {
			return err
		}
	}
	return nil
}

func openAIBPSSchemaTypes(raw any) ([]string, error) {
	switch value := raw.(type) {
	case string:
		return []string{value}, nil
	case []any:
		types := make([]string, 0, len(value))
		for _, entry := range value {
			typ, ok := entry.(string)
			if !ok {
				return nil, errors.New("tool parameters schema type entries must be strings")
			}
			types = append(types, typ)
		}
		return types, nil
	default:
		return nil, errors.New("tool parameters schema type must be a string or an array")
	}
}

func openAIBPSValidateSchemaNumber(schema map[string]any, value any) error {
	number, ok := openAIBPSNumberValue(value)
	if !ok {
		return nil
	}
	if raw, exists := schema["minimum"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && number < limit {
			return errors.New("value is below the tool schema minimum")
		}
	}
	if raw, exists := schema["maximum"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && number > limit {
			return errors.New("value is above the tool schema maximum")
		}
	}
	if raw, exists := schema["exclusiveMinimum"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && number <= limit {
			return errors.New("value is not above the tool schema exclusive minimum")
		}
	}
	if raw, exists := schema["exclusiveMaximum"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && number >= limit {
			return errors.New("value is not below the tool schema exclusive maximum")
		}
	}
	if raw, exists := schema["multipleOf"]; exists {
		if step, ok := openAIBPSNumberValue(raw); ok && step > 0 {
			quotient := number / step
			if math.Abs(quotient-math.Round(quotient)) > 1e-9 {
				return errors.New("value is not a multiple of the tool schema step")
			}
		}
	}
	return nil
}

func openAIBPSValidateSchemaString(schema map[string]any, text string) error {
	length := len([]rune(text))
	if raw, exists := schema["minLength"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && float64(length) < limit {
			return errors.New("value is shorter than the tool schema minimum length")
		}
	}
	if raw, exists := schema["maxLength"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && float64(length) > limit {
			return errors.New("value is longer than the tool schema maximum length")
		}
	}
	if raw, exists := schema["pattern"]; exists {
		pattern, ok := raw.(string)
		if !ok {
			return errors.New("tool parameters schema pattern must be a string")
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return errors.New("tool parameters schema pattern is not a valid regular expression")
		}
		if !compiled.MatchString(text) {
			return errors.New("value does not match the tool schema pattern")
		}
	}
	return nil
}

func openAIBPSValidateSchemaObjectValue(schema map[string]any, object map[string]any, root map[string]any, depth int) error {
	if raw, exists := schema["minProperties"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && float64(len(object)) < limit {
			return errors.New("value has fewer properties than the tool schema allows")
		}
	}
	if raw, exists := schema["maxProperties"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && float64(len(object)) > limit {
			return errors.New("value has more properties than the tool schema allows")
		}
	}
	if rawRequired, exists := schema["required"]; exists {
		required, ok := rawRequired.([]any)
		if !ok {
			return errors.New("tool parameters schema required must be an array")
		}
		for _, raw := range required {
			name, ok := raw.(string)
			if !ok {
				return errors.New("tool parameters schema required entries must be strings")
			}
			if _, present := object[name]; !present {
				return fmt.Errorf("required tool argument %q is missing", name)
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	patterns, _ := schema["patternProperties"].(map[string]any)
	known := make(map[string]bool, len(properties))
	for name := range properties {
		known[name] = true
	}
	for name, value := range object {
		if property, exists := properties[name]; exists {
			if err := openAIBPSValidateSchemaValue(property, value, root, depth+1); err != nil {
				return err
			}
			continue
		}
		matched := false
		for pattern, property := range patterns {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return errors.New("tool parameters schema patternProperties is not a valid regular expression")
			}
			if compiled.MatchString(name) {
				matched = true
				if err := openAIBPSValidateSchemaValue(property, value, root, depth+1); err != nil {
					return err
				}
			}
		}
		if matched {
			continue
		}
		if rawAdditional, exists := schema["additionalProperties"]; exists {
			switch additional := rawAdditional.(type) {
			case bool:
				if !additional {
					return fmt.Errorf("unexpected tool argument %q", name)
				}
			case map[string]any:
				if err := openAIBPSValidateSchemaValue(additional, value, root, depth+1); err != nil {
					return err
				}
			default:
				return errors.New("tool parameters schema additionalProperties must be a boolean or object")
			}
		}
	}
	return nil
}

func openAIBPSValidateSchemaArrayValue(schema map[string]any, array []any, root map[string]any, depth int) error {
	if raw, exists := schema["minItems"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && float64(len(array)) < limit {
			return errors.New("value has fewer items than the tool schema allows")
		}
	}
	if raw, exists := schema["maxItems"]; exists {
		if limit, ok := openAIBPSNumberValue(raw); ok && float64(len(array)) > limit {
			return errors.New("value has more items than the tool schema allows")
		}
	}
	if raw, exists := schema["uniqueItems"]; exists {
		if unique, ok := raw.(bool); ok && unique {
			for i := 0; i < len(array); i++ {
				for j := i + 1; j < len(array); j++ {
					if openAIBPSJSONEqual(array[i], array[j]) {
						return errors.New("value has duplicate items but the tool schema requires unique items")
					}
				}
			}
		}
	}
	if prefix, ok := schema["prefixItems"].([]any); ok {
		for index, item := range array {
			if index >= len(prefix) {
				break
			}
			if err := openAIBPSValidateSchemaValue(prefix[index], item, root, depth+1); err != nil {
				return err
			}
		}
	}
	if rawItems, exists := schema["items"]; exists {
		switch items := rawItems.(type) {
		case bool, map[string]any:
			// 布尔 schema（true/false）或单个 schema：对每个元素生效。
			for _, item := range array {
				if err := openAIBPSValidateSchemaValue(items, item, root, depth+1); err != nil {
					return err
				}
			}
		case []any:
			// 旧版 tuple 写法：按位置生效，多出的元素不校验。
			for index, item := range array {
				if index >= len(items) {
					break
				}
				if err := openAIBPSValidateSchemaValue(items[index], item, root, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("tool parameters schema items must be a schema or an array of schemas")
		}
	}
	return nil
}

// openAIBPSJSONTypeMatches 判断 JSON 值是否属于某个 JSON Schema type。
func openAIBPSJSONTypeMatches(value any, typ string) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := openAIBPSNumberValue(value)
		return ok
	case "integer":
		number, ok := openAIBPSNumberValue(value)
		return ok && number == math.Trunc(number)
	default:
		return false
	}
}

// openAIBPSNumberValue 把任意数值表示归一化成 float64。
func openAIBPSNumberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case int32:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}

// openAIBPSJSONEqual 比较两个 JSON 值的语义相等性。
func openAIBPSJSONEqual(left, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftData, rightData)
}

func openAIBPSJSONValueIn(value any, candidates []any) bool {
	for _, candidate := range candidates {
		if openAIBPSJSONEqual(value, candidate) {
			return true
		}
	}
	return false
}
