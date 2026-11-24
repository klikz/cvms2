# CVMS server. Run these inside the server/ folder:
#   make run       - run (Ubuntu)
#   make build     - build for Ubuntu  -> builds/cvms-server-<version>-linux-amd64
#   make runWin    - run (Windows)
#   make buildWin  - build for Windows -> builds/cvms-server-<version>-windows-amd64.exe
#   make stop      - stop the running server (OS service and `make run` copies)
#   make update    - git pull the whole project
#   make push      - commit + push the whole project (make push m="message")
# The version comes from ../VERSION. Recipes use only `go`, so they work in
# both cmd.exe (Windows make) and sh (Linux make).

APP     := server
VERSION := $(strip $(file < ../VERSION))
LDFLAGS := -s -w -X main.version=$(VERSION)
OUT     := builds

.PHONY: run build runWin buildWin stop sync-ui test vet

# copy ../shared/ui into web/shared (go:embed cannot reach outside the module)
sync-ui: export GOOS :=
sync-ui: export GOARCH :=
sync-ui:
	go run cvms/shared/tools/syncui ../shared/ui web/shared

run: sync-ui
	go run -ldflags "-X main.version=$(VERSION)" ./cmd/$(APP)

runWin: sync-ui
	go run -ldflags "-X main.version=$(VERSION)" ./cmd/$(APP)

build: export GOOS := linux
build: export GOARCH := amd64
build: export CGO_ENABLED := 0
build: sync-ui
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT)/cvms-$(APP)-$(VERSION)-linux-amd64 ./cmd/$(APP)

buildWin: export GOOS := windows
buildWin: export GOARCH := amd64
buildWin: export CGO_ENABLED := 0
buildWin: sync-ui
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT)/cvms-$(APP)-$(VERSION)-windows-amd64.exe ./cmd/$(APP)

# stop the OS service (needs admin/sudo) and any console copy (make run / built binary)
stop: export GOOS :=
stop: export GOARCH :=
stop:
	go run cvms/shared/tools/stopapp $(APP)

test: sync-ui
	go test ./...

vet: sync-ui
	go vet ./...

# make update / make push act on the whole project (see ../git.mk)
include ../git.mk
