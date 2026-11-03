GO_TAGS ?= with_quic,with_wireguard,with_utls,with_clash_api

.PHONY: test race vet native windows mobile-tools android apple apple-host-tests android-host-tests windows-host-tests apple-example protocol-check clean
test:
	go test -mod=readonly -tags '$(GO_TAGS)' ./...
race:
	go test -mod=readonly -race -tags '$(GO_TAGS)' ./...
vet:
	go vet -mod=readonly -tags '$(GO_TAGS)' ./...
native:
	GO_TAGS='$(GO_TAGS)' bash scripts/build-cshared.sh
windows:
	GO_TAGS='$(GO_TAGS)' bash scripts/build-windows.sh
protocol-check:
	python3 scripts/generate-protocol.py --check
mobile-tools:
	bash scripts/install-mobile-tools.sh
android:
	GO_TAGS='$(GO_TAGS)' bash scripts/build-mobile.sh android
apple:
	GO_TAGS='$(GO_TAGS)' bash scripts/build-mobile.sh apple
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
