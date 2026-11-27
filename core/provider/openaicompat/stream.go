package openaicompat

import (
	"encoding/json"
	"strconv"

	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/streamutil"
)

// classifyStreamError handles the SDK's error for a `data: {"error": ...}`
// event in a Chat Completions stream, which servers send when generation
// fails after the response started.
func classifyStreamError(se *ssestream.StreamError) error {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(se.Event.Data, &body) != nil || len(body.Error) == 0 {
		return &provider.Error{Kind: provider.ErrOther, Message: se.Message, Err: se}
	}
	var text string
	if json.Unmarshal(body.Error, &text) == nil {
		pe := streamutil.ClassifyFailure(text, "", "", 0)
		pe.Err = se
		return pe
	}
	var obj struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Status  string          `json:"status"`
	}
	_ = json.Unmarshal(body.Error, &obj)
	code, status := "", 0
	if n, err := strconv.Atoi(string(obj.Code)); err == nil {
		status = n
	} else {
		_ = json.Unmarshal(obj.Code, &code)
	}
	if code == "" {
		code = obj.Status // Gemini: "UNAVAILABLE", "RESOURCE_EXHAUSTED"
	}
	pe := streamutil.ClassifyFailure(obj.Message, code, obj.Type, status)
	pe.Err = se
	return pe
}
