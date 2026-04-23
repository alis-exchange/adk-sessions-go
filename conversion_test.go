package sessions

import (
	"testing"

	"google.golang.org/adk/tool/toolconfirmation"
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

func TestPartToProtoSanitizesToolConfirmationStructs(t *testing.T) {
	part := &genai.Part{
		FunctionCall: &genai.FunctionCall{
			ID:   "confirm-1",
			Name: "adk_request_confirmation",
			Args: map[string]any{
				"toolConfirmation": toolconfirmation.ToolConfirmation{
					Hint:      "Approve this tool call",
					Confirmed: false,
					Payload: map[string]any{
						"nestedFunctionCall": &genai.FunctionCall{
							ID:   "tool-1",
							Name: "run_deep_research",
							Args: map[string]any{"prompt": "test"},
						},
					},
				},
			},
		},
	}

	got, err := partToProto(part)
	if err != nil {
		t.Fatalf("partToProto() error = %v", err)
	}

	args := got.GetFunctionCall().GetArgs().AsMap()
	confirmation, ok := args["toolConfirmation"].(map[string]any)
	if !ok {
		t.Fatalf("toolConfirmation type = %T, want map[string]any", args["toolConfirmation"])
	}
	if confirmation["hint"] != "Approve this tool call" {
		t.Fatalf("toolConfirmation.hint = %v, want %q", confirmation["hint"], "Approve this tool call")
	}
}
