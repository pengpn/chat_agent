package response

import "errors"

const (
	RespCodeOK RespCode = 0  // operation succeeded
	RespFailed RespCode = -1 // operation failed
)

var responseText = map[RespCode]string{
	RespCodeOK: "ok",
	RespFailed: "failed",
}

type responseError struct {
	Code          RespCode
	InnerErrorMsg string
}

func (r responseError) Error() string { return r.InnerErrorMsg }

func NewError(code RespCode, msg string) error {
	return responseError{Code: code, InnerErrorMsg: msg}
}

func IsResponseError(err error) bool {
	var target responseError
	return errors.As(err, &target)
}

// IsReponseError is retained for compatibility with the spelling used in the
// referenced implementation. Prefer IsResponseError in new code.
func IsReponseError(err error) bool {
	return IsResponseError(err)
}
