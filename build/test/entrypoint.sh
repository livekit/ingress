#!/usr/bin/env bash
set -exo pipefail


# Run tests
if [[ -z "${GITHUB_WORKFLOW}" ]]; then
  exec go test -v -timeout 20m ./pkg/...
else
  go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
  exec go tool test2json -p ingress go test -test.v=true -test.timeout 20m ./pkg/... 2>&1 | "$HOME"/go/bin/gotestfmt
fi
