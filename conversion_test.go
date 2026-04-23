package sessions

import (
	"testing"

	"google.golang.org/genai"
)

func TestPartToProtoSanitizesNestedFunctionCalls(t *testing.T) {
	part := &genai.Part{
		FunctionCall: &genai.FunctionCall{
			ID:   "confirm-1",
			Name: "adk_request_confirmation",
			Args: map[string]any{
				"originalFunctionCall": &genai.FunctionCall{
					ID:   "tool-1",
					Name: "run_deep_research",
					Args: map[string]any{"prompt": "test"},
				},
			},
		},
	}

	got, err := partToProto(part)
	if err != nil {
		t.Fatalf("partToProto() error = %v", err)
	}

	args := got.GetFunctionCall().GetArgs().AsMap()
	original, ok := args["originalFunctionCall"].(map[string]any)
	if !ok {
		t.Fatalf("originalFunctionCall type = %T, want map[string]any", args["originalFunctionCall"])
	}
	if original["name"] != "run_deep_research" {
		t.Fatalf("originalFunctionCall.name = %v, want run_deep_research", original["name"])
	}
}
