package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ProbePoolUpstreamInfo triggers a manual pool upstream information refresh
// for one account. It runs through the same bounded probe path as the
// scheduler and persists the sanitized snapshot with CAS protection.
//
// POST /api/v1/admin/accounts/:id/pool-upstream-info-probe
func (h *AccountHandler) ProbePoolUpstreamInfo(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrPoolUpstreamInfoUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	snapshot, err := h.upstreamBillingProbe.ProbePoolUpstreamInfo(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.PoolUpstreamInfoResult{AccountID: accountID, Snapshot: snapshot})
}
