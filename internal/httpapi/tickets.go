package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

const (
	ticketStatusOpen   = "open"
	ticketStatusClosed = "closed"

	// Anti-abuse caps, customer side only - an admin must always be able to
	// answer, so neither cap applies to admin replies. Mirrors
	// app/routers/ticket.py's MAX_OPEN_TICKETS_PER_USER/MAX_MESSAGES_PER_TICKET.
	maxOpenTicketsPerUser = 5
	maxMessagesPerTicket  = 100

	ticketSubjectMaxLen = 255
	ticketBodyMaxLen    = 4000
)

// ticketTextOK counts CHARACTERS, not bytes, and rejects a
// whitespace-only value - both matching the real panel, whose limits are
// Python string lengths.
//
// Using len() here measured UTF-8 bytes instead, which quietly halved the
// limit for Persian or Arabic text (2 bytes per character) and quartered
// it for emoji: a 2001-character Persian reply was refused as "too long"
// while the same text is accepted by the panel this one replaced. Nearly
// every ticket on this system is written in Persian.
func ticketTextOK(v string, max int) (string, bool) {
	trimmed := strings.TrimSpace(v)
	n := utf8.RuneCountInString(trimmed)
	return trimmed, n > 0 && n <= max
}

type ticketMessageDTO struct {
	ID        int32     `json:"id"`
	IsAdmin   bool      `json:"is_admin"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func toTicketMessageDTO(m generated.TicketMessage) ticketMessageDTO {
	return ticketMessageDTO{ID: m.ID, IsAdmin: m.IsAdmin, Body: m.Body, CreatedAt: m.CreatedAt.Time}
}

func toTicketMessageDTOs(msgs []generated.TicketMessage) []ticketMessageDTO {
	out := make([]ticketMessageDTO, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toTicketMessageDTO(m))
	}
	return out
}

// ticketDTO is the customer-facing shape - deliberately carries no owner
// information, matching TicketResponse's docstring in app/models/ticket.py.
type ticketDTO struct {
	ID        int32              `json:"id"`
	Subject   string             `json:"subject"`
	Status    string             `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Messages  []ticketMessageDTO `json:"messages"`
}

// adminTicketDTO adds the two admin-only fields: which customer owns the
// ticket, and (if any) that customer's own reseller admin.
type adminTicketDTO struct {
	ticketDTO
	Username string  `json:"username"`
	Owner    *string `json:"owner"`
}

func toTicketDTO(t generated.Ticket, msgs []ticketMessageDTO) ticketDTO {
	return ticketDTO{ID: t.ID, Subject: t.Subject, Status: t.Status, CreatedAt: t.CreatedAt.Time, UpdatedAt: t.UpdatedAt.Time, Messages: msgs}
}

// scopedAdminID returns the pgtype.Int4 to pass into every ticket query's
// admin_id param: invalid/NULL for a sudo admin (unscoped, sees the whole
// fleet's tickets), or their own id otherwise - mirrors _owner_scope's
// sudo-vs-reseller split in app/routers/ticket.py.
func scopedAdminID(identity *auth.Identity) pgtype.Int4 {
	if identity.IsSudo {
		return pgtype.Int4{}
	}
	return pgInt4FromInt(int(identity.AdminID))
}

// handleListTickets implements GET /api/tickets: every admin (not just
// sudo) can use this - a reseller sees only their own users' tickets.
func (h *Handler) handleListTickets(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	ctx := c.Request.Context()

	var status pgtype.Text
	if v := c.Query("status"); v != "" {
		if v != ticketStatusOpen && v != ticketStatusClosed {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid status"})
			return
		}
		status = textFromPtr(&v)
	}
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	adminID := scopedAdminID(identity)
	rows, err := h.store.Queries.ListAdminTickets(ctx, generated.ListAdminTicketsParams{
		AdminID: adminID, Status: status, LimitCount: int32(limit), OffsetCount: int32(offset),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list tickets"})
		return
	}
	total, err := h.store.Queries.CountAdminTickets(ctx, generated.CountAdminTicketsParams{AdminID: adminID, Status: status})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list tickets"})
		return
	}

	ticketIDs := make([]int32, len(rows))
	for i, r := range rows {
		ticketIDs[i] = r.ID
	}
	messagesByTicket, err := h.loadMessagesByTicketID(ctx, ticketIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list tickets"})
		return
	}

	out := make([]adminTicketDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminTicketDTO{
			ticketDTO: toTicketDTO(generated.Ticket{
				ID: r.ID, UserID: r.UserID, Subject: r.Subject, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			}, messagesByTicket[r.ID]),
			Username: r.OwnerUsername, Owner: textToPtr(r.OwnerAdminUsername),
		})
	}
	c.JSON(http.StatusOK, gin.H{"tickets": out, "total": total})
}

