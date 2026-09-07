// Package sessions provides Google Cloud Spanner backed persistence for
// google.golang.org/adk/v2 sessions and events.
//
// The package exposes two layers:
//
//   - SpannerService: CRUD and event append operations over the protobuf
//     session model (go.alis.build/common/alis/adk/sessions), served as the
//     generated alis.adk.sessions.v1.SessionService gRPC API.
//   - ADKService: an adapter that implements
//     google.golang.org/adk/v2/session.Service on top of the same storage.
//
// ADKService passes ADK's session conformance suite
// (google.golang.org/adk/v2/session/sessiontestsuite); see
// adk_service_conformance_test.go for how to run it against the Spanner
// emulator.
//
// # Expected Spanner schema
//
// The package uses fixed logical table names:
// Sessions, SessionEvents, AppStates, and UserStates.
// When SpannerConfig.TablePrefix is set, the runtime resolves them as
// <prefix>_Sessions, <prefix>_SessionEvents, <prefix>_AppStates, and
// <prefix>_UserStates.
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
//
// The database's proto bundle must carry a version of alis.adk.sessions.v1
// that includes the ADK v2 event fields (EventActions.compaction,
// SessionEvent.isolation_scope, routes, requested_input, output, node_info).
// Re-apply the schema after upgrading go.alis.build/common/alis/adk/sessions
// so the bundle stays in step with the Go package.
//
// # Fidelity
//
// Events round-trip losslessly for every field the proto carries. A few
// genai fields have no counterpart in alis.adk.sessions.v1 and are not
// persisted: Part.MediaResolution, ToolCall, ToolResponse, PartMetadata and
// AudioTranscription; FunctionCall.PartialArgs and WillContinue;
// FunctionResponse.WillContinue and Scheduling; ExecutableCode.ID and
// CodeExecutionResult.ID; VideoMetadata.FPS; LLMResponse.InputTranscription,
// OutputTranscription and SessionResumptionHandle;
// GroundingMetadata.ImageSearchQueries, GroundingChunk.Image,
// GroundingSupport.RenderedParts, the RetrievedContext fields
// CustomMetadata, FileSearchStore, PageNumber and MediaID,
// RAGChunk.ChunkID and FileID, GroundingChunkMaps.Route, and
// LogprobsResult.LogProbabilitySum.
//
// Event.Output, RequestInput.Payload and ToolConfirmation.Payload are stored
// as JSON values, so integers read back as float64 and structs as maps.
// RequestInput.ResponseSchema is stored as its JSON object form.
package sessions
