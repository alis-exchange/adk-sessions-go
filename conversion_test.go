package sessions

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/jsonschema-go/jsonschema"
	adkmodel "google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
	"google.golang.org/protobuf/proto"

	pb "go.alis.build/common/alis/adk/sessions"
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

// testSession is the minimal adksession.Session the converters need.
type testSession struct{ id, appName, userID string }

func (s testSession) ID() string                { return s.id }
func (s testSession) AppName() string           { return s.appName }
func (s testSession) UserID() string            { return s.userID }
func (s testSession) State() adksession.State   { return nil }
func (s testSession) Events() adksession.Events { return nil }
func (s testSession) LastUpdateTime() time.Time { return time.Time{} }

// roundTrip converts an event to its proto, serializes and deserializes it
// the way Spanner will, and converts it back.
func roundTrip(t *testing.T, event *adksession.Event) *adksession.Event {
	t.Helper()
	sess := testSession{id: "s1", appName: "app", userID: "u1"}
	stored, err := eventToProto(sess, event)
	if err != nil {
		t.Fatalf("eventToProto() error = %v", err)
	}
	raw, err := proto.Marshal(stored)
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}
	var decoded pb.SessionEvent
	if err := proto.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal() error = %v", err)
	}
	if decoded.GetSessionId() != "s1" || decoded.GetAppName() != "app" || decoded.GetUserId() != "u1" {
		t.Fatalf("stored event keys = (%q, %q, %q), want (s1, app, u1)", decoded.GetSessionId(), decoded.GetAppName(), decoded.GetUserId())
	}
	got, err := protoToEvent(&decoded)
	if err != nil {
		t.Fatalf("protoToEvent() error = %v", err)
	}
	return got
}

var eventCmpOpts = []cmp.Option{
	cmp.AllowUnexported(adksession.Event{}),
	cmpopts.IgnoreFields(adksession.RequestInput{}, "ResponseSchema"),
}

