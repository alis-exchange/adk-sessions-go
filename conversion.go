package sessions

import (
	"time"

	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/structpb"

	pb "go.alis.build/common/alis/adk/sessions/v1"
)

func partToProto(in *genai.Part) (*pb.Part, error) {
	if in == nil {
		return &pb.Part{}, nil
	}
	out := &pb.Part{
		Thought:          in.Thought,
		ThoughtSignature: append([]byte(nil), in.ThoughtSignature...),
	}
	switch {
	case in.Text != "":
		out.Data = &pb.Part_Text{Text: in.Text}
	case in.InlineData != nil:
		out.Data = &pb.Part_InlineData{InlineData: &pb.Blob{MimeType: in.InlineData.MIMEType, Data: in.InlineData.Data}}
	case in.FileData != nil:
		out.Data = &pb.Part_FileData{FileData: &pb.FileData{MimeType: in.FileData.MIMEType, FileUri: in.FileData.FileURI}}
	case in.FunctionCall != nil:
		args, err := structpb.NewStruct(sanitizeMap(in.FunctionCall.Args))
		if err != nil {
			return nil, err
		}
		out.Data = &pb.Part_FunctionCall{FunctionCall: &pb.FunctionCall{Name: in.FunctionCall.Name, Args: args, Id: in.FunctionCall.ID}}
	case in.FunctionResponse != nil:
		resp, err := structpb.NewStruct(sanitizeMap(in.FunctionResponse.Response))
		if err != nil {
			return nil, err
		}
		out.Data = &pb.Part_FunctionResponse{FunctionResponse: &pb.FunctionResponse{Name: in.FunctionResponse.Name, Response: resp, Id: in.FunctionResponse.ID}}
	case in.ExecutableCode != nil:
		out.Data = &pb.Part_ExecutableCode{ExecutableCode: &pb.ExecutableCode{Code: in.ExecutableCode.Code, Language: languageToProto(in.ExecutableCode.Language)}}
	case in.CodeExecutionResult != nil:
		out.Data = &pb.Part_CodeExecutionResult{CodeExecutionResult: &pb.CodeExecutionResult{Outcome: outcomeToProto(in.CodeExecutionResult.Outcome), Output: in.CodeExecutionResult.Output}}
	}
	if in.VideoMetadata != nil {
		out.Metadata = &pb.Part_VideoMetadata{VideoMetadata: &pb.VideoMetadata{
			StartOffset: durationToProto(in.VideoMetadata.StartOffset),
			EndOffset:   durationToProto(in.VideoMetadata.EndOffset),
		}}
	}
	return out, nil
}

func contentFromProto(in *pb.Content) *genai.Content {
	if in == nil {
		return nil
	}
	out := &genai.Content{Role: in.GetRole()}
	for _, part := range in.GetParts() {
		out.Parts = append(out.Parts, partFromProto(part))
	}
	return out
}

func partFromProto(in *pb.Part) *genai.Part {
	if in == nil {
		return nil
	}
	out := &genai.Part{
		Thought:          in.GetThought(),
		ThoughtSignature: append([]byte(nil), in.GetThoughtSignature()...),
	}
	switch data := in.GetData().(type) {
	case *pb.Part_Text:
		out.Text = data.Text
	case *pb.Part_InlineData:
		out.InlineData = &genai.Blob{MIMEType: data.InlineData.GetMimeType(), Data: data.InlineData.GetData()}
	case *pb.Part_FileData:
		out.FileData = &genai.FileData{MIMEType: data.FileData.GetMimeType(), FileURI: data.FileData.GetFileUri()}
	case *pb.Part_FunctionCall:
		out.FunctionCall = &genai.FunctionCall{Name: data.FunctionCall.GetName(), Args: structMap(data.FunctionCall.GetArgs()), ID: data.FunctionCall.GetId()}
	case *pb.Part_FunctionResponse:
		out.FunctionResponse = &genai.FunctionResponse{Name: data.FunctionResponse.GetName(), Response: structMap(data.FunctionResponse.GetResponse()), ID: data.FunctionResponse.GetId()}
	case *pb.Part_ExecutableCode:
		out.ExecutableCode = &genai.ExecutableCode{Code: data.ExecutableCode.GetCode(), Language: languageFromProto(data.ExecutableCode.GetLanguage())}
	case *pb.Part_CodeExecutionResult:
		out.CodeExecutionResult = &genai.CodeExecutionResult{Outcome: outcomeFromProto(data.CodeExecutionResult.GetOutcome()), Output: data.CodeExecutionResult.GetOutput()}
	}
	if md := in.GetVideoMetadata(); md != nil {
		out.VideoMetadata = &genai.VideoMetadata{
			StartOffset: md.GetStartOffset().AsDuration(),
			EndOffset:   md.GetEndOffset().AsDuration(),
		}
	}
	return out
}

