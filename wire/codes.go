package wire

import "net/http"

// Code is what went wrong, in a form a client matches on rather than the
// wording. Every error answer is an Error carrying one, and each code has one
// status.
type Code string

const (
	CodeBadRequest           Code = "bad-request"
	CodeDigestMismatch       Code = "digest-mismatch"
	CodeUnauthorized         Code = "unauthorized"
	CodeNotFound             Code = "not-found"
	CodeMethodNotAllowed     Code = "method-not-allowed"
	CodeConflict             Code = "conflict"
	CodeLengthRequired       Code = "length-required"
	CodeTooLarge             Code = "too-large"
	CodeUnsupportedMediaType Code = "unsupported-media-type"
	CodeRangeNotSatisfiable  Code = "range-not-satisfiable"
	CodeInternal             Code = "internal"
	CodeStorage              Code = "storage"
	CodeNotConfigured        Code = "not-configured"
)

// CodeInfo is a code's status and meaning.
type CodeInfo struct {
	Code    Code
	Status  int
	Summary string
}

// Codes is every error code, in the order the docs list them.
var Codes = []CodeInfo{
	{CodeBadRequest, http.StatusBadRequest, "the request is malformed; the message says how"},
	{CodeDigestMismatch, http.StatusBadRequest, "the body did not hash to " + HeaderSHA256 + "; nothing was stored"},
	{CodeUnauthorized, http.StatusUnauthorized, "no valid bearer token for this route's scope, whatever the path, so a caller without one learns nothing"},
	{CodeNotFound, http.StatusNotFound, "no such route, object, target or key"},
	{CodeMethodNotAllowed, http.StatusMethodNotAllowed, "the path exists, not with this method; Allow lists the ones it has"},
	{CodeConflict, http.StatusConflict, "refused against what is stored: an older glaze run, an unreadable record, or an object replaced while being read"},
	{CodeLengthRequired, http.StatusLengthRequired, "send Content-Length: R2 stores a stream only of known length"},
	{CodeTooLarge, http.StatusRequestEntityTooLarge, "the body is over the route's limit"},
	{CodeUnsupportedMediaType, http.StatusUnsupportedMediaType, "the body is not the route's media type"},
	{CodeRangeNotSatisfiable, http.StatusRequestedRangeNotSatisfiable, "the Range starts past the end of the object"},
	{CodeInternal, http.StatusInternalServerError, "a bucket binding is missing, or an answer could not be encoded"},
	{CodeStorage, http.StatusBadGateway, "R2 failed, or a write did not read back as written"},
	{CodeNotConfigured, http.StatusServiceUnavailable, "the secret for this route's scope is not set on the Worker, so it refuses everything"},
}

// Status is the code's HTTP status; 500 for a code not in Codes.
func (c Code) Status() int {
	for _, i := range Codes {
		if i.Code == c {
			return i.Status
		}
	}
	return http.StatusInternalServerError
}
