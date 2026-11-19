#!/bin/sh
# Regenerate types.gen.go from the OpenAPI spec. docs/api is the source of
# truth; the Go emission needs the NON-dereferenced bundle so oapi-codegen
# sees $ref pointers into components/schemas (the --dereferenced api.json
# inlines every ref, which makes oapi-codegen skip the component models).
# oapi-codegen is pinned to the same version the CI drift check uses.
set -e
cd "$(dirname "$0")"
npm run build:bundle --prefix ../../../docs/api >/dev/null
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2 \
  --config oapi-codegen.yaml ../../../docs/api/dist/api.yaml
