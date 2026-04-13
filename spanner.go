package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "go.alis.build/common/alis/adk/sessions/v1"
)

const (
	sessionsTableName   = "Sessions"
	eventsTableName     = "SessionEvents"
	appStatesTableName  = "AppStates"
	userStatesTableName = "UserStates"
)

// SpannerConfig configures the Spanner-backed session store.
//
// The package always uses the logical table names Sessions, SessionEvents,
// AppStates, and UserStates. When TablePrefix is set, each table name is
// resolved as <prefix>_<logical-name>.
type SpannerConfig struct {
	// Project is the Google Cloud project that owns the Spanner database.
	Project string
	// Instance is the Spanner instance ID.
	Instance string
	// Database is the Spanner database ID.
	Database string
	// DatabaseRole is an optional Spanner database role for the client.
	DatabaseRole string
	// TablePrefix is an optional prefix applied to each logical table name.
	TablePrefix string
}

type SpannerService struct {
	db     *spanner.Client
	config SpannerConfig
	pb.UnimplementedSessionServiceServer
}

func NewSpannerService(ctx context.Context, config SpannerConfig) (*SpannerService, error) {
	dbName := fmt.Sprintf("projects/%s/instances/%s/databases/%s", config.Project, config.Instance, config.Database)
	db, err := spanner.NewClientWithConfig(ctx, dbName, spanner.ClientConfig{
		DisableNativeMetrics: true,
		DatabaseRole:         config.DatabaseRole,
	})
	if err != nil {
		return nil, err
	}
	return &SpannerService{db: db, config: config}, nil
}

func (s *SpannerService) sessionsTable() string {
	return prefixedTableName(s.config.TablePrefix, sessionsTableName)
}

func (s *SpannerService) eventsTable() string {
	return prefixedTableName(s.config.TablePrefix, eventsTableName)
}

func (s *SpannerService) appStatesTable() string {
	return prefixedTableName(s.config.TablePrefix, appStatesTableName)
}

func (s *SpannerService) userStatesTable() string {
	return prefixedTableName(s.config.TablePrefix, userStatesTableName)
}

func (s *SpannerService) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterSessionServiceServer(registrar, s)
}

func (s *SpannerService) CreateSession(ctx context.Context, req *pb.CreateSessionRequest) (*pb.Session, error) {
	session, _, _, err := s.createSession(ctx, req.GetSession(), req.GetSessionId())
	return session, err
}

func (s *SpannerService) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.Session, error) {
	sessionID, err := parseSessionName(req.GetName())
	if err != nil {
		return nil, err
	}
	record, err := s.readSessionByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return cloneSession(record.Session), nil
}

func (s *SpannerService) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	pageSize := normalizePageSize(req.GetPageSize())
	offset, err := parsePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"limit":  int64(pageSize + 1),
		"offset": int64(offset),
	}
	where, err := buildSessionFilter(req.GetFilter(), params)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("SELECT Session FROM %s", s.sessionsTable())
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY " + applySessionOrderBy(req.GetOrderBy()) + " LIMIT @limit OFFSET @offset"
	stmt := spanner.Statement{SQL: query, Params: params}
	iter := s.db.Single().Query(ctx, stmt)
	defer iter.Stop()

	var sessions []*pb.Session
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var session pb.Session
		if err := row.Columns(&session); err != nil {
			return nil, err
		}
		sessions = append(sessions, cloneSession(&session))
	}
	nextToken := ""
	if len(sessions) > pageSize {
		sessions = sessions[:pageSize]
		nextToken = newPageToken(offset + pageSize)
	}
	return &pb.ListSessionsResponse{Sessions: sessions, NextPageToken: nextToken}, nil
}

