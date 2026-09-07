// Package spannertest creates throwaway databases on the Cloud Spanner
// emulator carrying this module's schema, so tests can exercise the real
// store instead of a fake.
//
// Tests opt in by setting SPANNER_EMULATOR_HOST (see scripts/spanner-emulator.sh);
// without it every helper here skips the calling test.
package spannertest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// Registers alis/adk/sessions/v1/sessions.proto so the descriptor set
	// and proto bundle can be built from the global registry.
	_ "go.alis.build/common/alis/adk/sessions"
)

const (
	sessionsProtoPath = "alis/adk/sessions/v1/sessions.proto"
	project           = "adk-sessions-test"
	instanceID        = "adk-sessions"
)

// Database identifies a database created by NewDatabase.
type Database struct {
	Project     string
	Instance    string
	Database    string
	TablePrefix string
}

// EmulatorHost returns SPANNER_EMULATOR_HOST or skips the test when unset.
func EmulatorHost(t testing.TB) string {
	t.Helper()
	host := os.Getenv("SPANNER_EMULATOR_HOST")
	if host == "" {
		t.Skip("SPANNER_EMULATOR_HOST is not set; skipping Spanner emulator test")
	}
	return host
}

// NewDatabase creates a fresh database on the emulator with the four session
// tables under tablePrefix, and drops it when the test ends.
func NewDatabase(ctx context.Context, t testing.TB, tablePrefix string) Database {
	t.Helper()
	db := CreateDatabase(ctx, t, TableStatements(tablePrefix))
	db.TablePrefix = tablePrefix
	return db
}

// CreateDatabase creates a fresh database on the emulator carrying the
// sessions proto bundle plus the given DDL statements, and drops it when the
// test ends.
func CreateDatabase(ctx context.Context, t testing.TB, ddl []string) Database {
	t.Helper()
	EmulatorHost(t)
	ensureInstance(ctx, t)

	admin, err := database.NewDatabaseAdminClient(ctx)
	if err != nil {
		t.Fatalf("spannertest: database admin client: %v", err)
	}
	t.Cleanup(func() { admin.Close() })

	descriptors, err := DescriptorSet()
	if err != nil {
		t.Fatalf("spannertest: descriptor set: %v", err)
	}
	bundle, err := ProtoBundleStatement()
	if err != nil {
		t.Fatalf("spannertest: proto bundle: %v", err)
	}
	// Database IDs allow [a-z][a-z0-9_-]{0,28}[a-z0-9].
	dbID := fmt.Sprintf("sessions-%d", time.Now().UnixNano()%1_000_000_000)
	parent := fmt.Sprintf("projects/%s/instances/%s", project, instanceID)
	op, err := admin.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:           parent,
		CreateStatement:  fmt.Sprintf("CREATE DATABASE `%s`", dbID),
		ExtraStatements:  append([]string{bundle}, ddl...),
		ProtoDescriptors: descriptors,
	})
	if err != nil {
		t.Fatalf("spannertest: create database: %v", err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatalf("spannertest: create database: %v", err)
	}
	name := parent + "/databases/" + dbID
	t.Cleanup(func() {
		if err := admin.DropDatabase(context.Background(), &databasepb.DropDatabaseRequest{Database: name}); err != nil {
			t.Logf("spannertest: drop database %s: %v", name, err)
		}
	})
	return Database{Project: project, Instance: instanceID, Database: dbID}
}

func ensureInstance(ctx context.Context, t testing.TB) {
	t.Helper()
	admin, err := instance.NewInstanceAdminClient(ctx)
	if err != nil {
		t.Fatalf("spannertest: instance admin client: %v", err)
	}
	defer admin.Close()
	op, err := admin.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     "projects/" + project,
		InstanceId: instanceID,
		Instance: &instancepb.Instance{
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/emulator-config", project),
			DisplayName: "adk sessions tests",
			NodeCount:   1,
		},
	})
	if status.Code(err) == codes.AlreadyExists {
		return
	}
	if err != nil {
		t.Fatalf("spannertest: create instance: %v", err)
	}
	if _, err := op.Wait(ctx); err != nil && status.Code(err) != codes.AlreadyExists {
		t.Fatalf("spannertest: create instance: %v", err)
	}
}

// DescriptorSet serializes the sessions proto file and its transitive
// imports, which is what Spanner needs alongside CREATE PROTO BUNDLE.
func DescriptorSet() ([]byte, error) {
	fd, err := protoregistry.GlobalFiles.FindFileByPath(sessionsProtoPath)
	if err != nil {
		return nil, err
	}
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(protoreflect.FileDescriptor)
	add = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			add(imports.Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(f))
	}
	add(fd)
	return proto.Marshal(set)
}

