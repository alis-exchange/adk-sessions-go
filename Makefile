SPANNER_EMULATOR_HOST ?= localhost:9010

.PHONY: test test-emulator emulator-start emulator-stop

# Pure Go tests; emulator-backed tests skip themselves.
test:
	go test ./...

# Runs the ADK session conformance suite against a Spanner emulator.
# Start one first with `make emulator-start` (needs Docker).
test-emulator:
	SPANNER_EMULATOR_HOST=$(SPANNER_EMULATOR_HOST) go test ./... -run TestADKServiceConformance -v

emulator-start:
	./scripts/spanner-emulator.sh start

emulator-stop:
	./scripts/spanner-emulator.sh stop