func (s *SpannerService) UpdateSession(ctx context.Context, req *pb.UpdateSessionRequest) (*pb.Session, error) {
	if req.GetSession() == nil || req.GetSession().GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session.id is required")
	}
	record, err := s.readSessionByCompositeKey(ctx, req.GetSession().GetId(), req.GetSession().GetAppName(), req.GetSession().GetUserId())
	if err != nil {
		return nil, err
	}
	current := cloneSession(record.Session)
	mask := req.GetUpdateMask()
	if mask == nil || len(mask.GetPaths()) == 0 {
		mask = &fieldmaskpb.FieldMask{Paths: []string{"display_name", "state", "expire_time", "ttl"}}
	}
	next := cloneSession(current)
	for _, path := range mask.Paths {
		switch path {
		case "display_name":
			next.DisplayName = req.GetSession().DisplayName
		case "state":
			appState, userState, sessionState := splitScopedState(structMap(req.GetSession().GetState()))
			stateStruct, err := structpb.NewStruct(sessionState)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid state: %v", err)
			}
			next.State = stateStruct
			if err := s.writeScopedStates(ctx, next.GetAppName(), next.GetUserId(), appState, userState); err != nil {
				return nil, err
			}
		case "expire_time":
			if ts := req.GetSession().GetExpireTime(); ts != nil {
				next.Expiration = &pb.Session_ExpireTime{ExpireTime: ts}
			} else {
				next.Expiration = nil
			}
		case "ttl":
			if ttl := req.GetSession().GetTtl(); ttl != nil {
				next.Expiration = &pb.Session_ExpireTime{ExpireTime: timestamppb.New(time.Now().Add(ttl.AsDuration()))}
			} else {
				next.Expiration = nil
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unsupported update_mask path %q", path)
		}
	}
	next.UpdateTime = timestamppb.Now()
	mutation, err := s.sessionMutation(next)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return nil, err
	}
	return next, nil
}

func (s *SpannerService) DeleteSession(ctx context.Context, req *pb.DeleteSessionRequest) (*emptypb.Empty, error) {
	sessionID, err := parseSessionName(req.GetName())
	if err != nil {
		return nil, err
	}
	record, err := s.readSessionByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	muts := []*spanner.Mutation{
		spanner.Delete(s.sessionsTable(), spanner.Key{record.SessionID, record.AppName, record.UserID}),
	}
	eventKeys, err := s.listEventKeys(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, key := range eventKeys {
		muts = append(muts, spanner.Delete(s.eventsTable(), key))
	}
	_, err = s.db.Apply(ctx, muts)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *SpannerService) GetEvent(ctx context.Context, req *pb.GetEventRequest) (*pb.SessionEvent, error) {
	sessionID, eventID, err := parseEventName(req.GetName())
	if err != nil {
		return nil, err
	}
	record, err := s.readEventByID(ctx, sessionID, eventID)
	if err != nil {
		return nil, err
	}
	return cloneEvent(record.Event), nil
}

func (s *SpannerService) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsResponse, error) {
	sessionID, err := parseSessionName(req.GetParent())
	if err != nil {
		return nil, err
	}
	pageSize := normalizePageSize(req.GetPageSize())
	offset, err := parsePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"session_id": sessionID,
		"limit":      int64(pageSize + 1),
		"offset":     int64(offset),
	}
	where := "session_id = @session_id"
	filter, err := buildEventFilter(req.GetFilter(), params)
	if err != nil {
		return nil, err
	}
	if filter != "" {
		where += " AND " + filter
	}
	stmt := spanner.Statement{
		SQL: fmt.Sprintf(
			"SELECT SessionEvent FROM %s WHERE %s ORDER BY %s LIMIT @limit OFFSET @offset",
			s.eventsTable(),
			where,
			applyEventOrderBy(req.GetOrderBy()),
		),
		Params: params,
	}
	iter := s.db.Single().Query(ctx, stmt)
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
	nextToken := ""
	if len(events) > pageSize {
		events = events[:pageSize]
		nextToken = newPageToken(offset + pageSize)
	}
	return &pb.ListEventsResponse{SessionEvents: events, NextPageToken: nextToken}, nil
}

func (s *SpannerService) AppendEvent(ctx context.Context, req *pb.AppendEventRequest) (*pb.AppendEventResponse, error) {
	sessionID, err := parseSessionName(req.GetName())
	if err != nil {
		return nil, err
	}
	if req.GetEvent() == nil {
		return nil, status.Error(codes.InvalidArgument, "event is required")
	}
	if _, err := s.appendEvent(ctx, sessionID, req.GetEvent()); err != nil {
		return nil, err
	}
	return &pb.AppendEventResponse{}, nil
}

func (s *SpannerService) createSession(ctx context.Context, input *pb.Session, suppliedID string) (*pb.Session, map[string]any, map[string]any, error) {
	session, appState, userState, err := normalizeSession(input)
	if err != nil {
		return nil, nil, nil, err
	}
	session.Id = nextSessionID(strings.TrimSpace(firstNonEmpty(suppliedID, session.GetId())))
	now := timestamppb.Now()
	session.CreateTime = now
	session.UpdateTime = now
	mutation, err := s.sessionMutation(session)
	if err != nil {
		return nil, nil, nil, err
	}
	muts := []*spanner.Mutation{mutation}
	appMut, err := s.appStateMutation(ctx, session.GetAppName(), appState)
	if err != nil {
		return nil, nil, nil, err
	}
	if appMut != nil {
		muts = append(muts, appMut)
	}
	userMut, err := s.userStateMutation(ctx, session.GetAppName(), session.GetUserId(), userState)
	if err != nil {
		return nil, nil, nil, err
	}
	if userMut != nil {
		muts = append(muts, userMut)
	}
	_, err = s.db.Apply(ctx, muts)
	if err != nil {
		return nil, nil, nil, err
	}
	return session, appState, userState, nil
}

