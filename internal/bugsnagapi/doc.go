// Package bugsnagapi is the generated client for the Bugsnag Data Access API.
//
// Everything in client.gen.go is generated from the vendored spec plus
// api/openapi/overlay.yaml — do not hand-edit it. `make verify-codegen`
// regenerates and fails on any diff.
//
// Callers use the plain Client, never ClientWithResponses: the latter's Parse*
// helpers discard Body when json.Unmarshal fails, so a single spec/reality
// mismatch loses the response you wanted, and their per-operation response
// types make a single generic paginator impossible.
//
// The generated types are used only to render text. The --json path passes the
// API's own item bytes through, keeping its values intact, because re-marshalling
// through these types is lossy: metaData is map[string]interface{} so every
// number round-trips via float64, keys get alphabetised, and the closed _EventApp
// and _EventDevice structs drop any field a notifier added.
package bugsnagapi

//go:generate go tool oapi-codegen --config=codegen.yaml ../../api/openapi/bugsnag-data-access-api.yaml