func finishReasonToProto(in genai.FinishReason) pb.FinishReason {
	switch in {
	case genai.FinishReasonStop:
		return pb.FinishReason_FINISH_REASON_STOP
	case genai.FinishReasonMaxTokens:
		return pb.FinishReason_FINISH_REASON_MAX_TOKENS
	case genai.FinishReasonSafety:
		return pb.FinishReason_FINISH_REASON_SAFETY
	case genai.FinishReasonRecitation:
		return pb.FinishReason_FINISH_REASON_RECITATION
	case genai.FinishReasonLanguage:
		return pb.FinishReason_FINISH_REASON_LANGUAGE
	case genai.FinishReasonOther:
		return pb.FinishReason_FINISH_REASON_OTHER
	case genai.FinishReasonBlocklist:
		return pb.FinishReason_FINISH_REASON_BLOCKLIST
	case genai.FinishReasonProhibitedContent:
		return pb.FinishReason_FINISH_REASON_PROHIBITED_CONTENT
	case genai.FinishReasonSPII:
		return pb.FinishReason_FINISH_REASON_SPII
	case genai.FinishReasonMalformedFunctionCall:
		return pb.FinishReason_FINISH_REASON_MALFORMED_FUNCTION_CALL
	case genai.FinishReasonImageSafety:
		return pb.FinishReason_FINISH_REASON_IMAGE_SAFETY
	case genai.FinishReasonUnexpectedToolCall:
		return pb.FinishReason_FINISH_REASON_UNEXPECTED_TOOL_CALL
	case genai.FinishReasonImageProhibitedContent:
		return pb.FinishReason_FINISH_REASON_IMAGE_PROHIBITED_CONTENT
	case genai.FinishReasonNoImage:
		return pb.FinishReason_FINISH_REASON_NO_IMAGE
	case genai.FinishReasonImageRecitation:
		return pb.FinishReason_FINISH_REASON_IMAGE_RECITATION
	case genai.FinishReasonImageOther:
		return pb.FinishReason_FINISH_REASON_IMAGE_OTHER
	default:
		return pb.FinishReason_FINISH_REASON_UNSPECIFIED
	}
}

func finishReasonFromProto(in pb.FinishReason) genai.FinishReason {
	switch in {
	case pb.FinishReason_FINISH_REASON_STOP:
		return genai.FinishReasonStop
	case pb.FinishReason_FINISH_REASON_MAX_TOKENS:
		return genai.FinishReasonMaxTokens
	case pb.FinishReason_FINISH_REASON_SAFETY:
		return genai.FinishReasonSafety
	case pb.FinishReason_FINISH_REASON_RECITATION:
		return genai.FinishReasonRecitation
	case pb.FinishReason_FINISH_REASON_LANGUAGE:
		return genai.FinishReasonLanguage
	case pb.FinishReason_FINISH_REASON_OTHER:
		return genai.FinishReasonOther
	case pb.FinishReason_FINISH_REASON_BLOCKLIST:
		return genai.FinishReasonBlocklist
	case pb.FinishReason_FINISH_REASON_PROHIBITED_CONTENT:
		return genai.FinishReasonProhibitedContent
	case pb.FinishReason_FINISH_REASON_SPII:
		return genai.FinishReasonSPII
	case pb.FinishReason_FINISH_REASON_MALFORMED_FUNCTION_CALL:
		return genai.FinishReasonMalformedFunctionCall
	case pb.FinishReason_FINISH_REASON_IMAGE_SAFETY:
		return genai.FinishReasonImageSafety
	case pb.FinishReason_FINISH_REASON_UNEXPECTED_TOOL_CALL:
		return genai.FinishReasonUnexpectedToolCall
	case pb.FinishReason_FINISH_REASON_IMAGE_PROHIBITED_CONTENT:
		return genai.FinishReasonImageProhibitedContent
	case pb.FinishReason_FINISH_REASON_NO_IMAGE:
		return genai.FinishReasonNoImage
	case pb.FinishReason_FINISH_REASON_IMAGE_RECITATION:
		return genai.FinishReasonImageRecitation
	case pb.FinishReason_FINISH_REASON_IMAGE_OTHER:
		return genai.FinishReasonImageOther
	default:
		return genai.FinishReasonUnspecified
	}
}

