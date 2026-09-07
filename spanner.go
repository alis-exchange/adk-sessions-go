package sessions

import (
	"context"
	"fmt"
	"maps"
	"slices"
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

	pb "go.alis.build/common/alis/adk/sessions"
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

// SpannerService stores sessions, events, and scoped state in Spanner and
// serves the generated alis.adk.sessions.v1 gRPC API.
type SpannerService struct {
	db     *spanner.Client
	config SpannerConfig
	pb.UnimplementedSessionServiceServer
}

// NewSpannerService connects to the configured database.
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

// Register registers the generated SessionService server on a gRPC server.
func (s *SpannerService) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterSessionServiceServer(registrar, s)
}

// txReader is the read surface shared by single-use, read-only, and
// read-write Spanner transactions, so one set of helpers serves every path.
type txReader interface {
	ReadRow(ctx context.Context, table string, key spanner.Key, columns []string) (*spanner.Row, error)
	Query(ctx context.Context, statement spanner.Statement) *spanner.RowIterator
}

var (
	sessionColumns = []string{"session_id", "app_name", "user_id", "Session"}
	eventColumns   = []string{"session_id", "app_name", "user_id", "event_id", "SessionEvent"}
)

// gRPC surface.

func (s *SpannerService) CreateSession(ctx context.Context, req *pb.CreateSessionRequest) (*pb.Session, error) {
	session, _, _, err := s.createSession(ctx, req.GetSession(), req.GetSessionId())
	return session, err
}

func (s *SpannerService) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.Session, error) {
	sessionID, err := parseSessionName(req.GetName())
	if err != nil {
		return nil, err
	}
	record, err := s.readSessionByID(ctx, s.db.Single(), sessionID)
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
	iter := s.db.Single().Query(ctx, spanner.Statement{SQL: query, Params: params})
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
	record, err := s.readSessionByCompositeKey(ctx, s.db.Single(), req.GetSession().GetId(), req.GetSession().GetAppName(), req.GetSession().GetUserId())
	if err != nil {
		return nil, err
	}
	mask := req.GetUpdateMask()
	if mask == nil || len(mask.GetPaths()) == 0 {
		mask = &fieldmaskpb.FieldMask{Paths: []string{"display_name", "state", "expire_time", "ttl"}}
	}
	next := cloneSession(record.Session)
	var appDelta, userDelta map[string]any
	for _, path := range mask.Paths {
		switch path {
		case "display_name":
			next.DisplayName = req.GetSession().DisplayName
		case "state":
			var sessionState map[string]any
			appDelta, userDelta, sessionState = splitScopedState(structMap(req.GetSession().GetState()))
			stateStruct, err := structOrNil(sessionState)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid state: %v", err)
			}
			next.State = stateStruct
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
	_, err = s.db.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		muts, _, _, err := s.scopedStateMutations(ctx, txn, next.GetAppName(), next.GetUserId(), appDelta, userDelta)
		if err != nil {
			return err
		}
		muts = append(muts, s.sessionMutation(next))
		return txn.BufferWrite(muts)
	})
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
	record, err := s.readSessionByID(ctx, s.db.Single(), sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.deleteSession(ctx, record.SessionID, record.AppName, record.UserID); err != nil {
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
	events, err := s.queryEvents(ctx, s.db.Single(), spanner.Statement{
		SQL: fmt.Sprintf(
			"SELECT SessionEvent FROM %s WHERE %s ORDER BY %s LIMIT @limit OFFSET @offset",
			s.eventsTable(),
			where,
			applyEventOrderBy(req.GetOrderBy()),
		),
		Params: params,
	})
	if err != nil {
		return nil, err
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
	record, err := s.readSessionByID(ctx, s.db.Single(), sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(ctx, record.SessionID, record.AppName, record.UserID, req.GetEvent()); err != nil {
		return nil, err
	}
	return &pb.AppendEventResponse{}, nil
}

// Store operations shared by the gRPC and ADK surfaces.

// createSession inserts a session and folds any app:/user: keys of its
// initial state into the shared AppStates and UserStates rows. It returns
// the stored session together with the full app and user state after the
// write, read inside the same transaction.
func (s *SpannerService) createSession(ctx context.Context, input *pb.Session, suppliedID string) (*pb.Session, map[string]any, map[string]any, error) {
	session, appDelta, userDelta, err := normalizeSession(input)
	if err != nil {
		return nil, nil, nil, err
	}
	session.Id = nextSessionID(strings.TrimSpace(firstNonEmpty(suppliedID, session.GetId())))
	now := timestamppb.Now()
	session.CreateTime = now
	session.UpdateTime = now

	var appState, userState map[string]any
	_, err = s.db.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		muts, app, user, err := s.scopedStateMutations(ctx, txn, session.GetAppName(), session.GetUserId(), appDelta, userDelta)
		if err != nil {
			return err
		}
		appState, userState = app, user
		muts = append(muts, spanner.Insert(s.sessionsTable(), sessionColumns,
			[]any{session.GetId(), session.GetAppName(), session.GetUserId(), session}))
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return session, appState, userState, nil
}

// appendEvent stores an event under the session identified by the composite
// key and applies its state delta: session keys onto the Session row, app:
// and user: keys onto their shared rows. The stored event keeps the delta
// as written apart from "temp:" keys, which are never persisted.
func (s *SpannerService) appendEvent(ctx context.Context, sessionID, appName, userID string, input *pb.SessionEvent) (*pb.SessionEvent, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "event is required")
	}
	event := cloneEvent(input)
	event.Id = nextEventID(event.GetId())
	event.SessionId = sessionID
	event.AppName = appName
	event.UserId = userID
	if event.GetTimestamp() == nil {
		event.Timestamp = timestamppb.Now()
	}
	delta := trimTempKeys(structMap(event.GetActions().GetStateDelta()))
	appDelta, userDelta, sessionDelta := splitScopedState(delta)
	if event.Actions != nil {
		stateDelta, err := structOrNil(delta)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid state delta: %v", err)
		}
		event.Actions.StateDelta = stateDelta
	}

	_, err := s.db.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		record, err := s.readSessionByCompositeKey(ctx, txn, sessionID, appName, userID)
		if err != nil {
			return err
		}
		session := cloneSession(record.Session)
		state, err := mergeDelta(structMap(session.GetState()), sessionDelta)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid session state delta: %v", err)
		}
		session.State = state
		session.UpdateTime = event.Timestamp

		muts, _, _, err := s.scopedStateMutations(ctx, txn, appName, userID, appDelta, userDelta)
		if err != nil {
			return err
		}
		muts = append(muts,
			s.sessionMutation(session),
			spanner.Insert(s.eventsTable(), eventColumns,
				[]any{event.GetSessionId(), event.GetAppName(), event.GetUserId(), event.GetId(), event}),
		)
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return nil, err
	}
	return event, nil
}

