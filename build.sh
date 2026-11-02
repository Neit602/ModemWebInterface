#!/bin/sh
# Builds the panel for the MiFi 8800L CPU (ARMv7, 32-bit) and for this computer, for local testing.
set -e
cd "$(dirname "$0")"
mkdir -p dist
go vet ./...
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w" -o dist/mifi-panel .
go build -trimpath -ldflags "-s -w" -o dist/mifi-panel-local .
ls -la dist
