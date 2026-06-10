package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

// ShieldResponser flattens a ResponseShield into the map the API returns per client.
type ShieldResponser struct{}

func (p *ShieldResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	shield := response.GetShield()
	if shield == nil {
		return response
	}

	result := map[string]any{
		"success": response.GetSuccess(),
		"error":   response.GetError(),
	}
	if id := response.GetIdentity(); id != nil {
		result["client_id"] = id.GetClientId()
		result["client_name"] = id.GetClientName()
	}

	data := map[string]any{
		"success":         shield.GetSuccess(),
		"message":         shield.GetMessage(),
		"applied_version": shield.GetAppliedVersion(),
		"reload_ok":       shield.GetReloadOk(),
	}
	if op != nil {
		data["operation"] = op.GetSubTypeNum().String()
	}
	if status := shield.GetServiceStatus(); status != "" {
		data["service_status"] = status
	}
	if files := shield.GetCurrentFiles(); len(files) > 0 {
		out := make([]map[string]any, 0, len(files))
		for _, f := range files {
			out = append(out, map[string]any{
				"path": f.GetPath(), "sha256": f.GetSha256(), "mode": f.GetMode(),
			})
		}
		data["current_files"] = out
	}

	result["shield"] = data
	return result
}
