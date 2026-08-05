package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/service"
)

func resolveObservationScope(r *http.Request, resolver *service.ObservationScopeResolver) (service.ObservationScope, error) {
	if resolver == nil {
		return service.ObservationScope{All: true}, nil
	}
	q := r.URL.Query()
	return resolver.Resolve(q.Get("envId"), q.Get("namespaceId"))
}
