package sessions

import (
	"context"
	"testing"

	"cloud.google.com/go/spanner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"

	"go.alis.build/adk/sessions/v2/internal/spannertest"
)

// TestADKServiceConformance runs ADK's own session service conformance
// suite against a real Spanner emulator. It skips unless
// SPANNER_EMULATOR_HOST is set; see scripts/spanner-emulator.sh.
func TestADKServiceConformance(t *testing.T) {
	spannertest.EmulatorHost(t)
	ctx := context.Background()
	db := spannertest.NewDatabase(ctx, t, "adk_test")

	store, err := NewSpannerService(ctx, SpannerConfig{
		Project:     db.Project,
		Instance:    db.Instance,
		Database:    db.Database,
		TablePrefix: db.TablePrefix,
	})
	if err != nil {
		t.Fatalf("NewSpannerService() error = %v", err)
	}
	t.Cleanup(store.db.Close)

	setup := func(t *testing.T) adksession.Service {
		t.Helper()
		// Every subtest starts from empty tables; the suite counts rows.
		_, err := store.db.Apply(ctx, []*spanner.Mutation{
			spanner.Delete(store.sessionsTable(), spanner.AllKeys()),
			spanner.Delete(store.eventsTable(), spanner.AllKeys()),
			spanner.Delete(store.appStatesTable(), spanner.AllKeys()),
			spanner.Delete(store.userStatesTable(), spanner.AllKeys()),
		})
		if err != nil {
			t.Fatalf("truncate tables: %v", err)
		}
		return NewADKService(store)
	}

	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{
		SupportsUserProvidedSessionID: true,
		ProvidesServerAssignedEventID: false,
		AppName:                       "testApp",
	}, setup)
}
