#!/bin/sh
# Regenerate types.gen.go from the OpenAPI spec. docs/api is the source of
# truth; the Go emission needs the NON-dereferenced bundle so oapi-codegen
# sees $ref pointers into components/schemas (the --dereferenced api.json
# inlines every ref, which makes oapi-codegen skip the component models).
set -e
cd "$(dirname "$0")/../../../docs/api"
npm run build:bundle >/dev/null
oapi-codegen --config oapi-codegen.yaml dist/api.yaml
