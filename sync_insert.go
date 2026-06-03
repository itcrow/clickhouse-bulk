package main

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

const bulkSyncHeader = "X-Bulk-Sync"

func wantsSyncInsert(configEnabled bool, h http.Header) bool {
	if configEnabled {
		return true
	}
	if h == nil {
		return false
	}
	v := strings.TrimSpace(h.Get(bulkSyncHeader))
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func separateInsertParams(paramsIn string) (query, params string) {
	for _, p := range strings.Split(paramsIn, "&") {
		if p == "" {
			continue
		}
		if HasPrefix(p, "query=") {
			query = p[6:]
			continue
		}
		params += "&" + p
	}
	if len(params) > 0 {
		params = strings.TrimSpace(params[1:])
	}
	q, err := queryUnescape(query)
	if err != nil {
		return "", paramsIn
	}
	return q, params
}

func buildSyncBatchedRequest(params, content string, journalID uint64) *ClickhouseRequest {
	query, outParams := separateInsertParams(params)
	postBody := query + "\n" + content
	req := &ClickhouseRequest{
		Params:   outParams,
		Query:    query,
		Content:  postBody,
		Count:    1,
		isInsert: true,
	}
	if journalID > 0 {
		req.JournalIDs = []uint64{journalID}
	}
	return req
}

func buildSyncOpaqueRequest(params, content, contentType string, journalID uint64) *ClickhouseRequest {
	req := &ClickhouseRequest{
		Params:      params,
		Query:       insertQueryString(params, []byte(content)),
		Content:     content,
		ContentType: contentType,
		Count:       1,
		isInsert:    true,
		opaque:      true,
	}
	if journalID > 0 {
		req.JournalIDs = []uint64{journalID}
	}
	return req
}

func (server *Server) syncSendInsert(c echo.Context, req *ClickhouseRequest) error {
	if server.LiveSender == nil {
		return c.String(http.StatusServiceUnavailable, "No ClickHouse configured\n")
	}
	resp, status, headers, err := server.LiveSender.SendQuery(req)
	if err == nil {
		incSentCounter("")
		if server.BackupOn && server.BackupSender != nil {
			dup := *req
			server.BackupSender.Send(&dup)
		}
	} else if status == 0 {
		status = http.StatusBadGateway
		if resp == "" {
			resp = err.Error() + "\n"
		}
	}
	return server.finishSyncInsert(c, req, resp, status, headers, err)
}

func (server *Server) finishSyncInsert(c echo.Context, req *ClickhouseRequest, resp string, status int, headers http.Header, sendErr error) error {
	live := server.LiveSender
	if sendErr != nil && live != nil {
		prefix := "1"
		if status >= 400 && status < 502 {
			prefix = "2"
		}
		if dumpErr := live.Dump(req.Params, req.Content, resp, prefix, status); dumpErr != nil {
			log.Printf("ERROR: sync insert dump failed, journal entries retained: %+v\n", dumpErr)
		}
	}
	if live != nil {
		live.ackJournal(req.JournalIDs)
		if live.Journal != nil {
			setJournalPendingGauge(live.Journal)
		}
	}
	return writeProxiedQueryResponse(c, status, resp, headers)
}

func (server *Server) appendInsertJournal(params, content string) (uint64, error) {
	if server.Collector.Journal == nil {
		return 0, nil
	}
	id, err := server.Collector.Journal.Append(params, content)
	if err != nil {
		return 0, err
	}
	setJournalPendingGauge(server.Collector.Journal)
	return id, nil
}

func (server *Server) appendOpaqueJournal(params, content, contentType string) (uint64, error) {
	if server.Collector.Journal == nil {
		return 0, nil
	}
	id, err := server.Collector.Journal.AppendOpaque(params, content, contentType)
	if err != nil {
		return 0, err
	}
	setJournalPendingGauge(server.Collector.Journal)
	return id, nil
}

func (server *Server) journalAppendError(c echo.Context, err error) error {
	log.Printf("ERROR: journal append: %+v\n", err)
	if errors.Is(err, ErrJournalBacklog) {
		return c.String(http.StatusServiceUnavailable, "Journal backlog full\n")
	}
	return c.String(http.StatusInternalServerError, "Journal write failed\n")
}

func (server *Server) acceptBatchedInsertSync(c echo.Context, params, content string) error {
	journalID, err := server.appendInsertJournal(params, content)
	if err != nil {
		return server.journalAppendError(c, err)
	}
	pushCounter.Inc()
	req := buildSyncBatchedRequest(params, content, journalID)
	return server.syncSendInsert(c, req)
}
