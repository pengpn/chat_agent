package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// WriteRawResp writes a custom business code with an explicit HTTP status.
func WriteRawResp(c *gin.Context, httpStatus int, code RespCode, data RespData, msg string) {
	c.JSON(httpStatus, NewResp(code, data, msg))
}

// WriteResp writes a pre-built response with an explicit HTTP status.
func WriteResp(c *gin.Context, httpStatus int, resp Response) {
	c.JSON(httpStatus, resp)
}

func WriteOKResp(c *gin.Context, data RespData) {
	WriteRawResp(c, http.StatusOK, RespCodeOK, data, "")
}

func WriteCreatedResp(c *gin.Context, data RespData) {
	WriteRawResp(c, http.StatusCreated, RespCodeOK, data, "")
}

// WriteFailedResp preserves the caller-selected HTTP status while exposing a
// consistent application-level failure code in the JSON body.
func WriteFailedResp(c *gin.Context, httpStatus int, data RespData, msg string) {
	WriteRawResp(c, httpStatus, RespFailed, data, msg)
}

func WriteErrorResp(c *gin.Context, httpStatus int, msg string) {
	WriteFailedResp(c, httpStatus, nil, msg)
}

func WriteRespWithErr(c *gin.Context, httpStatus int, resp Response, err error) {
	if err != nil {
		resp.SetCode(RespFailed).SetMsg(err.Error())
		WriteResp(c, httpStatus, resp)
		return
	}
	WriteResp(c, httpStatus, resp)
}

func WriteDataWithError(c *gin.Context, data RespData, err error) {
	if err != nil {
		WriteFailedResp(c, http.StatusInternalServerError, data, err.Error())
		return
	}
	WriteOKResp(c, data)
}
