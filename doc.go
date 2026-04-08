// Package sessions provides Google Cloud Spanner backed persistence for ADK
// sessions and events.
//
// The package exposes two layers:
//
//   - SpannerService: CRUD and event append operations over the protobuf
//     session model.
//   - ADKService: an adapter that implements google.golang.org/adk/session.Service
//     on top of the same storage.
//
// Expected Spanner schema:
//
// Sessions table:
//
//	session_id STRING(MAX) NOT NULL,
//	app_name STRING(MAX) NOT NULL,
//	user_id STRING(MAX) NOT NULL,
//	Session PROTO<alis.adk.sessions.v1.Session> NOT NULL,
//	Policy PROTO<google.iam.v1.Policy>,
//	create_time TIMESTAMP AS (...) STORED,
//	update_time TIMESTAMP AS (...) STORED
//
// SessionEvents table:
//
//	session_id STRING(MAX) NOT NULL,
//	app_name STRING(MAX) NOT NULL,
//	user_id STRING(MAX) NOT NULL,
//	event_id STRING(MAX) NOT NULL,
//	SessionEvent PROTO<alis.adk.sessions.v1.SessionEvent> NOT NULL,
//	Policy PROTO<google.iam.v1.Policy>,
//	timestamp TIMESTAMP AS (...) STORED
//
// AppStates table:
//
//	app_name STRING(MAX) NOT NULL,
//	AppState PROTO<alis.adk.sessions.v1.AppState> NOT NULL,
//	Policy PROTO<google.iam.v1.Policy>,
//	update_time TIMESTAMP AS (...) STORED
//
// UserStates table:
//
//	app_name STRING(MAX) NOT NULL,
//	user_id STRING(MAX) NOT NULL,
//	UserState PROTO<alis.adk.sessions.v1.UserState> NOT NULL,
//	Policy PROTO<google.iam.v1.Policy>,
//	update_time TIMESTAMP AS (...) STORED
package sessions
