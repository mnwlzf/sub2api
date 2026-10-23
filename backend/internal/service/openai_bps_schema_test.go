package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func bpsDecodeSchemaValue(t *testing.T, raw string) any {
	t.Helper()
	var value any
	require.NoError(t, openAIBPSDecode([]byte(raw), &value))
	return value
}

func TestOpenAIBPSSchemaValidatesFunctionArguments(t *testing.T) {
	schema, err := openAIBPSCompileJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "minLength": 1, "pattern": "^[a-z]+$"},
			"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
			"mode":    map[string]any{"enum": []any{"fast", "slow"}},
			"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 2, "uniqueItems": true},
		},
		"required":             []any{"command"},
		"additionalProperties": false,
	})
	require.NoError(t, err)

	require.NoError(t, schema.Validate(bpsDecodeSchemaValue(t, `{"command":"pwd"}`)))
	require.NoError(t, schema.Validate(bpsDecodeSchemaValue(t, `{"command":"ls","limit":3,"mode":"fast","tags":["a","b"]}`)))

	for _, raw := range []string{
		`{}`,                                 // 缺少必填
		`{"command":123}`,                    // 类型错误
		`{"command":""}`,                     // minLength
		`{"command":"PWD"}`,                  // pattern
		`{"command":"pwd","extra":true}`,     // additionalProperties=false
		`{"command":"pwd","limit":0}`,        // minimum
		`{"command":"pwd","limit":11}`,       // maximum
		`{"command":"pwd","limit":1.5}`,      // integer
		`{"command":"pwd","mode":"medium"}`,  // enum
		`{"command":"pwd","tags":["a","a"]}`, // uniqueItems
		`{"command":"pwd","tags":["a","b","c"]}`,
	} {
		require.Error(t, schema.Validate(bpsDecodeSchemaValue(t, raw)), raw)
	}
}

func TestOpenAIBPSSchemaCombinatorsAndRefs(t *testing.T) {
	schema, err := openAIBPSCompileJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{"type": "string"},
			"item": map[string]any{"$ref": "#/$defs/item"},
			"one": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "integer"},
				},
			},
			"all": map[string]any{
				"allOf": []any{
					map[string]any{"type": "number"},
					map[string]any{"minimum": 2},
				},
			},
		},
		"$defs": map[string]any{
			"item": map[string]any{"type": "object", "required": []any{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		},
	})
	require.NoError(t, err)
	require.NoError(t, schema.Validate(bpsDecodeSchemaValue(t, `{"kind":"a","item":{"name":"x"},"one":3,"all":2}`)))
	require.Error(t, schema.Validate(bpsDecodeSchemaValue(t, `{"item":{}}`)))
	require.Error(t, schema.Validate(bpsDecodeSchemaValue(t, `{"one":true}`)))
	require.Error(t, schema.Validate(bpsDecodeSchemaValue(t, `{"all":1}`)))

	// 外部 $ref 在编译期就被拒绝：绝不联网/读文件系统。
	for _, ref := range []string{"file:///etc/passwd", "https://example.com/schema.json", "schema.json", "#/missing"} {
		_, err := openAIBPSCompileJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"x": map[string]any{"$ref": ref},
			},
		})
		require.Error(t, err, ref)
	}
	// 非法正则同样在编译期拒绝。
	_, err = openAIBPSCompileJSONSchema(map[string]any{"type": "string", "pattern": "["})
	require.Error(t, err)
}

func TestOpenAIBPSSchemaLimitsAndPrecision(t *testing.T) {
	// 大整数按 json.Number 校验，不经过 float64 丢精度。
	schema, err := openAIBPSCompileJSONSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "integer"}},
	})
	require.NoError(t, err)
	require.NoError(t, schema.Validate(bpsDecodeSchemaValue(t, `{"id":9007199254740993}`)))
	require.Error(t, schema.Validate(bpsDecodeSchemaValue(t, `{"id":1.5}`)))

	// 超大 schema 拒绝（1 MiB 上限）。
	_, err = openAIBPSCompileJSONSchema(map[string]any{"description": strings.Repeat("x", openAIBPSMaxToolSchemaBytes+1)})
	require.Error(t, err)

	// 深层嵌套 schema 在编译深度上限处失败，而不是栈溢出。
	deepSchema := any(map[string]any{"type": "string"})
	for i := 0; i < openAIBPSMaxSchemaDepth+8; i++ {
		deepSchema = map[string]any{"type": "object", "properties": map[string]any{"a": deepSchema}}
	}
	_, err = openAIBPSCompileJSONSchema(deepSchema)
	require.Error(t, err)

	// 未识别关键字按注解忽略（兼容新版客户端多带的描述字段）。
	annotated, err := openAIBPSCompileJSONSchema(map[string]any{
		"type":                 "object",
		"x-client-annotation":  map[string]any{"anything": true},
		"additionalProperties": true,
	})
	require.NoError(t, err)
	require.NoError(t, annotated.Validate(bpsDecodeSchemaValue(t, `{"anything":1}`)))
}