func (s *SpannerService) appendEvent(ctx context.Context, sessionID string, input *pb.SessionEvent) (*pb.SessionEvent, error) {
	record, err := s.readSessionByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	session := cloneSession(record.Session)
	event := cloneEvent(input)
	event.Id = nextEventID(event.GetId())
	event.SessionId = session.GetId()
	event.AppName = session.GetAppName()
	event.UserId = session.GetUserId()
	if event.GetTimestamp() == nil {
		event.Timestamp = timestamppb.Now()
	}
	appDelta, userDelta, sessionDelta := splitScopedState(structMap(event.GetActions().GetStateDelta()))
	if len(sessionDelta) == 0 {
		if event.Actions != nil {
			event.Actions.StateDelta = nil
		}
	} else {
		stateDelta, err := structpb.NewStruct(sessionDelta)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid state delta: %v", err)
		}
		if event.Actions == nil {
			event.Actions = &pb.EventActions{}
		}
		event.Actions.StateDelta = stateDelta
	}
	eventMutation := spanner.Insert(s.eventsTable(),
		[]string{"session_id", "app_name", "user_id", "event_id", "SessionEvent"},
		[]any{event.GetSessionId(), event.GetAppName(), event.GetUserId(), event.GetId(), event},
	)
	sessionState, err := mergeDelta(structMap(session.GetState()), sessionDelta)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session state delta: %v", err)
	}
	session.State = sessionState
	session.UpdateTime = event.Timestamp
	sessionMutation, err := s.sessionMutation(session)
	if err != nil {
		return nil, err
	}
	muts := []*spanner.Mutation{sessionMutation, eventMutation}
	appMut, err := s.appStateMutation(ctx, session.GetAppName(), appDelta)
	if err != nil {
		return nil, err
	}
	if appMut != nil {
		muts = append(muts, appMut)
	}
	userMut, err := s.userStateMutation(ctx, session.GetAppName(), session.GetUserId(), userDelta)
	if err != nil {
		return nil, err
	}
	if userMut != nil {
		muts = append(muts, userMut)
	}
	_, err = s.db.Apply(ctx, muts)
	if err != nil {
		return nil, err
	}
	return event, nil
}

func (s *SpannerService) sessionMutation(session *pb.Session) (*spanner.Mutation, error) {
	return spanner.InsertOrUpdate(s.sessionsTable(),
		[]string{"session_id", "app_name", "user_id", "Session"},
		[]any{session.GetId(), session.GetAppName(), session.GetUserId(), session},
	), nil
}

func (s *SpannerService) writeScopedStates(ctx context.Context, appName, userID string, appState, userState map[string]any) error {
	var muts []*spanner.Mutation
	appMut, err := s.appStateMutation(ctx, appName, appState)
	if err != nil {
		return err
	}
	if appMut != nil {
		muts = append(muts, appMut)
	}
	userMut, err := s.userStateMutation(ctx, appName, userID, userState)
	if err != nil {
		return err
	}
	if userMut != nil {
		muts = append(muts, userMut)
	}
	if len(muts) == 0 {
		return nil
	}
	_, err = s.db.Apply(ctx, muts)
	return err
}

func (s *SpannerService) appStateMutation(ctx context.Context, appName string, delta map[string]any) (*spanner.Mutation, error) {
	if len(delta) == 0 {
		return nil, nil
	}
	existing, err := s.readAppState(ctx, appName)
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	state, err := mergeDelta(existing, delta)
	if err != nil {
		return nil, err
	}
	resource := &pb.AppState{AppName: appName, State: state, UpdateTime: timestamppb.Now()}
	return spanner.InsertOrUpdate(s.appStatesTable(),
		[]string{"app_name", "AppState"},
		[]any{appName, resource},
	), nil
}