func languageToProto(in genai.Language) pb.ExecutableCode_Language {
	switch in {
	case genai.LanguagePython:
		return pb.ExecutableCode_PYTHON
	default:
		return pb.ExecutableCode_LANGUAGE_UNSPECIFIED
	}
}

func languageFromProto(in pb.ExecutableCode_Language) genai.Language {
	switch in {
	case pb.ExecutableCode_PYTHON:
		return genai.LanguagePython
	default:
		return genai.LanguageUnspecified
	}
}

func outcomeToProto(in genai.Outcome) pb.CodeExecutionResult_Outcome {
	switch in {
	case genai.OutcomeOK:
		return pb.CodeExecutionResult_OUTCOME_OK
	case genai.OutcomeDeadlineExceeded:
		return pb.CodeExecutionResult_OUTCOME_DEADLINE_EXCEEDED
	case genai.OutcomeFailed:
		return pb.CodeExecutionResult_OUTCOME_FAILED
	default:
		return pb.CodeExecutionResult_OUTCOME_UNSPECIFIED
	}
}

func outcomeFromProto(in pb.CodeExecutionResult_Outcome) genai.Outcome {
	switch in {
	case pb.CodeExecutionResult_OUTCOME_OK:
		return genai.OutcomeOK
	case pb.CodeExecutionResult_OUTCOME_DEADLINE_EXCEEDED:
		return genai.OutcomeDeadlineExceeded
	case pb.CodeExecutionResult_OUTCOME_FAILED:
		return genai.OutcomeFailed
	default:
		return genai.OutcomeUnspecified
	}
}

func boolPtr(v bool) *bool {
	return &v
}

// sanitizeMap rewrites nested values into forms accepted by structpb.NewStruct.
//
// ADK tool-confirmation events can embed typed GenAI values such as
// *genai.FunctionCall inside otherwise JSON-like argument maps
// (for example "originalFunctionCall" inside a confirmation request).
// structpb.NewStruct rejects those typed SDK structs, so persistence must
// flatten them into plain map[string]any values before proto conversion.
func sanitizeMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = sanitizeValue(value)
	}
	return out
}

// sanitizeValue recursively normalizes values for protobuf Struct storage.
//
// Most values already arrive as JSON-compatible Go types and are returned as-is.
// The special cases here exist for ADK-generated nested GenAI structs, which are
// valid runtime values but not directly serializable through structpb.NewStruct.
func sanitizeValue(v any) any {
	switch typed := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return sanitizeMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeValue(item)
		}
		return out
	case *genai.FunctionCall:
		return map[string]any{
			"id":   typed.ID,
			"name": typed.Name,
			"args": sanitizeMap(typed.Args),
		}
	case genai.FunctionCall:
		return sanitizeValue(&typed)
	case *genai.FunctionResponse:
		return map[string]any{
			"id":       typed.ID,
			"name":     typed.Name,
			"response": sanitizeMap(typed.Response),
		}
	case genai.FunctionResponse:
		return sanitizeValue(&typed)
	case *toolconfirmation.ToolConfirmation:
		return map[string]any{
			"hint":      typed.Hint,
			"confirmed": typed.Confirmed,
			"payload":   sanitizeValue(typed.Payload),
		}
	case toolconfirmation.ToolConfirmation:
		return sanitizeValue(&typed)
	default:
		return v
	}
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func float64Ptr(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func _unused(_ time.Time) {}