func TestEventRoundTrip(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)

	cases := map[string]*adksession.Event{
		"minimal": {
			ID:           "e1",
			Timestamp:    ts,
			InvocationID: "inv1",
			Author:       "user",
		},
		// Mirrors sessiontestsuite's "with_all_fields" case.
		"all_fields": {
			ID:                 "event_complete",
			Timestamp:          ts,
			Author:             "user",
			InvocationID:       "inv1",
			LongRunningToolIDs: []string{"tool123"},
			Actions:            adksession.EventActions{StateDelta: map[string]any{"user:k2": "v2"}},
			LLMResponse: adkmodel.LLMResponse{
				Content:      genai.NewContentFromText("test_text", "user"),
				TurnComplete: true,
				ErrorCode:    "error_code",
				ErrorMessage: "error_message",
				Interrupted:  true,
				GroundingMetadata: &genai.GroundingMetadata{
					WebSearchQueries: []string{"query1"},
				},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					PromptTokenCount:     1,
					CandidatesTokenCount: 1,
					TotalTokenCount:      2,
				},
				CitationMetadata: &genai.CitationMetadata{
					Citations: []*genai.Citation{{Title: "test", URI: "google.com"}},
				},
				CustomMetadata: map[string]any{"custom_key": "custom_value"},
			},
		},
		"bytes_content": {
			ID:           "event_with_bytes",
			Timestamp:    ts,
			Author:       "user",
			InvocationID: "inv1",
			LLMResponse: adkmodel.LLMResponse{
				Content: genai.NewContentFromBytes([]byte("test_image_data"), "image/png", "user"),
			},
		},
		"compaction": {
			ID:           "compaction_event",
			Timestamp:    ts,
			Author:       "user",
			InvocationID: "inv-compaction",
			Actions: adksession.EventActions{
				Compaction: &adksession.EventCompaction{
					StartTimestamp:   ts,
					EndTimestamp:     ts.Add(5*time.Second + 987*time.Nanosecond),
					CompactedContent: genai.NewContentFromText("summary of earlier turns", "model"),
					ExcludedEvents: []adksession.EventRef{
						{InvocationID: "inv-1", Timestamp: ts},
						{InvocationID: "inv-2", Timestamp: ts.Add(5*time.Second + 987*time.Nanosecond)},
					},
				},
			},
		},
		"workflow_fields": {
			ID:             "wf1",
			Timestamp:      ts,
			Author:         "planner",
			InvocationID:   "inv1",
			Branch:         "root.planner",
			IsolationScope: "scope-a",
			Routes:         []string{"review", "publish"},
			RequestedInput: &adksession.RequestInput{
				InterruptID:    "int-1",
				Message:        "approve?",
				ResponseSchema: &jsonschema.Schema{Type: "object"},
				Payload:        map[string]any{"doc": "draft", "version": float64(2)},
			},
			Output:   map[string]any{"answer": float64(42)},
			NodeInfo: &adksession.NodeInfo{Path: "root/child@1", MessageAsOutput: true, OutputFor: []string{"root", "root/child@1"}},
			Actions: adksession.EventActions{
				ArtifactDelta:     map[string]int64{"report.md": 3},
				SkipSummarization: true,
				TransferToAgent:   "reviewer",
				Escalate:          true,
				RequestedToolConfirmations: map[string]toolconfirmation.ToolConfirmation{
					"call-1": {Hint: "confirm", Confirmed: true, Payload: map[string]any{"k": "v"}},
				},
			},
			LLMResponse: adkmodel.LLMResponse{
				ModelVersion: "gemini-x",
				FinishReason: genai.FinishReasonStop,
				AvgLogprobs:  -0.5,
				LogprobsResult: &genai.LogprobsResult{
					ChosenCandidates: []*genai.LogprobsResultCandidate{{LogProbability: -0.1, Token: "hi", TokenID: 7}},
					TopCandidates:    []*genai.LogprobsResultTopCandidates{{Candidates: []*genai.LogprobsResultCandidate{{Token: "hi", TokenID: 7}}}},
				},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					PromptTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 3}},
					TrafficType:         genai.TrafficTypeOnDemand,
				},
			},
		},
		"function_parts": {
			ID:           "fn1",
			Timestamp:    ts,
			Author:       "model",
			InvocationID: "inv1",
			LLMResponse: adkmodel.LLMResponse{
				Content: &genai.Content{Role: "model", Parts: []*genai.Part{
					{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "lookup", Args: map[string]any{"q": "x"}}},
					{FunctionResponse: &genai.FunctionResponse{ID: "c1", Name: "lookup", Response: map[string]any{"ok": true}, Parts: []*genai.FunctionResponsePart{
						{InlineData: &genai.FunctionResponseBlob{MIMEType: "text/plain", Data: []byte("hi"), DisplayName: "hi.txt"}},
						{FileData: &genai.FunctionResponseFileData{MIMEType: "image/png", FileURI: "gs://b/o", DisplayName: "o"}},
					}}},
					{ExecutableCode: &genai.ExecutableCode{Code: "print(1)", Language: genai.LanguagePython}},
					{CodeExecutionResult: &genai.CodeExecutionResult{Outcome: genai.OutcomeOK, Output: "1"}},
					{Text: "thinking", Thought: true, ThoughtSignature: []byte{1, 2}},
					{FileData: &genai.FileData{MIMEType: "video/mp4", FileURI: "gs://b/v", DisplayName: "v"}, VideoMetadata: &genai.VideoMetadata{StartOffset: time.Second, EndOffset: 2 * time.Second}},
				}},
			},
		},
	}

	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			got := roundTrip(t, event)
			if diff := cmp.Diff(event, got, eventCmpOpts...); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEventRoundTripKeepsResponseSchemaJSON(t *testing.T) {
	event := &adksession.Event{
		ID: "wf1", Timestamp: time.Now(), Author: "a", InvocationID: "i",
		RequestedInput: &adksession.RequestInput{
			InterruptID:    "int-1",
			ResponseSchema: &jsonschema.Schema{Type: "object", Required: []string{"approved"}},
		},
	}
	got := roundTrip(t, event)
	if got.RequestedInput == nil || got.RequestedInput.ResponseSchema == nil {
		t.Fatal("response schema was not persisted")
	}
	if got.RequestedInput.ResponseSchema.Type != "object" {
		t.Errorf("schema type = %q, want object", got.RequestedInput.ResponseSchema.Type)
	}
	if diff := cmp.Diff([]string{"approved"}, got.RequestedInput.ResponseSchema.Required); diff != "" {
		t.Errorf("schema required mismatch (-want +got):\n%s", diff)
	}
}

