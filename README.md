# adk-sessions-go

Session persistence and state management for multi-surface agent conversations.

This repository now contains a standalone Go package that provides:

- a Spanner-backed session CRUD and event append service over `go.alis.build/common/alis/adk/sessions/v1`
- an ADK adapter implementing `google.golang.org/adk/session.Service`

The package expects four Spanner tables: `Sessions`, `SessionEvents`, `AppStates`, and `UserStates`.
The exact column layout is documented in [`doc.go`](/Users/jankrynauw/test/adk-sessions-go/doc.go).

## Terraform Example

The package is designed around Spanner `PROTO` columns. A minimal Terraform example looks like this:

```hcl
resource "alis_google_spanner_table" "Sessions" {
  project  = var.spanner_project
  instance = var.spanner_instance
  database = var.spanner_database
  name     = "Sessions"

  schema = {
    columns = [
      {
        name           = "session_id"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name           = "app_name"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name           = "user_id"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name          = "Session"
        type          = "PROTO"
        proto_package = "alis.adk.sessions.v1.Session"
        required      = true
      },
      {
        name            = "create_time"
        type            = "TIMESTAMP"
        required        = false
        is_computed     = true
        computation_ddl = "TIMESTAMP_ADD(TIMESTAMP_SECONDS(Session.create_time.seconds), INTERVAL CAST(FLOOR(Session.create_time.nanos / 1000) AS INT64) MICROSECOND)"
        is_stored       = true
      },
      {
        name            = "update_time"
        type            = "TIMESTAMP"
        required        = false
        is_computed     = true
        computation_ddl = "TIMESTAMP_ADD(TIMESTAMP_SECONDS(Session.update_time.seconds), INTERVAL CAST(FLOOR(Session.update_time.nanos / 1000) AS INT64) MICROSECOND)"
        is_stored       = true
      },
    ]
  }
}

resource "alis_google_spanner_table" "AppStates" {
  project  = var.spanner_project
  instance = var.spanner_instance
  database = var.spanner_database
  name     = "AppStates"

  schema = {
    columns = [
      {
        name           = "app_name"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name          = "AppState"
        type          = "PROTO"
        proto_package = "alis.adk.sessions.v1.AppState"
        required      = true
      },
      {
        name            = "update_time"
        type            = "TIMESTAMP"
        required        = false
        is_computed     = true
        computation_ddl = "TIMESTAMP_ADD(TIMESTAMP_SECONDS(AppState.update_time.seconds), INTERVAL CAST(FLOOR(AppState.update_time.nanos / 1000) AS INT64) MICROSECOND)"
        is_stored       = true
      },
    ]
  }
}

resource "alis_google_spanner_table" "UserStates" {
  project  = var.spanner_project
  instance = var.spanner_instance
  database = var.spanner_database
  name     = "UserStates"

  schema = {
    columns = [
      {
        name           = "app_name"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name           = "user_id"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name          = "UserState"
        type          = "PROTO"
        proto_package = "alis.adk.sessions.v1.UserState"
        required      = true
      },
      {
        name            = "update_time"
        type            = "TIMESTAMP"
        required        = false
        is_computed     = true
        computation_ddl = "TIMESTAMP_ADD(TIMESTAMP_SECONDS(UserState.update_time.seconds), INTERVAL CAST(FLOOR(UserState.update_time.nanos / 1000) AS INT64) MICROSECOND)"
        is_stored       = true
      },
    ]
  }
}

resource "alis_google_spanner_table" "SessionEvents" {
  project  = var.spanner_project
  instance = var.spanner_instance
  database = var.spanner_database
  name     = "SessionEvents"

  schema = {
    columns = [
      {
        name           = "session_id"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name           = "app_name"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name           = "user_id"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name           = "event_id"
        type           = "STRING"
        is_primary_key = true
        required       = true
      },
      {
        name          = "SessionEvent"
        type          = "PROTO"
        proto_package = "alis.adk.sessions.v1.SessionEvent"
        required      = true
      },
      {
        name            = "timestamp"
        type            = "TIMESTAMP"
        required        = false
        is_computed     = true
        computation_ddl = "TIMESTAMP_ADD(TIMESTAMP_SECONDS(SessionEvent.timestamp.seconds), INTERVAL CAST(FLOOR(SessionEvent.timestamp.nanos / 1000) AS INT64) MICROSECOND)"
        is_stored       = true
      },
    ]
  }
}
```

If you want to mirror the old internal setup exactly, add optional `Policy`
proto columns and the foreign key from `SessionEvents.session_id` to
`Sessions.session_id`.

## Usage

Construct the Spanner-backed store first, then wrap it with the ADK adapter:

```go
package main

import (
	"context"
	"log"

	sessions "github.com/alis-exchange/adk-sessions-go"
	adksession "google.golang.org/adk/session"
)

func initSessionService(ctx context.Context) adksession.Service {
	store, err := sessions.NewSpannerService(ctx, sessions.SpannerConfig{
		Project:         "my-spanner-project",
		Instance:        "my-spanner-instance",
		Database:        "my-spanner-database",
		SessionsTable:   "Sessions",
		EventsTable:     "SessionEvents",
		AppStatesTable:  "AppStates",
		UserStatesTable: "UserStates",
	})
	if err != nil {
		log.Fatalf("init session store: %v", err)
	}

	return sessions.NewADKService(store)
}
```

If you also want to expose the CRUD/event RPC service from your gRPC server,
register the same store directly:

```go
grpcServer := grpc.NewServer()
store.Register(grpcServer)
```

That gives you both layers:

- `sessions.NewADKService(store)` for ADK runner integration
- `store.Register(grpcServer)` for the generated `SessionService` gRPC API
