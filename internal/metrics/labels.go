package metrics

import (
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/prometheus/client_golang/prometheus"
	"strconv"
)

// LabelsFromRequest extracts Prometheus labels from a stored request.
// Empty values are replaced with "unknown" or empty string as appropriate.
func LabelsFromRequest(req *types.StoredRequest) prometheus.Labels {
	model := req.Model
	if model == "" {
		model = "unknown"
	}
	statusCode := strconv.Itoa(req.StatusCode)
	errorType := ""
	if req.ErrorType != nil {
		errorType = *req.ErrorType
	}
	return prometheus.Labels{
		"model":       model,
		"status_code": statusCode,
		"error_type":  errorType,
	}
}
