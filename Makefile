GO_TAGS ?= $(shell bash scripts/build-tags.sh native)
CLI_TAGS ?= $(shell bash scripts/build-tags.sh cli)
# SDK scripts choose their target profile. GNU make exports command-line and
# environment GO_TAGS overrides, but not the host-default value above.

.PHONY: test race vet native cli cli-test windows mobile-tools android apple apple-host-tests android-host-tests windows-host-tests apple-example protocol-check tags-check clean
test:
	go test -mod=readonly -tags '$(GO_TAGS)' -ldflags=-checklinkname=0 ./...
race:
	go test -mod=readonly -race -tags '$(GO_TAGS)' -ldflags=-checklinkname=0 ./...
vet:
	go vet -mod=readonly -tags '$(GO_TAGS)' ./...
native:
	bash scripts/build-cshared.sh
cli:
	GO_TAGS='$(CLI_TAGS)' bash scripts/build-linux-cli.sh $(CLI_TARGETS)
cli-test:
	CGO_ENABLED=0 go test -mod=readonly -tags '$(CLI_TAGS)' -ldflags=-checklinkname=0 ./cmd/sing-box/
windows:
	bash scripts/build-windows.sh
protocol-check:
	python3 scripts/generate-protocol.py --check
tags-check:
	python3 scripts/check-build-tags.py
mobile-tools:
	bash scripts/install-mobile-tools.sh
android:
	bash scripts/build-mobile.sh android
apple:
	bash scripts/build-mobile.sh apple
apple-host-tests:
	bash scripts/test-apple-host.sh
android-host-tests:
	bash scripts/test-android-host.sh
windows-host-tests:
	dotnet run --project examples/windows/Tests/HostTests.csproj -c Release
apple-example:
	bash scripts/build-apple-example.sh
clean:
	python3 scripts/clean.py
