package sessions

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"time"

	"cloud.google.com/go/spanner"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/api/iterator"
	"google.golang.org/genai"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "go.alis.build/common/alis/adk/sessions/v1"
)

type ADKService struct {
	store *SpannerService
}

func NewADKService(store *SpannerService) *ADKService {
	return &ADKService{store: store}
}

var _ adksession.Service = (*ADKService)(nil)

func (s *ADKService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	state, err := structpb.NewStruct(req.State)
	if err != nil {
		return nil, err
	}
	session, appState, userState, err := s.store.createSession(ctx, &pb.Session{
		Id:      req.SessionID,
		AppName: req.AppName,
		UserId:  req.UserID,
		State:   state,
	}, req.SessionID)
	if err != nil {
		return nil, err
	}
	return &adksession.CreateResponse{
		Session: &DatabaseSession{session: session, appState: appState, userState: userState},
	}, nil
}

func (s *ADKService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	record, err := s.store.readSessionByCompositeKey(ctx, req.SessionID, req.AppName, req.UserID)
	if err != nil {
		return nil, err
	}
	sessionProto := cloneSession(record.Session)
	appState, err := s.store.readAppState(ctx, req.AppName)
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	userState, err := s.store.readUserState(ctx, req.AppName, req.UserID)
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	events, err := s.listEventsForADK(ctx, req)
	if err != nil {
		return nil, err
	}
	return &adksession.GetResponse{
		Session: &DatabaseSession{
			session:   sessionProto,
			events:    events,
			appState:  appState,
			userState: userState,
		},
	}, nil
}

func (s *ADKService) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	stmt := spanner.Statement{
		SQL:    fmt.Sprintf("SELECT Session FROM %s WHERE app_name=@app_name", s.store.sessionsTable()),
		Params: map[string]any{"app_name": req.AppName},
	}
	if req.UserID != "" {
		stmt.SQL += " AND user_id=@user_id"
		stmt.Params["user_id"] = req.UserID
	}
	stmt.SQL += " ORDER BY update_time DESC LIMIT 100"
	iter := s.store.db.Single().Query(ctx, stmt)
	defer iter.Stop()
	var out []adksession.Session
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var sessionProto pb.Session
		if err := row.Columns(&sessionProto); err != nil {
			return nil, err
		}
		out = append(out, &DatabaseSession{session: cloneSession(&sessionProto)})
	}
	return &adksession.ListResponse{Sessions: out}, nil
}

func (s *ADKService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	_, err := s.store.DeleteSession(ctx, &pb.DeleteSessionRequest{Name: sessionName(req.SessionID)})
	return err
}

func (s *ADKService) AppendEvent(ctx context.Context, sess adksession.Session, event *adksession.Event) error {
	if event == nil || event.Partial {
		return nil
	}
	pbEvent, err := eventToProto(sess, event)
	if err != nil {
		return err
	}
	_, err = s.store.appendEvent(ctx, sess.ID(), pbEvent)
	if err != nil {
		return err
	}
	if dbSession, ok := sess.(*DatabaseSession); ok {
		dbSession.events = append(dbSession.events, pbEvent)
		dbSession.session.UpdateTime = pbEvent.Timestamp
		if delta := pbEvent.GetActions().GetStateDelta(); delta != nil {
			current := structMap(dbSession.session.GetState())
			if current == nil {
				current = map[string]any{}
			}
			maps.Copy(current, delta.AsMap())
			state, err := structpb.NewStruct(current)
			if err == nil {
				dbSession.session.State = state
			}
		}
	}
	return nil
}

func (s *ADKService) listEventsForADK(ctx context.Context, req *adksession.GetRequest) ([]*pb.SessionEvent, error) {
	params := map[string]any{"session_id": req.SessionID}
	where := "session_id=@session_id"
	if !req.After.IsZero() {
		where += " AND timestamp>=@after"
		params["after"] = req.After
	}
	limit := ""
	if req.NumRecentEvents > 0 {
		params["limit"] = int64(req.NumRecentEvents)
		limit = " LIMIT @limit"
	}
	stmt := spanner.Statement{
		SQL:    fmt.Sprintf("SELECT SessionEvent FROM %s WHERE %s ORDER BY timestamp ASC%s", s.store.eventsTable(), where, limit),
		Params: params,
	}
	iter := s.store.db.Single().Query(ctx, stmt)
	defer iter.Stop()
	var events []*pb.SessionEvent
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var event pb.SessionEvent
		if err := row.Columns(&event); err != nil {
			return nil, err
		}
		events = append(events, cloneEvent(&event))
	}
	return events, nil
}

type DatabaseSession struct {
	session   *pb.Session
	events    []*pb.SessionEvent
	appState  map[string]any
	userState map[string]any
}

func (s *DatabaseSession) ID() string                { return s.session.GetId() }
func (s *DatabaseSession) AppName() string           { return s.session.GetAppName() }
func (s *DatabaseSession) UserID() string            { return s.session.GetUserId() }
func (s *DatabaseSession) LastUpdateTime() time.Time { return s.session.GetUpdateTime().AsTime() }
func (s *DatabaseSession) State() adksession.State {
	return &DatabaseSessionState{state: mergeStateMaps(s.appState, s.userState, structMap(s.session.GetState()))}
}
func (s *DatabaseSession) Events() adksession.Events { return &DatabaseSessionEvents{events: s.events} }

