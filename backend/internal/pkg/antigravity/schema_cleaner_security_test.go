//go:build unit

package antigravity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanJSONSchemaRejectsSelfReferentialDefinitionWithoutRecursing(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"node": map[string]any{"$ref": "#/$defs/Node"},
		},
		"$defs": map[string]any{
			"Node": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"next": map[string]any{"$ref": "#/$defs/Node"},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(schema)
	require.Equal(t, safeJSONSchemaFallback(), cleaned)
}

func TestCleanJSONSchemaRejectsMutuallyRecursiveDefinitions(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		"$ref": "#/$defs/A",
		"$defs": map[string]any{
			"A": map[string]any{"$ref": "#/$defs/B"},
			"B": map[string]any{"$ref": "#/$defs/A"},
		},
	}

	cleaned := CleanJSONSchema(schema)
	require.Equal(t, safeJSONSchemaFallback(), cleaned)
}

func TestJSONSchemaWithinLimitsRejectsExcessiveDepth(t *testing.T) {
	t.Parallel()

	root := map[string]any{"type": "object"}
	current := root
	for i := 0; i <= maxJSONSchemaDepth; i++ {
		next := map[string]any{"type": "object"}
		current["properties"] = map[string]any{"next": next}
		current = next
	}

	require.False(t, jsonSchemaWithinLimits(root))
	require.Equal(t, safeJSONSchemaFallback(), CleanJSONSchema(root))
}

func TestCleanJSONSchemaStillExpandsBoundedLocalReferences(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"first":  map[string]any{"$ref": "#/$defs/Name"},
			"second": map[string]any{"$ref": "#/$defs/Name"},
		},
		"$defs": map[string]any{
			"Name": map[string]any{"type": "string", "description": "display name"},
		},
	}

	cleaned := CleanJSONSchema(schema)
	properties, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"first", "second"} {
		property, ok := properties[key].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "string", property["type"])
		require.NotContains(t, property, "$ref")
	}
}