// ProtoBundleStatement lists every message and enum reachable from the
// sessions proto. Spanner requires the column types, every type they
// reference, and the parents of nested types to be in the bundle; listing
// the closure is simpler than curating that by hand.
func ProtoBundleStatement() (string, error) {
	fd, err := protoregistry.GlobalFiles.FindFileByPath(sessionsProtoPath)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	var names []string
	addEnum := func(ed protoreflect.EnumDescriptor) {
		name := string(ed.FullName())
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	var addMessage func(md protoreflect.MessageDescriptor)
	addMessage = func(md protoreflect.MessageDescriptor) {
		if md.IsMapEntry() {
			// Map entries are synthetic and cannot be bundled; the value
			// type is what needs to be known.
			if value := md.Fields().ByName("value"); value != nil {
				if value.Message() != nil {
					addMessage(value.Message())
				}
				if value.Enum() != nil {
					addEnum(value.Enum())
				}
			}
			return
		}
		name := string(md.FullName())
		if seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			field := fields.Get(i)
			if field.Message() != nil {
				addMessage(field.Message())
			}
			if field.Enum() != nil {
				addEnum(field.Enum())
			}
		}
		nested := md.Messages()
		for i := 0; i < nested.Len(); i++ {
			addMessage(nested.Get(i))
		}
		enums := md.Enums()
		for i := 0; i < enums.Len(); i++ {
			addEnum(enums.Get(i))
		}
	}
	messages := fd.Messages()
	for i := 0; i < messages.Len(); i++ {
		addMessage(messages.Get(i))
	}
	enums := fd.Enums()
	for i := 0; i < enums.Len(); i++ {
		addEnum(enums.Get(i))
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "`" + name + "`"
	}
	return "CREATE PROTO BUNDLE (" + strings.Join(quoted, ", ") + ")", nil
}

// TableStatements is the schema documented in the root package's doc.go,
// minus the optional Policy columns.
func TableStatements(prefix string) []string {
	table := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "_" + name
	}
	// Production DDL (see the README) derives these columns at microsecond
	// precision from Timestamp.seconds and Timestamp.nanos. The emulator
	// (1.5.57) crashes on any access to the int32 nanos field, so on the
	// emulator the generated columns carry whole seconds. Ordering and the
	// After filter are therefore second-granular in these tests.
	timestampColumn := func(column, path string) string {
		return fmt.Sprintf("%s TIMESTAMP AS (TIMESTAMP_SECONDS(%s.seconds)) STORED", column, path)
	}
	return []string{
		fmt.Sprintf(`CREATE TABLE %s (
  session_id STRING(MAX) NOT NULL,
  app_name STRING(MAX) NOT NULL,
  user_id STRING(MAX) NOT NULL,
  Session `+"`alis.adk.sessions.v1.Session`"+` NOT NULL,
  %s,
  %s
) PRIMARY KEY (session_id, app_name, user_id)`,
			table("Sessions"),
			timestampColumn("create_time", "Session.create_time"),
			timestampColumn("update_time", "Session.update_time")),
		fmt.Sprintf(`CREATE TABLE %s (
  session_id STRING(MAX) NOT NULL,
  app_name STRING(MAX) NOT NULL,
  user_id STRING(MAX) NOT NULL,
  event_id STRING(MAX) NOT NULL,
  SessionEvent `+"`alis.adk.sessions.v1.SessionEvent`"+` NOT NULL,
  %s
) PRIMARY KEY (session_id, app_name, user_id, event_id)`,
			table("SessionEvents"),
			timestampColumn("timestamp", "SessionEvent.timestamp")),
		fmt.Sprintf(`CREATE TABLE %s (
  app_name STRING(MAX) NOT NULL,
  AppState `+"`alis.adk.sessions.v1.AppState`"+` NOT NULL,
  %s
) PRIMARY KEY (app_name)`,
			table("AppStates"),
			timestampColumn("update_time", "AppState.update_time")),
		fmt.Sprintf(`CREATE TABLE %s (
  app_name STRING(MAX) NOT NULL,
  user_id STRING(MAX) NOT NULL,
  UserState `+"`alis.adk.sessions.v1.UserState`"+` NOT NULL,
  %s
) PRIMARY KEY (app_name, user_id)`,
			table("UserStates"),
			timestampColumn("update_time", "UserState.update_time")),
	}
}
