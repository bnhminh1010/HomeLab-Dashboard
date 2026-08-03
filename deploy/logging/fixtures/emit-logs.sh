#!/bin/sh

# CI fixture for the optional Loki + Vector integration. Keep emitting after
# Vector attaches so the test asserts collection through the Podman-compatible
# Docker API rather than merely validating static configuration.
while :; do
  printf '%s\n' '{"level":"info","msg":"logging-pipeline-e2e","request_id":"fixture-request","trace_id":"fixture-trace"}'
  printf '%s\n' 'logging-pipeline-plaintext-e2e'
  sleep 1
done