// deleteSession removes a session row and every event under it. Spanner
// delete mutations on missing rows are no-ops, so this is idempotent.
func (s *SpannerService) deleteSession(ctx context.Context, sessionID, appName, userID string) error {
	key := spanner.Key{sessionID, appName, userID}
	_, err := s.db.Apply(ctx, []*spanner.Mutation{
		spanner.Delete(s.sessionsTable(), key),
		spanner.Delete(s.eventsTable(), key.AsPrefix()),
	})
	return err
}

func (s *SpannerService) sessionMutation(session *pb.Session) *spanner.Mutation {
	return spanner.InsertOrUpdate(s.sessionsTable(), sessionColumns,
		[]any{session.GetId(), session.GetAppName(), session.GetUserId(), session})
}

// scopedStateMutations reads the current app and user state through r,
// merges the deltas, and returns the mutations to write plus the resulting
// state maps. Callers run it inside the transaction that commits the
// mutations so concurrent deltas cannot overwrite each other.
func (s *SpannerService) scopedStateMutations(ctx context.Context, r txReader, appName, userID string, appDelta, userDelta map[string]any) ([]*spanner.Mutation, map[string]any, map[string]any, error) {
	appState, err := s.readAppState(ctx, r, appName)
	if err != nil {
		return nil, nil, nil, err
	}
	userState, err := s.readUserState(ctx, r, appName, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	var muts []*spanner.Mutation
	if len(appDelta) > 0 {
		appState = mergedState(appState, appDelta)
		state, err := structpb.NewStruct(appState)
		if err != nil {
			return nil, nil, nil, status.Errorf(codes.InvalidArgument, "invalid app state: %v", err)
		}
		resource := &pb.AppState{AppName: appName, State: state, UpdateTime: timestamppb.Now()}
		muts = append(muts, spanner.InsertOrUpdate(s.appStatesTable(), []string{"app_name", "AppState"}, []any{appName, resource}))
	}
	if len(userDelta) > 0 {
		userState = mergedState(userState, userDelta)
		state, err := structpb.NewStruct(userState)
		if err != nil {
			return nil, nil, nil, status.Errorf(codes.InvalidArgument, "invalid user state: %v", err)
		}
		resource := &pb.UserState{AppName: appName, UserId: userID, State: state, UpdateTime: timestamppb.Now()}
		muts = append(muts, spanner.InsertOrUpdate(s.userStatesTable(), []string{"app_name", "user_id", "UserState"}, []any{appName, userID, resource}))
	}
	return muts, appState, userState, nil
}

func mergedState(existing, delta map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(delta))
	maps.Copy(out, existing)
	maps.Copy(out, delta)
	return out
}

