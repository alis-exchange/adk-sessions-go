package sessions

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	adkmodel "google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	pb "go.alis.build/common/alis/adk/sessions"
)

// eventToProto converts an ADK event for storage under the given session.
//
// The event's state delta is stored whole apart from "temp:" keys, which are
// invocation-scoped and never persisted. Splitting the delta into app, user,
// and session scopes is the store's job at write time; the stored event keeps
// the prefixed keys so it reads back exactly as ADK wrote it.
func eventToProto(sess adksession.Session, event *adksession.Event) (*pb.SessionEvent, error) {
	actions, err := actionsToProto(event.Actions)
	if err != nil {
		return nil, err
	}
	content, err := contentToProto(event.Content)
	if err != nil {
		return nil, err
	}
	customMetadata, err := structOrNil(sanitizeMap(event.CustomMetadata))
	if err != nil {
		return nil, fmt.Errorf("custom metadata: %w", err)
	}
	requestedInput, err := requestInputToProto(event.RequestedInput)
	if err != nil {
		return nil, err
	}
	output, err := jsonValue(event.Output)
	if err != nil {
		return nil, fmt.Errorf("output: %w", err)
	}
	return &pb.SessionEvent{
		Id:                 event.ID,
		AppName:            sess.AppName(),
		UserId:             sess.UserID(),
		SessionId:          sess.ID(),
		InvocationId:       event.InvocationID,
		Author:             event.Author,
		Actions:            actions,
		LongRunningToolIds: cloneOrNil(event.LongRunningToolIDs),
		Branch:             stringPtr(event.Branch),
		Content:            content,
		GroundingMetadata:  groundingMetadataToProto(event.GroundingMetadata),
		CustomMetadata:     customMetadata,
		UsageMetadata:      usageMetadataToProto(event.UsageMetadata),
		CitationMetadata:   citationMetadataToProto(event.CitationMetadata),
		Partial:            boolPtr(event.Partial),
		TurnComplete:       boolPtr(event.TurnComplete),
		ErrorCode:          stringPtr(event.ErrorCode),
		ErrorMessage:       stringPtr(event.ErrorMessage),
		Interrupted:        boolPtr(event.Interrupted),
		FinishReason:       finishReasonToProto(event.FinishReason),
		AvgLogprobs:        float64Ptr(event.AvgLogprobs),
		ModelVersion:       stringPtr(event.ModelVersion),
		LogprobsResult:     logprobsResultToProto(event.LogprobsResult),
		IsolationScope:     stringPtr(event.IsolationScope),
		Routes:             cloneOrNil(event.Routes),
		RequestedInput:     requestedInput,
		Output:             output,
		NodeInfo:           nodeInfoToProto(event.NodeInfo),
		Timestamp:          timestampOrNil(event.Timestamp),
	}, nil
}

func protoToEvent(in *pb.SessionEvent) (*adksession.Event, error) {
	if in == nil {
		return nil, nil
	}
	requestedInput, err := requestInputFromProto(in.GetRequestedInput())
	if err != nil {
		return nil, err
	}
	return &adksession.Event{
		LLMResponse: adkmodel.LLMResponse{
			Content:           contentFromProto(in.GetContent()),
			CitationMetadata:  citationMetadataFromProto(in.GetCitationMetadata()),
			GroundingMetadata: groundingMetadataFromProto(in.GetGroundingMetadata()),
			UsageMetadata:     usageMetadataFromProto(in.GetUsageMetadata()),
			CustomMetadata:    structMap(in.GetCustomMetadata()),
			LogprobsResult:    logprobsResultFromProto(in.GetLogprobsResult()),
			ModelVersion:      in.GetModelVersion(),
			Partial:           in.GetPartial(),
			TurnComplete:      in.GetTurnComplete(),
			Interrupted:       in.GetInterrupted(),
			ErrorCode:         in.GetErrorCode(),
			ErrorMessage:      in.GetErrorMessage(),
			FinishReason:      finishReasonFromProto(in.GetFinishReason()),
			AvgLogprobs:       in.GetAvgLogprobs(),
		},
		ID:                 in.GetId(),
		Timestamp:          timeFromProto(in.GetTimestamp()),
		InvocationID:       in.GetInvocationId(),
		Branch:             in.GetBranch(),
		IsolationScope:     in.GetIsolationScope(),
		Author:             in.GetAuthor(),
		Actions:            actionsFromProto(in.GetActions()),
		LongRunningToolIDs: cloneOrNil(in.GetLongRunningToolIds()),
		Routes:             cloneOrNil(in.GetRoutes()),
		RequestedInput:     requestedInput,
		Output:             valueToAny(in.GetOutput()),
		NodeInfo:           nodeInfoFromProto(in.GetNodeInfo()),
	}, nil
}

