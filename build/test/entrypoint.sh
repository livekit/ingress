#!/usr/bin/env bash
# Copyright 2023 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -exo pipefail


# Run tests. INTEGRATION selects the prebuilt integration suite, which needs a
# Redis and a room server that INGRESS_CONFIG_BODY or INGRESS_CONFIG_FILE points
# at. Without it, the unit tests run and need neither.
if [[ -n "${INTEGRATION}" ]]; then
  if [[ -z "${GITHUB_WORKFLOW}" ]]; then
    exec ./test.test -test.v -test.timeout 20m
  fi

  # Without the exit, the unit tests below run too: exec in a pipeline
  # replaces the subshell running that stage, not this script.
  go install github.com/mfridman/tparse@ba2512e7be150bfcbd6f6220d517d3741f8f2f75 # v0.18.0
  # stderr stays out of the pipe: tparse rejects the whole stream if a
  # non-JSON line follows the first JSON event.
  go tool test2json -p ingress ./test.test -test.v -test.timeout 20m | "$HOME"/go/bin/tparse -progress
  exit
fi

if [[ -z "${GITHUB_WORKFLOW}" ]]; then
  exec go test -v -timeout 20m ./pkg/...
else
  go install github.com/mfridman/tparse@ba2512e7be150bfcbd6f6220d517d3741f8f2f75 # v0.18.0
  exec go test -json -timeout 20m ./pkg/... | "$HOME"/go/bin/tparse -progress
fi
