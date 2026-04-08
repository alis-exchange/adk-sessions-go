package sessions

import (
	"encoding/base64"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "go.alis.build/common/alis/adk/sessions/v1"
)

var (
	sessionNamePattern = regexp.MustCompile(`^sessions/([^/]+)$`)
	eventNamePattern   = regexp.MustCompile(`^sessions/([^/]+)/events/([^/]+)$`)
	sessionFilterRE    = regexp.MustCompile(`^\s*(display_name|user_id|app_name)\s*=\s*"([^"]*)"\s*$`)
	eventFilterRE      = regexp.MustCompile(`^\s*timestamp\s*(<=|>=|=|<|>)\s*"([^"]+)"\s*$`)
)

type sessionRecord struct {
	SessionID  string
	AppName    string
	UserID     string
	CreateTime time.Time
	UpdateTime time.Time
	Session    *pb.Session
}

type eventRecord struct {
	SessionID string
	AppName   string
	UserID    string
	EventID   string
	Timestamp time.Time
	Event     *pb.SessionEvent
}

func parseSessionName(name string) (string, error) {
	m := sessionNamePattern.FindStringSubmatch(name)
	if len(m) != 2 {
		return "", status.Errorf(codes.InvalidArgument, "invalid session name %q", name)
	}
	return m[1], nil
}

func parseEventName(name string) (string, string, error) {
	m := eventNamePattern.FindStringSubmatch(name)
	if len(m) != 3 {
		return "", "", status.Errorf(codes.InvalidArgument, "invalid event name %q", name)
	}
	return m[1], m[2], nil
}

func sessionName(id string) string {
	return "sessions/" + id
}

func eventName(sessionID, eventID string) string {
	return sessionName(sessionID) + "/events/" + eventID
}

func newPageToken(offset int) string {
	if offset <= 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func parsePageToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid page_token")
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, status.Errorf(codes.InvalidArgument, "invalid page_token")
	}
	return offset, nil
}

func cloneSession(in *pb.Session) *pb.Session {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*pb.Session)
}

func cloneEvent(in *pb.SessionEvent) *pb.SessionEvent {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*pb.SessionEvent)
}

func normalizePageSize(size int32) int {
	if size <= 0 {
		return 100
	}
	if size > 1000 {
		return 1000
	}
	return int(size)
}

func normalizeSession(session *pb.Session) (*pb.Session, map[string]any, map[string]any, error) {
	out := cloneSession(session)
	if out == nil {
		return nil, nil, nil, status.Error(codes.InvalidArgument, "session is required")
	}
	if out.GetAppName() == "" || out.GetUserId() == "" {
		return nil, nil, nil, status.Error(codes.InvalidArgument, "session.app_name and session.user_id are required")
	}
	appState, userState, sessionState := splitScopedState(structMap(out.GetState()))
	if len(sessionState) == 0 {
		out.State = nil
	} else {
		st, err := structpb.NewStruct(sessionState)
		if err != nil {
			return nil, nil, nil, status.Errorf(codes.InvalidArgument, "invalid session state: %v", err)
		}
		out.State = st
	}
	switch exp := out.GetExpiration().(type) {
	case *pb.Session_Ttl:
		if exp.Ttl != nil {
			out.Expiration = &pb.Session_ExpireTime{
				ExpireTime: timestamppb.New(time.Now().Add(exp.Ttl.AsDuration())),
			}
		} else {
			out.Expiration = nil
		}
	}
	return out, appState, userState, nil
}

func splitScopedState(state map[string]any) (appState, userState, sessionState map[string]any) {
	appState = map[string]any{}
	userState = map[string]any{}
	sessionState = map[string]any{}
	for key, value := range state {
		switch {
		case strings.HasPrefix(key, adksession.KeyPrefixTemp):
		case strings.HasPrefix(key, adksession.KeyPrefixApp):
			appState[strings.TrimPrefix(key, adksession.KeyPrefixApp)] = value
		case strings.HasPrefix(key, adksession.KeyPrefixUser):
			userState[strings.TrimPrefix(key, adksession.KeyPrefixUser)] = value
		default:
			sessionState[key] = value
		}
	}
	return appState, userState, sessionState
}

func structMap(st *structpb.Struct) map[string]any {
	if st == nil {
		return nil
	}
	return st.AsMap()
}

func mergeStateMaps(appState, userState, sessionState map[string]any) map[string]any {
	out := make(map[string]any, len(appState)+len(userState)+len(sessionState))
	maps.Copy(out, sessionState)
	for k, v := range appState {
		out[adksession.KeyPrefixApp+k] = v
	}
	for k, v := range userState {
		out[adksession.KeyPrefixUser+k] = v
	}
	return out
}

func mergeDelta(existing, delta map[string]any) (*structpb.Struct, error) {
	if len(existing) == 0 && len(delta) == 0 {
		return nil, nil
	}
	merged := map[string]any{}
	maps.Copy(merged, existing)
	maps.Copy(merged, delta)
	return structpb.NewStruct(merged)
}

func nextSessionID(candidate string) string {
	if candidate != "" {
		return candidate
	}
	return uuid.NewString()
}

func nextEventID(candidate string) string {
	if candidate != "" {
		return candidate
	}
	return uuid.NewString()
}

func applySessionOrderBy(orderBy string) string {
	if strings.TrimSpace(orderBy) == "" {
		return "update_time DESC"
	}
	switch strings.ToLower(strings.TrimSpace(orderBy)) {
	case "create_time", "create_time asc":
		return "create_time ASC"
	case "create_time desc":
		return "create_time DESC"
	case "update_time", "update_time asc":
		return "update_time ASC"
	case "update_time desc":
		return "update_time DESC"
	default:
		return "update_time DESC"
	}
}

func applyEventOrderBy(orderBy string) string {
	if strings.TrimSpace(orderBy) == "" {
		return "timestamp ASC"
	}
	switch strings.ToLower(strings.TrimSpace(orderBy)) {
	case "timestamp", "timestamp asc":
		return "timestamp ASC"
	case "timestamp desc":
		return "timestamp DESC"
	default:
		return "timestamp ASC"
	}
}

func buildSessionFilter(filter string, params map[string]any) (string, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "", nil
	}
	m := sessionFilterRE.FindStringSubmatch(filter)
	if len(m) != 3 {
		return "", status.Errorf(codes.InvalidArgument, "unsupported filter %q", filter)
	}
	field := m[1]
	params[field] = m[2]
	return fmt.Sprintf("%s = @%s", field, field), nil
}

func buildEventFilter(filter string, params map[string]any) (string, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "", nil
	}
	m := eventFilterRE.FindStringSubmatch(filter)
	if len(m) != 3 {
		return "", status.Errorf(codes.InvalidArgument, "unsupported filter %q", filter)
	}
	ts, err := time.Parse(time.RFC3339, m[2])
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "invalid timestamp filter %q", m[2])
	}
	params["filter_ts"] = ts
	return fmt.Sprintf("timestamp %s @filter_ts", m[1]), nil
}

func durationToProto(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}