func actionsToProto(in adksession.EventActions) (*pb.EventActions, error) {
	stateDelta, err := structOrNil(sanitizeMap(trimTempKeys(in.StateDelta)))
	if err != nil {
		return nil, fmt.Errorf("state delta: %w", err)
	}
	var confirmations map[string]*pb.ToolConfirmation
	for id, confirmation := range in.RequestedToolConfirmations {
		payload, err := jsonValue(sanitizeValue(confirmation.Payload))
		if err != nil {
			return nil, fmt.Errorf("tool confirmation %q: %w", id, err)
		}
		if confirmations == nil {
			confirmations = map[string]*pb.ToolConfirmation{}
		}
		confirmations[id] = &pb.ToolConfirmation{
			Hint:      confirmation.Hint,
			Confirmed: confirmation.Confirmed,
			Payload:   payload,
		}
	}
	compaction, err := compactionToProto(in.Compaction)
	if err != nil {
		return nil, err
	}
	return &pb.EventActions{
		SkipSummarization:          in.SkipSummarization,
		StateDelta:                 stateDelta,
		ArtifactDelta:              maps.Clone(in.ArtifactDelta),
		Escalate:                   in.Escalate,
		RequestedToolConfirmations: confirmations,
		TransferAgent:              in.TransferToAgent,
		Compaction:                 compaction,
	}, nil
}

func actionsFromProto(in *pb.EventActions) adksession.EventActions {
	out := adksession.EventActions{
		StateDelta:        structMap(in.GetStateDelta()),
		SkipSummarization: in.GetSkipSummarization(),
		TransferToAgent:   in.GetTransferAgent(),
		Escalate:          in.GetEscalate(),
		Compaction:        compactionFromProto(in.GetCompaction()),
	}
	if delta := in.GetArtifactDelta(); len(delta) > 0 {
		out.ArtifactDelta = maps.Clone(delta)
	}
	for id, confirmation := range in.GetRequestedToolConfirmations() {
		if out.RequestedToolConfirmations == nil {
			out.RequestedToolConfirmations = map[string]toolconfirmation.ToolConfirmation{}
		}
		out.RequestedToolConfirmations[id] = toolconfirmation.ToolConfirmation{
			Hint:      confirmation.GetHint(),
			Confirmed: confirmation.GetConfirmed(),
			Payload:   valueToAny(confirmation.GetPayload()),
		}
	}
	return out
}

// Compaction timestamps are stored at full nanosecond precision. A hole in
// a compaction range names its event by timestamp, and a reference that no
// longer matches is read as no hole at all, so rounding here would silently
// drop conversation from later prompts.
func compactionToProto(in *adksession.EventCompaction) (*pb.EventCompaction, error) {
	if in == nil {
		return nil, nil
	}
	content, err := contentToProto(in.CompactedContent)
	if err != nil {
		return nil, fmt.Errorf("compacted content: %w", err)
	}
	out := &pb.EventCompaction{
		StartTimestamp:   timestampOrNil(in.StartTimestamp),
		EndTimestamp:     timestampOrNil(in.EndTimestamp),
		CompactedContent: content,
	}
	for _, ref := range in.ExcludedEvents {
		out.ExcludedEvents = append(out.ExcludedEvents, &pb.EventRef{
			InvocationId: ref.InvocationID,
			Timestamp:    timestampOrNil(ref.Timestamp),
		})
	}
	return out, nil
}