type DatabaseSessionState struct {
	state map[string]any
}

func (s *DatabaseSessionState) Get(key string) (any, error) {
	value, ok := s.state[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return value, nil
}

func (s *DatabaseSessionState) Set(key string, value any) error {
	if s.state == nil {
		s.state = map[string]any{}
	}
	s.state[key] = value
	return nil
}

type DatabaseSessionEvents struct {
	events []*pb.SessionEvent
}

func (s *DatabaseSessionState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range s.state {
			if !yield(k, v) {
				return
			}
		}
	}
}

func (s *DatabaseSessionEvents) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, event := range s.events {
			if !yield(protoToEvent(event)) {
				return
			}
		}
	}
}

func (s *DatabaseSessionEvents) Len() int                   { return len(s.events) }
func (s *DatabaseSessionEvents) At(i int) *adksession.Event { return protoToEvent(s.events[i]) }

func protoToEvent(in *pb.SessionEvent) *adksession.Event {
	if in == nil {
		return nil
	}
	event := &adksession.Event{
		ID:                 in.GetId(),
		Timestamp:          in.GetTimestamp().AsTime(),
		InvocationID:       in.GetInvocationId(),
		Branch:             in.GetBranch(),
		Author:             in.GetAuthor(),
		LongRunningToolIDs: append([]string(nil), in.GetLongRunningToolIds()...),
		LLMResponse: adkmodel.LLMResponse{
			Content:           contentFromProto(in.GetContent()),
			CitationMetadata:  nil,
			GroundingMetadata: nil,
			UsageMetadata:     nil,
			CustomMetadata:    structMap(in.GetCustomMetadata()),
			LogprobsResult:    nil,
			Partial:           in.GetPartial(),
			TurnComplete:      in.GetTurnComplete(),
			Interrupted:       in.GetInterrupted(),
			ErrorCode:         in.GetErrorCode(),
			ErrorMessage:      in.GetErrorMessage(),
			FinishReason:      finishReasonFromProto(in.GetFinishReason()),
			AvgLogprobs:       in.GetAvgLogprobs(),
		},
		Actions: adksession.EventActions{
			StateDelta:        structMap(in.GetActions().GetStateDelta()),
			ArtifactDelta:     in.GetActions().GetArtifactDelta(),
			SkipSummarization: in.GetActions().GetSkipSummarization(),
			TransferToAgent:   in.GetActions().GetTransferAgent(),
			Escalate:          in.GetActions().GetEscalate(),
		},
	}
	return event
}

func eventToProto(sess adksession.Session, event *adksession.Event) (*pb.SessionEvent, error) {
	var stateDelta *structpb.Struct
	var err error
	if len(event.Actions.StateDelta) > 0 {
		_, _, sessionDelta := splitScopedState(event.Actions.StateDelta)
		if len(sessionDelta) > 0 {
			stateDelta, err = structpb.NewStruct(sanitizeMap(sessionDelta))
			if err != nil {
				return nil, err
			}
		}
	}
	customMetadata, err := structpb.NewStruct(sanitizeMap(event.CustomMetadata))
	if err != nil && len(event.CustomMetadata) > 0 {
		return nil, err
	}
	parts := []*pb.Part{}
	if event.Content != nil {
		for _, part := range event.Content.Parts {
			converted, err := partToProto(part)
			if err != nil {
				return nil, err
			}
			parts = append(parts, converted)
		}
	}
	return &pb.SessionEvent{
		Id:                 event.ID,
		AppName:            sess.AppName(),
		UserId:             sess.UserID(),
		SessionId:          sess.ID(),
		InvocationId:       event.InvocationID,
		Author:             event.Author,
		LongRunningToolIds: append([]string(nil), event.LongRunningToolIDs...),
		Content:            &pb.Content{Role: roleOrDefault(event.Content), Parts: parts},
		CustomMetadata:     customMetadata,
		Partial:            boolPtr(event.Partial),
		TurnComplete:       boolPtr(event.TurnComplete),
		ErrorCode:          stringPtr(event.ErrorCode),
		ErrorMessage:       stringPtr(event.ErrorMessage),
		Interrupted:        boolPtr(event.Interrupted),
		FinishReason:       finishReasonToProto(event.FinishReason),
		AvgLogprobs:        float64Ptr(event.AvgLogprobs),
		Timestamp:          timestamppb.New(event.Timestamp),
		Actions: &pb.EventActions{
			StateDelta:        stateDelta,
			ArtifactDelta:     event.Actions.ArtifactDelta,
			SkipSummarization: event.Actions.SkipSummarization,
			TransferAgent:     event.Actions.TransferToAgent,
			Escalate:          event.Actions.Escalate,
		},
		Branch: stringPtr(event.Branch),
	}, nil
}

func roleOrDefault(content *genai.Content) string {
	if content == nil || content.Role == "" {
		return string(genai.RoleUser)
	}
	return content.Role
}
