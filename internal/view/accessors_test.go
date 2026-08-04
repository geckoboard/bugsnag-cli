package view_test

import (
	"encoding/json"
	"testing"

	"github.com/geckoboard/bugsnag-cli/internal/view"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

// The four payload shapes that actually occur. These are trimmed from real
// responses, keeping the parts that differ: a Go full report, a Rails full
// report, the thin projection `events list` returns, and a Node full report.

// goFullReport: snake_case keys, and in_project absent on every frame. Absent is
// the point: treating it as false would hide the whole trace.
const goFullReport = `{
  "id": "6a68d7fa018f5404872a0000",
  "received_at": "2026-07-28T11:59:22.000Z",
  "severity": "warning",
  "unhandled": false,
  "context": "unknown method:0",
  "exceptions": [
    {
      "error_class": "*fmt.wrapError",
      "message": "store: unable to upsert records: context deadline exceeded",
      "stacktrace": [
        {
          "file": "/home/runner/project/queue/manager.go",
          "method": "(*Manager).Process",
          "line_number": 105,
          "in_project": null
        },
        {
          "file": "github.com/example-org/queue/event/event.go",
          "method": "(*EnvelopePayload).DecodeAndProcess",
          "line_number": 73
        }
      ]
    }
  ]
}`

// railsFullReport: snake_case, in_project genuinely present.
const railsFullReport = `{
  "id": "aaa1",
  "exceptions": [
    {
      "error_class": "NoMethodError",
      "message": "undefined method ` + "`foo`" + ` for nil:NilClass",
      "stacktrace": [
        {
          "file": "app/controllers/users_controller.rb",
          "method": "show",
          "line_number": 42,
          "in_project": true,
          "code": {"41": "  def show", "42": "    @user.foo", "43": "  end"}
        },
        {
          "file": "actionpack/lib/action_controller/metal.rb",
          "method": "dispatch",
          "line_number": 190,
          "in_project": false
        }
      ]
    }
  ]
}`

// nodeThinProjection is what `events list` returns: camelCase keys, and no
// top-level error_class or message at all.
const nodeThinProjection = `{
  "id": "bbb2",
  "received_at": "2026-07-28T11:00:00.000Z",
  "exceptions": [
    {
      "errorClass": "TypeError",
      "message": "Cannot read properties of undefined (reading 'id')",
      "stacktrace": [
        {
          "file": "src/handlers/user.js",
          "method": "getUser",
          "lineNumber": 27,
          "columnNumber": 13,
          "inProject": true
        }
      ]
    }
  ]
}`

// nodeFullReport: snake_case, column_number as an integer despite the spec
// declaring it a string, and source_control_link as "" rather than null.
const nodeFullReport = `{
  "id": "ccc3",
  "exceptions": [
    {
      "error_class": "TypeError",
      "message": "Cannot read properties of undefined (reading 'id')",
      "stacktrace": [
        {
          "file": "src/handlers/user.js",
          "method": "getUser",
          "line_number": 27,
          "column_number": 13,
          "in_project": true,
          "source_control_link": ""
        }
      ]
    }
  ]
}`

// TestErrorClassAndMessageAcrossShapes guards against empty event listings.
// Reading the top-level error_class and message, which `events list` does not
// send, yields zero values; both live in exceptions[0].
func TestErrorClassAndMessageAcrossShapes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		payload    string
		wantClass  string
		wantPrefix string
	}{
		{"go full report, snake_case", goFullReport, "*fmt.wrapError", "store: unable to upsert"},
		{"rails full report, snake_case", railsFullReport, "NoMethodError", "undefined method"},
		{"node thin projection, camelCase", nodeThinProjection, "TypeError", "Cannot read properties"},
		{"node full report, snake_case", nodeFullReport, "TypeError", "Cannot read properties"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e view.Event
			assert.NilError(t, json.Unmarshal([]byte(tc.payload), &e))

			assert.Check(t, is.Equal(e.Class(), tc.wantClass))
			msg := e.Msg()
			assert.Check(t, len(msg) >= len(tc.wantPrefix) && msg[:len(tc.wantPrefix)] == tc.wantPrefix,
				"Msg = %q, want it to start with %q", msg, tc.wantPrefix)
		})
	}
}

