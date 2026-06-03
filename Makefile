install:
	go mod download
	go install

build:
	go mod download
	go build

docker_build:
	docker build -t itcrow/clickhouse-bulk:local .

# Sustained load test (not run in default `go test`; see docs/LOAD_TEST.md)
loadtest:
	LOAD_TEST=1 LOAD_TEST_DURATION=2m LOAD_TEST_RPS=500 LOAD_TEST_MAC_COUNT=500 \
		go test -timeout=10m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-journal:
	LOAD_TEST=1 LOAD_TEST_DURATION=2m LOAD_TEST_RPS=500 LOAD_TEST_MAC_COUNT=500 LOAD_TEST_JOURNAL=true LOAD_TEST_MAX_INFLIGHT=500 \
		go test -timeout=10m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-long:
	LOAD_TEST=1 LOAD_TEST_DURATION=10m LOAD_TEST_RPS=500 LOAD_TEST_MAC_COUNT=500 \
		go test -timeout=25m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-long-journal:
	LOAD_TEST=1 LOAD_TEST_DURATION=10m LOAD_TEST_RPS=500 LOAD_TEST_MAC_COUNT=500 LOAD_TEST_JOURNAL=true LOAD_TEST_MAX_INFLIGHT=500 \
		go test -timeout=25m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-ch-delay:
	LOAD_TEST=1 LOAD_TEST_DURATION=1m LOAD_TEST_RPS=300 LOAD_TEST_MAC_COUNT=300 LOAD_TEST_CH_DELAY=200ms \
		go test -timeout=10m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-ch-delay-journal:
	LOAD_TEST=1 LOAD_TEST_DURATION=1m LOAD_TEST_RPS=300 LOAD_TEST_MAC_COUNT=300 LOAD_TEST_CH_DELAY=200ms LOAD_TEST_JOURNAL=true LOAD_TEST_MAX_INFLIGHT=500 \
		go test -timeout=10m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-ch-delay-long:
	LOAD_TEST=1 LOAD_TEST_DURATION=10m LOAD_TEST_RPS=300 LOAD_TEST_MAC_COUNT=300 LOAD_TEST_CH_DELAY=2000ms LOAD_TEST_MAX_INFLIGHT=500 \
		go test -timeout=25m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-ch-delay-long-journal:
	LOAD_TEST=1 LOAD_TEST_DURATION=10m LOAD_TEST_RPS=300 LOAD_TEST_MAC_COUNT=300 LOAD_TEST_CH_DELAY=200ms LOAD_TEST_JOURNAL=true LOAD_TEST_MAX_INFLIGHT=500 \
		go test -timeout=25m -run TestLoad_SustainedInsert -count=1 -v .

loadtest-profile:
	LOAD_TEST=1 LOAD_TEST_DURATION=1m LOAD_TEST_RPS=300 LOAD_TEST_MAC_COUNT=300 LOAD_TEST_PROFILE=1 LOAD_TEST_PROFILE_DIR=/tmp/bulk-load \
		go test -timeout=10m -run TestLoad_SustainedInsert -count=1 -v .

include Makefile.loadtest
	