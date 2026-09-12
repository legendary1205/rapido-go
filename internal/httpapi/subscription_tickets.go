package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// Machine-readable error codes for the customer-side ticket endpoints only,
// sent as response headers alongside the unchanged English `detail` body -
// the subscription page is bilingual and can't show raw English to a
// Persian customer, so its JS maps these to translated strings.
// X-Error-Arg carries the cap since the translated sentence interpolates
// the number. The admin-side endpoints in tickets.go deliberately don't
// set these - their client (the dashboard) is English-only.
const (
	errCodeTicketOpenLimit    = "ticket_open_limit"
	errCodeTicketMessageLimit = "ticket_message_limit"
	errCodeTicketNotFound     = "ticket_not_found"
)

// handleListMyTickets implements GET /sub/:token/tickets: the subscription's
// own user's tickets, newest-created-first, paginated (every ticket comes
// with its full message thread, so an unbounded list could be megabytes for
// a long-lived customer).
func (h *Handler) handleListMyTickets(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	limit := 20
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100 {
			limit = n
		}
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	rows, err := h.store.Queries.ListUserTickets(ctx, generated.ListUserTicketsParams{
		UserID: user.ID, Limit: int32(limit), Offset: int32(offset),
	})
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
	out := make([]ticketDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTicketDTO(r, messagesByTicket[r.ID]))
	}
	// A bare array, no wrapper - matches TicketResponse's list shape (no
	// owner info, no total count, unlike the admin list endpoint).
	c.JSON(http.StatusOK, out)
}

type ticketCreateRequest struct {
	Subject string `json:"subject" binding:"required"`
	Message string `json:"message" binding:"required"`
}

// handleCreateMyTicket implements POST /sub/:token/tickets.
func (h *Handler) handleCreateMyTicket(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	var req ticketCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid subject/message"})
		return
	}
	subject, subjectOK := ticketTextOK(req.Subject, ticketSubjectMaxLen)
	message, messageOK := ticketTextOK(req.Message, ticketBodyMaxLen)
	if !subjectOK || !messageOK {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid subject/message"})
		return
	}
	req.Subject, req.Message = subject, message

	ctx := c.Request.Context()
	openCount, err := h.store.Queries.CountOpenTicketsByUserID(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create ticket"})
		return
	}
	if openCount >= maxOpenTicketsPerUser {
		c.Header("X-Error-Code", errCodeTicketOpenLimit)
		c.Header("X-Error-Arg", strconv.Itoa(maxOpenTicketsPerUser))
		c.JSON(http.StatusBadRequest, gin.H{"detail": "You already have 5 open tickets"})
		return
	}

	ticket, err := h.store.Queries.CreateTicket(ctx, generated.CreateTicketParams{UserID: user.ID, Subject: req.Subject})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create ticket"})
		return
	}
	msg, err := h.store.Queries.CreateTicketMessage(ctx, generated.CreateTicketMessageParams{
		TicketID: ticket.ID, IsAdmin: false, Body: req.Message,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create ticket"})
		return
	}
	h.logger.Info("ticket opened", "ticket_id", ticket.ID, "username", user.Username)
	c.JSON(http.StatusOK, toTicketDTO(ticket, []ticketMessageDTO{toTicketMessageDTO(msg)}))
}

// handleReplyMyTicket implements POST /sub/:token/tickets/:id/messages.
// Ownership is enforced purely by including the token's own user_id in the
// ticket lookup - a foreign ticket_id is indistinguishable from a
// nonexistent one, so it can be neither read nor written (no leak of
// which ticket ids exist).
func (h *Handler) handleReplyMyTicket(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
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
	if _, err := h.store.Queries.GetUserTicketByID(ctx, generated.GetUserTicketByIDParams{ID: id, UserID: user.ID}); err != nil {
		c.Header("X-Error-Code", errCodeTicketNotFound)
		c.JSON(http.StatusNotFound, gin.H{"detail": "Ticket not found"})
		return
	}

	msgCount, err := h.store.Queries.CountTicketMessages(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not send reply"})
		return
	}
	if msgCount >= maxMessagesPerTicket {
		c.Header("X-Error-Code", errCodeTicketMessageLimit)
		c.Header("X-Error-Arg", strconv.Itoa(maxMessagesPerTicket))
		c.JSON(http.StatusBadRequest, gin.H{"detail": "This ticket already has 100 messages"})
		return
	}

	if err := h.insertTicketMessage(ctx, id, req.Body, false); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not send reply"})
		return
	}

	updated, err := h.store.Queries.GetUserTicketByID(ctx, generated.GetUserTicketByIDParams{ID: id, UserID: user.ID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Reply sent but could not be read back"})
		return
	}
	msgs, err := h.store.Queries.ListTicketMessagesByTicketIDs(ctx, []int32{id})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Reply sent but could not be read back"})
		return
	}
	c.JSON(http.StatusOK, toTicketDTO(updated, toTicketMessageDTOs(msgs)))
}