// TestInProjectIsThreeValued: on Go events the field is absent, and absent must
// not read as false. Frame filtering keys off this, so getting it wrong shows an
// empty stack trace on every Go service.
func TestInProjectIsThreeValued(t *testing.T) {
	t.Run("absent or null on go events", func(t *testing.T) {
		frames := view.ExceptionsFrom(json.RawMessage(goFullReport))[0].Stacktrace
		assert.Assert(t, is.Len(frames, 2))
		for i, f := range frames {
			assert.Check(t, is.Equal(f.InProject, view.Unknown), "frame %d", i)
			_, present := f.InProject.Bool()
			assert.Check(t, !present, "frame %d reports a value it does not have", i)
		}
	})

	t.Run("present on rails events", func(t *testing.T) {
		frames := view.ExceptionsFrom(json.RawMessage(railsFullReport))[0].Stacktrace
		assert.Check(t, is.Equal(frames[0].InProject, view.Yes))
		assert.Check(t, is.Equal(frames[1].InProject, view.No))

		// false must be distinguishable from absent.
		v, present := frames[1].InProject.Bool()
		assert.Check(t, !v && present, "library frame Bool() = (%v, %v), want (false, true)", v, present)
	})

	t.Run("camelCase key is read too", func(t *testing.T) {
		frames := view.ExceptionsFrom(json.RawMessage(nodeThinProjection))[0].Stacktrace
		assert.Check(t, is.Equal(frames[0].InProject, view.Yes))
	})
}

// TestColumnNumberIsRead covers the field the spec declares a string and the API
// returns as an integer, in both key casings.
func TestColumnNumberIsRead(t *testing.T) {
	for name, payload := range map[string]string{
		"camelCase columnNumber":   nodeThinProjection,
		"snake_case column_number": nodeFullReport,
	} {
		t.Run(name, func(t *testing.T) {
			frame := view.ExceptionsFrom(json.RawMessage(payload))[0].Stacktrace[0]
			assert.Check(t, is.Equal(frame.ColumnNumber, 13))
			assert.Check(t, is.Equal(frame.LineNumber, 27))
		})
	}
}

// TestColumnNumberAcceptsTheStringForm, in case a platform sends what the spec
// declares.
func TestColumnNumberAcceptsTheStringForm(t *testing.T) {
	payload := `{"exceptions":[{"error_class":"E","stacktrace":[{"file":"a.js","column_number":"13","line_number":"27"}]}]}`
	frame := view.ExceptionsFrom(json.RawMessage(payload))[0].Stacktrace[0]

	assert.Check(t, is.Equal(frame.ColumnNumber, 13))
	assert.Check(t, is.Equal(frame.LineNumber, 27))
}

func TestCodeSnippetIsRead(t *testing.T) {
	frame := view.ExceptionsFrom(json.RawMessage(railsFullReport))[0].Stacktrace[0]
	assert.Assert(t, is.Len(frame.Code, 3))
	assert.Check(t, is.Equal(frame.Code["42"], "    @user.foo"))
}

// TestExceptionChainOrder: the first entry is the outermost exception, and each
// later one caused the previous. That is the order "Caused by" reads in.
func TestExceptionChainOrder(t *testing.T) {
	payload := `{"exceptions":[
		{"error_class":"*fmt.wrapError","message":"outer"},
		{"error_class":"*errors.errorString","message":"inner cause"}
	]}`

	chain := view.ExceptionsFrom(json.RawMessage(payload))
	assert.Assert(t, is.Len(chain, 2))
	assert.Check(t, is.Equal(chain[0].Message, "outer"), "first exception, want the outermost")
	assert.Check(t, is.Equal(chain[1].Message, "inner cause"), "second exception, want the cause")
}

func TestMalformedPayloadsReturnNothingRatherThanPanicking(t *testing.T) {
	for _, payload := range []string{
		``,
		`null`,
		`{}`,
		`{"exceptions":null}`,
		`{"exceptions":[]}`,
		`{"exceptions":"not an array"}`,
		`{"exceptions":[null]}`,
		`{"exceptions":[{"stacktrace":"not an array"}]}`,
		`{"exceptions":[{"stacktrace":[null,"string"]}]}`,
		`not json at all`,
	} {
		view.ExceptionsFrom(json.RawMessage(payload))

		// Decoding a malformed payload may fail; it must not panic, and the
		// accessors must stay callable either way.
		var e view.Event
		_ = json.Unmarshal([]byte(payload), &e)
		e.Class()
		e.Msg()
	}
}
