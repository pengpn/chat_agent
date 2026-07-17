// Package response defines the JSON envelope shared by HTTP API handlers.
package response

type RespCode int
type RespData any

// Response is the stable JSON contract returned by ordinary REST endpoints.
// Transport-level failures still use their appropriate HTTP status code.
type Response struct {
	Code RespCode `json:"code"`
	Msg  string   `json:"msg"`
	Data RespData `json:"data"`
}

func (resp *Response) SetData(data RespData) *Response {
	resp.Data = data
	return resp
}

func (resp *Response) SetMsg(msg string) *Response {
	resp.Msg = msg
	return resp
}

func (resp *Response) SetCode(code RespCode) *Response {
	resp.Code = code
	return resp
}

func (resp *Response) SetCodeAndMsg(code RespCode) *Response {
	resp.Code = code
	resp.Msg = CodeToText(code)
	return resp
}

func NewResp(code RespCode, data RespData, msg string) *Response {
	if msg == "" {
		msg = CodeToText(code)
	}
	return &Response{Code: code, Data: data, Msg: msg}
}

func NewSuccessResp() *Response {
	return NewResp(RespCodeOK, nil, "")
}

func NewSuccessRespWithData(data RespData) *Response {
	return NewResp(RespCodeOK, data, "")
}

func NewFailResp() *Response {
	return NewResp(RespFailed, nil, "")
}

func CodeToText(code RespCode) string {
	return responseText[code]
}