func compactionFromProto(in *pb.EventCompaction) *adksession.EventCompaction {
	if in == nil {
		return nil
	}
	out := &adksession.EventCompaction{
		StartTimestamp:   timeFromProto(in.GetStartTimestamp()),
		EndTimestamp:     timeFromProto(in.GetEndTimestamp()),
		CompactedContent: contentFromProto(in.GetCompactedContent()),
	}
	for _, ref := range in.GetExcludedEvents() {
		out.ExcludedEvents = append(out.ExcludedEvents, adksession.EventRef{
			InvocationID: ref.GetInvocationId(),
			Timestamp:    timeFromProto(ref.GetTimestamp()),
		})
	}
	return out
}

// The JSON schema of a requested input is stored as its JSON object form.
// jsonschema.Schema has its own marshaller, so the round trip is faithful to
// the JSON, not necessarily to the exact Go struct that produced it.
func requestInputToProto(in *adksession.RequestInput) (*pb.RequestInput, error) {
	if in == nil {
		return nil, nil
	}
	out := &pb.RequestInput{InterruptId: in.InterruptID, Message: in.Message}
	if in.ResponseSchema != nil {
		raw, err := json.Marshal(in.ResponseSchema)
		if err != nil {
			return nil, fmt.Errorf("requested input schema: %w", err)
		}
		out.ResponseSchema = &structpb.Struct{}
		if err := protojson.Unmarshal(raw, out.ResponseSchema); err != nil {
			return nil, fmt.Errorf("requested input schema: %w", err)
		}
	}
	payload, err := jsonValue(in.Payload)
	if err != nil {
		return nil, fmt.Errorf("requested input payload: %w", err)
	}
	out.Payload = payload
	return out, nil
}

func requestInputFromProto(in *pb.RequestInput) (*adksession.RequestInput, error) {
	if in == nil {
		return nil, nil
	}
	out := &adksession.RequestInput{
		InterruptID: in.GetInterruptId(),
		Message:     in.GetMessage(),
		Payload:     valueToAny(in.GetPayload()),
	}
	if schema := in.GetResponseSchema(); schema != nil {
		raw, err := protojson.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("requested input schema: %w", err)
		}
		out.ResponseSchema = &jsonschema.Schema{}
		if err := json.Unmarshal(raw, out.ResponseSchema); err != nil {
			return nil, fmt.Errorf("requested input schema: %w", err)
		}
	}
	return out, nil
}

func nodeInfoToProto(in *adksession.NodeInfo) *pb.NodeInfo {
	if in == nil {
		return nil
	}
	return &pb.NodeInfo{
		Path:            in.Path,
		MessageAsOutput: in.MessageAsOutput,
		OutputFor:       cloneOrNil(in.OutputFor),
	}
}

func nodeInfoFromProto(in *pb.NodeInfo) *adksession.NodeInfo {
	if in == nil {
		return nil
	}
	return &adksession.NodeInfo{
		Path:            in.GetPath(),
		MessageAsOutput: in.GetMessageAsOutput(),
		OutputFor:       cloneOrNil(in.GetOutputFor()),
	}
}

// trimTempKeys drops invocation-scoped "temp:" keys from a state delta. It
// returns the input untouched when there is nothing to drop so nil stays nil.
func trimTempKeys(delta map[string]any) map[string]any {
	hasTemp := false
	for key := range delta {
		if strings.HasPrefix(key, adksession.KeyPrefixTemp) {
			hasTemp = true
			break
		}
	}
	if !hasTemp {
		return delta
	}
	out := make(map[string]any, len(delta))
	for key, value := range delta {
		if !strings.HasPrefix(key, adksession.KeyPrefixTemp) {
			out[key] = value
		}
	}
	return out
}
