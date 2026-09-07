package sessions

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/adk/v2/platform"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "go.alis.build/common/alis/adk/sessions"
)

// ADKService adapts a SpannerService to google.golang.org/adk/v2/session.Service.
type ADKService struct {
	store *SpannerService
}

// NewADKService wraps a Spanner-backed store as an ADK session service.
func NewADKService(store *SpannerService) *ADKService {
	return &ADKService{store: store}
}

var _ adksession.Service = (*ADKService)(nil)

// Create stores a new session. A client-supplied SessionID that already
// exists for the same app and user fails with codes.AlreadyExists.
func (s *ADKService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	state, err := structOrNil(sanitizeMap(req.State))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid state: %v", err)
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
		Session: newDatabaseSession(session, nil, appState, userState),
	}, nil
}

// Get reads a session, its scoped state, and its events in one consistent
// snapshot.
func (s *ADKService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	txn := s.store.db.ReadOnlyTransaction()
	defer txn.Close()

	record, err := s.store.readSessionByCompositeKey(ctx, txn, req.SessionID, req.AppName, req.UserID)
	if err != nil {
		return nil, err
	}
	appState, err := s.store.readAppState(ctx, txn, req.AppName)
	if err != nil {
		return nil, err
	}
	userState, err := s.store.readUserState(ctx, txn, req.AppName, req.UserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.listSessionEvents(ctx, txn, req.SessionID, req.AppName, req.UserID, req.After, req.NumRecentEvents)
	if err != nil {
		return nil, err
	}
	var events []*adksession.Event
	for _, row := range rows {
		event, err := protoToEvent(row)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return &adksession.GetResponse{
		Session: newDatabaseSession(record.Session, events, appState, userState),
	}, nil
}

// List returns the sessions of an app, optionally narrowed to one user, most
// recently updated first. Listed sessions carry no events and no app or user
// state; call Get for the full view.
func (s *ADKService) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	stmt := spanner.Statement{
		SQL:    fmt.Sprintf("SELECT Session FROM %s WHERE app_name=@app_name", s.store.sessionsTable()),
		Params: map[string]any{"app_name": req.AppName},
	}
	if req.UserID != "" {
		stmt.SQL += " AND user_id=@user_id"
		stmt.Params["user_id"] = req.UserID
	}
	stmt.SQL += " ORDER BY update_time DESC"
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
		out = append(out, newDatabaseSession(cloneSession(&sessionProto), nil, nil, nil))
	}
	return &adksession.ListResponse{Sessions: out}, nil
}

// Delete removes a session and its events. Deleting a session that does not
// exist for the given app and user is a no-op.
func (s *ADKService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return status.Error(codes.InvalidArgument, "app_name, user_id and session_id are required")
	}
	return s.store.deleteSession(ctx, req.SessionID, req.AppName, req.UserID)
}

// AppendEvent persists an event and applies its state delta.
//
// Partial (streaming) events are not persisted. An event that arrives
// without an ID or timestamp gets them assigned in place, through the
// platform package so a provider installed on ctx controls them. The stored
// copy drops "temp:" state keys; the live session keeps them for the rest of
// the invocation, as ADK's own backends do.
func (s *ADKService) AppendEvent(ctx context.Context, sess adksession.Session, event *adksession.Event) error {
	if sess == nil {
		return status.Error(codes.InvalidArgument, "session is required")
	}
	if event == nil {
		return status.Error(codes.InvalidArgument, "event is required")
	}
	if event.Partial {
		return nil
	}
	if event.ID == "" {
		event.ID = platform.NewUUID(ctx)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = platform.Now(ctx)
	}
	pbEvent, err := eventToProto(sess, event)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid event: %v", err)
	}
	if _, err := s.store.appendEvent(ctx, sess.ID(), sess.AppName(), sess.UserID(), pbEvent); err != nil {
		return err
	}
	if dbSession, ok := sess.(*DatabaseSession); ok {
		dbSession.appendEvent(event)
	}
	return nil
}

// DatabaseSession is the ADK-facing view of a stored session: a merged
// app/user/session state map and the event list, both kept live as events
// are appended so a running invocation sees its own writes.
type DatabaseSession struct {
	mu         sync.RWMutex
	id         string
	appName    string
	userID     string
	updateTime time.Time
	state      map[string]any
	events     []*adksession.Event
}