func TestEventToProtoDropsTempStateKeys(t *testing.T) {
	event := &adksession.Event{
		ID: "e1", Timestamp: time.Now(), Author: "a", InvocationID: "i",
		Actions: adksession.EventActions{StateDelta: map[string]any{"temp:scratch": "x", "app:a": 1, "user:u": 2, "k": "v"}},
	}
	stored, err := eventToProto(testSession{id: "s", appName: "a", userID: "u"}, event)
	if err != nil {
		t.Fatalf("eventToProto() error = %v", err)
	}
	want := map[string]any{"app:a": float64(1), "user:u": float64(2), "k": "v"}
	if diff := cmp.Diff(want, stored.GetActions().GetStateDelta().AsMap()); diff != "" {
		t.Errorf("stored delta mismatch (-want +got):\n%s", diff)
	}
	// The caller's delta is untouched: the live session still needs temp keys.
	if _, ok := event.Actions.StateDelta["temp:scratch"]; !ok {
		t.Error("caller's delta lost its temp key")
	}
}

func TestProtoToEventUnspecifiedEnumsReadAsEmpty(t *testing.T) {
	got, err := protoToEvent(&pb.SessionEvent{
		Id:           "e1",
		FinishReason: pb.FinishReason_FINISH_REASON_UNSPECIFIED,
		Content: &pb.Content{Role: "model", Parts: []*pb.Part{
			{Data: &pb.Part_ExecutableCode{ExecutableCode: &pb.ExecutableCode{Code: "x"}}},
			{Data: &pb.Part_CodeExecutionResult{CodeExecutionResult: &pb.CodeExecutionResult{Output: "y"}}},
		}},
		UsageMetadata: &pb.UsageMetadata{PromptTokensDetails: []*pb.ModalityTokenCount{{TokenCount: 1}}},
	})
	if err != nil {
		t.Fatalf("protoToEvent() error = %v", err)
	}
	if got.FinishReason != "" {
		t.Errorf("FinishReason = %q, want empty", got.FinishReason)
	}
	if lang := got.Content.Parts[0].ExecutableCode.Language; lang != "" {
		t.Errorf("Language = %q, want empty", lang)
	}
	if outcome := got.Content.Parts[1].CodeExecutionResult.Outcome; outcome != "" {
		t.Errorf("Outcome = %q, want empty", outcome)
	}
	if modality := got.UsageMetadata.PromptTokensDetails[0].Modality; modality != "" {
		t.Errorf("Modality = %q, want empty", modality)
	}
	if got.UsageMetadata.TrafficType != "" {
		t.Errorf("TrafficType = %q, want empty", got.UsageMetadata.TrafficType)
	}
	if !got.Timestamp.IsZero() {
		t.Errorf("Timestamp = %v, want zero for an unset proto timestamp", got.Timestamp)
	}
}

func TestEnumsAcceptUnspecifiedStringsOnWrite(t *testing.T) {
	if got := finishReasonToProto(genai.FinishReasonUnspecified); got != pb.FinishReason_FINISH_REASON_UNSPECIFIED {
		t.Errorf("finishReasonToProto(unspecified) = %v", got)
	}
	if got := finishReasonToProto(genai.FinishReasonMaxTokens); got != pb.FinishReason_FINISH_REASON_MAX_TOKENS {
		t.Errorf("finishReasonToProto(max tokens) = %v", got)
	}
	if got := mediaModalityToProto(genai.MediaModalityUnspecified); got != pb.MediaModality_MEDIA_MODALITY_UNSPECIFIED {
		t.Errorf("mediaModalityToProto(unspecified) = %v", got)
	}
	if got := trafficTypeToProto(genai.TrafficTypeProvisionedThroughput); got != pb.TrafficType_TRAFFIC_TYPE_PROVISIONED_THROUGHPUT {
		t.Errorf("trafficTypeToProto(provisioned) = %v", got)
	}
}