func (h *Handler) loadMessagesByTicketID(ctx context.Context, ticketIDs []int32) (map[int32][]ticketMessageDTO, error) {
	out := map[int32][]ticketMessageDTO{}
	if len(ticketIDs) == 0 {
		return out, nil
	}
	msgs, err := h.store.Queries.ListTicketMessagesByTicketIDs(ctx, ticketIDs)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		out[m.TicketID] = append(out[m.TicketID], toTicketMessageDTO(m))
	}
	return out, nil
}

// renderAdminTicket re-fetches one ticket (scoped) plus its messages, for
// use as the response body after every admin-side write.
func (h *Handler) renderAdminTicket(ctx context.Context, id int32, identity *auth.Identity) (adminTicketDTO, bool, error) {
	row, err := h.store.Queries.GetAdminTicketByID(ctx, generated.GetAdminTicketByIDParams{ID: id, AdminID: scopedAdminID(identity)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return adminTicketDTO{}, false, nil
		}
		return adminTicketDTO{}, false, err
	}
	msgs, err := h.store.Queries.ListTicketMessagesByTicketIDs(ctx, []int32{id})
	if err != nil {
		return adminTicketDTO{}, false, err
	}
	dto := adminTicketDTO{
		ticketDTO: toTicketDTO(generated.Ticket{
			ID: row.ID, UserID: row.UserID, Subject: row.Subject, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}, toTicketMessageDTOs(msgs)),
		Username: row.OwnerUsername, Owner: textToPtr(row.OwnerAdminUsername),
	}
	return dto, true, nil
}

// handleGetTicket implements GET /api/tickets/:id - 404, not 403, for an
// out-of-scope ticket so one reseller can't probe another's ticket ids.
func (h *Handler) handleGetTicket(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid ticket id"})
		return
	}
	dto, found, err := h.renderAdminTicket(c.Request.Context(), id, identity)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read ticket"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Ticket not found"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

type ticketMessageWriteRequest struct {
	Body string `json:"body" binding:"required"`
}

// handleAdminReplyTicket implements POST /api/tickets/:id/messages: no
// anti-abuse cap (an admin must always be able to answer), reopens a
// closed ticket and bumps it to the top of the inbox.
func (h *Handler) handleAdminReplyTicket(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid ticket id"})
		return
	}
	var req ticketMessageWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "body must be 1 to 4000 characters"})
		return
	}
	body, ok := ticketTextOK(req.Body, ticketBodyMaxLen)
	if !ok {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "body must be 1 to 4000 characters"})
		return
	}
	req.Body = body

	ctx := c.Request.Context()
	if _, found, err := h.renderAdminTicket(ctx, id, identity); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read ticket"})
		return
	} else if !found {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Ticket not found"})
		return
	}

	if err := h.insertTicketMessage(ctx, id, req.Body, true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not send reply"})
		return
	}

	dto, found, err := h.renderAdminTicket(ctx, id, identity)
	if err != nil || !found {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Reply sent but could not be read back"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

type ticketStatusWriteRequest struct {
	Status string `json:"status" binding:"required"`
}

// handleUpdateTicketStatus implements PUT /api/tickets/:id - close or
// reopen (PUT {"status":"open"} is how a closed ticket is reopened by hand).
func (h *Handler) handleUpdateTicketStatus(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid ticket id"})
		return
	}
	var req ticketStatusWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != ticketStatusOpen && req.Status != ticketStatusClosed) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "status must be 'open' or 'closed'"})
		return
	}

	ctx := c.Request.Context()
	if _, found, err := h.renderAdminTicket(ctx, id, identity); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read ticket"})
		return
	} else if !found {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Ticket not found"})
		return
	}

	if _, err := h.store.Queries.UpdateTicketStatus(ctx, generated.UpdateTicketStatusParams{ID: id, Status: req.Status}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not update ticket"})
		return
	}

	dto, found, err := h.renderAdminTicket(ctx, id, identity)
	if err != nil || !found {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Status updated but could not be read back"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

// insertTicketMessage is the one shared write path for both the admin and
// customer reply endpoints: insert the message, then touch+reopen the
// ticket - mirrors add_ticket_message's exact behavior (see
// TouchAndReopenTicket's own doc comment).
func (h *Handler) insertTicketMessage(ctx context.Context, ticketID int32, body string, isAdmin bool) error {
	if _, err := h.store.Queries.CreateTicketMessage(ctx, generated.CreateTicketMessageParams{
		TicketID: ticketID, IsAdmin: isAdmin, Body: body,
	}); err != nil {
		return err
	}
	_, err := h.store.Queries.TouchAndReopenTicket(ctx, ticketID)
	return err
}
