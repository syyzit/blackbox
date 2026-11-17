package store

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/syyzit/blackbox/internal/event"
)

// LinkExternalCall tags the proxy's recording of one MCP tool call with the agent
// session that made it, so the call joins that agent session's trace.
//
// Some agents put their session id on every MCP call and need none of this. Others,
// such as Claude Code, only reveal which session made a call to their own hooks, after
// the proxy has recorded it. The hook then calls this with what it knows: the tool,
// its arguments and roughly when it ran.
//
// tools lists the names the proxied tool might have, since an agent-side name such as
// mcp__server__tool does not say where the server name ends. arguments is the call's
// argument object, redacted the way the proxy redacts it. Among untagged tools/call
// requests in the window, the newest one with equal arguments is linked. If none is
// equal but exactly one candidate exists, that one is linked, because redaction can
// make equal arguments look different. It reports whether a call was linked.
func LinkExternalCall(home, externalID string, tools []string, arguments string, since, until time.Time) (bool, error) {
	if externalID == "" || len(tools) == 0 {
		return false, nil
	}
	db, err := openHomeDB(home)
	if err != nil {
		return false, err
	}
	defer db.Close()

	// Choosing a recording and tagging it is one step: parallel identical calls give
	// several hooks the same candidates, and each must claim a different one.
	linked := false
	err = withWriteLock(db, func(tx writeTx) error {
		var err error
		linked, err = linkExternalCall(tx, externalID, tools, arguments, since, until)
		return err
	})
	return linked, err
}

func linkExternalCall(db writeTx, externalID string, tools []string, arguments string, since, until time.Time) (bool, error) {
	query := `SELECT session_id, seq, call_id, args FROM events
	           WHERE method = 'tools/call' AND direction = ? AND external_id = ''
	             AND at >= ? AND at <= ?
	             AND tool IN (?` + strings.Repeat(", ?", len(tools)-1) + `)
	           ORDER BY at DESC, seq DESC LIMIT 50`
	params := []any{string(event.ClientToServer), since.UnixMilli(), until.UnixMilli()}
	for _, t := range tools {
		params = append(params, t)
	}
	rows, err := db.Query(query, params...)
	if err != nil {
		return false, err
	}
	type candidate struct {
		session, callID, args string
		seq                   int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.session, &c.seq, &c.callID, &c.args); err != nil {
			rows.Close()
			return false, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	var match *candidate
	for i := range candidates {
		var params struct {
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(candidates[i].args), &params) != nil {
			continue
		}
		if sameJSONValue(string(params.Arguments), arguments) {
			match = &candidates[i]
			break
		}
	}
	if match == nil && len(candidates) == 1 {
		match = &candidates[0]
	}
	if match == nil {
		return false, nil
	}

	// The request and its response share a call id; without one, tag the request alone.
	var result sql.Result
	if match.callID != "" {
		result, err = db.Exec(
			`UPDATE events SET external_id = ?
			  WHERE session_id = ? AND call_id = ? AND external_id = ''`,
			externalID, match.session, match.callID)
	} else {
		result, err = db.Exec(
			`UPDATE events SET external_id = ?
			  WHERE session_id = ? AND seq = ? AND external_id = ''`,
			externalID, match.session, match.seq)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// sameJSONValue compares two JSON texts by value. Missing arguments and an empty
// object are the same call.
func sameJSONValue(a, b string) bool {
	decode := func(s string) (any, bool) {
		if strings.TrimSpace(s) == "" || strings.TrimSpace(s) == "null" {
			return map[string]any{}, true
		}
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return nil, false
		}
		return v, true
	}
	x, okX := decode(a)
	y, okY := decode(b)
	return okX && okY && reflect.DeepEqual(x, y)
}
