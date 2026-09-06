package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

type userTemplateWriteRequest struct {
	Name           string              `json:"name" binding:"required"`
	DataLimit      int64               `json:"data_limit"`
	ExpireDuration int64               `json:"expire_duration"`
	UsernamePrefix *string             `json:"username_prefix"`
	UsernameSuffix *string             `json:"username_suffix"`
	Inbounds       map[string][]string `json:"inbounds"`
}

type userTemplateDTO struct {
	ID             int32               `json:"id"`
	Name           string              `json:"name"`
	DataLimit      int64               `json:"data_limit"`
	ExpireDuration int64               `json:"expire_duration"`
	UsernamePrefix *string             `json:"username_prefix"`
	UsernameSuffix *string             `json:"username_suffix"`
	Inbounds       map[string][]string `json:"inbounds"`
}

func (h *Handler) toUserTemplateDTO(ctx context.Context, t generated.UserTemplate) (userTemplateDTO, error) {
	tags, err := h.store.Queries.ListTemplateInboundTags(ctx, t.ID)
	if err != nil {
		return userTemplateDTO{}, err
	}
	inbounds := map[string][]string{}
	for _, tag := range tags {
		row, err := h.store.Queries.GetInboundByTag(ctx, tag)
		if err != nil {
			continue // a tag whose inbound has since disappeared - skip rather than fail the whole read
		}
		inbounds[row.Protocol] = append(inbounds[row.Protocol], tag)
	}
	return userTemplateDTO{
		ID: t.ID, Name: t.Name, DataLimit: pgInt8ToInt64(t.DataLimit), ExpireDuration: pgInt8ToInt64(t.ExpireDuration),
		UsernamePrefix: textToPtr(t.UsernamePrefix), UsernameSuffix: textToPtr(t.UsernameSuffix), Inbounds: inbounds,
	}, nil
}

// handleCreateUserTemplate implements POST /api/user_template (sudo only).
func (h *Handler) handleCreateUserTemplate(c *gin.Context) {
	var req userTemplateWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	ctx := c.Request.Context()

	for _, tags := range req.Inbounds {
		for _, tag := range tags {
			if _, err := h.store.Queries.GetInboundByTag(ctx, tag); err != nil {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Inbound " + tag + " doesn't exist"})
				return
			}
		}
	}

	template, err := h.store.Queries.CreateUserTemplate(ctx, generated.CreateUserTemplateParams{
		Name: req.Name, DataLimit: pgInt8FromInt64(req.DataLimit), ExpireDuration: pgInt8FromInt64(req.ExpireDuration),
		UsernamePrefix: textFromPtr(req.UsernamePrefix), UsernameSuffix: textFromPtr(req.UsernameSuffix),
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"detail": "User template already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create user template"})
		return
	}
	if err := h.replaceTemplateInbounds(ctx, template.ID, req.Inbounds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	dto, err := h.toUserTemplateDTO(ctx, template)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read back user template"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

func (h *Handler) replaceTemplateInbounds(ctx context.Context, templateID int32, inbounds map[string][]string) error {
	if err := h.store.Queries.DeleteTemplateInbounds(ctx, templateID); err != nil {
		return err
	}
	var tags []string
	for _, protocolTags := range inbounds {
		tags = append(tags, protocolTags...)
	}
	if len(tags) == 0 {
		return nil
	}
	return h.store.Queries.ReplaceTemplateInbounds(ctx, generated.ReplaceTemplateInboundsParams{UserTemplateID: templateID, Column2: tags})
}

// handleGetUserTemplate implements GET /api/user_template/:id.
func (h *Handler) handleGetUserTemplate(c *gin.Context) {
	template, ok := h.loadUserTemplate(c)
	if !ok {
		return
	}
	dto, err := h.toUserTemplateDTO(c.Request.Context(), template)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user template"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

func (h *Handler) loadUserTemplate(c *gin.Context) (generated.UserTemplate, bool) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid template id"})
		return generated.UserTemplate{}, false
	}
	template, err := h.store.Queries.GetUserTemplateByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User Template not found"})
		return generated.UserTemplate{}, false
	}
	return template, true
}

// handleListUserTemplates implements GET /api/user_template.
func (h *Handler) handleListUserTemplates(c *gin.Context) {
	templates, err := h.store.Queries.ListUserTemplates(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list user templates"})
		return
	}
	out := make([]userTemplateDTO, 0, len(templates))
	for _, t := range templates {
		dto, err := h.toUserTemplateDTO(c.Request.Context(), t)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user templates"})
			return
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, out)
}

// handleModifyUserTemplate implements PUT /api/user_template/:id (sudo only).
func (h *Handler) handleModifyUserTemplate(c *gin.Context) {
	template, ok := h.loadUserTemplate(c)
	if !ok {
		return
	}
	var req userTemplateWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	ctx := c.Request.Context()
	for _, tags := range req.Inbounds {
		for _, tag := range tags {
			if _, err := h.store.Queries.GetInboundByTag(ctx, tag); err != nil {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Inbound " + tag + " doesn't exist"})
				return
			}
		}
	}
	updated, err := h.store.Queries.UpdateUserTemplate(ctx, generated.UpdateUserTemplateParams{
		ID: template.ID, Name: req.Name, DataLimit: pgInt8FromInt64(req.DataLimit), ExpireDuration: pgInt8FromInt64(req.ExpireDuration),
		UsernamePrefix: textFromPtr(req.UsernamePrefix), UsernameSuffix: textFromPtr(req.UsernameSuffix),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not update user template"})
		return
	}
	if err := h.replaceTemplateInbounds(ctx, updated.ID, req.Inbounds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	dto, err := h.toUserTemplateDTO(ctx, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read back user template"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

// handleDeleteUserTemplate implements DELETE /api/user_template/:id (sudo only).
func (h *Handler) handleDeleteUserTemplate(c *gin.Context) {
	template, ok := h.loadUserTemplate(c)
	if !ok {
		return
	}
	if err := h.store.Queries.DeleteUserTemplate(c.Request.Context(), template.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete user template"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "User Template removed successfully"})
}
