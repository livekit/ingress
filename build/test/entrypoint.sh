#!/usr/bin/env bash
set -exo pipefail


# Run tests
if [[ -z "${GITHUB_WORKFLOW}" ]]; then
  exec ./test.test -test.v -test.timeout 20m
else
  go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
  exec go tool test2json -p ingress ./test.test -test.v -test.timeout 20m 2>&1 | "$HOME"/go/bin/gotestfmt
fi