func (s *SpannerService) readSessionByCompositeKey(ctx context.Context, r txReader, sessionID, appName, userID string) (*sessionRecord, error) {
	row, err := r.ReadRow(ctx, s.sessionsTable(), spanner.Key{sessionID, appName, userID},
		[]string{"session_id", "app_name", "user_id", "create_time", "update_time", "Session"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, status.Error(codes.NotFound, "session not found")
		}
		return nil, err
	}
	return scanSessionRecord(row)
}

// readSessionByID resolves a session by ID alone, for the gRPC resource
// names that carry no app or user. It requires the ID to be globally unique.
func (s *SpannerService) readSessionByID(ctx context.Context, r txReader, sessionID string) (*sessionRecord, error) {
	iter := r.Query(ctx, spanner.Statement{
		SQL:    fmt.Sprintf("SELECT session_id, app_name, user_id, create_time, update_time, Session FROM %s WHERE session_id=@session_id LIMIT 2", s.sessionsTable()),
		Params: map[string]any{"session_id": sessionID},
	})
	defer iter.Stop()
	var records []*sessionRecord
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		record, err := scanSessionRecord(row)
		if err != nil {
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
	return records[0], nil
}

func scanSessionRecord(row *spanner.Row) (*sessionRecord, error) {
	record := &sessionRecord{Session: &pb.Session{}}
	if err := row.Columns(&record.SessionID, &record.AppName, &record.UserID, &record.CreateTime, &record.UpdateTime, record.Session); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *SpannerService) readEventByID(ctx context.Context, sessionID, eventID string) (*eventRecord, error) {
	iter := s.db.Single().Query(ctx, spanner.Statement{
		SQL:    fmt.Sprintf("SELECT session_id, app_name, user_id, event_id, timestamp, SessionEvent FROM %s WHERE session_id=@session_id AND event_id=@event_id LIMIT 2", s.eventsTable()),
		Params: map[string]any{"session_id": sessionID, "event_id": eventID},
	})
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

// listSessionEvents returns a session's events in timestamp order. With
// numRecent > 0 only the most recent numRecent events are returned, still
// oldest first. after, when set, excludes events before that instant; it is
// compared at microsecond precision because that is the resolution of the
// generated timestamp column.
func (s *SpannerService) listSessionEvents(ctx context.Context, r txReader, sessionID, appName, userID string, after time.Time, numRecent int) ([]*pb.SessionEvent, error) {
	params := map[string]any{"session_id": sessionID, "app_name": appName, "user_id": userID}
	where := "session_id=@session_id AND app_name=@app_name AND user_id=@user_id"
	if !after.IsZero() {
		where += " AND timestamp>=@after"
		params["after"] = after.Truncate(time.Microsecond)
	}
	order := "timestamp ASC, event_id ASC"
	limit := ""
	if numRecent > 0 {
		order = "timestamp DESC, event_id DESC"
		limit = " LIMIT @limit"
		params["limit"] = int64(numRecent)
	}
	events, err := s.queryEvents(ctx, r, spanner.Statement{
		SQL:    fmt.Sprintf("SELECT SessionEvent FROM %s WHERE %s ORDER BY %s%s", s.eventsTable(), where, order, limit),
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	if numRecent > 0 {
		slices.Reverse(events)
	}
	return events, nil
}

func (s *SpannerService) queryEvents(ctx context.Context, r txReader, stmt spanner.Statement) ([]*pb.SessionEvent, error) {
	iter := r.Query(ctx, stmt)
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

// readAppState returns the shared state of an app, or nil when none is stored.
func (s *SpannerService) readAppState(ctx context.Context, r txReader, appName string) (map[string]any, error) {
	row, err := r.ReadRow(ctx, s.appStatesTable(), spanner.Key{appName}, []string{"AppState"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	resource := &pb.AppState{}
	if err := row.Columns(resource); err != nil {
		return nil, err
	}
	return structMap(resource.GetState()), nil
}

// readUserState returns a user's state within an app, or nil when none is stored.
func (s *SpannerService) readUserState(ctx context.Context, r txReader, appName, userID string) (map[string]any, error) {
	row, err := r.ReadRow(ctx, s.userStatesTable(), spanner.Key{appName, userID}, []string{"UserState"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
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
