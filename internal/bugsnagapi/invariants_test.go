package bugsnagapi_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/bugsnagapi"
	"gotest.tools/v3/assert"
)

// responseTypes are the shapes the renderer decodes. Adding a command that
// decodes a new type should add it here.
func responseTypes() map[string]any {
	return map[string]any{
		"CommentApiView":            &bugsnagapi.CommentApiView{},
		"ElasticSearchEventApiView": &bugsnagapi.ElasticSearchEventApiView{},
		"ErrorApiView":              &bugsnagapi.ErrorApiView{},
		"EventApiView":              &bugsnagapi.EventApiView{},
		"EventField":                &bugsnagapi.EventField{},
		"OrganizationApiView":       &bugsnagapi.OrganizationApiView{},
		"PivotApiView":              &bugsnagapi.PivotApiView{},
		"ProjectApiView":            &bugsnagapi.ProjectApiView{},
	}
}

// TestNoFloat32 is the canary for the overlay's numeric patches.
//
// The spec types every count as `type: number`, which oapi-codegen maps to
// float32 — exact only up to 2^24. ErrorApiView.events and
// unthrottled_occurrence_count are the two counts the error view prints side by
// side, so silently rounding one of them is the worst output bug this tool could
// have. If an upstream spec refresh introduces a new `type: number` count, this
// fails and the fix is a new overlay action.
func TestNoFloat32(t *testing.T) {

	for name, v := range responseTypes() {
		t.Run(name, func(t *testing.T) {
			walk(t, reflect.TypeOf(v).Elem(), name, map[reflect.Type]bool{})
		})
	}
}

func walk(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
	t.Helper()

	if seen[typ] {
		return
	}
	seen[typ] = true

	switch typ.Kind() {
	case reflect.Float32:
		t.Errorf("%s is float32: counts must be integer and genuine floats must be float64 "+
			"(add an overlay action in api/openapi/overlay.yaml)", path)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walk(t, typ.Elem(), path, seen)
	case reflect.Map:
		walk(t, typ.Elem(), path+"[]", seen)
	case reflect.Struct:
		for i := range typ.NumField() {
			f := typ.Field(i)
			walk(t, f.Type, path+"."+f.Name, seen)
		}
	}
}

// TestFrameNumbersAreInts covers the overlay's column_number/line_number
// patches on both frame schemas. The spec models frames twice — snake_case for
// full event reports and camelCase for the thin list projection — and declares
// column_number a string despite the API returning an integer. Left unpatched,
// every JS/Node event fails to unmarshal and takes the text path down.
func TestFrameNumbersAreInts(t *testing.T) {

	t.Run("snake_case full report", func(t *testing.T) {
		var f bugsnagapi.UnderscoreEventExceptionStacktrace
		err := json.Unmarshal([]byte(`{"line_number":105,"column_number":13}`), &f)
		assert.NilError(t, err)
		assert.Assert(t, f.LineNumber != nil, "line_number is nil")
		assert.Equal(t, *f.LineNumber, 105)
		assert.Assert(t, f.ColumnNumber != nil, "column_number is nil")
		assert.Equal(t, *f.ColumnNumber, 13)
	})

	t.Run("camelCase thin projection", func(t *testing.T) {
		var f bugsnagapi.UnderscoreCoreFrameFields
		err := json.Unmarshal([]byte(`{"lineNumber":105,"columnNumber":13}`), &f)
		assert.NilError(t, err)
		assert.Assert(t, f.LineNumber != nil, "lineNumber is nil")
		assert.Equal(t, *f.LineNumber, 105)
		assert.Assert(t, f.ColumnNumber != nil, "columnNumber is nil")
		assert.Equal(t, *f.ColumnNumber, 13)
	})
}

// TestReopenRulesIsAnObject guards reopen_rules against being typed as a bool,
// which would fail outright on any error carrying one.
func TestReopenRulesIsAnObject(t *testing.T) {

	body := `{"id":"abc","reopen_rules":{"reopen_if":"n_occurrences_in_m_hours","occurrences":10,"hours":2}}`

	var got bugsnagapi.ErrorApiView
	err := json.Unmarshal([]byte(body), &got)
	assert.NilError(t, err)
	assert.Assert(t, got.ReopenRules != nil, "reopen_rules decoded as nil")
	assert.Assert(t, got.ReopenRules.Occurrences != nil, "occurrences is nil")
	assert.Equal(t, *got.ReopenRules.Occurrences, 10)
}

// TestFilterParamsAreAbsent proves the overlay removed the filter query
// parameters. oapi-codegen emits form+explode serialisation for them, which
// flattens to `error.status=<json>` instead of the bracket syntax the API
// wants; internal/filters hand-rolls the encoder instead. If a spec refresh
// reintroduces them, the generated encoder silently starts producing wrong
// query strings, so fail here instead.
func TestFilterParamsAreAbsent(t *testing.T) {

	typ := reflect.TypeFor[bugsnagapi.ListProjectErrorsParams]()
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		switch name {
		case "filters", "filter_groups", "filter_groups_join":
			t.Errorf("%s param is generated again; the overlay should remove it", name)
		}
	}
}
