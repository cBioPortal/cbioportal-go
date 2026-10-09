package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
)

// Error bodies and messages of Java's GlobalExceptionHandler: every handled
// exception is answered with {"message": "..."}.

// MsgBadJSON answers a request body that is not JSON (HttpMessageNotReadable).
const MsgBadJSON = "There is an error in the JSON format of the request payload"

// msgNotImplemented answers UnsupportedOperationException (HTTP 501).
const msgNotImplemented = "Requested API is not implemented yet"

// MsgStudyNotFound answers an unknown study id.
func MsgStudyNotFound(studyID string) string { return "Study not found: " + studyID }

func msgPatientNotFound(studyID, patientID string) string {
	return "Patient not found in study " + studyID + ": " + patientID
}

func msgSampleNotFound(studyID, sampleID string) string {
	return "Sample not found in study " + studyID + ": " + sampleID
}

func msgCancerTypeNotFound(id string) string { return "Cancer type not found: " + id }

// MsgMolecularProfileNotFound answers an unknown molecular profile id.
func MsgMolecularProfileNotFound(id string) string { return "Molecular profile not found: " + id }

// MsgGeneNotFound answers an unknown gene.
func MsgGeneNotFound(id string) string { return "Gene not found: " + id }

// MsgSampleListNotFound answers an unknown sample list id.
func MsgSampleListNotFound(id string) string { return "Sample list not found: " + id }

func msgGenePanelNotFound(id string) string { return "Gene panel not found: " + id }

func msgClinicalAttributeNotFound(studyID, attrID string) string {
	return "Clinical attribute not found in study " + studyID + ": " + attrID
}

// MsgEnumMismatch is the TypeMismatchException message for a request parameter
// that names no constant of the Java enum enumClass.
func MsgEnumMismatch(enumClass, value string) string {
	return "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant " +
		enumClass + "." + value
}

// MsgIntMismatch is the TypeMismatchException message for a non-integer
// Integer request parameter.
func MsgIntMismatch(value string) string {
	return `Request parameter type mismatch: java.lang.NumberFormatException: For input string: "` + value + `"`
}

// MsgListSize is the @Size(min = 1, max = 10000000) violation message.
func MsgListSize(name string) string { return name + " size must be between 1 and 10000000" }

// WriteError writes Java's ErrorResponse body with the given status.
func WriteError(w http.ResponseWriter, status int, msg string) {
	body, _ := json.Marshal(map[string]string{"message": msg})
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Del("Content-Length")
	w.WriteHeader(status)
	w.Write(body)
}

// badBody is raised (as a panic, recovered by bodyErrors) by readBody when the
// request body is not JSON of the shape the handler decodes into, so every
// handler answers like Spring's HttpMessageNotReadableException without
// checking the decode itself.
type badBody struct{}

// ReadBody decodes the JSON request body into v (when non-nil) and drains it:
// an unread large body makes Go close the keep-alive connection, which the
// browser reports as a failed request. Syntax errors and a top-level or
// object/array shape mismatch abort the request with Java's malformed-payload
// answer; scalar type mismatches are tolerated as Jackson coerces them. An
// empty body leaves v untouched, except on the paths whose filter the
// InvolvedCancerStudyExtractorInterceptor parses first.
func ReadBody(r *http.Request, v any) {
	if v == nil {
		io.Copy(io.Discard, r.Body)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if interceptedBodyPath(r.URL.Path) {
			panic(badBody{})
		}
		return
	}
	if !topLevelFits(raw, v) {
		panic(badBody{})
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(v); err != nil && malformedJSON(err) {
		panic(badBody{})
	}
}

// topLevelFits reports whether the body's top-level JSON value can decode into
// v: an array for slices, an object for structs and maps (null fits both).
func topLevelFits(raw []byte, v any) bool {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return true
	}
	first := bytes.TrimSpace(raw)[0]
	if first == 'n' {
		return true
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return true // json.RawMessage / []byte take any value
		}
		return first == '['
	case reflect.Struct, reflect.Map:
		return first == '{'
	}
	return true
}

// malformedJSON reports whether a decode error is one Jackson rejects too:
// invalid JSON, or an object / array where the target type has neither.
func malformedJSON(err error) bool {
	var syn *json.SyntaxError
	if errors.As(err, &syn) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) {
		return typ.Value == "object" || typ.Value == "array"
	}
	return false
}

// BodyErrors answers requests aborted by readBody: legacy endpoints whose
// filter the InvolvedCancerStudyExtractorInterceptor parses reply 200 with an
// empty body (the interceptor vetoes the request), every other endpoint 400
// with the malformed-payload message.
func BodyErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(badBody); !ok {
					panic(v)
				}
				if interceptedBodyPath(r.URL.Path) {
					w.WriteHeader(http.StatusOK)
					return
				}
				WriteError(w, http.StatusBadRequest, MsgBadJSON)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// interceptedBodyPaths are the POST endpoints (relative to /api) whose body
// InvolvedCancerStudyExtractorInterceptor parses before the legacy controller
// runs; endpoints served by the column-store controllers skip it.
var interceptedBodyPaths = map[string]bool{
	"/patients/fetch": true, "/molecular-profiles/fetch": true, "/clinical-attributes/counts/fetch": true,
	"/namespace-attributes/counts/fetch": true, "/clinical-data/fetch": true, "/gene-panel-data/fetch": true,
	"/molecular-data/fetch": true, "/mutations/fetch": true, "/copy-number-segments/fetch": true,
	"/custom-data-bin-counts/fetch": true, "/namespace-data-counts/fetch": true, "/custom-data-counts/fetch": true,
	"/structuralvariant-counts/fetch": true, "/sample-counts/fetch": true, "/clinical-data-table/fetch": true,
	"/clinical-data-enrichments/fetch": true, "/mutation-enrichments/fetch": true,
	"/copy-number-enrichments/fetch": true, "/expression-enrichments/fetch": true, "/treatments/fetch": true,
	"/structural-variant/fetch": true, "/generic_assay_data/fetch": true, "/treatments/patient": true,
	"/treatments/sample": true, "/generic-assay-enrichments/fetch": true,
	"/generic-assay-categorical-enrichments/fetch": true, "/generic-assay-binary-enrichments/fetch": true,
	"/survival-data/fetch": true, "/clinical-events-meta/fetch": true,
}

func interceptedBodyPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api")
	return ok && interceptedBodyPaths[rest]
}

// InterceptedBodyPath reports whether Java's
// InvolvedCancerStudyExtractorInterceptor parses the body of a POST to path,
// an /api/legacy/ alias resolved.
func InterceptedBodyPath(path string) bool {
	if to, ok := APIAliasPath(path); ok {
		path = to
	}
	return interceptedBodyPath(path)
}