func (s *SpannerService) userStateMutation(ctx context.Context, appName, userID string, delta map[string]any) (*spanner.Mutation, error) {
	if len(delta) == 0 {
		return nil, nil
	}
	existing, err := s.readUserState(ctx, appName, userID)
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	state, err := mergeDelta(existing, delta)
	if err != nil {
		return nil, err
	}
	resource := &pb.UserState{AppName: appName, UserId: userID, State: state, UpdateTime: timestamppb.Now()}
	return spanner.InsertOrUpdate(s.userStatesTable(),
		[]string{"app_name", "user_id", "UserState"},
		[]any{appName, userID, resource},
	), nil
}

func (s *SpannerService) readSessionByCompositeKey(ctx context.Context, sessionID, appName, userID string) (*sessionRecord, error) {
	row, err := s.db.Single().ReadRow(ctx, s.sessionsTable(), spanner.Key{sessionID, appName, userID},
		[]string{"session_id", "app_name", "user_id", "create_time", "update_time", "Session"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, status.Error(codes.NotFound, "session not found")
		}
		return nil, err
	}
	var record sessionRecord
	record.Session = &pb.Session{}
	if err := row.Columns(&record.SessionID, &record.AppName, &record.UserID, &record.CreateTime, &record.UpdateTime, record.Session); err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *SpannerService) readSessionByID(ctx context.Context, sessionID string) (*sessionRecord, error) {
	stmt := spanner.Statement{
		SQL:    fmt.Sprintf("SELECT session_id, app_name, user_id, create_time, update_time, Session FROM %s WHERE session_id=@session_id LIMIT 2", s.sessionsTable()),
		Params: map[string]any{"session_id": sessionID},
	}
	iter := s.db.Single().Query(ctx, stmt)
	defer iter.Stop()
	var records []sessionRecord
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var record sessionRecord
		record.Session = &pb.Session{}
		if err := row.Columns(&record.SessionID, &record.AppName, &record.UserID, &record.CreateTime, &record.UpdateTime, record.Session); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	if len(records) > 1 {
		return nil, status.Error(codes.FailedPrecondition, "session id is not globally unique")
	}
	return &records[0], nil
}

func (s *SpannerService) readEventByID(ctx context.Context, sessionID, eventID string) (*eventRecord, error) {
	stmt := spanner.Statement{
		SQL:    fmt.Sprintf("SELECT session_id, app_name, user_id, event_id, timestamp, SessionEvent FROM %s WHERE session_id=@session_id AND event_id=@event_id LIMIT 2", s.eventsTable()),
		Params: map[string]any{"session_id": sessionID, "event_id": eventID},
	}
	iter := s.db.Single().Query(ctx, stmt)
	defer iter.Stop()
	var records []eventRecord
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var record eventRecord
		record.Event = &pb.SessionEvent{}
		if err := row.Columns(&record.SessionID, &record.AppName, &record.UserID, &record.EventID, &record.Timestamp, record.Event); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, status.Error(codes.NotFound, "event not found")
	}
	if len(records) > 1 {
		return nil, status.Error(codes.FailedPrecondition, "event id is not unique within session")
	}
	return &records[0], nil
}

func (s *SpannerService) listEventKeys(ctx context.Context, sessionID string) ([]spanner.Key, error) {
	stmt := spanner.Statement{
		SQL:    fmt.Sprintf("SELECT session_id, app_name, user_id, event_id FROM %s WHERE session_id=@session_id", s.eventsTable()),
		Params: map[string]any{"session_id": sessionID},
	}
	iter := s.db.Single().Query(ctx, stmt)
	defer iter.Stop()
	var out []spanner.Key
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var sid, app, user, eventID string
		if err := row.Columns(&sid, &app, &user, &eventID); err != nil {
			return nil, err
		}
		out = append(out, spanner.Key{sid, app, user, eventID})
	}
	return out, nil
}

func (s *SpannerService) readAppState(ctx context.Context, appName string) (map[string]any, error) {
	row, err := s.db.Single().ReadRow(ctx, s.appStatesTable(), spanner.Key{appName}, []string{"AppState"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, err
		}
		return nil, err
	}
	resource := &pb.AppState{}
	if err := row.Columns(resource); err != nil {
		return nil, err
	}
	return structMap(resource.GetState()), nil
}

func (s *SpannerService) readUserState(ctx context.Context, appName, userID string) (map[string]any, error) {
	row, err := s.db.Single().ReadRow(ctx, s.userStatesTable(), spanner.Key{appName, userID}, []string{"UserState"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, err
		}
		return nil, err
	}
	resource := &pb.UserState{}
	if err := row.Columns(resource); err != nil {
		return nil, err
	}
	return structMap(resource.GetState()), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
