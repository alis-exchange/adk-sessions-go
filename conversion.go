package sessions

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "go.alis.build/common/alis/adk/sessions"
)

// The converters in this package must be lossless for every field the proto
// can carry, and they must reproduce the exact Go shape ADK hands us: nil for
// absent slices, maps, and pointers, never an empty non-nil value. ADK's
// session conformance suite compares round-tripped events with cmp.Diff,
// where nil and empty are different values.

func contentToProto(in *genai.Content) (*pb.Content, error) {
	if in == nil {
		return nil, nil
	}
	out := &pb.Content{Role: in.Role}
	for _, part := range in.Parts {
		converted, err := partToProto(part)
		if err != nil {
			return nil, err
		}
		out.Parts = append(out.Parts, converted)
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

// partToProto maps a genai part onto the proto oneof. A genai part is
// expected to carry exactly one data field; if several are set, the first
// populated one in the order below wins.
func partToProto(in *genai.Part) (*pb.Part, error) {
	if in == nil {
		return &pb.Part{}, nil
	}
	out := &pb.Part{
		Thought:          in.Thought,
		ThoughtSignature: bytesOrNil(in.ThoughtSignature),
	}
	switch {
	case in.Text != "":
		out.Data = &pb.Part_Text{Text: in.Text}
	case in.InlineData != nil:
		out.Data = &pb.Part_InlineData{InlineData: &pb.Blob{
			MimeType:    in.InlineData.MIMEType,
			Data:        bytesOrNil(in.InlineData.Data),
			DisplayName: stringPtr(in.InlineData.DisplayName),
		}}
	case in.FileData != nil:
		out.Data = &pb.Part_FileData{FileData: &pb.FileData{
			MimeType:    in.FileData.MIMEType,
			FileUri:     in.FileData.FileURI,
			DisplayName: in.FileData.DisplayName,
		}}
	case in.FunctionCall != nil:
		args, err := structOrNil(sanitizeMap(in.FunctionCall.Args))
		if err != nil {
			return nil, err
		}
		out.Data = &pb.Part_FunctionCall{FunctionCall: &pb.FunctionCall{
			Name: in.FunctionCall.Name,
			Args: args,
			Id:   in.FunctionCall.ID,
		}}
	case in.FunctionResponse != nil:
		resp, err := structOrNil(sanitizeMap(in.FunctionResponse.Response))
		if err != nil {
			return nil, err
		}
		fr := &pb.FunctionResponse{
			Name:     in.FunctionResponse.Name,
			Response: resp,
			Id:       in.FunctionResponse.ID,
		}
		for _, part := range in.FunctionResponse.Parts {
			fr.Parts = append(fr.Parts, functionResponsePartToProto(part))
		}
		out.Data = &pb.Part_FunctionResponse{FunctionResponse: fr}
	case in.ExecutableCode != nil:
		out.Data = &pb.Part_ExecutableCode{ExecutableCode: &pb.ExecutableCode{
			Code:     in.ExecutableCode.Code,
			Language: languageToProto(in.ExecutableCode.Language),
		}}
	case in.CodeExecutionResult != nil:
		out.Data = &pb.Part_CodeExecutionResult{CodeExecutionResult: &pb.CodeExecutionResult{
			Outcome: outcomeToProto(in.CodeExecutionResult.Outcome),
			Output:  in.CodeExecutionResult.Output,
		}}
	}
	if in.VideoMetadata != nil {
		out.Metadata = &pb.Part_VideoMetadata{VideoMetadata: &pb.VideoMetadata{
			StartOffset: durationToProto(in.VideoMetadata.StartOffset),
			EndOffset:   durationToProto(in.VideoMetadata.EndOffset),
		}}
	}
	return out, nil
}

func partFromProto(in *pb.Part) *genai.Part {
	if in == nil {
		return nil
	}
	out := &genai.Part{
		Thought:          in.GetThought(),
		ThoughtSignature: bytesOrNil(in.GetThoughtSignature()),
	}
	switch data := in.GetData().(type) {
	case *pb.Part_Text:
		out.Text = data.Text
	case *pb.Part_InlineData:
		out.InlineData = &genai.Blob{
			MIMEType:    data.InlineData.GetMimeType(),
			Data:        bytesOrNil(data.InlineData.GetData()),
			DisplayName: data.InlineData.GetDisplayName(),
		}
	case *pb.Part_FileData:
		out.FileData = &genai.FileData{
			MIMEType:    data.FileData.GetMimeType(),
			FileURI:     data.FileData.GetFileUri(),
			DisplayName: data.FileData.GetDisplayName(),
		}
	case *pb.Part_FunctionCall:
		out.FunctionCall = &genai.FunctionCall{
			Name: data.FunctionCall.GetName(),
			Args: structMap(data.FunctionCall.GetArgs()),
			ID:   data.FunctionCall.GetId(),
		}
	case *pb.Part_FunctionResponse:
		fr := &genai.FunctionResponse{
			Name:     data.FunctionResponse.GetName(),
			Response: structMap(data.FunctionResponse.GetResponse()),
			ID:       data.FunctionResponse.GetId(),
		}
		for _, part := range data.FunctionResponse.GetParts() {
			fr.Parts = append(fr.Parts, functionResponsePartFromProto(part))
		}
		out.FunctionResponse = fr
	case *pb.Part_ExecutableCode:
		out.ExecutableCode = &genai.ExecutableCode{
			Code:     data.ExecutableCode.GetCode(),
			Language: languageFromProto(data.ExecutableCode.GetLanguage()),
		}
	case *pb.Part_CodeExecutionResult:
		out.CodeExecutionResult = &genai.CodeExecutionResult{
			Outcome: outcomeFromProto(data.CodeExecutionResult.GetOutcome()),
			Output:  data.CodeExecutionResult.GetOutput(),
		}
	}
	if md := in.GetVideoMetadata(); md != nil {
		out.VideoMetadata = &genai.VideoMetadata{
			StartOffset: md.GetStartOffset().AsDuration(),
			EndOffset:   md.GetEndOffset().AsDuration(),
		}
	}
	return out
}

func functionResponsePartToProto(in *genai.FunctionResponsePart) *pb.FunctionResponsePart {
	out := &pb.FunctionResponsePart{}
	if in == nil {
		return out
	}
	switch {
	case in.InlineData != nil:
		out.Data = &pb.FunctionResponsePart_InlineData{InlineData: &pb.FunctionResponseBlob{
			MimeType:    in.InlineData.MIMEType,
			Data:        bytesOrNil(in.InlineData.Data),
			DisplayName: in.InlineData.DisplayName,
		}}
	case in.FileData != nil:
		out.Data = &pb.FunctionResponsePart_FileData{FileData: &pb.FunctionResponseFileData{
			MimeType:    in.FileData.MIMEType,
			FileUri:     in.FileData.FileURI,
			DisplayName: in.FileData.DisplayName,
		}}
	}
	return out
}

func functionResponsePartFromProto(in *pb.FunctionResponsePart) *genai.FunctionResponsePart {
	out := &genai.FunctionResponsePart{}
	switch data := in.GetData().(type) {
	case *pb.FunctionResponsePart_InlineData:
		out.InlineData = &genai.FunctionResponseBlob{
			MIMEType:    data.InlineData.GetMimeType(),
			Data:        bytesOrNil(data.InlineData.GetData()),
			DisplayName: data.InlineData.GetDisplayName(),
		}
	case *pb.FunctionResponsePart_FileData:
		out.FileData = &genai.FunctionResponseFileData{
			MIMEType:    data.FileData.GetMimeType(),
			FileURI:     data.FileData.GetFileUri(),
			DisplayName: data.FileData.GetDisplayName(),
		}
	}
	return out
}

// Enum mapping.
//
// genai enums are strings ("STOP", "TEXT", "PYTHON"); the proto enums carry
// the same names, sometimes behind a type prefix ("FINISH_REASON_STOP",
// "MEDIA_MODALITY_TEXT"). Mapping by name keeps future values working
// without a switch per enum. Zero maps to "" on the way out because that is
// what genai leaves in a field that was never set; the "*_UNSPECIFIED"
// strings are accepted on the way in but never produced.

func enumToProto(values map[string]int32, prefix, name string) int32 {
	if name == "" {
		return 0
	}
	if v, ok := values[name]; ok {
		return v
	}
	if v, ok := values[prefix+name]; ok {
		return v
	}
	return 0
}

func enumFromProto(names map[int32]string, prefix string, value int32) string {
	if value == 0 {
		return ""
	}
	return strings.TrimPrefix(names[value], prefix)
}

func finishReasonToProto(in genai.FinishReason) pb.FinishReason {
	return pb.FinishReason(enumToProto(pb.FinishReason_value, "FINISH_REASON_", string(in)))
}

func finishReasonFromProto(in pb.FinishReason) genai.FinishReason {
	return genai.FinishReason(enumFromProto(pb.FinishReason_name, "FINISH_REASON_", int32(in)))
}

func languageToProto(in genai.Language) pb.ExecutableCode_Language {
	return pb.ExecutableCode_Language(enumToProto(pb.ExecutableCode_Language_value, "", string(in)))
}

func languageFromProto(in pb.ExecutableCode_Language) genai.Language {
	return genai.Language(enumFromProto(pb.ExecutableCode_Language_name, "", int32(in)))
}

func outcomeToProto(in genai.Outcome) pb.CodeExecutionResult_Outcome {
	return pb.CodeExecutionResult_Outcome(enumToProto(pb.CodeExecutionResult_Outcome_value, "", string(in)))
}

func outcomeFromProto(in pb.CodeExecutionResult_Outcome) genai.Outcome {
	return genai.Outcome(enumFromProto(pb.CodeExecutionResult_Outcome_name, "", int32(in)))
}

func mediaModalityToProto(in genai.MediaModality) pb.MediaModality {
	return pb.MediaModality(enumToProto(pb.MediaModality_value, "MEDIA_MODALITY_", string(in)))
}

func mediaModalityFromProto(in pb.MediaModality) genai.MediaModality {
	return genai.MediaModality(enumFromProto(pb.MediaModality_name, "MEDIA_MODALITY_", int32(in)))
}

func trafficTypeToProto(in genai.TrafficType) pb.TrafficType {
	return pb.TrafficType(enumToProto(pb.TrafficType_value, "TRAFFIC_TYPE_", string(in)))
}

func trafficTypeFromProto(in pb.TrafficType) genai.TrafficType {
	return genai.TrafficType(enumFromProto(pb.TrafficType_name, "TRAFFIC_TYPE_", int32(in)))
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

// structOrNil is structpb.NewStruct that keeps "no map" distinguishable from
// "empty map": an empty Struct reads back as a non-nil empty map, which ADK
// treats as a different value from nil.
func structOrNil(m map[string]any) (*structpb.Struct, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(m)
}

// jsonValue stores an arbitrary Go value as a protobuf Value by way of its
// JSON encoding. Anything json.Marshal accepts works, including typed
// structs, at the cost of JSON's type flattening: ints come back as float64
// and structs as maps. That matches ADK's own database backend.
func jsonValue(v any) (*structpb.Value, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}
	out := &structpb.Value{}
	if err := protojson.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}
	return out, nil
}

func valueToAny(v *structpb.Value) any {
	if v == nil {
		return nil
	}
	return v.AsInterface()
}

func bytesOrNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return slices.Clone(b)
}

func cloneOrNil[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return slices.Clone(s)
}

func boolPtr(v bool) *bool {
	return &v
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

func durationToProto(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

// timestampOrNil keeps the zero time.Time distinguishable from an instant:
// timestamppb.New(time.Time{}) would encode year 1, which reads back as a
// real timestamp rather than "unset".
func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
