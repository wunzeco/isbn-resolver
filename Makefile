.PHONY: check fmt vet test

check: fmt vet test

fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to be run on:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	go vet ./...

# Deliberately no -count=1. `go test` links each test binary into a fresh
# temp dir and execs it, and macOS XProtect scans every unsigned executable on
# its first exec — Go does not sign its darwin/amd64 output, so every package
# pays a scan on every run that actually execs. Measured on this tree: the
# scans are globally serialised and cost the whole wall time (20 first-execs =
# 9.4s serial, 9.4s concurrent, with XprotectService burning +9.5s CPU either
# way). Without -count=1 the test result cache skips the exec entirely and the
# scan cost is zero. Note that -p 1 does NOT help (identical scan cost, and a
# slower build), and neither does ad-hoc codesigning the binaries.
test:
	go test ./...