func newDatabaseSession(session *pb.Session, events []*adksession.Event, appState, userState map[string]any) *DatabaseSession {
	return &DatabaseSession{
		id:         session.GetId(),
		appName:    session.GetAppName(),
		userID:     session.GetUserId(),
		updateTime: timeFromProto(session.GetUpdateTime()),
		state:      mergeStateMaps(appState, userState, structMap(session.GetState())),
		events:     events,
	}
}

func (s *DatabaseSession) ID() string      { return s.id }
func (s *DatabaseSession) AppName() string { return s.appName }
func (s *DatabaseSession) UserID() string  { return s.userID }

func (s *DatabaseSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updateTime
}

func (s *DatabaseSession) State() adksession.State   { return &DatabaseSessionState{session: s} }
func (s *DatabaseSession) Events() adksession.Events { return &DatabaseSessionEvents{session: s} }

func (s *DatabaseSession) appendEvent(event *adksession.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, copyEvent(event))
	s.updateTime = event.Timestamp
	maps.Copy(s.state, event.Actions.StateDelta)
}

// DatabaseSessionState is a live view over a DatabaseSession's merged state.
type DatabaseSessionState struct {
	session *DatabaseSession
}

func (s *DatabaseSessionState) Get(key string) (any, error) {
	s.session.mu.RLock()
	defer s.session.mu.RUnlock()
	value, ok := s.session.state[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return value, nil
}

func (s *DatabaseSessionState) Set(key string, value any) error {
	s.session.mu.Lock()
	defer s.session.mu.Unlock()
	s.session.state[key] = value
	return nil
}

func (s *DatabaseSessionState) All() iter.Seq2[string, any] {
	s.session.mu.RLock()
	snapshot := maps.Clone(s.session.state)
	s.session.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for k, v := range snapshot {
			if !yield(k, v) {
				return
			}
		}
	}
}

// DatabaseSessionEvents is a live view over a DatabaseSession's events.
type DatabaseSessionEvents struct {
	session *DatabaseSession
}

func (s *DatabaseSessionEvents) All() iter.Seq[*adksession.Event] {
	s.session.mu.RLock()
	snapshot := slices.Clone(s.session.events)
	s.session.mu.RUnlock()
	return func(yield func(*adksession.Event) bool) {
		for _, event := range snapshot {
			if !yield(event) {
				return
			}
		}
	}
}

func (s *DatabaseSessionEvents) Len() int {
	s.session.mu.RLock()
	defer s.session.mu.RUnlock()
	return len(s.session.events)
}

func (s *DatabaseSessionEvents) At(i int) *adksession.Event {
	s.session.mu.RLock()
	defer s.session.mu.RUnlock()
	if i < 0 || i >= len(s.session.events) {
		return nil
	}
	return s.session.events[i]
}

// copyEvent detaches the stored copy from the caller's event the same way
// ADK's in-memory service does: maps and slices are cloned, the compaction
// record is deep-copied, everything else is shared.
func copyEvent(in *adksession.Event) *adksession.Event {
	out := *in
	out.Actions.StateDelta = maps.Clone(in.Actions.StateDelta)
	out.Actions.ArtifactDelta = maps.Clone(in.Actions.ArtifactDelta)
	out.Actions.RequestedToolConfirmations = maps.Clone(in.Actions.RequestedToolConfirmations)
	out.Actions.Compaction = copyCompaction(in.Actions.Compaction)
	out.LongRunningToolIDs = slices.Clone(in.LongRunningToolIDs)
	out.Routes = slices.Clone(in.Routes)
	return &out
}

// copyCompaction deep-copies a compaction record. A stored record decides
// which events every later prompt drops, so the caller must not be able to
// edit it after the append.
func copyCompaction(in *adksession.EventCompaction) *adksession.EventCompaction {
	if in == nil {
		return nil
	}
	out := *in
	out.ExcludedEvents = slices.Clone(in.ExcludedEvents)
	if in.CompactedContent != nil {
		content := *in.CompactedContent
		content.Parts = slices.Clone(in.CompactedContent.Parts)
		for i, part := range content.Parts {
			if part == nil {
				continue
			}
			copied := *part
			content.Parts[i] = &copied
		}
		out.CompactedContent = &content
	}
	return &out
}
